package rules

import (
	"hash/maphash"
	"math"
	"slices"
)

// The language has four equality relations, not one (ADR-021 §2.3), and numbers inside
// containers are never compared with Go's == on float64 but by the relation's own rule:
//
//	E1, == and != at the top of an expression: an int equals a float of the same value
//	    (1 == 1.0), and floats compare as IEEE 754 does (0 == -0.0, NaN != NaN).
//	E2, lists and maps under ==, at any depth: the type must match ([1] != [1.0]), and ±0 are
//	    two values ([0.0] != [-0.0], [-0.0] == [-0.0]); NaN equals nothing ([NaN] != [NaN]).
//	E3, the elements of a list, for in and the list methods: the type must match
//	    (1 in [1.0] is false), ±0 are one value (-0.0 in [0.0]), NaN is in nothing; an element
//	    that is a list or a map compares by E2 ([0.0] in [[-0.0]] is false).
//	E4, the members of a set, for toSet(), in, the set methods, == between sets, and diff():
//	    numbers by value at any depth (1 in [1.0].toSet()), exactly (2^53+1 is not 2^53 as a
//	    float), ±0 are one member, and so are two NaN.
//
// Evidence, all from Pyric's recordings of the Rules Test API (production) where they exist:
//   - E1 and E2: cross-type-operator-overloads "equalityIdentity", list-map-literals-and-slice
//     "equality". hidane (value.rs, emulator) compares floats in containers with IEEE ==, which
//     makes [0.0] == [-0.0] true; production says false, so we follow Pyric.
//   - E3: list-methods-concat-removeall-toset "listMethod", stdlib-sets-and-mapdiff "list: in".
//   - E4: set-algebra-difference-union-intersection "setIdentity", stdlib-sets-and-mapdiff
//     "value identity". hidane's loose_eq makes NaN unequal to NaN in a set; production finds
//     NaN in a set of NaN and makes two NaN one member, so we follow Pyric.
//
// Two map diffs are never equal (hidane). Every node a comparison visits is charged to the
// meter's work: a value shared within itself (let a = [b, b] thirty times) is small in memory
// but vast to walk.

// visitWork is the work charged for each value a comparison or a hash visits: about what four
// steps of a pattern's program cost, so a whole evaluation's work of visits takes about as long
// as its work of matching (some 0.5 s on an M1).
const visitWork = 4

// relation is one of the four equality relations.
type relation uint8

const (
	relTop       relation = iota // E1
	relContainer                 // E2
	relList                      // E3
	relSet                       // E4
)

// equalTop is == at the top of an expression (E1). A reserved value is an error.
func equalTop(m *meter, a, b Value) (bool, error) {
	if err := reserved(a); err != nil {
		return false, err
	}
	if err := reserved(b); err != nil {
		return false, err
	}
	return equal(m, relTop, a, b)
}

// equal compares a and b by rel, charging work for each pair of values it visits.
func equal(m *meter, rel relation, a, b Value) (bool, error) {
	if err := m.work(visitWork); err != nil {
		return false, err
	}
	if err := reserved(a); err != nil {
		return false, err // a reserved value a service put in a container
	}
	if err := reserved(b); err != nil {
		return false, err
	}
	if a.isNumber() && b.isNumber() {
		return numbersEqual(rel, a, b), nil
	}
	if a.kind != b.kind {
		return false, nil
	}
	// The relation of what a container holds: E4 stays E4, the others compare by E2.
	inner := relContainer
	if rel == relSet {
		inner = relSet
	}
	switch a.kind {
	case KindNull:
		return true, nil
	case KindBool:
		return a.boolean() == b.boolean(), nil
	case KindString, KindBytes:
		x, y := a.str(), b.str()
		if len(x) != len(y) {
			return false, nil
		}
		if err := m.work(scanWork(len(x))); err != nil {
			return false, err
		}
		return x == y, nil
	case KindList:
		x, y := a.items(), b.items()
		if len(x) != len(y) {
			return false, nil
		}
		for i := range x {
			if eq, err := equal(m, inner, x[i], y[i]); err != nil || !eq {
				return false, err
			}
		}
		return true, nil
	case KindMap:
		x, y := a.mapv().entries, b.mapv().entries
		if len(x) != len(y) {
			return false, nil
		}
		for k, v := range x {
			if err := m.work(scanWork(len(k))); err != nil {
				return false, err
			}
			w, ok := y[k]
			if !ok {
				return false, nil
			}
			if eq, err := equal(m, inner, v, w); err != nil || !eq {
				return false, err
			}
		}
		return true, nil
	case KindSet:
		return setsEqual(m, a.items(), b.items())
	case KindTimestamp:
		return a.instant().Equal(b.instant()), nil
	case KindDuration:
		return a.dur() == b.dur(), nil
	case KindLatLng:
		p, q := a.point(), b.point()
		return p.lat == q.lat && p.lng == q.lng, nil
	case KindPath:
		return slices.Equal(a.segments(), b.segments()), nil
	}
	// Map diffs, and the reserved kinds, which equalTop refuses before they get here.
	return false, nil
}

