package main

import (
	"math"
	"math/bits"
	"time"
)

// Histogram records durations in log-linear buckets, the layout HdrHistogram uses: values
// below 128 ns are exact, and above that every power of two is split into 64 equal buckets, so
// a bucket is never wider than 1/64 (≈1.6 %) of the values it holds. Memory is fixed (about
// 30 KiB) whatever the number of samples. The count, minimum, maximum and sum are exact.
//
// A Histogram is not safe for concurrent use: give each worker its own and Merge them.
type Histogram struct {
	counts [numBuckets]uint64
	n      uint64
	min    int64
	max    int64
	sum    float64
}

const (
	subBits    = 6
	subCount   = 1 << subBits       // buckets per power of two
	linearMax  = 1 << (subBits + 1) // values below this are stored exactly
	numBuckets = linearMax + (63-subBits)*subCount
)

// Record adds one duration. Negative durations count as zero.
func (h *Histogram) Record(d time.Duration) {
	v := max(int64(d), 0)
	h.counts[bucketOf(v)]++
	if h.n == 0 || v < h.min {
		h.min = v
	}
	if v > h.max {
		h.max = v
	}
	h.n++
	h.sum += float64(v)
}

// Merge adds every sample of o.
func (h *Histogram) Merge(o *Histogram) {
	if o.n == 0 {
		return
	}
	for i, c := range o.counts {
		h.counts[i] += c
	}
	if h.n == 0 || o.min < h.min {
		h.min = o.min
	}
	if o.max > h.max {
		h.max = o.max
	}
	h.n += o.n
	h.sum += o.sum
}

// Count is the number of samples.
func (h *Histogram) Count() uint64 { return h.n }

// Min is the smallest sample, exact.
func (h *Histogram) Min() time.Duration { return time.Duration(h.min) }

// Max is the largest sample, exact.
func (h *Histogram) Max() time.Duration { return time.Duration(h.max) }

// Mean is the exact arithmetic mean.
func (h *Histogram) Mean() time.Duration {
	if h.n == 0 {
		return 0
	}
	return time.Duration(h.sum / float64(h.n))
}

// Quantile returns the q-quantile (0 ≤ q ≤ 1) by the nearest-rank method: the smallest
// recorded value v such that at least ⌈q·n⌉ samples are ≤ v. The bucket holding that sample
// reports its upper edge, clamped to the exact min and max, so a quantile is never
// understated and is at most one bucket width (≤1.6 %) above the true sample.
func (h *Histogram) Quantile(q float64) time.Duration {
	if h.n == 0 {
		return 0
	}
	if q <= 0 {
		return time.Duration(h.min)
	}
	if q >= 1 {
		return time.Duration(h.max)
	}
	rank := uint64(math.Ceil(q * float64(h.n)))
	if rank < 1 {
		rank = 1
	}
	var seen uint64
	for i, c := range h.counts {
		seen += c
		if seen >= rank {
			_, hi := bucketBounds(i)
			return time.Duration(min(max(hi, h.min), h.max))
		}
	}
	return time.Duration(h.max)
}

// bucketOf maps a non-negative value to its bucket index.
func bucketOf(v int64) int {
	if v < linearMax {
		return int(v)
	}
	u := uint64(v)
	shift := bits.Len64(u) - (subBits + 1) // ≥ 1
	top := int(u >> shift)                 // in [subCount, 2·subCount)
	return linearMax + (shift-1)*subCount + (top - subCount)
}

// bucketBounds returns the smallest and largest value a bucket holds.
func bucketBounds(i int) (lo, hi int64) {
	if i < linearMax {
		return int64(i), int64(i)
	}
	k := i - linearMax
	shift := k/subCount + 1
	top := int64(subCount + k%subCount)
	lo = top << shift
	hi = (top+1)<<shift - 1
	if hi < lo { // the very last bucket overflows int64
		hi = math.MaxInt64
	}
	return lo, hi
}
