package rules

import (
	"math"
	"testing"
	"time"
)

// This file is S1-B's (rules-layout.md). The contract commit puts here the cases that build
// values from their representation, which S1-B may change along with these cases.

// A rules duration reaches ±10,000 years; AsDuration answers only for what time.Duration holds,
// to the nanosecond at both ends.
func TestAsDurationRange(t *testing.T) {
	const maxSeconds = math.MaxInt64 / int64(time.Second)
	for _, c := range []struct {
		d    duration
		want time.Duration
		ok   bool
	}{
		{duration{maxSeconds, 854_775_807}, math.MaxInt64, true},
		{duration{maxSeconds, 854_775_808}, 0, false},
		{duration{maxSeconds, 999_999_999}, 0, false},
		{duration{maxSeconds + 1, 0}, 0, false},
		{duration{-maxSeconds, -854_775_808}, math.MinInt64, true},
		{duration{-maxSeconds, -854_775_809}, 0, false},
		{duration{-maxSeconds, -999_999_999}, 0, false},
		{duration{10_000 * 366 * 86400, 0}, 0, false},
		{duration{-1, -500_000_000}, -1500 * time.Millisecond, true},
	} {
		got, ok := Value{kind: KindDuration, x: c.d}.AsDuration()
		if ok != c.ok || got != c.want {
			t.Errorf("%+v: AsDuration() = %v, %v; want %v, %v", c.d, got, ok, c.want, c.ok)
		}
	}
}
