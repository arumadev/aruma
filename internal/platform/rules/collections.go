package rules

import (
	"slices"
	"strings"
)

// The methods of lists, sets, maps and map diffs. Each charges a unit of work for every element
// it touches, and finds elements through an index (equal.go) rather than by comparing every pair,
// so a call is linear in its operands. Lists compare their elements by E3 and sets by E4.
//
// Which receiver takes which argument is production's (Pyric list-methods-concat-removeall-toset,
// set-algebra-difference-union-intersection, stdlib-sets-and-mapdiff, upload-primitives-boundaries):
// a list's hasAll, hasAny, hasOnly, concat and removeAll take a list, never a set; a set's
// hasAll, hasAny and hasOnly take a set or a list, and its difference, union and intersection a
// set only.

// listMethod calls l.name(args).
func listMethod(m *meter, l []Value, name string, args []Value) (Value, error) {
	listArg := func() ([]Value, error) {
		if len(args) != 1 || args[0].kind != KindList {
			return nil, mismatch("list."+name, args, []string{"list"})
		}
		return args[0].items(), nil
	}
	switch name {
	case "size", "toSet":
		if len(args) != 0 {
			return Value{}, mismatch("list."+name, args, []string{})
		}
		if name == "size" {
			return Int(int64(len(l))), nil
		}
		if err := m.work(int64(len(l))); err != nil {
			return Value{}, err
		}
		members, err := distinct(m, l)
		if err != nil {
			return Value{}, err
		}
		if err := chargeList(m, len(members)); err != nil {
			return Value{}, err
		}
		return setOf(members), nil
	case "hasAll", "hasAny", "hasOnly", "removeAll":
		other, err := listArg()
		if err != nil {
			return Value{}, err
		}
		return listMembership(m, name, l, other)
	case "concat":
		other, err := listArg()
		if err != nil {
			return Value{}, err
		}
		if err := chargeList(m, len(l)+len(other)); err != nil {
			return Value{}, err
		}
		return List(slices.Concat(l, other)), nil
	case "join":
		if len(args) != 1 || args[0].kind != KindString {
			return Value{}, mismatch("list.join", args, []string{"string"})
		}
		return join(m, l, args[0].str())
	}
	return Value{}, errNotFound(name)
}

// listMembership is l.hasAll(other), hasAny, hasOnly or removeAll, by E3.
func listMembership(m *meter, name string, l, other []Value) (Value, error) {
	if err := m.work(int64(len(l) + len(other))); err != nil {
		return Value{}, err
	}
	// hasOnly asks whether every element of l is in other; the rest look up l.
	within, probes := l, other
	if name == "hasOnly" || name == "removeAll" {
		within, probes = other, l
	}
	ix, err := newIndex(m, relList, within)
	if err != nil {
		return Value{}, err
	}
	var kept []Value
	for _, v := range probes {
		found, err := ix.contains(m, v)
		if err != nil {
			return Value{}, err
		}
		switch {
		case name == "hasAny" && found:
			return Bool(true), nil
		case (name == "hasAll" || name == "hasOnly") && !found:
			return Bool(false), nil
		case name == "removeAll" && !found:
			kept = append(kept, v)
		}
	}
	switch name {
	case "hasAny":
		return Bool(false), nil
	case "removeAll":
		if err := chargeList(m, len(kept)); err != nil {
			return Value{}, err
		}
		return List(kept), nil
	}
	return Bool(true), nil
}

// join is l.join(sep): each element as string() writes it, so a null is "null" and a float
// keeps its fraction; a list, a map, bytes or a duration is an error (Pyric listMethod join).
func join(m *meter, l []Value, sep string) (Value, error) {
	if err := m.work(int64(len(l))); err != nil {
		return Value{}, err
	}
	parts := make([]string, len(l))
	size := 0
	for i, v := range l {
		s, err := toString(v)
		if err != nil {
			return Value{}, err
		}
		parts[i] = s
		size += len(s)
	}
	if len(l) > 1 {
		size += (len(l) - 1) * len(sep)
	}
	if err := chargeString(m, size); err != nil {
		return Value{}, err
	}
	return String(strings.Join(parts, sep)), nil
}

