// Package chaoskit is what the kill-and-restart tools (tools/docs-chaos, tools/storage-chaos)
// share: a server whose life is cut into epochs, a schedule of SIGKILLs, and the list of
// violations a run found.
package chaoskit

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"github.com/arumadev/aruma/tools/internal/rtdbkit"
)

// Schedule is how a run kills its server.
type Schedule struct {
	Cycles       int           // SIGKILL and restart cycles, before the final SIGTERM
	MinUp, MaxUp time.Duration // how long the server runs before it is stopped
	MaxDown      time.Duration // how long it stays down
}

// Runner is a server cut into epochs: epoch n is the n-th process. Clients wait for the next
// epoch when a request fails for lack of a server.
type Runner struct {
	Srv  *rtdbkit.Server
	Seed int64
	Out  io.Writer // where violations are printed as they are found

	mu    sync.Mutex
	ep    int64
	up    chan struct{}
	downs map[int64]string
	viols []string
	nviol int
}

// NewRunner wraps a server that is not running yet.
func NewRunner(srv *rtdbkit.Server, seed int64) *Runner {
	return &Runner{Srv: srv, Seed: seed, Out: os.Stderr, up: make(chan struct{}), downs: map[int64]string{}}
}

// Epoch is the current epoch: the number of the process running, or of the last one.
func (r *Runner) Epoch() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ep
}

// Up reports whether a server runs.
func (r *Runner) Up() bool {
	r.mu.Lock()
	up := r.up
	r.mu.Unlock()
	select {
	case <-up:
		return true
	default:
		return false
	}
}

// WaitUp waits until a server runs and returns its epoch.
func (r *Runner) WaitUp(ctx context.Context) (int64, error) {
	r.mu.Lock()
	up := r.up
	r.mu.Unlock()
	select {
	case <-up:
		return r.Epoch(), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// WaitNext waits until a server of an epoch after ep runs: the one after the stop that broke a
// request of epoch ep. A request that failed while epoch ep still runs (a refusal, not a lost
// connection) returns at once.
func (r *Runner) WaitNext(ctx context.Context, ep int64) (int64, error) {
	for {
		cur, err := r.WaitUp(ctx)
		if err != nil || cur > ep {
			return cur, err
		}
		if r.Stopped(ep) == "" {
			// The server of ep still runs: the failure was not the stop's. Wait a little, so a
			// caller that retries does not spin.
			select {
			case <-time.After(20 * time.Millisecond):
			case <-ctx.Done():
				return 0, ctx.Err()
			}
			if r.Epoch() == ep && r.Stopped(ep) == "" {
				return ep, nil
			}
			continue
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// Stopped is how epoch ep ended ("kill" or "term"), "" while it runs.
func (r *Runner) Stopped(ep int64) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.downs[ep]
}

// Violation records a broken invariant.
func (r *Runner) Violation(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.mu.Lock()
	r.nviol++
	if len(r.viols) < 200 {
		r.viols = append(r.viols, msg)
	}
	r.mu.Unlock()
	fmt.Fprintf(r.Out, "VIOLATION (seed %d): %s\n", r.Seed, msg)
}

// Violations returns the violations found, at most 200, and how many there were.
func (r *Runner) Violations() ([]string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.viols...), r.nviol
}

// BringUp starts the server of the next epoch.
func (r *Runner) BringUp(ctx context.Context) error {
	if err := r.Srv.Start(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	r.ep++
	close(r.up)
	r.mu.Unlock()
	return nil
}

// BringDown stops the server: "kill" is SIGKILL, "term" SIGTERM.
func (r *Runner) BringDown(how string) error {
	r.mu.Lock()
	r.downs[r.ep] = how
	r.up = make(chan struct{})
	r.mu.Unlock()
	if how == "kill" {
		return r.Srv.Kill()
	}
	return r.Srv.Term()
}

// Run follows the schedule: it lets the server run, kills it, keeps it down and starts it
// again, Cycles times, then stops it with SIGTERM and starts it a last time. onCycle, when set,
// is told of each stop.
func (r *Runner) Run(ctx context.Context, s Schedule, rng *rand.Rand, onCycle func(how string, up, down time.Duration)) error {
	for i := 0; i <= s.Cycles; i++ {
		how := "kill"
		if i == s.Cycles {
			how = "term"
		}
		up := s.MinUp + time.Duration(rng.Int64N(int64(s.MaxUp-s.MinUp)+1))
		select {
		case <-time.After(up):
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := r.BringDown(how); err != nil {
			return fmt.Errorf("%s: %w", how, err)
		}
		down := time.Duration(rng.Int64N(int64(s.MaxDown) + 1))
		time.Sleep(down)
		if onCycle != nil {
			onCycle(how, up, down)
		}
		if err := r.BringUp(ctx); err != nil {
			return fmt.Errorf("restart after %s: %w", how, err)
		}
	}
	return nil
}
