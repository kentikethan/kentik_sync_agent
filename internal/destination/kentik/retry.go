package kentik

import (
	"context"
	"math/rand"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxAttempts = 5

// call rate-limits, times out, and retries a single Kentik RPC. Retries
// apply only to transient failures (rate limiting and transient
// unavailability); anything else is returned immediately since retrying a
// malformed request or an auth failure would just waste attempts.
func (c *Client) call(ctx context.Context, fn func(ctx context.Context) error) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepBackoff(ctx, attempt); err != nil {
				return err
			}
		}
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}

		callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
		err := fn(callCtx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryable(err) {
			return err
		}
	}
	return lastErr
}

func isRetryable(err error) bool {
	switch status.Code(err) {
	case codes.ResourceExhausted, codes.Unavailable, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}

// sleepBackoff waits an exponentially increasing, jittered delay before a
// retry attempt (attempt 1 -> ~1s, attempt 2 -> ~2s, ...), honoring ctx
// cancellation.
func sleepBackoff(ctx context.Context, attempt int) error {
	base := time.Duration(1<<uint(attempt-1)) * time.Second
	jitter := time.Duration(rand.Int63n(int64(base) / 2)) //nolint:gosec // non-cryptographic jitter is fine here
	select {
	case <-time.After(base + jitter):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