// setMethod calls s.name(args).
func setMethod(m *meter, s []Value, name string, args []Value) (Value, error) {
	switch name {
	case "size":
		if len(args) != 0 {
			return Value{}, mismatch("set.size", args, []string{})
		}
		return Int(int64(len(s))), nil
	case "hasAll", "hasAny", "hasOnly":
		if len(args) != 1 || args[0].kind != KindSet && args[0].kind != KindList {
			return Value{}, mismatch("set."+name, args, []string{"set"}, []string{"list"})
		}
		return setMembership(m, name, s, args[0].items())
	case "difference", "union", "intersection":
		if len(args) != 1 || args[0].kind != KindSet {
			return Value{}, mismatch("set."+name, args, []string{"set"})
		}
		return setAlgebra(m, name, s, args[0].items())
	}
	return Value{}, errNotFound(name)
}

// setMembership is s.hasAll(other), hasAny or hasOnly, by E4.
func setMembership(m *meter, name string, s, other []Value) (Value, error) {
	if err := m.work(int64(len(s) + len(other))); err != nil {
		return Value{}, err
	}
	within, probes := s, other
	if name == "hasOnly" {
		within, probes = other, s
	}
	ix, err := newIndex(m, relSet, within)
	if err != nil {
		return Value{}, err
	}
	for _, v := range probes {
		found, err := ix.contains(m, v)
		if err != nil {
			return Value{}, err
		}
		if name == "hasAny" && found {
			return Bool(true), nil
		}
		if name != "hasAny" && !found {
			return Bool(false), nil
		}
	}
	return Bool(name != "hasAny"), nil
}

// setAlgebra is s.difference(other), union or intersection, by E4.
func setAlgebra(m *meter, name string, s, other []Value) (Value, error) {
	if err := m.work(int64(len(s) + len(other))); err != nil {
		return Value{}, err
	}
	var members []Value
	if name == "union" {
		var err error
		if members, err = distinct(m, slices.Concat(s, other)); err != nil {
			return Value{}, err
		}
	} else {
		ix, err := newIndex(m, relSet, other)
		if err != nil {
			return Value{}, err
		}
		for _, v := range s {
			found, err := ix.contains(m, v)
			if err != nil {
				return Value{}, err
			}
			if found == (name == "intersection") {
				members = append(members, v)
			}
		}
	}
	if err := chargeList(m, len(members)); err != nil {
		return Value{}, err
	}
	return setOf(members), nil
}

// mapMethod calls mv.name(args).
func mapMethod(m *meter, mv *mapValue, name string, args []Value) (Value, error) {
	switch name {
	case "size", "keys", "values":
		if len(args) != 0 {
			return Value{}, mismatch("map."+name, args, []string{})
		}
		switch name {
		case "size":
			return Int(int64(len(mv.keys))), nil
		case "keys":
			// Sorted by code point, which is Go's order of UTF-8 strings: production
			// (Pyric keyOrder: "keys() does not order by UTF-16 code unit"). hidane sorts by
			// UTF-16 code unit, as Java compares strings; production wins.
			if err := m.work(int64(len(mv.keys))); err != nil {
				return Value{}, err
			}
			if err := chargeList(m, len(mv.keys)); err != nil {
				return Value{}, err
			}
			keys := slices.Clone(mv.keys)
			slices.Sort(keys)
			out := make([]Value, len(keys))
			for i, k := range keys {
				out[i] = String(k)
			}
			return List(out), nil
		}
		// In the order written (Pyric keyOrder: "values() keeps the written order").
		if err := chargeList(m, len(mv.keys)); err != nil {
			return Value{}, err
		}
		out := make([]Value, len(mv.keys))
		for i, k := range mv.keys {
			out[i] = mv.entries[k]
		}
		return List(out), nil
	case "get":
		if len(args) != 2 || args[0].kind != KindString && args[0].kind != KindList {
			return Value{}, mismatch("map.get", args, []string{"string", "any"}, []string{"list", "any"})
		}
		return mapGet(m, mv, args[0], args[1])
	case "diff":
		if len(args) != 1 || args[0].kind != KindMap {
			return Value{}, mismatch("map.diff", args, []string{"map"})
		}
		return mapDiffOf(m, mv, args[0].mapv())
	}
	return Value{}, errNotFound(name)
}

