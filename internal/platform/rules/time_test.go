package rules

import (
	"math"
	"testing"
	"time"
)

func TestTimestamps(t *testing.T) {
	// Pyric stdlib-timestamp-duration: request.time is 2025-06-15T13:45:30.25Z, a Sunday.
	now := vts("2025-06-15T13:45:30.25Z")
	runValueCases(t, []valueCase{
		{"year", vCall(now, "year"), vi(2025), ""},
		{"month", vCall(now, "month"), vi(6), ""},
		{"day", vCall(now, "day"), vi(15), ""},
		{"hours", vCall(now, "hours"), vi(13), ""},
		{"minutes", vCall(now, "minutes"), vi(45), ""},
		{"seconds of the minute, not of the epoch", vCall(now, "seconds"), vi(30), ""},
		{"nanos of the second", vCall(now, "nanos"), vi(250_000_000), ""},
		{"toMillis is the epoch's", vCall(now, "toMillis"), vi(1749995130250), ""},
		{"dayOfWeek: Sunday is 7", vCall(now, "dayOfWeek"), vi(7), ""},
		{"dayOfWeek: Monday is 1", vCall(vts("2025-06-16T00:00:00Z"), "dayOfWeek"), vi(1), ""},
		{"dayOfYear", vCall(now, "dayOfYear"), vi(166), ""},
		{"date", vCall(now, "date"), vts("2025-06-15T00:00:00Z"), ""},
		{"time", vCall(now, "time"), vdur(13*time.Hour + 45*time.Minute + 30250*time.Millisecond), ""},
		{"date() == timestamp.date()", vOpOn(vCall(now, "date"), tokEQ, vFn("timestamp.date", vi(2025), vi(6), vi(15))), vb(true), ""},
		{"time() == duration.time()", vOpOn(vCall(now, "time"), tokEQ, vFn("duration.time", vi(13), vi(45), vi(30), vi(250000000))), vb(true), ""},
		// Pyric timestamp-component-accessors: before the epoch.
		{"toMillis before the epoch", vThen(vFn("timestamp.value", vi(-1500)), "toMillis"), vi(-1500), ""},
		{"seconds before the epoch", vThen(vFn("timestamp.value", vi(-1500)), "seconds"), vi(58), ""},
		{"nanos before the epoch", vThen(vFn("timestamp.value", vi(-1500)), "nanos"), vi(500_000_000), ""},
		{"year(1)", vCall(now, "year", vi(1)), Value{}, "Expected: timestamp.year()."},
		{"an int has no hours()", vCall(vi(1), "hours"), Value{}, "Function not found error: Name: [hours]."},
		{"no such method", vCall(now, "week"), Value{}, "Name: [week]."},
		// timestamp namespace: reference, Pyric builtins-time-and-math, simulator-language-semantics.
		{"timestamp.date", vFn("timestamp.date", vi(1984), vi(1), vi(2)), vts("1984-01-02T00:00:00Z"), ""},
		{"a leap day", vFn("timestamp.date", vi(2024), vi(2), vi(29)), vts("2024-02-29T00:00:00Z"), ""},
		{"month 13", vFn("timestamp.date", vi(2024), vi(13), vi(1)), Value{}, "Date is not valid. year: 2024, month: 13 day: 1."},
		{"Feb 30", vFn("timestamp.date", vi(2024), vi(2), vi(30)), Value{}, "Date is not valid. year: 2024, month: 2 day: 30."},
		{"year 0", vFn("timestamp.date", vi(0), vi(1), vi(1)), Value{}, "Timestamp should be between 0001-01-01T00.00.00Z and 9999-12-31T23.59.59Z. year: 0."},
		{"timestamp.date of strings", vFn("timestamp.date", vs("2024"), vi(1), vi(1)), Value{}, "Received: timestamp.date(string, int, int). Expected: timestamp.date(int, int, int)."},
		{"timestamp.value", vFn("timestamp.value", vi(1749995130250)), now, ""},
		{"timestamp.value(0)", vFn("timestamp.value", vi(0)), vts("1970-01-01T00:00:00Z"), ""},
		{"timestamp.value past 9999", vFn("timestamp.value", vi(math.MaxInt64)), Value{}, "Timestamp is not valid."},
		{"timestamp.value of RFC 3339", vFn("timestamp.value", vs("2025-06-15T15:45:30.25+02:00")), now, ""},
		{"timestamp.value rolls a month over", vFn("timestamp.value", vs("2020-13-02T00:00:00Z")), vts("2021-01-02T00:00:00Z"), ""},
		{"timestamp.value without a zone", vFn("timestamp.value", vs("2020-01-02T03:04:05")), Value{}, "Timestamp is not valid."},
		{"timestamp.value of a bool", vFn("timestamp.value", vb(true)), Value{}, "Received: timestamp.value(bool). Expected: timestamp.value(int), timestamp.value(string)."},
		{"timestamp.value()", vFn("timestamp.value"), Value{}, "Incorrect number of arguments. Received: 0."},
	})
}