// numbersEqual compares two numbers by rel.
func numbersEqual(rel relation, a, b Value) bool {
	if a.kind == KindInt && b.kind == KindInt {
		return a.integer() == b.integer()
	}
	switch rel {
	case relTop:
		return a.number() == b.number()
	case relSet:
		switch {
		case a.kind == KindInt:
			return intIsFloat(a.integer(), b.float())
		case b.kind == KindInt:
			return intIsFloat(b.integer(), a.float())
		}
		x, y := a.float(), b.float()
		return x == y || math.IsNaN(x) && math.IsNaN(y)
	}
	if a.kind != b.kind {
		return false
	}
	x, y := a.float(), b.float()
	if rel == relContainer {
		return x == y && math.Signbit(x) == math.Signbit(y)
	}
	return x == y // E3
}

// intIsFloat reports whether i and f are the same number exactly, as members of a set (E4):
// 2^53+1 is not 2^53 as a float, nor MaxInt64 2^63 (production, the review's probes of the Rules
// Test API, 2026-10-24), though == at the top compares them as floats and finds them equal.
func intIsFloat(i int64, f float64) bool {
	// float64(math.MinInt64) is -2^63 exactly, float64(math.MaxInt64) rounds up to 2^63.
	if f != math.Trunc(f) || f < math.MinInt64 || f >= math.MaxInt64 {
		return false
	}
	return int64(f) == i
}

// setsEqual reports whether two sets have the same members (E4).
func setsEqual(m *meter, x, y []Value) (bool, error) {
	if len(x) != len(y) {
		return false, nil
	}
	ix, err := newIndex(m, relSet, y)
	if err != nil {
		return false, err
	}
	for _, v := range x {
		if found, err := ix.contains(m, v); err != nil || !found {
			return false, err
		}
	}
	return true, nil
}

// valueIndex finds values among items by a relation in about constant time: items are bucketed
// by a hash that values equal by the relation share, and a bucket is searched by the relation
// itself. The hash of a number under E4 is that of its float64 value, so 2^53+1 and 2^53 share
// a bucket even where they are different ints; the relation decides.
type valueIndex struct {
	rel     relation
	items   []Value
	buckets map[uint64][]int
}

// smallIndex is the size under which an index compares every item instead of hashing.
const smallIndex = 8

// newIndex indexes items by rel, charging the work of hashing them and the memory of the index.
func newIndex(m *meter, rel relation, items []Value) (*valueIndex, error) {
	ix := &valueIndex{rel: rel, items: items}
	if len(items) <= smallIndex {
		return ix, nil
	}
	if err := m.alloc(int64(len(items)) * entryBytes); err != nil {
		return nil, err
	}
	ix.buckets = make(map[uint64][]int, len(items))
	for i, v := range items {
		h, err := hashValue(m, rel, v)
		if err != nil {
			return nil, err
		}
		ix.buckets[h] = append(ix.buckets[h], i)
	}
	return ix, nil
}

// contains reports whether one of the index's items equals v by its relation.
func (ix *valueIndex) contains(m *meter, v Value) (bool, error) {
	if ix.buckets == nil {
		for _, w := range ix.items {
			if eq, err := equal(m, ix.rel, w, v); err != nil || eq {
				return eq, err
			}
		}
		return false, nil
	}
	h, err := hashValue(m, ix.rel, v)
	if err != nil {
		return false, err
	}
	for _, i := range ix.buckets[h] {
		if eq, err := equal(m, ix.rel, ix.items[i], v); err != nil || eq {
			return eq, err
		}
	}
	return false, nil
}

// add appends v to the index's items, charging its place in them.
func (ix *valueIndex) add(m *meter, v Value) error {
	if err := m.alloc(elemBytes); err != nil {
		return err
	}
	ix.items = append(ix.items, v)
	if ix.buckets == nil {
		if len(ix.items) <= smallIndex {
			return nil
		}
		// Past the small size: index what is there.
		built, err := newIndex(m, ix.rel, ix.items)
		if err != nil {
			return err
		}
		*ix = *built
		return nil
	}
	if err := m.alloc(entryBytes); err != nil {
		return err
	}
	h, err := hashValue(m, ix.rel, v)
	if err != nil {
		return err
	}
	ix.buckets[h] = append(ix.buckets[h], len(ix.items)-1)
	return nil
}

