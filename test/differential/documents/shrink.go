package documents

import (
	"maps"
	"slices"
)

// Shrink reduces a JSON value (a request body) while fails keeps reporting a difference: it tries
// one reduction at a time (drop an array element or an object key, halve a string, replace a
// number by 0 or a value by null) and keeps the first that still fails, until none does or
// budget calls of fails are spent. The result is one-minimal: no single reduction of it still
// fails. fails is not called on v itself, which the caller knows fails.
func Shrink(v any, fails func(any) bool, budget int) any {
	for budget > 0 {
		shrunk := false
		for _, c := range reductions(v) {
			if budget == 0 {
				break
			}
			budget--
			if fails(c) {
				v, shrunk = c, true
				break
			}
		}
		if !shrunk {
			break
		}
	}
	return v
}

// reductions lists the values one reduction smaller than v, the largest cuts first.
func reductions(v any) []any {
	var out []any
	switch x := v.(type) {
	case map[string]any:
		keys := slices.Sorted(maps.Keys(x))
		for _, k := range keys {
			c := maps.Clone(x)
			delete(c, k)
			out = append(out, c)
		}
		for _, k := range keys {
			for _, r := range reductions(x[k]) {
				c := maps.Clone(x)
				c[k] = r
				out = append(out, c)
			}
		}
	case []any:
		if len(x) > 1 {
			out = append(out, slices.Clone(x[:len(x)/2]), slices.Clone(x[len(x)/2:]))
		}
		for i := range x {
			out = append(out, slices.Delete(slices.Clone(x), i, i+1))
		}
		for i := range x {
			for _, r := range reductions(x[i]) {
				c := slices.Clone(x)
				c[i] = r
				out = append(out, c)
			}
		}
	case string:
		if r := []rune(x); len(r) > 0 {
			out = append(out, string(r[:len(r)/2]))
		}
	case float64:
		if x != 0 {
			out = append(out, float64(0))
		}
	case bool:
		if x {
			out = append(out, false)
		}
	}
	if v != nil {
		out = append(out, nil)
	}
	return out
}
