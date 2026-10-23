package rules

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Aruma's limits on the library (ADR-021 §4, §6), each with a bound on time: hostile rules end
// in a denial within bounded time and memory, never in an outage. The interpreter's expression
// limit (1000) bounds how many operations one evaluation makes; these tests make that many.

const expressionLimit = firebaseExpressions

// within returns d, widened under the race detector.
func within(d time.Duration) time.Duration {
	if raceEnabled {
		return 10 * d
	}
	return d
}

// allocated returns the bytes the process allocated while f ran.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A rule that doubles a string within the expression limit (s + s, thirty times reaches
// gigabytes) is refused before a value passes the value cap or the evaluation passes its
// allocation, quickly.
func TestConcatIsBounded(t *testing.T) {
	lim := DefaultLimits()
	in := NewInflight(lim.InflightBytes)
	m := newMeter(&lim, in, newRegexCache(&lim))
	start := time.Now()
	s := String("x")
	var err error
	var mem uint64
	largest := 0
	mem = allocated(func() {
		for range expressionLimit {
			var next Value
			if next, err = binary(m, tokPlus, s, s); err != nil {
				break
			}
			s = next
			largest = len(s.str())
		}
	})
	var a *abort
	if !errors.As(err, &a) || a.err != nil {
		t.Fatalf("doubling a string ended in %v, want an abort for the rules", err)
	}
	if int64(largest) > lim.ValueBytes {
		t.Errorf("a string of %d bytes was built, past the value cap %d", largest, lim.ValueBytes)
	}
	if m.allocated > lim.AllocBytes {
		t.Errorf("the evaluation charged %d bytes, past its %d", m.allocated, lim.AllocBytes)
	}
	if mem > 4*uint64(lim.AllocBytes) && !raceEnabled {
		t.Errorf("doubling allocated %d bytes, far past the charged %d", mem, m.allocated)
	}
	if d := time.Since(start); d > within(2*time.Second) {
		t.Errorf("doubling took %v", d)
	}
	m.release()
	if in.used.Load() != 0 {
		t.Error("the evaluation's reservation was not returned")
	}

	// The same with lists, concat doubling them: the element cap.
	m = newMeter(&lim, NewInflight(lim.InflightBytes), newRegexCache(&lim))
	l := List([]Value{Int(1)})
	for range expressionLimit {
		var next Value
		if next, err = callMethod(m, l, "concat", []Value{l}); err != nil {
			break
		}
		l = next
	}
	if !errors.As(err, &a) || len(l.items()) > lim.ValueElements {
		t.Errorf("doubling a list ended in %v at %d elements", err, len(l.items()))
	}

	// Replacing every character of the largest string with a long replacement is refused before
	// the result is built.
	m = newMeter(&lim, NewInflight(lim.InflightBytes), newRegexCache(&lim))
	big := strings.Repeat("a", int(lim.ValueBytes))
	mem = allocated(func() {
		_, err = replaceString(m, big, "a", strings.Repeat("b", 64))
	})
	if !errors.As(err, &a) {
		t.Errorf("a replace past the value cap = %v, want an abort", err)
	}
	if mem > 4*uint64(lim.AllocBytes) && !raceEnabled {
		t.Errorf("the refused replace allocated %d bytes", mem)
	}
}

// matches() charges the string's length times the program's instructions to one meter for the
// whole evaluation, as RTDB's maxMatchCost does for one call (ADR-021 §4): the dearest call that
// fits runs in bounded time, and as many calls as the expression limit allows end in a denial
// within bounded time too.
func TestMatchesIsBounded(t *testing.T) {
	lim := DefaultLimits()
	pattern := "(?:[a-z]?){1000}(?:[a-z]?){500}[a-z]{1000}[a-z]{500}x"
	m := newMeter(&lim, NewInflight(lim.InflightBytes), newRegexCache(&lim))
	p, err := m.regexes.get(m, pattern)
	if err != nil {
		t.Fatal(err)
	}
	fits := int((lim.Work-m.worked)/int64(p.insts)) - 1
	s := strings.Repeat("a", fits)

	start := time.Now()
	if _, err := matchString(m, s, pattern); err != nil {
		t.Fatalf("the dearest call that fits: %v", err)
	}
	if d := time.Since(start); d > within(3*time.Second) {
		t.Errorf("the dearest call that fits took %v", d)
	}

	// A whole evaluation: as many calls as expressions, on one meter.
	m = newMeter(&lim, NewInflight(lim.InflightBytes), newRegexCache(&lim))
	start = time.Now()
	calls := 0
	for range expressionLimit {
		if _, err = matchString(m, s, pattern); err != nil {
			break
		}
		calls++
	}
	var a *abort
	if !errors.As(err, &a) || a.err != nil {
		t.Fatalf("an evaluation of %d matches ended in %v, want an abort", expressionLimit, err)
	}
	if calls > 1 {
		t.Errorf("%d dear calls ran on one evaluation's work", calls)
	}
	if d := time.Since(start); d > within(3*time.Second) {
		t.Errorf("the evaluation took %v", d)
	}
	// A call past the work limit is refused before it runs.
	m = newMeter(&lim, NewInflight(lim.InflightBytes), newRegexCache(&lim))
	start = time.Now()
	_, err = matchString(m, strings.Repeat("a", int(lim.ValueBytes)), pattern)
	if !errors.As(err, &a) || time.Since(start) > within(time.Second) {
		t.Errorf("a call past the work limit = %v after %v", err, time.Since(start))
	}
}

