package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Outcome is what one request produced: an HTTP status, or a transport error.
type Outcome struct {
	Status int
	Err    error
}

// Plan says how to drive one scenario.
type Plan struct {
	Concurrency int           // workers, and so the most requests in flight at once
	Warmup      time.Duration // run but not recorded: connections, pools and caches settle
	Duration    time.Duration // the measured window
	// Rate, when positive, switches from closed-loop to open-loop: requests are scheduled at
	// this many per second whatever the server does, and latency is measured from each
	// request's scheduled time, not from when a free worker finally sent it. A server that
	// stalls therefore shows the queueing it caused instead of hiding it (coordinated
	// omission). Concurrency then caps the requests in flight.
	Rate float64
	// OnWindow, when set, is called as the measured window opens and again as it closes, so
	// server-side counters can be sampled over exactly the same span.
	OnWindow func(open bool)
}

// Measurement is the result of driving one scenario.
type Measurement struct {
	Latency     Histogram     // successful (2xx) requests only
	ErrLatency  Histogram     // non-2xx and transport errors
	Statuses    map[int]int64 // every response by status
	Transport   int64         // requests with no response at all
	FirstError  string        // one example, to make failures diagnosable
	Elapsed     time.Duration // the measured window as it actually ran
	Scheduled   int64         // open-loop: requests the schedule called for in the window
	MaxLag      time.Duration // open-loop: the worst delay between schedule and actual send
	InFlightEnd int64         // requests still running when the window closed
	Unsent      int64         // open-loop: scheduled requests abandoned at the hard stop
}

// OK is the number of 2xx responses.
func (m *Measurement) OK() int64 {
	var n int64
	for s, c := range m.Statuses {
		if s >= 200 && s < 300 {
			n += c
		}
	}
	return n
}

// Total is the number of requests recorded, failed or not.
func (m *Measurement) Total() int64 {
	n := m.Transport
	for _, c := range m.Statuses {
		n += c
	}
	return n
}

type workerStats struct {
	ok, bad   Histogram
	statuses  map[int]int64
	transport int64
	firstErr  string
	maxLag    time.Duration
}

// Drive runs do with the plan. A request belongs to the measured window when it was started
// (closed loop) or scheduled (open loop) inside it; requests still in flight when the window
// closes are awaited and counted, so the slowest ones are never dropped from the tail.
func Drive(ctx context.Context, p Plan, do func(ctx context.Context, worker int) Outcome) *Measurement {
	start := time.Now()
	measureFrom := start.Add(p.Warmup)
	end := measureFrom.Add(p.Duration)

	var (
		wg       sync.WaitGroup
		next     atomic.Int64 // open loop: the next schedule slot
		inFlight atomic.Int64
		unsent   atomic.Int64
		stats    = make([]*workerStats, p.Concurrency)
	)
	var interval time.Duration
	if p.Rate > 0 {
		interval = time.Duration(float64(time.Second) / p.Rate)
	}
	for w := range p.Concurrency {
		st := &workerStats{statuses: map[int]int64{}}
		stats[w] = st
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				var t0 time.Time // when the request counts as started
				if interval > 0 {
					t0 = start.Add(time.Duration(next.Add(1)-1) * interval)
					if !t0.Before(end) {
						return
					}
					// A server far slower than the rate would keep us running long after the
					// window. Stop one window-length late and report what was never sent.
					if time.Now().After(end.Add(p.Duration)) {
						if !t0.Before(measureFrom) {
							unsent.Add(1)
						}
						continue
					}
					if d := time.Until(t0); d > 0 {
						if !sleep(ctx, d) {
							return
						}
					}
				} else {
					t0 = time.Now()
					if !t0.Before(end) {
						return
					}
				}
				sent := time.Now()
				inFlight.Add(1)
				out := do(ctx, w)
				done := time.Now()
				inFlight.Add(-1)
				if t0.Before(measureFrom) || errors.Is(out.Err, context.Canceled) {
					continue
				}
				if lag := sent.Sub(t0); lag > st.maxLag {
					st.maxLag = lag
				}
				lat := done.Sub(t0)
				switch {
				case out.Err != nil:
					st.transport++
					st.bad.Record(lat)
					if st.firstErr == "" {
						st.firstErr = out.Err.Error()
					}
				default:
					st.statuses[out.Status]++
					if out.Status >= 200 && out.Status < 300 {
						st.ok.Record(lat)
					} else {
						st.bad.Record(lat)
					}
				}
			}
		}()
	}

	// Note how many requests were still running as the window closed (a stalled server shows
	// up here even in closed-loop mode).
	var inFlightEnd int64
	if sleep(ctx, time.Until(measureFrom)) && p.OnWindow != nil {
		p.OnWindow(true)
	}
	if sleep(ctx, time.Until(end)) {
		inFlightEnd = inFlight.Load()
		if p.OnWindow != nil {
			p.OnWindow(false)
		}
	}
	wg.Wait()

	m := &Measurement{Statuses: map[int]int64{}, Elapsed: p.Duration, InFlightEnd: inFlightEnd}
	if ctx.Err() != nil {
		m.Elapsed = max(time.Since(measureFrom), 0)
	}
	for _, st := range stats {
		m.Latency.Merge(&st.ok)
		m.ErrLatency.Merge(&st.bad)
		for s, c := range st.statuses {
			m.Statuses[s] += c
		}
		m.Transport += st.transport
		if m.FirstError == "" {
			m.FirstError = st.firstErr
		}
		m.MaxLag = max(m.MaxLag, st.maxLag)
	}
	if interval > 0 {
		m.Scheduled = int64(p.Duration / interval)
		m.Unsent = unsent.Load()
	}
	return m
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
