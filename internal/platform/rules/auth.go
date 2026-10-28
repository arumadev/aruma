package rules

import (
	"encoding/json"
	"math"
	"strconv"
	"time"
)

// Auth returns request.auth for a signed-in user: a map of uid and token, the token's claims
// converted to values. A number that is whole and fits an int64 is an int, 2 and 2.0 alike, and
// any other number a float: so the Rules Test API reads a token (the review's probes,
// 2026-10-24: a claim level of 2 or 2.0 is int, 2.5 float, nested ones alike), and so exp, iat
// and auth_time are ints. Deployed rules, which read a real ID token, are not measured yet: the
// rule is provisional (docs/reference/compatibility.md). Claims are what a decoded JWT holds: nil, bools, strings,
// numbers (any Go integer or float type, or json.Number), []any and map[string]any, at any
// depth; a time.Time is a timestamp; a claim of any other type is left out. A request without a
// user has Null() instead.
func Auth(uid string, token map[string]any) Value {
	claims := make(map[string]Value, len(token))
	for k, v := range token {
		if val, ok := claimValue(v); ok {
			claims[k] = val
		}
	}
	return Map(map[string]Value{"uid": String(uid), "token": Map(claims)})
}

// claimValue converts one claim.
func claimValue(v any) (Value, bool) {
	number := func(f float64) (Value, bool) {
		if f == math.Trunc(f) && f >= math.MinInt64 && f < math.MaxInt64 {
			return Int(int64(f)), true
		}
		return Float(f), true
	}
	integer := func(i int64) (Value, bool) { return Int(i), true }
	switch x := v.(type) {
	case nil:
		return Null(), true
	case bool:
		return Bool(x), true
	case string:
		return String(x), true
	case float64:
		return number(x)
	case float32:
		return number(float64(x))
	case int:
		return integer(int64(x))
	case int32:
		return integer(int64(x))
	case int64:
		return integer(x)
	case uint32:
		return integer(int64(x))
	case uint64:
		if x > math.MaxInt64 {
			return number(float64(x))
		}
		return integer(int64(x))
	case json.Number:
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return integer(i)
		}
		f, err := x.Float64()
		if err != nil {
			return Value{}, false
		}
		return number(f)
	case time.Time:
		return Timestamp(x), true
	case []any:
		out := make([]Value, 0, len(x))
		for _, e := range x {
			if ev, ok := claimValue(e); ok {
				out = append(out, ev)
			}
		}
		return List(out), true
	case []string:
		out := make([]Value, len(x))
		for i, s := range x {
			out[i] = String(s)
		}
		return List(out), true
	case map[string]any:
		out := make(map[string]Value, len(x))
		for k, e := range x {
			if ev, ok := claimValue(e); ok {
				out[k] = ev
			}
		}
		return Map(out), true
	}
	return Value{}, false
}