// distinct returns the members of the lists distinct by E4, the first of equal ones kept in its
// place: toSet(), union() and the rest. It charges the set it builds, as large as the lists
// together, and the index it builds as it goes (add).
func distinct(m *meter, lists ...[]Value) ([]Value, error) {
	n := 0
	for _, l := range lists {
		n += len(l)
	}
	if err := chargeList(m, n); err != nil {
		return nil, err
	}
	ix := &valueIndex{rel: relSet}
	out := make([]Value, 0, n)
	for _, l := range lists {
		for _, v := range l {
			found, err := ix.contains(m, v)
			if err != nil {
				return nil, err
			}
			if !found {
				if err := ix.add(m, v); err != nil {
					return nil, err
				}
				out = append(out, v)
			}
		}
	}
	return out, nil
}

var hashSeed = maphash.MakeSeed()

// hashValue hashes v so that values equal by rel hash alike, charging work for each value it
// visits.
func hashValue(m *meter, rel relation, v Value) (uint64, error) {
	if err := m.work(visitWork); err != nil {
		return 0, err
	}
	if err := reserved(v); err != nil {
		return 0, err
	}
	inner := relContainer
	if rel == relSet {
		inner = relSet
	}
	k := uint64(v.kind)
	switch v.kind {
	case KindInt, KindFloat:
		if rel == relSet {
			// Numbers by value: an int and the equal float alike, ±0 alike, every NaN alike.
			f := v.number()
			switch {
			case f == 0:
				f = 0
			case math.IsNaN(f):
				f = math.NaN()
			}
			return mix(uint64(KindFloat), math.Float64bits(f)), nil
		}
		if v.kind == KindInt {
			return mix(k, uint64(v.integer())), nil
		}
		f := v.float()
		if rel != relContainer && f == 0 {
			f = 0 // E1 and E3 make ±0 one value; E2 keeps them apart.
		}
		return mix(k, math.Float64bits(f)), nil
	case KindBool:
		if v.boolean() {
			return mix(k, 1), nil
		}
		return mix(k, 0), nil
	case KindString, KindBytes:
		if err := m.work(scanWork(len(v.str()))); err != nil {
			return 0, err
		}
		return mix(k, maphash.String(hashSeed, v.str())), nil
	case KindList:
		h := k
		for _, e := range v.items() {
			eh, err := hashValue(m, inner, e)
			if err != nil {
				return 0, err
			}
			h = mix(h, eh)
		}
		return h, nil
	case KindMap:
		// The order of entries does not count: their hashes are summed.
		var sum uint64
		for key, e := range v.mapv().entries {
			eh, err := hashValue(m, inner, e)
			if err != nil {
				return 0, err
			}
			if err := m.work(scanWork(len(key))); err != nil {
				return 0, err
			}
			sum += mix(maphash.String(hashSeed, key), eh)
		}
		return mix(k, sum), nil
	case KindSet:
		var sum uint64
		for _, e := range v.items() {
			eh, err := hashValue(m, relSet, e)
			if err != nil {
				return 0, err
			}
			sum += mix(0, eh)
		}
		return mix(k, sum), nil
	case KindTimestamp:
		t := v.instant()
		return mix(mix(k, uint64(t.Unix())), uint64(t.Nanosecond())), nil
	case KindDuration:
		d := v.dur()
		return mix(mix(k, uint64(d.seconds)), uint64(d.nanos)), nil
	case KindLatLng:
		p := v.point()
		lat, lng := p.lat, p.lng
		if lat == 0 {
			lat = 0
		}
		if lng == 0 {
			lng = 0
		}
		return mix(mix(k, math.Float64bits(lat)), math.Float64bits(lng)), nil
	case KindPath:
		h := k
		for _, s := range v.segments() {
			if err := m.work(scanWork(len(s))); err != nil {
				return 0, err
			}
			h = mix(h, maphash.String(hashSeed, s))
		}
		return h, nil
	}
	return k, nil
}

// mix combines h and x (splitmix64's finalizer).
func mix(h, x uint64) uint64 {
	z := h*0x9e3779b97f4a7c15 + x
	z = (z ^ z>>30) * 0xbf58476d1ce4e5b9
	z = (z ^ z>>27) * 0x94d049bb133111eb
	return z ^ z>>31
}
