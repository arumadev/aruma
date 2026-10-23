package rules

import (
	"errors"
	"sync"
	"testing"
)

func TestInflightReserveAndRelease(t *testing.T) {
	in := NewInflight(100)
	if !in.reserve(60) || !in.reserve(40) {
		t.Fatal("reservations within the cap refused")
	}
	if in.reserve(1) {
		t.Fatal("a reservation past the cap accepted")
	}
	if got := in.used.Load(); got != 100 {
		t.Fatalf("used = %d after a refused reservation, want 100", got)
	}
	in.release(60)
	if !in.reserve(60) || in.reserve(1) {
		t.Fatal("released bytes not available again, or the cap moved")
	}
	in.release(100)
	if got := in.used.Load(); got != 0 {
		t.Fatalf("used = %d after releasing everything, want 0", got)
	}
	if NewInflight(0).limit != 1 {
		t.Error("a cap below 1 is not raised to 1")
	}
}

// The cap holds under contention: no interleaving of reservations passes it, and what is
// reserved is exactly what is released.
func TestInflightConcurrent(t *testing.T) {
	const limit, workers, rounds = 1000, 16, 2000
	in := NewInflight(limit)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := range rounds {
				n := int64(i%97 + 1)
				if in.reserve(n) {
					if u := in.used.Load(); u > limit {
						t.Errorf("used %d past the cap %d", u, limit)
					}
					in.release(n)
				}
			}
		})
	}
	wg.Wait()
	if got := in.used.Load(); got != 0 {
		t.Errorf("used = %d after every reservation was released, want 0", got)
	}
}

func testMeter(in *Inflight) *meter {
	lim := DefaultLimits()
	lim.Work, lim.AllocBytes, lim.ValueBytes, lim.ValueElements = 100, 1000, 50, 5
	return newMeter(&lim, in, newRegexCache(&lim))
}

func TestMeterWork(t *testing.T) {
	m := testMeter(NewInflight(1 << 20))
	if err := m.work(60); err != nil {
		t.Fatal(err)
	}
	if err := m.work(40); err != nil {
		t.Fatalf("work up to the limit: %v", err)
	}
	var a *abort
	if err := m.work(1); !errors.As(err, &a) || a.err != nil {
		t.Fatalf("work past the limit = %v, want an abort for the rules", err)
	}
	if err := m.work(-1); !errors.As(err, &a) {
		t.Fatalf("negative work = %v, want an abort", err)
	}
}

func TestMeterAllocReservesFromInflight(t *testing.T) {
	in := NewInflight(1 << 20)
	m := testMeter(in)
	if err := m.alloc(600); err != nil {
		t.Fatal(err)
	}
	if err := m.alloc(400); err != nil {
		t.Fatalf("allocation up to the limit: %v", err)
	}
	if got := in.used.Load(); got != 1000 {
		t.Fatalf("inflight holds %d, want the meter's 1000", got)
	}
	var a *abort
	if err := m.alloc(1); !errors.As(err, &a) || a.err != nil {
		t.Fatalf("allocation past the limit = %v, want an abort for the rules", err)
	}
	if got := in.used.Load(); got != 1000 {
		t.Fatalf("a refused allocation reserved: inflight holds %d", got)
	}
	m.release()
	m.release() // a second release returns nothing more
	if got := in.used.Load(); got != 0 {
		t.Fatalf("inflight holds %d after release, want 0", got)
	}
}

// A full Inflight is an outage, not a denial: the abort carries ErrOverloaded, which Evaluate
// returns, and the meters that did reserve give it all back.
func TestMeterAllocOverloaded(t *testing.T) {
	in := NewInflight(1500)
	m1, m2 := testMeter(in), testMeter(in)
	if err := m1.alloc(1000); err != nil {
		t.Fatal(err)
	}
	err := m2.alloc(600)
	var a *abort
	if !errors.As(err, &a) || !errors.Is(err, ErrOverloaded) {
		t.Fatalf("allocation past the process cap = %v, want an abort wrapping ErrOverloaded", err)
	}
	if err := m2.alloc(500); err != nil {
		t.Fatalf("allocation that fits the process cap: %v", err)
	}
	m1.release()
	m2.release()
	if got := in.used.Load(); got != 0 {
		t.Fatalf("inflight holds %d after both released, want 0", got)
	}
}

func TestMeterValue(t *testing.T) {
	m := testMeter(NewInflight(1 << 20))
	if err := m.value(50, 5); err != nil {
		t.Fatalf("a value at the limits: %v", err)
	}
	var a *abort
	if err := m.value(51, 0); !errors.As(err, &a) {
		t.Errorf("a value past value_bytes = %v, want an abort", err)
	}
	if err := m.value(0, 6); !errors.As(err, &a) {
		t.Errorf("a value past value_elements = %v, want an abort", err)
	}
	if m.allocated != 0 {
		t.Errorf("value charged %d bytes; only alloc charges", m.allocated)
	}
}