func TestDurations(t *testing.T) {
	runValueCases(t, []valueCase{
		// Pyric duration-and-latlng and stdlib-timestamp-duration.
		{"seconds of 1500ms", vThen(vFn("duration.value", vi(1500), vs("ms")), "seconds"), vi(1), ""},
		{"nanos of 1500ms", vThen(vFn("duration.value", vi(1500), vs("ms")), "nanos"), vi(500_000_000), ""},
		{"90m in seconds", vThen(vFn("duration.value", vi(90), vs("m")), "seconds"), vi(5400), ""},
		{"duration.time nanos", vThen(vFn("duration.time", vi(1), vi(30), vi(0), vi(5)), "nanos"), vi(5), ""},
		{"negative seconds and nanos", vThen(vFn("duration.value", vi(-1500), vs("ms")), "nanos"), vi(-500_000_000), ""},
		{"w", vFn("duration.value", vi(1), vs("w")), vdur(7 * 24 * time.Hour), ""},
		{"d", vFn("duration.value", vi(1), vs("d")), vdur(24 * time.Hour), ""},
		{"h", vFn("duration.value", vi(2), vs("h")), vdur(2 * time.Hour), ""},
		{"s", vFn("duration.value", vi(-3), vs("s")), vdur(-3 * time.Second), ""},
		{"ns", vFn("duration.value", vi(1_500_000_001), vs("ns")), vdur(1500*time.Millisecond + 1), ""},
		{"an unknown unit, as the emulator reports it", vFn("duration.value", vi(1), vs("y")), Value{}, "Invalid regular expression pattern. Pattern: y."},
		{"past 10,000 years", vFn("duration.value", vi(math.MaxInt64), vs("w")), Value{}, "Duration is not valid."},
		{"just within 10,000 years", vThen(vFn("duration.value", vi(maxDurationSeconds), vs("s")), "seconds"), vi(maxDurationSeconds), ""},
		{"ms past int64 nanoseconds", vThen(vFn("duration.value", vi(300_000_000_000_123), vs("ms")), "nanos"), vi(123_000_000), ""},
		{"ms of the largest int", vFn("duration.value", vi(math.MaxInt64), vs("ms")), Value{}, "Duration is not valid."},
		{"duration.value of floats", vFn("duration.value", vf(1), vs("s")), Value{}, "Received: duration.value(float, string). Expected: duration.value(int, string)."},
		{"duration.abs", vFn("duration.abs", vdur(-3*time.Second)), vdur(3 * time.Second), ""},
		{"duration.abs of a positive", vFn("duration.abs", vdur(time.Second)), vdur(time.Second), ""},
		{"duration.abs(int)", vFn("duration.abs", vi(1)), Value{}, "Received: duration.abs(int). Expected: duration.abs(duration)."},
		{"duration.time", vFn("duration.time", vi(1), vi(0), vi(0), vi(0)), vdur(time.Hour), ""},
		{"duration.time with mixed signs", vFn("duration.time", vi(1), vi(-30), vi(0), vi(0)), vdur(30 * time.Minute), ""},
		{"duration.time overflowing", vFn("duration.time", vi(math.MaxInt64), vi(0), vi(0), vi(0)), Value{}, "Duration is not valid."},
		{"duration.time of three", vFn("duration.time", vi(1), vi(0), vi(0)), Value{}, "Incorrect number of arguments. Received: 3. Expected: duration.time(int, int, int, int)."},
		{"seconds(1)", vCall(vdur(time.Second), "seconds", vi(1)), Value{}, "Expected: duration.seconds()."},
		{"no such method", vCall(vdur(time.Second), "hours"), Value{}, "Name: [hours]."},
		{"1h + 30m == 90m", vOpOn(vFn("duration.value", vi(1), vs("h")), tokPlus, vFn("duration.value", vi(30), vs("m"))), vdur(90 * time.Minute), ""},
	})
}

func TestTimestampArithmeticRanges(t *testing.T) {
	m := newTestMeter()
	// The widest timestamps and durations stay exact: no time.Duration in between.
	first := vts("0001-01-01T00:00:00Z")
	last := vts("9999-12-31T23:59:59.999999999Z")
	span, err := binary(m, tokMinus, last, first)
	if err != nil {
		t.Fatal(err)
	}
	if d := span.dur(); d.seconds != maxTimestampSeconds-minTimestampSeconds || d.nanos != 999_999_999 {
		t.Errorf("9999 - 0001 = %+v", d)
	}
	back, err := binary(m, tokMinus, last, span)
	if err != nil || !sameValue(back, first) {
		t.Errorf("last - span = %s, %v; want the first instant", showValue(back), err)
	}
	if _, ok := span.AsDuration(); ok {
		t.Error("a 10,000-year duration reads as a time.Duration")
	}
}
