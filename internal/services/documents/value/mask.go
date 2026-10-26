package value

import (
	"slices"
	"strings"
)

// Project returns the fields of a document that a read mask (DocumentMask) keeps: each path's
// value, with the maps above it holding only what the mask names (values-mask.json, reads).
// A path that reaches past an array or a scalar, or names nothing, keeps nothing; a path and
// its descendants keep the whole value; __name__ keeps no field. A vector or a BSON shape is
// one value, not a map: vec.value keeps nothing (values-mask-vector.json; ValidateMaskPath
// refuses vec.__type__ and b.__int__ first).
func Project(fields Value, paths []FieldPath) Value {
	t := newPathTree(paths)
	if t == nil {
		return Value{kind: KindMap}
	}
	out, _ := t.project(fields)
	return out
}

// ApplyMask returns the fields of a document after an update with an update mask: each masked
// path takes update's value there, or is deleted where update has none, and nothing else
// changes; a field of update outside the mask is ignored (values-mask.json, updates). Maps
// missing on the way are created and a scalar on the way is replaced by one; a deletion stops
// at a scalar or a missing map. A path and its descendant act as the path alone; __name__
// changes nothing. A vector or a BSON shape on the way is a scalar: vec.value replaces the
// vector with {value: …}, and deleting vec.value leaves it whole (values-mask-vector.json).
//
// It walks the paths as one tree, so it costs the paths' length and the maps it rebuilds, each
// once, however many paths there are.
func ApplyMask(old, update Value, paths []FieldPath) Value {
	if old.kind != KindMap {
		old = Value{kind: KindMap}
	}
	t := newPathTree(paths)
	if t == nil {
		return old
	}
	out, _, _ := t.apply(old, update)
	return out
}

// isMap reports whether v is a map a mask path enters: not a vector, not a BSON shape.
func isMap(v Value) bool {
	return v.kind == KindMap && !v.IsVector() && v.BSONKind() == NotBSON
}

// apply applies the paths below t to map old (any other value counts as an empty map) from
// update. changed reports that the result differs from old; set, that some path took a value
// from update, which is what makes a map where old had none.
func (t *pathTree) apply(old, update Value) (out Value, changed, set bool) {
	if !isMap(old) {
		old = Value{kind: KindMap}
	}
	if !isMap(update) {
		update = Value{}
	}
	type change struct {
		v   Value
		del bool
	}
	changes := make(map[string]change, len(t.children))
	for name, c := range t.children {
		u, inUpdate := update.Get(name)
		cur, inOld := old.Get(name)
		switch {
		case c.all && inUpdate:
			changes[name], set = change{v: u}, true
		case c.all:
			if inOld {
				changes[name] = change{del: true}
			}
		case isMap(cur):
			if sub, subChanged, subSet := c.apply(cur, u); subChanged {
				changes[name], set = change{v: sub}, set || subSet
			}
		default:
			// A scalar or nothing: replaced by a map only if a path below sets a value.
			if sub, _, subSet := c.apply(Value{}, u); subSet {
				changes[name], set = change{v: sub}, true
			}
		}
	}
	if len(changes) == 0 {
		return old, false, set
	}
	fields := make([]Field, 0, len(old.m)+len(changes))
	for _, f := range old.m {
		ch, ok := changes[f.Name]
		switch {
		case !ok:
			fields = append(fields, f)
		case !ch.del:
			fields = append(fields, Field{f.Name, ch.v})
		}
		delete(changes, f.Name)
	}
	kept := len(fields)
	for name, ch := range changes {
		if !ch.del {
			fields = append(fields, Field{name, ch.v})
		}
	}
	if len(fields) > kept {
		slices.SortFunc(fields, func(a, b Field) int { return strings.Compare(a.Name, b.Name) })
	}
	return Value{kind: KindMap, m: fields}, true, set
}

// pathTree is a set of paths by segment; a node with all set keeps its whole value.
type pathTree struct {
	all      bool
	children map[string]*pathTree
}

func newPathTree(paths []FieldPath) *pathTree {
	var t *pathTree
	for _, p := range paths {
		if len(p) == 0 || p.IsDocumentID() {
			continue
		}
		if t == nil {
			t = &pathTree{}
		}
		n := t
		for _, seg := range p {
			if n.all {
				break
			}
			if n.children == nil {
				n.children = map[string]*pathTree{}
			}
			c := n.children[seg]
			if c == nil {
				c = &pathTree{}
				n.children[seg] = c
			}
			n = c
		}
		n.all, n.children = true, nil
	}
	return t
}

// project keeps the parts of map m that t names; ok is false when it keeps nothing.
func (t *pathTree) project(m Value) (Value, bool) {
	var out []Field
	for _, f := range m.Fields() {
		c := t.children[f.Name]
		switch {
		case c == nil:
		case c.all:
			out = append(out, f)
		case isMap(f.Value):
			if sub, ok := c.project(f.Value); ok {
				out = append(out, Field{f.Name, sub})
			}
		}
	}
	return Value{kind: KindMap, m: out}, len(out) > 0
}
