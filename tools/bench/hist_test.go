package main

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

func TestBucketsCoverEveryValueOnce(t *testing.T) {
	// Every bucket's bounds map back to it, and consecutive buckets touch without gaps.
	prevHi := int64(-1)
	for i := 0; i <= bucketOf(math.MaxInt64); i++ {
		lo, hi := bucketBounds(i)
		if lo != prevHi+1 {
			t.Fatalf("bucket %d starts at %d, previous ended at %d", i, lo, prevHi)
		}
		if bucketOf(lo) != i || bucketOf(hi) != i {
			t.Fatalf("bucket %d: bounds [%d, %d] map to %d and %d", i, lo, hi, bucketOf(lo), bucketOf(hi))
		}
		if lo >= linearMax && float64(hi-lo+1)/float64(lo) > 1.0/subCount+1e-12 {
			t.Fatalf("bucket %d [%d, %d] is wider than 1/%d of its values", i, lo, hi, subCount)
		}
		if hi == math.MaxInt64 {
			return
		}
		prevHi = hi
	}
	t.Fatal("buckets end before MaxInt64")
}

func TestEmpty(t *testing.T) {
	var h Histogram
	if h.Count() != 0 || h.Quantile(0.5) != 0 || h.Mean() != 0 || h.Max() != 0 {
		t.Fatal("empty histogram should report zeros")
	}
}

func TestSingleValue(t *testing.T) {
	var h Histogram
	h.Record(1234567 * time.Nanosecond)
	for _, q := range []float64{0, 0.5, 0.99, 1} {
		if got := h.Quantile(q); got != 1234567 {
			t.Errorf("Quantile(%v) = %d, want the only value 1234567", q, got)
		}
	}
	if h.Min() != 1234567 || h.Max() != 1234567 || h.Mean() != 1234567 {
		t.Errorf("min/max/mean = %d/%d/%d", h.Min(), h.Max(), h.Mean())
	}
}

func TestSmallValuesAreExact(t *testing.T) {
	var h Histogram
	for v := 1; v <= 100; v++ {
		h.Record(time.Duration(v))
	}
	// Nearest rank: p50 of 1..100 is the 50th value, p99 the 99th, p100 the max.
	cases := map[float64]time.Duration{0.01: 1, 0.5: 50, 0.9: 90, 0.99: 99, 0.999: 100, 1: 100}
	for q, want := range cases {
		if got := h.Quantile(q); got != want {
			t.Errorf("Quantile(%v) = %d, want %d", q, got, want)
		}
	}
	if h.Mean() != 50 { // 50.5 truncated
		t.Errorf("Mean = %d, want 50", h.Mean())
	}
}

func TestNegativeCountsAsZero(t *testing.T) {
	var h Histogram
	h.Record(-5)
	if h.Min() != 0 || h.Max() != 0 || h.Count() != 1 {
		t.Fatalf("got min %d max %d count %d", h.Min(), h.Max(), h.Count())
	}
}

// TestQuantilesAgainstSort compares with exact nearest-rank quantiles of the same samples: the
// histogram may overstate by at most one bucket (1/64) and never understate.
func TestQuantilesAgainstSort(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var h Histogram
	vals := make([]int64, 200_000)
	for i := range vals {
		// Log-normal around 1 ms with a long tail, like request latencies.
		v := int64(math.Exp(r.NormFloat64()*1.2) * 1e6)
		vals[i] = v
		h.Record(time.Duration(v))
	}
	slices.Sort(vals)
	for _, q := range []float64{0.5, 0.9, 0.99, 0.999, 0.9999} {
		rank := int(math.Ceil(q * float64(len(vals))))
		exact := vals[rank-1]
		got := int64(h.Quantile(q))
		if got < exact {
			t.Errorf("q=%v: %d understates exact %d", q, got, exact)
		}
		if float64(got-exact) > float64(exact)/subCount {
			t.Errorf("q=%v: %d is more than 1/%d above exact %d", q, got, subCount, exact)
		}
	}
	if h.Max() != time.Duration(vals[len(vals)-1]) || h.Min() != time.Duration(vals[0]) {
		t.Error("min/max must be exact")
	}
	if h.Quantile(1) != h.Max() {
		t.Error("p100 must equal max")
	}
}

func TestMergeEqualsRecordingTogether(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	var a, b, all Histogram
	for i := range 10_000 {
		d := time.Duration(r.Int64N(int64(50 * time.Millisecond)))
		all.Record(d)
		if i%3 == 0 {
			a.Record(d)
		} else {
			b.Record(d)
		}
	}
	var m Histogram
	m.Merge(&a)
	m.Merge(&b)
	m.Merge(&Histogram{}) // merging an empty histogram changes nothing
	if m.Count() != all.Count() || m.Min() != all.Min() || m.Max() != all.Max() {
		t.Fatalf("count/min/max differ: %d/%d/%d vs %d/%d/%d", m.Count(), m.Min(), m.Max(), all.Count(), all.Min(), all.Max())
	}
	for _, q := range []float64{0.5, 0.9, 0.99, 0.999} {
		if m.Quantile(q) != all.Quantile(q) {
			t.Errorf("q=%v: merged %v, together %v", q, m.Quantile(q), all.Quantile(q))
		}
	}
	if d := m.Mean() - all.Mean(); d < -1 || d > 1 {
		t.Errorf("mean differs: %v vs %v", m.Mean(), all.Mean())
	}
}

func TestBimodalTailIsVisible(t *testing.T) {
	// 98 fast requests and 2 stalls: p50 and p90 stay fast, p99 lands on a stall.
	var h Histogram
	for range 98 {
		h.Record(time.Millisecond)
	}
	h.Record(time.Second)
	h.Record(time.Second)
	if got := h.Quantile(0.9); got > time.Millisecond+time.Millisecond/subCount {
		t.Errorf("p90 = %v, want ≈1ms", got)
	}
	if got := h.Quantile(0.99); got != time.Second {
		t.Errorf("p99 = %v, want 1s (clamped to max)", got)
	}
}