// \pL{1000} repeated is 27 KB of pattern and 3,000,002 instructions: 742 MB through
// regexp.Compile (ADR-021 §4). It is refused as a literal when the file compiles and as a
// pattern built during evaluation, without regexp.Compile, in bounded time and allocation.
func TestRegexProgramIsBounded(t *testing.T) {
	lim := DefaultLimits()
	for _, pattern := range []string{
		strings.Repeat(`\pL{1000}`, 3000),
		strings.Repeat(`\pL{0}`, 5000),          // few instructions, a dear parse
		strings.Repeat(`(?:)`, 20_000),          // a long pattern of nothing
		strings.Repeat(`(\pL{1000}){1000}`, 10), // refused by the parser itself
		`((((a{100}){100}){100}){100})`,
	} {
		start := time.Now()
		var literal, built error
		mem := allocated(func() {
			literal = compileLiteralRegex(newRegexCache(&lim), pattern)
			m := newMeter(&lim, NewInflight(lim.InflightBytes), newRegexCache(&lim))
			_, built = matchString(m, "a", pattern)
		})
		if literal == nil {
			t.Errorf("%.30q…: compiled as a literal", pattern)
		}
		var ee *evalError
		if !errors.As(built, &ee) {
			t.Errorf("%.30q…: built during evaluation = %v, want an evaluation error", pattern, built)
		}
		if mem > 64<<20 && !raceEnabled {
			t.Errorf("%.30q…: refusing it allocated %d MB", pattern, mem>>20)
		}
		if d := time.Since(start); d > within(2*time.Second) {
			t.Errorf("%.30q…: refusing it took %v", pattern, d)
		}
	}
	// Just under the limit compiles.
	if err := compileLiteralRegex(newRegexCache(&lim), strings.Repeat(`\pL{1000}`, 9)); err != nil {
		t.Errorf(`\pL{1000} nine times: %v`, err)
	}
}

// The collection functions are linear: large lists in hasAll and toSet finish quickly, and a
// value shared within itself, small in memory and vast to walk, ends on the work limit.
func TestCollectionsAreBounded(t *testing.T) {
	lim := DefaultLimits()
	const n = 50_000
	a, b := make([]Value, n), make([]Value, n)
	for i := range n {
		a[i], b[i] = Int(int64(i)), Float(float64(n-1-i))
	}
	m := newMeter(&lim, NewInflight(lim.InflightBytes), newRegexCache(&lim))
	start := time.Now()
	if v, err := callMethod(m, List(a), "hasAll", []Value{List(a)}); err != nil || !v.boolean() {
		t.Errorf("hasAll of itself = %s, %v", showValue(v), err)
	}
	set, err := callMethod(m, List(append(append([]Value{}, a...), b...)), "toSet", nil)
	if err != nil || len(set.items()) != n {
		t.Errorf("toSet of ints and their equal floats = %d members, %v; want %d", len(set.items()), err, n)
	}
	if d := time.Since(start); d > within(3*time.Second) {
		t.Errorf("hasAll and toSet of %d took %v", n, d)
	}

	// let x = [x, x], thirty times over: 2^30 leaves.
	m = newMeter(&lim, NewInflight(lim.InflightBytes), newRegexCache(&lim))
	x := List([]Value{Int(1)})
	y := List([]Value{Int(1)})
	for range 30 {
		x, y = List([]Value{x, x}), List([]Value{y, y})
	}
	start = time.Now()
	_, err = binary(m, tokEQ, x, y)
	var ab *abort
	if !errors.As(err, &ab) {
		t.Errorf("comparing 2^30 shared leaves = %v, want an abort", err)
	}
	if _, err := callMethod(newMeter(&lim, NewInflight(lim.InflightBytes), newRegexCache(&lim)), List([]Value{x, y}), "toSet", nil); !errors.As(err, &ab) {
		t.Errorf("a set of 2^30 shared leaves = %v, want an abort", err)
	}
	if d := time.Since(start); d > within(3*time.Second) {
		t.Errorf("walking shared leaves took %v", d)
	}
}
