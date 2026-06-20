package trigger

import (
	"context"
	"time"
)

// RunContinuous loops a continuous-trigger assignment: fire, then wait `delay`
// measured from the run's COMPLETION (not wall-clock), forever, until ctx is
// done. A zero delay re-fires back-to-back. This differs from cron, whose gap
// is between *starts* — continuous never overlaps its own runs, which is what a
// long-running audit loop wants.
//
// ponytail: one goroutine per continuous assignment. Fine for the handful of
// low-frequency loops these are; revisit only if assignments number in the
// thousands.
func RunContinuous(ctx context.Context, delay time.Duration, fire func(context.Context)) {
	for {
		if ctx.Err() != nil {
			return
		}
		fire(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
