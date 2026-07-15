// Package scheduler runs each configured source's sync.Job on its own
// interval-based ticker, independently of the other sources, so one slow
// or misbehaving source can't delay another's schedule. Rate limiting
// against both the source API and Kentik happens lower down (in the
// source plugin's HTTP client and the Kentik destination client
// respectively), so the scheduler itself doesn't need a separate bounded
// worker pool — one goroutine per configured source is already exactly as
// concurrent as the customer's config asked for.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	syncpkg "github.com/kentikethan/kentik_sync_agent/internal/sync"
)

// ScheduledJob pairs a sync.Job with the interval it should run on.
type ScheduledJob struct {
	Job      syncpkg.Job
	Interval time.Duration
}

// RunFunc executes one sync pass; swappable in tests.
type RunFunc func(ctx context.Context, job syncpkg.Job) (syncpkg.Result, error)

// Scheduler runs a set of ScheduledJobs until its context is canceled.
type Scheduler struct {
	Run    RunFunc
	Logger *slog.Logger
}

// Start runs every job on its own ticker (firing once immediately, then on
// each Interval) until ctx is canceled, then waits for in-flight runs to
// finish before returning.
func (s *Scheduler) Start(ctx context.Context, jobs []ScheduledJob) {
	var wg sync.WaitGroup
	for _, sj := range jobs {
		wg.Add(1)
		go func(sj ScheduledJob) {
			defer wg.Done()
			s.runLoop(ctx, sj)
		}(sj)
	}
	wg.Wait()
}

func (s *Scheduler) runLoop(ctx context.Context, sj ScheduledJob) {
	s.runOnce(ctx, sj.Job)

	ticker := time.NewTicker(sj.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOnce(ctx, sj.Job)
		}
	}
}

func (s *Scheduler) runOnce(ctx context.Context, job syncpkg.Job) {
	log := s.Logger.With("source", job.SourceName)
	log.Info("sync run starting")
	start := time.Now()

	result, err := s.Run(ctx, job)
	if err != nil {
		log.Error("sync run failed to start", "error", err)
		return
	}

	log.Info("sync run finished",
		"duration", time.Since(start).String(),
		"sites", result.Sites.String(),
		"devices", result.Devices.String(),
		"ip_groups", result.IPGroups.String(),
		"device_labels", result.DeviceLabels.String(),
		"fetch_errors", len(result.FetchErrors),
	)
	for _, ferr := range result.FetchErrors {
		log.Error("sync fetch error", "error", ferr)
	}
}