// mapGet is m.get(key, default): the entry, or default when there is none. A list of keys walks
// nested maps, and default answers as soon as a key is missing or a value on the way is not a
// map (Pyric map-get-string-and-list-form).
func mapGet(m *meter, mv *mapValue, key, def Value) (Value, error) {
	if key.kind == KindString {
		v, ok := mv.entries[key.str()]
		if !ok {
			return def, nil
		}
		return v, reserved(v)
	}
	keys := key.items()
	if err := m.work(int64(len(keys))); err != nil {
		return Value{}, err
	}
	cur := orderedMap(mv.keys, mv.entries)
	for _, k := range keys {
		if k.kind != KindString {
			return Value{}, errType(k.kind.String(), "string")
		}
		if cur.kind != KindMap {
			return def, nil
		}
		next, ok := cur.mapv().entries[k.str()]
		if !ok {
			return def, nil
		}
		if err := reserved(next); err != nil {
			return Value{}, err
		}
		cur = next
	}
	return cur, nil
}

// mapDiffOf is left.diff(right): the keys of left alone are added, of right alone removed, and
// of both changed or unchanged by E4, so an int and the equal float, 0.0 and -0.0, or two NaN
// are unchanged (Pyric keyOrder and "value identity" diff cases).
func mapDiffOf(m *meter, left, right *mapValue) (Value, error) {
	if err := m.work(int64(len(left.keys) + len(right.keys))); err != nil {
		return Value{}, err
	}
	if err := chargeList(m, len(left.keys)+len(right.keys)); err != nil {
		return Value{}, err
	}
	d := &mapDiff{}
	for _, k := range left.keys {
		w, ok := right.entries[k]
		if !ok {
			d.added = append(d.added, k)
			continue
		}
		eq, err := equal(m, relSet, left.entries[k], w)
		if err != nil {
			return Value{}, err
		}
		if eq {
			d.unchanged = append(d.unchanged, k)
		} else {
			d.changed = append(d.changed, k)
		}
	}
	for _, k := range right.keys {
		if _, ok := left.entries[k]; !ok {
			d.removed = append(d.removed, k)
		}
	}
	for _, keys := range [][]string{d.added, d.removed, d.changed, d.unchanged} {
		slices.Sort(keys)
	}
	return Value{kind: KindMapDiff, x: d}, nil
}

// mapDiffMethod calls d.name(): the keys of each kind, as a set of strings. affectedKeys are
// the added, removed and changed ones. A map diff has nothing else, not even size()
// (Pyric setMethod).
func mapDiffMethod(m *meter, d *mapDiff, name string, args []Value) (Value, error) {
	var keys []string
	switch name {
	case "addedKeys":
		keys = d.added
	case "removedKeys":
		keys = d.removed
	case "changedKeys":
		keys = d.changed
	case "unchangedKeys":
		keys = d.unchanged
	case "affectedKeys":
		keys = slices.Concat(d.added, d.removed, d.changed)
		slices.Sort(keys)
	default:
		return Value{}, errNotFound(name)
	}
	if len(args) != 0 {
		return Value{}, mismatch("map_diff."+name, args, []string{})
	}
	if err := chargeList(m, len(keys)); err != nil {
		return Value{}, err
	}
	out := make([]Value, len(keys))
	for i, k := range keys {
		out[i] = String(k)
	}
	return setOf(out), nil
}
