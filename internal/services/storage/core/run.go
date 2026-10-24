package core

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// writeGate keeps the data directory's lock until the writers stop (ADR-022 §5.1): every write
// path and Run enter it, Close shuts it and waits for them, and only then gives the lock up, so
// no request of this process writes after the next one holds the directory.
type writeGate struct {
	mu      sync.Mutex
	closing bool
	active  sync.WaitGroup
}

// closeWait bounds how long Close waits for the writers. Past it the lock is not released: the
// process ends, and its exit releases it once nothing of it can write.
var closeWait = 30 * time.Second

// enter admits a write, ErrUnavailable once Close began; the caller calls leave when done.
// S2-C's Update, Delete, the token and CORS writes, and Stat's /v0 mint, take it too.
func (s *Service) enter() (leave func(), err error) {
	s.gate.mu.Lock()
	defer s.gate.mu.Unlock()
	if s.gate.closing {
		return nil, fmt.Errorf("%w: the server is shutting down", ErrUnavailable)
	}
	s.gate.active.Add(1)
	return s.gate.active.Done, nil
}

// shut refuses every new write and waits up to closeWait for those in flight; false when some
// are still running.
func (s *Service) shut() bool {
	s.gate.mu.Lock()
	s.gate.closing = true
	s.gate.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.gate.active.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(closeWait):
		return false
	}
}

// maintainEvery is how often Run expires sessions, releases idle ones, collects garbage and sets
// the gauges.
const maintainEvery = 30 * time.Second

// reportEvery is how often the periodic sweep reports what it finds.
const reportEvery = 24 * time.Hour

// Run works after the listener opened, until ctx ends: it deletes Boot's candidates that no row
// points to, runs the blob_gc worker, expires sessions, and reports what the periodic sweep finds.
func (s *Service) Run(ctx context.Context) error {
	leave, err := s.enter()
	if err != nil {
		return nil // closed already
	}
	defer leave()
	if err := s.deleteCandidates(ctx); err != nil && ctx.Err() == nil {
		// The orphans stay until the next boot; they are files, not rows.
		s.log.Error("storage: sweep", "error", err)
	}
	t := time.NewTicker(maintainEvery)
	defer t.Stop()
	lastReport := time.Now()
	for {
		s.maintain(ctx)
		if time.Since(lastReport) >= reportEvery {
			if err := s.reportInconsistencies(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("storage: periodic sweep", "error", err)
			}
			lastReport = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// maintain is one round of the background work; an error is logged, and the next round tries
// again.
func (s *Service) maintain(ctx context.Context) {
	for _, step := range []struct {
		name string
		run  func(context.Context) (int, error)
	}{
		{"expire sessions", s.expireSessions},
		{"release idle sessions", s.releaseIdle},
		{"gc", s.collectGarbage},
	} {
		// A full batch means more is waiting: take it now, while the context lasts.
		for {
			n, err := step.run(ctx)
			if err != nil {
				if ctx.Err() == nil {
					s.log.Error("storage: "+step.name, "error", err)
				}
				break
			}
			if n < gcBatch || ctx.Err() != nil {
				break
			}
		}
	}
	if err := s.reportGauges(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("storage: gauges", "error", err)
	}
}
