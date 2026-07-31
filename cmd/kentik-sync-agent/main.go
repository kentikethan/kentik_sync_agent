// Command kentik-sync-agent syncs inventory data from source-of-truth
// systems (NetBox, and in future releases Nautobot, Infoblox, etc.) into
// Kentik on a schedule, or once for testing via --once/--dry-run.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kentikethan/kentik_sync_agent/internal/config"
	"github.com/kentikethan/kentik_sync_agent/internal/destination/kentik"
	"github.com/kentikethan/kentik_sync_agent/internal/observability"
	"github.com/kentikethan/kentik_sync_agent/internal/scheduler"
	"github.com/kentikethan/kentik_sync_agent/internal/source"
	"github.com/kentikethan/kentik_sync_agent/internal/state"
	syncpkg "github.com/kentikethan/kentik_sync_agent/internal/sync"

	// Registering a new source plugin means adding its import here (for
	// side-effect registration via init()) — see docs/plugins.md.
	_ "github.com/kentikethan/kentik_sync_agent/internal/source/netbox"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `kentik-sync-agent — sync inventory into Kentik from NetBox and other sources

Usage:
  kentik-sync-agent run --config <path> [--source <name>] [--once] [--dry-run]`)
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to the agent's YAML config file")
	only := fs.String("source", "", "only run the named source (default: all configured sources)")
	once := fs.Bool("once", false, "run each selected source exactly once and exit, instead of starting the scheduler")
	dryRun := fs.Bool("dry-run", false, "compute and log the sync diff without writing to Kentik or the state store")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	log := observability.NewLogger(cfg.Observability.LogLevel, cfg.Observability.LogFormat)

	store, err := openStore(cfg, *dryRun)
	if err != nil {
		return fmt.Errorf("opening state store: %w", err)
	}
	defer store.Close()

	kentikClient, err := kentik.NewClient(kentik.Config{
		APIURL:               cfg.Kentik.APIURL,
		Email:                cfg.Kentik.Email,
		APIToken:             cfg.Kentik.APIToken,
		Timeout:              cfg.Kentik.Timeout,
		DefaultPlanID:        cfg.Kentik.DefaultPlanID,
		DefaultDeviceSubtype: cfg.Kentik.DefaultDeviceSubtype,
		RequestsPerMinute:    cfg.Kentik.RateLimit.RequestsPerMinute,
	})
	if err != nil {
		return fmt.Errorf("connecting to Kentik: %w", err)
	}
	defer kentikClient.Close()

	engine := &syncpkg.Engine{
		Store:    store,
		Sites:    kentik.NewSiteApplier(kentikClient),
		Devices:  kentik.NewDeviceApplier(kentikClient),
		IPGroups: buildIPGroupDestinations(kentikClient, cfg.Kentik),
		Labels:   kentik.NewLabelApplier(kentikClient),
		Logger:   log,
	}

	metricsReporter := observability.NewMetricsReporter(cfg.Observability.KentikMetrics, cfg.Kentik.Email, cfg.Kentik.APIToken, log)

	jobs, err := buildJobs(cfg, *only, *dryRun)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return fmt.Errorf("no matching source found for --source %q", *only)
	}

	if *once {
		return runOnce(context.Background(), engine, jobs, cfg.AgentName, metricsReporter, log)
	}
	return runScheduled(engine, jobs, cfg, metricsReporter, log)
}

func openStore(cfg config.Config, dryRun bool) (state.Store, error) {
	if dryRun {
		return state.NewMemoryStore(), nil
	}
	path := cfg.State.Path
	if path == "" {
		path = "kentik-sync-agent.db"
	}
	return state.OpenSQLite(path)
}

