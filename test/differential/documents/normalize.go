package documents

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// volatile are the keys whose values change on every run; lib.mjs (normalise) replaces the same
// keys in the recordings.
var volatile = []string{"readTime", "updateTime", "createTime", "commitTime", "executionDuration", "expireTime", "nextPageToken"}

var (
	rfc3339  = regexp.MustCompile(`^\d{4}-\d\d-\d\dT`)
	duration = regexp.MustCompile(`^\d+(\.\d+)?s$`)
)

// placeholder is lib.mjs's placeholder for a volatile value: its key and type, not its value.
func placeholder(key string, v any) string {
	if m, ok := v.(map[string]any); ok {
		if _, ok := m["seconds"]; ok {
			return "<" + key + ": Timestamp>"
		}
	}
	if s, ok := v.(string); ok {
		switch {
		case strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">"):
			return s // already a placeholder
		case rfc3339.MatchString(s):
			return "<" + key + ": RFC 3339>"
		case duration.MatchString(s):
			return "<" + key + ": Duration>"
		}
	}
	return "<" + key + ">"
}

// Normalise prepares a live answer for comparison with a recording, as lib.mjs normalised the
// recording: volatile values become placeholders, a transaction id becomes "<transaction>", and
// the live project id becomes the recorded one where it names a project.
func Normalise(v any, liveProject, recordedProject string) any {
	r := strings.NewReplacer(
		"projects/"+liveProject+"/", "projects/"+recordedProject+"/",
		"project "+liveProject+" ", "project "+recordedProject+" ",
		"project="+liveProject+" ", "project="+recordedProject+" ",
		"project/"+liveProject+"/", "project/"+recordedProject+"/",
	)
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			o := make(map[string]any, len(x))
			for k, y := range x {
				switch {
				case y != nil && slices.Contains(volatile, k):
					o[k] = placeholder(k, y)
				case k == "transaction":
					if _, ok := y.(string); ok {
						o[k] = "<transaction>"
						continue
					}
					o[k] = walk(y)
				default:
					o[k] = walk(y)
				}
			}
			return o
		case []any:
			o := make([]any, len(x))
			for i, y := range x {
				o[i] = walk(y)
			}
			return o
		case string:
			return r.Replace(x)
		default:
			return v
		}
	}
	return walk(v)
}

// Diff lists where got differs from want, one line per difference, each naming its JSON path
// ("$" is the root). It is empty when they are equal.
func Diff(want, got any) []string {
	var out []string
	var walk func(path string, w, g any)
	walk = func(path string, w, g any) {
		switch wv := w.(type) {
		case map[string]any:
			gv, ok := g.(map[string]any)
			if !ok {
				out = append(out, fmt.Sprintf("%s: want an object, got %s", path, short(g)))
				return
			}
			for _, k := range slices.Sorted(maps.Keys(wv)) {
				if _, ok := gv[k]; !ok {
					out = append(out, fmt.Sprintf("%s.%s: missing, want %s", path, k, short(wv[k])))
					continue
				}
				walk(path+"."+k, wv[k], gv[k])
			}
			for _, k := range slices.Sorted(maps.Keys(gv)) {
				if _, ok := wv[k]; !ok {
					out = append(out, fmt.Sprintf("%s.%s: unexpected %s", path, k, short(gv[k])))
				}
			}
		case []any:
			gv, ok := g.([]any)
			if !ok {
				out = append(out, fmt.Sprintf("%s: want an array, got %s", path, short(g)))
				return
			}
			if len(gv) != len(wv) {
				out = append(out, fmt.Sprintf("%s: %d elements, want %d", path, len(gv), len(wv)))
			}
			for i := range min(len(wv), len(gv)) {
				walk(fmt.Sprintf("%s[%d]", path, i), wv[i], gv[i])
			}
		default:
			if w != g {
				out = append(out, fmt.Sprintf("%s: %s, want %s", path, short(g), short(w)))
			}
		}
	}
	walk("$", want, got)
	return out
}

func short(v any) string {
	s := fmt.Sprintf("%#v", v)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
