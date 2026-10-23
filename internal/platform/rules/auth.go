package rules

import (
	"encoding/json"
	"math"
	"strconv"
	"time"
)

// intClaims are the token's claims that are whole numbers of seconds, which request.auth.token
// holds as ints; every other number is a float (ADR-021 §2.3; the oracle checks it).
var intClaims = map[string]bool{"exp": true, "iat": true, "auth_time": true}

// Auth returns request.auth for a signed-in user: a map of uid and token, the token's claims
// converted to values. exp, iat and auth_time are ints when they are whole numbers, and every
// other number is a float. Claims are what a decoded JWT holds: nil, bools, strings, numbers
// (any Go integer or float type, or json.Number), []any and map[string]any, at any depth;
// a time.Time is a timestamp; a claim of any other type is left out. A request without a user
// has Null() instead.
func Auth(uid string, token map[string]any) Value {
	claims := make(map[string]Value, len(token))
	for k, v := range token {
		val, ok := claimValue(v, intClaims[k])
		if ok {
			claims[k] = val
		}
	}
	return Map(map[string]Value{"uid": String(uid), "token": Map(claims)})
}

// claimValue converts one claim; asInt makes a whole number an int.
func claimValue(v any, asInt bool) (Value, bool) {
	number := func(f float64) (Value, bool) {
		if asInt && f == math.Trunc(f) && f >= math.MinInt64 && f < math.MaxInt64 {
			return Int(int64(f)), true
		}
		return Float(f), true
	}
	integer := func(i int64) (Value, bool) {
		if asInt {
			return Int(i), true
		}
		return Float(float64(i)), true
	}
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
			if ev, ok := claimValue(e, false); ok {
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
			if ev, ok := claimValue(e, false); ok {
				out[k] = ev
			}
		}
		return Map(out), true
	}
	return Value{}, false
}