// buildIPGroupDestinations constructs one kentik.PopulatorApplier per
// distinct Custom Dimension referenced by config (the default plus each
// configured route, deduped) and wires them into an IPGroupDestinations
// that the sync engine uses to route/resolve IP groups by tenant/VRF.
func buildIPGroupDestinations(kentikClient *kentik.Client, kc config.KentikConfig) *syncpkg.IPGroupDestinations {
	appliers := map[string]*kentik.PopulatorApplier{}
	ensure := func(dimensionID string) {
		if dimensionID == "" {
			return
		}
		if _, ok := appliers[dimensionID]; !ok {
			appliers[dimensionID] = kentik.NewPopulatorApplier(kentikClient, dimensionID)
		}
	}
	ensure(kc.SourceCustomDimensionID)
	ensure(kc.DestinationCustomDimensionID)

	routes := make([]syncpkg.IPGroupRoute, 0, len(kc.IPGroupDimensions))
	for _, r := range kc.IPGroupDimensions {
		ensure(r.SourceCustomDimensionID)
		ensure(r.DestinationCustomDimensionID)
		routes = append(routes, syncpkg.IPGroupRoute{
			Tenant:                       r.Tenant,
			VRF:                          r.VRF,
			SourceCustomDimensionID:      r.SourceCustomDimensionID,
			DestinationCustomDimensionID: r.DestinationCustomDimensionID,
		})
	}
	return &syncpkg.IPGroupDestinations{
		DefaultSourceDimensionID:      kc.SourceCustomDimensionID,
		DefaultDestinationDimensionID: kc.DestinationCustomDimensionID,
		Routes:                        routes,
		Appliers:                      appliers,
	}
}

func buildJobs(cfg config.Config, only string, dryRun bool) ([]scheduler.ScheduledJob, error) {
	var jobs []scheduler.ScheduledJob
	for _, sc := range cfg.Sources {
		if only != "" && sc.Name != only {
			continue
		}
		src, err := source.New(sc.Type, sc.Connection)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", sc.Name, err)
		}
		jobs = append(jobs, scheduler.ScheduledJob{
			Interval: sc.Sync.Interval,
			Job: syncpkg.Job{
				SourceName:            sc.Name,
				Source:                src,
				Objects:               sc.Sync.Objects,
				Incremental:           sc.Sync.Incremental,
				FullReconcileInterval: sc.Sync.FullReconcileInterval,
				DryRun:                dryRun,
			},
		})
	}
	return jobs, nil
}

func runOnce(ctx context.Context, engine *syncpkg.Engine, jobs []scheduler.ScheduledJob, agentName string, metricsReporter *observability.MetricsReporter, log *slog.Logger) error {
	failed := false
	for _, j := range jobs {
		log.Info("sync run starting", "source", j.Job.SourceName, "dry_run", j.Job.DryRun)
		start := time.Now()
		result, err := engine.RunJob(ctx, j.Job)
		if err != nil {
			return fmt.Errorf("source %q: %w", j.Job.SourceName, err)
		}
		duration := time.Since(start)
		log.Info("sync run finished",
			"source", j.Job.SourceName,
			"duration", duration.String(),
			"sites", result.Sites.String(),
			"devices", result.Devices.String(),
			"ip_groups", result.IPGroups.String(),
			"device_labels", result.DeviceLabels.String(),
		)
		if !j.Job.DryRun {
			metricsReporter.Report(ctx, observability.NewSyncMetrics(agentName, j.Job, result, duration))
		}
		for _, ferr := range result.FetchErrors {
			log.Error("fetch error", "source", j.Job.SourceName, "error", ferr)
		}
		for _, obj := range []struct {
			name string
			res  syncpkg.ObjectResult
		}{
			{"site", result.Sites},
			{"device", result.Devices},
			{"ip_group", result.IPGroups},
			{"device_label", result.DeviceLabels},
		} {
			for _, aerr := range obj.res.Errors {
				log.Error("apply error", "source", j.Job.SourceName, "object_type", obj.name, "error", aerr)
			}
		}
		if result.HasFailures() {
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("one or more objects failed to sync; see logged errors above")
	}
	return nil
}

func runScheduled(engine *syncpkg.Engine, jobs []scheduler.ScheduledJob, cfg config.Config, metricsReporter *observability.MetricsReporter, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sch := &scheduler.Scheduler{
		Logger:    log,
		Run:       engine.RunJob,
		Reporter:  metricsReporter,
		AgentName: cfg.AgentName,
	}
	sch.Start(ctx, jobs)
	return nil
}
