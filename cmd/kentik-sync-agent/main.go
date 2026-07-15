// Command kentik-sync-agent syncs inventory data from source-of-truth
// systems (NetBox, and in future releases Nautobot, Infoblox, etc.) into
// Kentik on a schedule, or once for testing via --once/--dry-run.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
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
	case "healthcheck":
		err = healthcheckCmd(os.Args[2:])
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
  kentik-sync-agent run --config <path> [--source <name>] [--once] [--dry-run]
  kentik-sync-agent healthcheck [--health-addr <addr>]`)
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
		CustomDimensionID:    cfg.Kentik.CustomDimensionID,
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
		IPGroups: kentik.NewPopulatorApplier(kentikClient, cfg.Kentik.CustomDimensionID),
		Labels:   kentik.NewLabelApplier(kentikClient),
	}

	jobs, err := buildJobs(cfg, *only, *dryRun)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return fmt.Errorf("no matching source found for --source %q", *only)
	}

	if *once {
		return runOnce(context.Background(), engine, jobs, log)
	}
	return runScheduled(engine, jobs, cfg, log)
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

func runOnce(ctx context.Context, engine *syncpkg.Engine, jobs []scheduler.ScheduledJob, log *slog.Logger) error {
	failed := false
	for _, j := range jobs {
		log.Info("sync run starting", "source", j.Job.SourceName, "dry_run", j.Job.DryRun)
		start := time.Now()
		result, err := engine.RunJob(ctx, j.Job)
		if err != nil {
			return fmt.Errorf("source %q: %w", j.Job.SourceName, err)
		}
		log.Info("sync run finished",
			"source", j.Job.SourceName,
			"duration", time.Since(start).String(),
			"sites", result.Sites.String(),
			"devices", result.Devices.String(),
			"ip_groups", result.IPGroups.String(),
			"device_labels", result.DeviceLabels.String(),
		)
		for _, ferr := range result.FetchErrors {
			log.Error("fetch error", "source", j.Job.SourceName, "error", ferr)
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

func runScheduled(engine *syncpkg.Engine, jobs []scheduler.ScheduledJob, cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startObservabilityServers(ctx, cfg, log)

	sch := &scheduler.Scheduler{
		Logger: log,
		Run:    engine.RunJob,
	}
	sch.Start(ctx, jobs)
	return nil
}

func startObservabilityServers(ctx context.Context, cfg config.Config, log *slog.Logger) {
	metricsAddr := cfg.Observability.MetricsAddr
	if metricsAddr == "" {
		metricsAddr = ":9090"
	}
	healthAddr := cfg.Observability.HealthAddr
	if healthAddr == "" {
		healthAddr = ":8080"
	}

	metricsSrv := &http.Server{Addr: metricsAddr, Handler: observability.NewMetricsMux()}
	healthSrv := &http.Server{Addr: healthAddr, Handler: observability.NewHealthMux(func(context.Context) error { return nil })}

	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server exited", "error", err)
		}
	}()
	go func() {
		if err := healthSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("health server exited", "error", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = metricsSrv.Shutdown(shutdownCtx)
		_ = healthSrv.Shutdown(shutdownCtx)
	}()
}

func healthcheckCmd(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	healthAddr := fs.String("health-addr", "localhost:8080", "the agent's health check listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + *healthAddr + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %s", resp.Status)
	}
	return nil
}
