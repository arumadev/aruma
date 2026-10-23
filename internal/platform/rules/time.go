package rules

import (
	"strings"
	"time"
)

// Timestamps span the years 1 to 9999 in UTC, to the nanosecond; durations ±10,000 years
// (315,576,000,000 seconds), their seconds and nanoseconds of one sign. A result outside either
// range is an error with the official message (hidane value.rs, from the emulator).
const (
	minTimestampSeconds = -62_135_596_800 // 0001-01-01T00:00:00Z
	maxTimestampSeconds = 253_402_300_799 // 9999-12-31T23:59:59Z
	maxDurationSeconds  = 315_576_000_000
	nanosPerSecond      = 1_000_000_000
)

// timestampAt returns the timestamp seconds and nanos (any sign, any size below 2^62) after the
// epoch, or the official error outside years 1 to 9999.
func timestampAt(seconds, nanos int64) (Value, error) {
	seconds += floorDiv(nanos, nanosPerSecond)
	nanos = floorMod(nanos, nanosPerSecond)
	if seconds < minTimestampSeconds || seconds > maxTimestampSeconds {
		return Value{}, errTimestamp()
	}
	return Timestamp(time.Unix(seconds, nanos)), nil
}

// durationOf returns the duration of seconds and nanos (any signs, nanos below 2^62), or the
// official error past 10,000 years.
func durationOf(seconds, nanos int64) (Value, error) {
	seconds, ok := addInts(seconds, nanos/nanosPerSecond)
	nanos %= nanosPerSecond
	switch {
	case seconds > 0 && nanos < 0:
		seconds, nanos = seconds-1, nanos+nanosPerSecond
	case seconds < 0 && nanos > 0:
		seconds, nanos = seconds+1, nanos-nanosPerSecond
	}
	if !ok || seconds > maxDurationSeconds || seconds < -maxDurationSeconds {
		return Value{}, errDuration()
	}
	return Value{kind: KindDuration, x: duration{seconds: seconds, nanos: int32(nanos)}}, nil
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func floorMod(a, b int64) int64 { return a - floorDiv(a, b)*b }

func compareDurations(a, b duration) int {
	if c := compareInts(a.seconds, b.seconds); c != 0 {
		return c
	}
	return compareInts(int64(a.nanos), int64(b.nanos))
}

// addDurations returns a + sign·b.
func addDurations(a, b duration, sign int64) (Value, error) {
	return durationOf(a.seconds+sign*b.seconds, int64(a.nanos)+sign*int64(b.nanos))
}

// shiftTimestamp returns t + sign·d.
func shiftTimestamp(t time.Time, d duration, sign int64) (Value, error) {
	return timestampAt(t.Unix()+sign*d.seconds, int64(t.Nanosecond())+sign*int64(d.nanos))
}

// timestampsApart returns a - b.
func timestampsApart(a, b time.Time) (Value, error) {
	return durationOf(a.Unix()-b.Unix(), int64(a.Nanosecond())-int64(b.Nanosecond()))
}

// timestampMethod calls t.name(): the parts of a timestamp in UTC. seconds() and nanos() are
// those of the minute and the second, not since the epoch, and toMillis() is the epoch's
// milliseconds (Pyric timestamp-component-accessors); dayOfWeek() is 1 for Monday to 7 for
// Sunday (Pyric stdlib-timestamp-duration: 2025-06-15 is 7); date() is the day's midnight and
// time() the duration since it.
func timestampMethod(m *meter, t time.Time, name string, args []Value) (Value, error) {
	switch name {
	case "date", "time", "toMillis", "year", "month", "day", "hours", "minutes", "seconds",
		"nanos", "dayOfWeek", "dayOfYear":
	default:
		return Value{}, errNotFound(name)
	}
	if len(args) != 0 {
		return Value{}, mismatch("timestamp."+name, args, []string{})
	}
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	switch name {
	case "date":
		return Timestamp(midnight), nil
	case "time":
		return timestampsApart(t, midnight)
	case "toMillis":
		return Int(t.Unix()*1000 + int64(t.Nanosecond()/1_000_000)), nil
	case "year":
		return Int(int64(t.Year())), nil
	case "month":
		return Int(int64(t.Month())), nil
	case "day":
		return Int(int64(t.Day())), nil
	case "hours":
		return Int(int64(t.Hour())), nil
	case "minutes":
		return Int(int64(t.Minute())), nil
	case "seconds":
		return Int(int64(t.Second())), nil
	case "nanos":
		return Int(int64(t.Nanosecond())), nil
	case "dayOfWeek":
		return Int(int64((t.Weekday()+6)%7 + 1)), nil
	}
	return Int(int64(t.YearDay())), nil
}

// durationMethod calls d.name(): its whole seconds and the nanoseconds left over, both of the
// duration's sign (duration.value(1500, 'ms') has 1 and 500000000: Pyric duration-and-latlng).
func durationMethod(d duration, name string, args []Value) (Value, error) {
	if name != "seconds" && name != "nanos" {
		return Value{}, errNotFound(name)
	}
	if len(args) != 0 {
		return Value{}, mismatch("duration."+name, args, []string{})
	}
	if name == "seconds" {
		return Int(d.seconds), nil
	}
	return Int(int64(d.nanos)), nil
}

// durationUnits are what duration.value takes, in nanoseconds.
var durationUnits = map[string]int64{
	"w": 7 * 86_400 * nanosPerSecond, "d": 86_400 * nanosPerSecond, "h": 3_600 * nanosPerSecond,
	"m": 60 * nanosPerSecond, "s": nanosPerSecond, "ms": 1_000_000, "ns": 1,
}

// durationFunction calls duration.abs, duration.time or duration.value.
func durationFunction(name string, args []Value) (Value, error) {
	switch name {
	case "duration.abs":
		if len(args) != 1 || args[0].kind != KindDuration {
			return Value{}, mismatch(name, args, []string{"duration"})
		}
		d := args[0].dur()
		if d.seconds < 0 || d.nanos < 0 {
			d = duration{-d.seconds, -d.nanos}
		}
		return Value{kind: KindDuration, x: d}, nil
	case "duration.time":
		form := []string{"int", "int", "int", "int"}
		if len(args) != 4 {
			return Value{}, mismatch(name, args, form)
		}
		for _, a := range args {
			if a.kind != KindInt {
				return Value{}, mismatch(name, args, form)
			}
		}
		h, mi, s, n := args[0].integer(), args[1].integer(), args[2].integer(), args[3].integer()
		hs, ok1 := mulInts(h, 3600)
		ms, ok2 := mulInts(mi, 60)
		sum, ok3 := addInts(hs, ms)
		sum, ok4 := addInts(sum, s)
		if !ok1 || !ok2 || !ok3 || !ok4 {
			return Value{}, errDuration()
		}
		return durationOf(sum, n)
	case "duration.value":
		if len(args) != 2 || args[0].kind != KindInt || args[1].kind != KindString {
			return Value{}, mismatch(name, args, []string{"int", "string"})
		}
		n, unit := args[0].integer(), args[1].str()
		scale, ok := durationUnits[unit]
		if !ok {
			// The emulator reports an unknown unit this way (hidane library.rs).
			return Value{}, errRegex(unit)
		}
		if scale >= nanosPerSecond {
			s, ok := mulInts(n, scale/nanosPerSecond)
			if !ok {
				return Value{}, errDuration()
			}
			return durationOf(s, 0)
		}
		// ms and ns: n·scale may not fit, so split n into seconds first.
		per := nanosPerSecond / scale
		return durationOf(n/per, n%per*scale)
	}
	return Value{}, errNotFound(name)
}

// timestampFunction calls timestamp.date or timestamp.value.
func timestampFunction(name string, args []Value) (Value, error) {
	switch name {
	case "timestamp.date":
		form := []string{"int", "int", "int"}
		if len(args) != 3 {
			return Value{}, mismatch(name, args, form)
		}
		for _, a := range args {
			if a.kind != KindInt {
				return Value{}, mismatch(name, args, form)
			}
		}
		y, mo, d := args[0].integer(), args[1].integer(), args[2].integer()
		if y < 1 || y > 9999 {
			return Value{}, errorf("Timestamp should be between 0001-01-01T00.00.00Z and 9999-12-31T23.59.59Z. year: %d.", y)
		}
		if mo < 1 || mo > 12 || d < 1 || d > int64(daysIn(int(y), time.Month(mo))) {
			// Pyric simulator-language-semantics: "Date is not valid. year: 2024, month: 2 day: 30."
			return Value{}, errorf("Date is not valid. year: %d, month: %d day: %d.", y, mo, d)
		}
		return Timestamp(time.Date(int(y), time.Month(mo), int(d), 0, 0, 0, 0, time.UTC)), nil
	case "timestamp.value":
		if len(args) != 1 {
			return Value{}, mismatch(name, args, []string{"int"}, []string{"string"})
		}
		switch a := args[0]; a.kind {
		case KindInt:
			ms := a.integer()
			return timestampAt(floorDiv(ms, 1000), floorMod(ms, 1000)*1_000_000)
		case KindString:
			seconds, nanos, ok := parseRFC3339(a.str())
			if !ok {
				return Value{}, errTimestamp()
			}
			return timestampAt(seconds, nanos)
		}
		return Value{}, mismatch(name, args, []string{"int"}, []string{"string"})
	}
	return Value{}, errNotFound(name)
}

func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// parseRFC3339 reads timestamp.value's string: yyyy-MM-ddTHH:mm:ss, a fraction, and Z or ±HH:mm.
// As the emulator does (hidane time.rs), a field too large runs into the next one and digits of
// the fraction past the ninth are dropped. The docs give only the int form; production is not
// recorded for the string one.
func parseRFC3339(s string) (seconds, nanos int64, ok bool) {
	date, rest, found := strings.Cut(s, "T")
	if !found {
		return 0, 0, false
	}
	ymd := strings.Split(date, "-")
	if len(ymd) != 3 {
		return 0, 0, false
	}
	var offset int64
	clock := ""
	if c, isZ := strings.CutSuffix(rest, "Z"); isZ {
		clock = c
	} else {
		at := strings.LastIndexAny(rest, "+-")
		if at < 0 {
			return 0, 0, false
		}
		hh, mm, found := strings.Cut(rest[at+1:], ":")
		h, ok1 := field(hh)
		mi, ok2 := field(mm)
		if !found || len(hh) != 2 || len(mm) != 2 || !ok1 || !ok2 {
			return 0, 0, false
		}
		offset = h*3600 + mi*60
		if rest[at] == '-' {
			offset = -offset
		}
		clock = rest[:at]
	}
	hms, fraction, _ := strings.Cut(clock, ".")
	parts := strings.Split(hms, ":")
	if len(parts) != 3 {
		return 0, 0, false
	}
	var f [6]int64
	for i, p := range append(ymd, parts...) {
		v, ok := field(p)
		if !ok {
			return 0, 0, false
		}
		f[i] = v
	}
	for i := 0; i < len(fraction); i++ {
		if fraction[i] < '0' || fraction[i] > '9' {
			return 0, 0, false
		}
	}
	fraction = (fraction + "000000000")[:9]
	nanos, _ = field(fraction)
	months := f[0]*12 + f[1] - 1
	day := time.Date(int(floorDiv(months, 12)), time.Month(floorMod(months, 12)+1), 1, 0, 0, 0, 0, time.UTC)
	seconds = day.Unix() + (f[2]-1)*86_400 + f[3]*3600 + f[4]*60 + f[5] - offset
	return seconds, nanos, true
}

// field reads one to nine decimal digits.
func field(s string) (int64, bool) {
	if s == "" || len(s) > 9 {
		return 0, false
	}
	var n int64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int64(s[i]-'0')
	}
	return n, true
}
