package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Source is the stored data that rules read through root and data, and that newData overlays.
// Core implements it over Postgres; an evaluation asks only for the paths its rules touch, and
// asks each path at most once (the engine caches answers for one check). Paths are lists of
// keys from the root; the empty path is the root.
type Source interface {
	// Get reports what is stored at path.
	Get(ctx context.Context, path []string) (Node, error)
	// HasChildExcept reports whether the node at path has a child whose key is not in keys.
	// The engine asks it only for a path that has children, when a write deletes some of them
	// and a rule needs to know whether the node survives.
	HasChildExcept(ctx context.Context, path []string, keys []string) (bool, error)
}

// Node is what a Source finds at a path: a leaf value, a node with children, or nothing.
type Node struct {
	// Value is a leaf's value: a string, a float64 or a bool. It is nil for a node with
	// children and for a path that holds nothing.
	Value any
	// HasChildren is true for a node with children.
	HasChildren bool
}

func (n Node) exists() bool { return n.Value != nil || n.HasChildren }

// MemorySource is a Source over a value held in memory, for tests and for small tools. The
// value is what encoding/json produces: maps, slices, strings, numbers, booleans and nil.
type MemorySource struct {
	root any
}

// NewMemorySource returns a Source holding v. Arrays become objects keyed "0", "1"...,
// empty objects and nulls vanish, and ".priority" is dropped, as when Firebase stores a value.
func NewMemorySource(v any) (*MemorySource, error) {
	n, err := normalize(v, 0)
	if err != nil {
		return nil, err
	}
	return &MemorySource{root: n}, nil
}

func (m *MemorySource) Get(_ context.Context, path []string) (Node, error) {
	return nodeOf(lookup(m.root, path)), nil
}

func (m *MemorySource) HasChildExcept(_ context.Context, path []string, keys []string) (bool, error) {
	obj, ok := lookup(m.root, path).(map[string]any)
	if !ok {
		return false, nil
	}
	for k := range obj {
		if !contains(keys, k) {
			return true, nil
		}
	}
	return false, nil
}

func lookup(v any, path []string) any {
	for _, k := range path {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = obj[k]
	}
	return v
}

func nodeOf(v any) Node {
	if _, ok := v.(map[string]any); ok {
		return Node{HasChildren: true}
	}
	return Node{Value: v}
}

func contains(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

// maxValueDepth bounds the nesting of a written value; Firebase allows 32 levels of keys.
const maxValueDepth = 64

// normalize turns a JSON-like value into the form the engine reads: nil, bool, float64,
// string, or a non-empty map of the same. Server values ({".sv": ...}) must have been resolved
// by the caller, as Firebase's rules see the resolved value.
func normalize(v any, depth int) (any, error) {
	if depth > maxValueDepth {
		return nil, fmt.Errorf("rules: value nested deeper than %d levels", maxValueDepth)
	}
	switch v := v.(type) {
	case nil, bool, string:
		return v, nil
	case float64:
		return v, nil
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return nil, fmt.Errorf("rules: number %q: %w", v, err)
		}
		return f, nil
	case map[string]any:
		if inner, ok := v[".value"]; ok {
			return normalize(inner, depth)
		}
		out := map[string]any{}
		for k, c := range v {
			if k == ".priority" {
				continue
			}
			n, err := normalize(c, depth+1)
			if err != nil {
				return nil, err
			}
			if n != nil {
				out[k] = n
			}
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	case []any:
		out := map[string]any{}
		for i, c := range v {
			n, err := normalize(c, depth+1)
			if err != nil {
				return nil, err
			}
			if n != nil {
				out[strconv.Itoa(i)] = n
			}
		}
		if len(out) == 0 {
			return nil, nil
		}
		return out, nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), nil
	case reflect.Float32:
		f := rv.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("rules: number %v is not JSON", f)
		}
		return f, nil
	}
	return nil, fmt.Errorf("rules: unsupported value of type %T", v)
}

// world is the data one check reads: the stored data through a cached Source, and the data as
// it would be after the write, newData, as the stored data overlaid with the changes.
type world struct {
	ctx      context.Context
	src      Source
	cache    map[string]Node // stored data by path
	newCache map[string]Node // newData by path
	changes  []change
	// written indexes changes by path, and under lists, for each ancestor of a change, the
	// child keys that lead to changes, so newData costs the depth of a path, not the number
	// of changes.
	written map[string]int
	under   map[string][]string
	leads   map[string]bool // paths in under, as keys
	// err is the first Source failure. The check that meets one returns it instead of a
	// verdict: a database that cannot be read is not a denial.
	err error
}

type change struct {
	path  []string
	value any // normalized; nil deletes
}

func pathKey(path []string) string { return strings.Join(path, "/") }

func (w *world) addChange(path []string, value any) {
	if w.written == nil {
		w.written, w.under, w.leads = map[string]int{}, map[string][]string{}, map[string]bool{}
	}
	w.written[pathKey(path)] = len(w.changes)
	w.changes = append(w.changes, change{path: path, value: value})
	for i := 0; i < len(path); i++ {
		if child := pathKey(path[:i+1]); !w.leads[child] {
			w.leads[child] = true
			key := pathKey(path[:i])
			w.under[key] = append(w.under[key], path[i])
		}
	}
}

// old reads the stored data at path.
func (w *world) old(path []string) Node {
	if w.err != nil {
		return Node{}
	}
	key := pathKey(path)
	if n, ok := w.cache[key]; ok {
		return n
	}
	n, err := w.src.Get(w.ctx, path)
	if err != nil {
		w.err = err
		return Node{}
	}
	w.cache[key] = n
	return n
}

// fresh reads newData at path.
func (w *world) fresh(path []string) Node {
	key := pathKey(path)
	if n, ok := w.newCache[key]; ok {
		return n
	}
	n := w.overlay(path)
	if w.err == nil {
		if w.newCache == nil {
			w.newCache = map[string]Node{}
		}
		w.newCache[key] = n
	}
	return n
}

func (w *world) overlay(path []string) Node {
	for i := len(path); i >= 0; i-- {
		if j, ok := w.written[pathKey(path[:i])]; ok {
			return nodeOf(lookup(w.changes[j].value, path[i:]))
		}
	}
	below := w.under[pathKey(path)] // the keys under path that lead to changes
	if below == nil {
		return w.old(path)
	}
	survives := false
	for _, k := range below {
		if w.fresh(append(path[:len(path):len(path)], k)).exists() {
			survives = true
			break
		}
	}
	o := w.old(path)
	switch {
	case survives:
		return Node{HasChildren: true}
	case o.Value != nil:
		// Deleting below a leaf leaves the leaf as it is.
		return o
	case o.HasChildren:
		if w.err != nil {
			return Node{}
		}
		keys := append([]string(nil), below...)
		sort.Strings(keys)
		other, err := w.src.HasChildExcept(w.ctx, path, keys)
		if err != nil {
			w.err = err
			return Node{}
		}
		return Node{HasChildren: other}
	}
	return Node{}
}
