package core

import (
	"context"
	"time"
)

// maintainEvery is how often Run expires sessions, releases idle ones, collects garbage and sets
// the gauges.
const maintainEvery = 30 * time.Second

// reportEvery is how often the periodic sweep reports what it finds.
const reportEvery = 24 * time.Hour

// Run works after the listener opened, until ctx ends: it deletes Boot's candidates that no row
// points to, runs the blob_gc worker, expires sessions, and reports what the periodic sweep finds.
func (s *Service) Run(ctx context.Context) error {
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
