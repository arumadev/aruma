package main

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// A "server" that takes 5 ms per request and serves one at a time handles 200 req/s.

func slowServer(ctx context.Context, _ int) Outcome {
	time.Sleep(5 * time.Millisecond)
	return Outcome{Status: 200}
}

func TestClosedLoopMeasuresServiceTime(t *testing.T) {
	m := Drive(context.Background(), Plan{Concurrency: 1, Warmup: 20 * time.Millisecond, Duration: 300 * time.Millisecond}, slowServer)
	if m.OK() < 30 || m.OK() > 70 {
		t.Fatalf("closed loop at 5 ms/request did %d requests in 300 ms, want ≈60", m.OK())
	}
	if p50 := m.Latency.Quantile(0.5); p50 < 5*time.Millisecond || p50 > 20*time.Millisecond {
		t.Errorf("p50 = %v, want ≈5ms", p50)
	}
}

// TestOpenLoopCountsQueueing asks for 1000 req/s from the 200 req/s server. A closed-loop tool
// would report ≈5 ms; the open-loop driver measures from the scheduled time, so latency grows
// with the backlog, as a real client arriving at that rate would see it.
func TestOpenLoopCountsQueueing(t *testing.T) {
	m := Drive(context.Background(), Plan{Concurrency: 1, Duration: 200 * time.Millisecond, Rate: 1000}, slowServer)
	if m.Scheduled != 200 {
		t.Errorf("scheduled %d, want 200", m.Scheduled)
	}
	if p99 := m.Latency.Quantile(0.99); p99 < 100*time.Millisecond {
		t.Errorf("p99 = %v; the backlog should push it far above the 5 ms service time", p99)
	}
	if m.MaxLag < 100*time.Millisecond {
		t.Errorf("max lag %v, want the schedule to fall well behind", m.MaxLag)
	}
	if m.OK()+m.Unsent != m.Scheduled {
		t.Errorf("ok %d + unsent %d != scheduled %d", m.OK(), m.Unsent, m.Scheduled)
	}
}

func TestOpenLoopWithinCapacity(t *testing.T) {
	m := Drive(context.Background(), Plan{Concurrency: 4, Duration: 300 * time.Millisecond, Rate: 100}, slowServer)
	if m.OK() < 25 || m.OK() > 31 {
		t.Errorf("ok = %d, want ≈30 at 100 req/s for 300 ms", m.OK())
	}
	if p50 := m.Latency.Quantile(0.5); p50 > 20*time.Millisecond {
		t.Errorf("p50 = %v; within capacity latency should stay near the service time", p50)
	}
}

func TestStatusesAndErrorsAreSeparated(t *testing.T) {
	i := 0
	m := Drive(context.Background(), Plan{Concurrency: 1, Duration: 50 * time.Millisecond}, func(context.Context, int) Outcome {
		i++
		time.Sleep(time.Millisecond)
		switch i % 3 {
		case 0:
			return Outcome{Status: 429}
		case 1:
			return Outcome{Err: errors.New("connection refused")}
		}
		return Outcome{Status: 200}
	})
	if m.OK() == 0 || m.Statuses[429] == 0 || m.Transport == 0 {
		t.Fatalf("statuses %v transport %d", m.Statuses, m.Transport)
	}
	if m.Latency.Count() != uint64(m.OK()) || m.ErrLatency.Count() != uint64(m.Statuses[429]+m.Transport) {
		t.Errorf("latency histograms must split success from failure")
	}
	if m.FirstError != "connection refused" {
		t.Errorf("first error %q", m.FirstError)
	}
}

func TestCancelStopsPromptly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	Drive(ctx, Plan{Concurrency: 2, Duration: 10 * time.Second}, slowServer)
	if time.Since(t0) > time.Second {
		t.Fatal("Drive ignored cancellation")
	}
}

func TestRedactArgs(t *testing.T) {
	got := redactArgs([]string{"-url", "http://x", "-token", "aruma_pat_secret", "--password=hunter22", "-c", "8"})
	want := []string{"-url", "http://x", "-token", "[REDACTED]", "--password=[REDACTED]", "-c", "8"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}
