package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// The ground truth is plain JSON trees, built independently of the server's own data model
// (core.Node), so a bug there cannot hide in the check: a value is nil (null), a float64, a
// string, a bool or a map[string]any that is never empty. Values carry no priorities.

// split turns "a/b/c" into its keys; "" and "/" are the root.
func split(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

// clone copies a tree deeply.
func clone(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := make(map[string]any, len(m))
	for k, c := range m {
		out[k] = clone(c)
	}
	return out
}

// normalize turns empty objects into null, as the database does.
func normalize(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	for k, c := range m {
		if c = normalize(c); c != nil {
			out[k] = c
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// setAt returns root with v (nil deletes) written at path: a write below a leaf turns the
// leaf into an object, and an object left empty disappears, all the way up.
func setAt(root any, path []string, v any) any {
	v = normalize(clone(v))
	if len(path) == 0 {
		return v
	}
	m, ok := root.(map[string]any)
	if !ok {
		if v == nil {
			return root // deleting below a leaf or below nothing changes nothing
		}
		m = map[string]any{}
	}
	out := maps.Clone(m)
	if child := setAt(m[path[0]], path[1:], v); child == nil {
		delete(out, path[0])
	} else {
		out[path[0]] = child
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// getAt returns the value at path.
func getAt(root any, path []string) any {
	for _, k := range path {
		m, ok := root.(map[string]any)
		if !ok {
			return nil
		}
		root = m[k]
	}
	return root
}

// equal compares two trees.
func equal(a, b any) bool { return reflect.DeepEqual(normalize(a), normalize(b)) }

// decode parses JSON read from the server into a tree.
func decode(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return normalize(v), nil
}

// diff describes how two trees differ, for reports: the first few differing paths.
func diff(want, got any) string {
	var out []string
	var walk func(path string, a, b any)
	walk = func(path string, a, b any) {
		if len(out) >= 8 {
			return
		}
		am, aok := a.(map[string]any)
		bm, bok := b.(map[string]any)
		if aok && bok {
			keys := slices.Sorted(maps.Keys(am))
			for k := range bm {
				if _, ok := am[k]; !ok {
					keys = append(keys, k)
				}
			}
			for _, k := range keys {
				walk(path+"/"+k, am[k], bm[k])
			}
			return
		}
		if !reflect.DeepEqual(a, b) {
			out = append(out, fmt.Sprintf("%s: want %s, got %s", path, short(a), short(b)))
		}
	}
	walk("", normalize(want), normalize(got))
	return strings.Join(out, "; ")
}

func short(v any) string {
	b, _ := json.Marshal(v)
	if len(b) > 120 {
		return string(b[:117]) + "..."
	}
	return string(b)
}
