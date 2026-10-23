package rules

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// grants compiles nested match statements (outermost first) under Firestore's root, the
// innermost granting get, and reports which of the documents it grants.
func grants(t *testing.T, version string, nested []string, docs []string) []string {
	t.Helper()
	inner := matchb(nested[len(nested)-1], block{allows: []*allow{allowb("get", nil)}})
	for i := len(nested) - 2; i >= 0; i-- {
		inner = matchb(nested[i], block{matches: []*match{inner}})
	}
	rs := compile(t, Firestore, rulesFile(version, Firestore, docsBlock(block{matches: []*match{inner}})))
	var allowed []string
	for _, d := range docs {
		if evaluate(t, rs, request(MethodGet, docPath(strings.Split(d, "/")...), nil)).Allow {
			allowed = append(allowed, d)
		}
	}
	return allowed
}

// hidane's measurement of the emulator (rules_paths.json): which documents each match covers,
// in both versions. {x=**} takes one or more segments in version 1 and zero or more in 2.
func TestMatchPaths(t *testing.T) {
	docs := []string{"c/d", "c/x", "x/y", "c/d/e/f", "c/d/e/g", "c/d/x/f", "c/d/e/f/g/h", "a/b/e/f", "e/f"}
	all := strings.Join(docs, " ")
	for _, c := range []struct {
		version string
		match   []string
		want    string
	}{
		{"1", []string{"/{document=**}"}, all},
		{"1", []string{"/c/{rest=**}"}, "c/d c/x c/d/e/f c/d/e/g c/d/x/f c/d/e/f/g/h"},
		{"1", []string{"/c/{d}"}, "c/d c/x"},
		{"1", []string{"/{c}/{d}"}, "c/d c/x x/y e/f"},
		{"1", []string{"/c/{d}/{rest=**}"}, "c/d/e/f c/d/e/g c/d/x/f c/d/e/f/g/h"},
		{"1", []string{"/c/d/{rest=**}"}, "c/d/e/f c/d/e/g c/d/x/f c/d/e/f/g/h"},
		{"1", []string{"/c/{d}", "/{rest=**}"}, "c/d/e/f c/d/e/g c/d/x/f c/d/e/f/g/h"},
		{"1", []string{"/c/{d}", "/e/{f}"}, "c/d/e/f c/d/e/g"},
		{"1", []string{"/{a}/{b}", "/{rest=**}"}, "c/d/e/f c/d/e/g c/d/x/f c/d/e/f/g/h a/b/e/f"},
		{"2", []string{"/{document=**}"}, all},
		{"2", []string{"/c/{rest=**}"}, "c/d c/x c/d/e/f c/d/e/g c/d/x/f c/d/e/f/g/h"},
		{"2", []string{"/c/{d}"}, "c/d c/x"},
		{"2", []string{"/{c}/{d}"}, "c/d c/x x/y e/f"},
		{"2", []string{"/c/{d}/{rest=**}"}, "c/d c/x c/d/e/f c/d/e/g c/d/x/f c/d/e/f/g/h"},
		{"2", []string{"/c/d/{rest=**}"}, "c/d c/d/e/f c/d/e/g c/d/x/f c/d/e/f/g/h"},
		{"2", []string{"/{path=**}/e/{f}"}, "c/d/e/f c/d/e/g a/b/e/f e/f"},
		{"2", []string{"/c/{rest=**}/f"}, "c/d/e/f c/d/x/f"},
		{"2", []string{"/c/{d}", "/{rest=**}"}, "c/d c/x c/d/e/f c/d/e/g c/d/x/f c/d/e/f/g/h"},
		{"2", []string{"/c/{d}", "/e/{f}"}, "c/d/e/f c/d/e/g"},
		{"2", []string{"/{a}/{b}", "/{rest=**}"}, all},
	} {
		got := strings.Join(grants(t, c.version, c.match, docs), " ")
		if got != c.want {
			t.Errorf("v%s %v:\n got  %s\n want %s", c.version, c.match, got, c.want)
		}
	}
}

// Nesting is not concatenation (S0-A on the Rules Test API and the emulator): under a non-final
// {x=**} a nested block never matches; under a final one it matches at any depth in version 2
// only. The shapes are checked in both services and every version line.
func TestNestedMatchRules(t *testing.T) {
	type shape struct {
		name   string
		nested []string
		paths  map[string]bool // path: granted in version 2
		v1     bool            // whether version 1 grants as version 2 does
	}
	shapes := []shape{
		{"nested under a non-final {x=**}", []string{"/{p=**}/groups/{g}", "/members/{m}"},
			map[string]bool{"x/groups/g1/members/m1": false, "groups/g1/members/m1": false, "x/groups/g1": false}, false},
		{"the same written flat", []string{"/{r=**}/groups/{g}/members/{m}"},
			map[string]bool{"x/groups/g1/members/m1": true, "groups/g1/members/m1": true, "x/groups/g1": false}, false},
		{"nested under a final {x=**} after a literal", []string{"/p/{r=**}", "/docs/{d}"},
			map[string]bool{"p/a/b/docs/d1": true, "p/docs/d1": true, "q/docs/d1": false, "p/a/docs": false}, false},
		{"nested under a root {x=**}", []string{"/{r=**}", "/groups/{g}/members/{m}"},
			map[string]bool{"groups/g/members/m": true, "a/b/groups/g/members/m": true, "groups/g": false}, false},
		{"two levels under a root {x=**}", []string{"/{r=**}", "/a/{x}", "/b/{y}"},
			map[string]bool{"a/1/b/2": true, "z/a/1/b/2": true, "a/1": false}, false},
		{"no {x=**}", []string{"/a/{x}", "/b/{y}"},
			map[string]bool{"a/1/b/2": true, "a/1": false}, true},
	}
	roots := map[Service]struct {
		wrap func(*match) block
		path func(string) []Segment
	}{
		Firestore: {func(m *match) block { return docsBlock(block{matches: []*match{m}}) },
			func(p string) []Segment { return docPath(strings.Split(p, "/")...) }},
		Storage: {func(m *match) block {
			return block{matches: []*match{matchb("/b/{bucket}/o", block{matches: []*match{m}})}}
		},
			func(p string) []Segment {
				return Literals(append([]string{"b", "bkt", "o"}, strings.Split(p, "/")...)...)
			}},
	}
	for _, s := range shapes {
		for svc, root := range roots {
			for _, version := range []string{"2", "1", ""} {
				if version != "2" && strings.Contains(strings.Join(s.nested, ""), "**}/") {
					continue // a non-final {x=**} does not compile in version 1
				}
				inner := matchb(s.nested[len(s.nested)-1], block{allows: []*allow{allowb("read", nil)}})
				for i := len(s.nested) - 2; i >= 0; i-- {
					inner = matchb(s.nested[i], block{matches: []*match{inner}})
				}
				f := rulesFile(version, svc, root.wrap(inner))
				got, rs := issuesOf(svc, f)
				if version == "2" && len(got) != 0 {
					t.Errorf("%s, %s v2: issues %q", s.name, svc, got)
				}
				if rs == nil {
					t.Fatalf("%s, %s v%q does not compile: %q", s.name, svc, version, got)
				}
				for p, v2 := range s.paths {
					want := v2 && (version == "2" || s.v1)
					if got := evaluate(t, rs, request(MethodGet, root.path(p), nil)).Allow; got != want {
						t.Errorf("%s, %s v%q, %s: allow %v, want %v", s.name, svc, version, p, got, want)
					}
				}
			}
		}
	}
}

// cascade is Pyric's hierarchical-match-cascade ruleset (Rules Test API), its conditions on
// variables left out: they go through the library. Blocks grant get, or read for items.
func cascade(t *testing.T) *Ruleset {
	t.Helper()
	grant := func(path, methods string, inner ...*match) *match {
		return matchb(path, block{matches: inner, allows: []*allow{allowb(methods, nil)}})
	}
	body := docsBlock(block{matches: []*match{
		matchb("/parents/{parentId}", block{matches: []*match{grant("/children/{childId}", "get")}}),
		grant("/{path=**}/items/{id}", "read"),
		matchb("/{group=**}/groups/{groupId}", block{matches: []*match{grant("/members/{memberId}", "get")}}),
		matchb("/{document=**}", block{matches: []*match{grant("/notes/{noteId}", "get")}}),
	}})
	return compile(t, Firestore, rulesFile("2", Firestore, body))
}

func TestHierarchicalMatchCascade(t *testing.T) {
	rs := cascade(t)
	for _, c := range []struct {
		path   string
		method Method
		allow  bool
	}{
		{"parents/p1/children/c1", MethodGet, true},
		{"parents/p1", MethodGet, false},
		{"parents/p1/siblings/s1", MethodGet, false},
		{"parents/p1/children/c1/grandchildren/g1", MethodGet, false},
		{"users/u1/items/i1", MethodGet, true},
		{"items/i1", MethodGet, true},
		{"a/b/c/d/items/i1", MethodGet, true},
		{"items/i1/items/i2", MethodGet, true},
		{"items/items/items/i3", MethodGet, true},
		{"users/u1/other/o1", MethodGet, false},
		{"users/u1/items/i1/sub/s1", MethodGet, false},
		{"users/u1", MethodGet, false},
		{"users/u1/items/i1", MethodList, true},
		{"orgs/o1/groups/g1/members/m1", MethodGet, false}, // nested under a non-final {x=**}
		{"groups/g1/members/m1", MethodGet, false},
		{"orgs/o1/groups/g1", MethodGet, false},
		{"notes/n1", MethodGet, true}, // nested after a final {x=**}, at the root
		{"a/1/b/2/notes/n1", MethodGet, true},
	} {
		if got := evaluate(t, rs, request(c.method, docPath(strings.Split(c.path, "/")...), nil)).Allow; got != c.allow {
			t.Errorf("%v %s: allow %v, want %v", c.method, c.path, got, c.allow)
		}
	}
}

// flatBlock flattens nested statements of version under Storage's service and returns the
// innermost block.
func flatBlock(t *testing.T, version string, nested ...string) *compiledBlock {
	t.Helper()
	inner := matchb(nested[len(nested)-1], block{allows: []*allow{allowb("read", nil)}})
	for i := len(nested) - 2; i >= 0; i-- {
		inner = matchb(nested[i], block{matches: []*match{inner}})
	}
	rs := compile(t, Storage, rulesFile(version, Storage, block{matches: []*match{inner}}))
	return rs.blocks[len(rs.blocks)-1]
}

// show writes bindings as name=value: a string, a path in slashes, or ? for none.
func show(b *compiledBlock, version int, path []Segment) string {
	vars, ok := b.match(path, version)
	if !ok {
		return "no match"
	}
	names := map[int]string{}
	for _, s := range b.segs {
		if s.kind != patternLiteral {
			names[s.slot] = s.text
		}
	}
	var out []string
	for i, v := range vars {
		val := "?"
		if v.ok {
			if s, ok := v.v.AsString(); ok {
				val = s
			} else if p, ok := v.v.AsPath(); ok {
				val = "/" + strings.Join(p, "/")
			}
		}
		out = append(out, names[i]+"="+val)
	}
	return strings.Join(out, " ")
}

func TestMatchBindings(t *testing.T) {
	lit := func(p string) []Segment { return Literals(strings.Split(p, "/")...) }
	for _, c := range []struct {
		version int
		nested  []string
		path    []Segment
		want    string
	}{
		{2, []string{"/{document=**}", "/notes/{noteId}"}, lit("notes/n1/notes/n2"), "document=/notes/n1 noteId=n2"},
		{2, []string{"/{document=**}", "/notes/{noteId}"}, lit("notes/n1"), "document=/ noteId=n1"},
		{1, []string{"/{document=**}", "/notes/{noteId}"}, lit("a/notes/n1"), "no match"}, // dead in version 1
		{2, []string{"/{prefix=**}/tagged/{tagId}"}, lit("shops/s1/tagged/x1"), "prefix=/shops/s1 tagId=x1"},
		{1, []string{"/c/{rest=**}"}, lit("c/d/e"), "rest=/d/e"},
		{1, []string{"/c/{rest=**}"}, lit("c"), "no match"},
		{2, []string{"/c/{rest=**}"}, lit("c"), "rest=/"},
		// Shadowing: the inner x has its own slot.
		{2, []string{"/a/{x}", "/b/{x}"}, lit("a/1/b/2"), "x=1 x=2"},
		// The unknown segments of a list query (ADR-021 §2.4).
		{2, []string{"/c/{id}"}, append(lit("c"), UnknownSegment), "id=?"},
		{2, []string{"/c/{rest=**}"}, append(lit("c"), UnknownSegment), "rest=?"},
		{2, []string{"/c/d"}, append(lit("c"), UnknownSegment), "no match"},
		{2, []string{"/{p=**}/items/{id}"}, []Segment{UnknownRun, Literal("items"), UnknownSegment}, "p=? id=?"},
		{2, []string{"/{c}/items/{id}"}, []Segment{UnknownRun, Literal("items"), UnknownSegment}, "no match"},
		{2, []string{"/x/items/{id}"}, []Segment{UnknownRun, Literal("items"), UnknownSegment}, "no match"},
	} {
		b := flatBlock(t, fmt.Sprint(c.version), c.nested...)
		if got := show(b, c.version, c.path); got != c.want {
			t.Errorf("v%d %v on %v: %s, want %s", c.version, c.nested, c.path, got, c.want)
		}
	}
	// A pattern with two {x=**} never compiles; were one to reach the matcher, it refuses.
	b := &compiledBlock{segs: []matchSeg{{kind: patternRest}, {kind: patternRest, slot: 1}}, globs: 2, nvars: 2}
	if _, ok := b.match(Literals("a", "b"), 2); ok {
		t.Error("a pattern with two {x=**} matched")
	}
}

// Matching is linear: the longest request path against many deeply nested blocks under a
// {x=**} (ADR-021 §6) ends in bounded time.
func TestMatchIsLinear(t *testing.T) {
	const depth, siblings = 11, 100
	var roots []*match
	for s := range siblings {
		inner := matchb("/z", block{allows: []*allow{allowb("read", nil)}})
		for i := depth - 2; i >= 1; i-- {
			inner = matchb(fmt.Sprintf("/k/{x%d}", i), block{matches: []*match{inner}, allows: []*allow{allowb("read", nil)}})
		}
		inner = matchb(fmt.Sprintf("/s%d/{g=**}", s%2), block{matches: []*match{inner}, allows: []*allow{allowb("write", nil)}})
		roots = append(roots, inner)
	}
	lim := DefaultLimits()
	rs, issues := check(rulesFile("2", Storage, block{matches: roots}), Storage, lim)
	if rs == nil {
		t.Fatalf("does not compile: %v", issues)
	}
	path := make([]Segment, lim.RequestSegments)
	for i := range path {
		path[i] = Literal("a")
	}
	path[0] = Literal("s1")
	start := time.Now()
	for range 20 {
		if evaluate(t, rs, request(MethodGet, path, nil)).Allow {
			t.Fatal("a path that no block takes granted")
		}
	}
	elapsed := time.Since(start)
	for i := range depth - 2 {
		path[len(path)-1-2*(depth-2)+2*i] = Literal("k")
	}
	path[len(path)-1] = Literal("z")
	if !evaluate(t, rs, request(MethodGet, path, nil)).Allow {
		t.Fatal("the innermost block does not take the path")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("20 evaluations against %d blocks took %v", siblings*depth, elapsed)
	}
	vars, ok := rs.blocks[len(rs.blocks)-1].match(path, 2)
	if p, _ := vars[0].v.AsPath(); !ok || len(p) != len(path)-1-2*(depth-2)-1 {
		t.Fatalf("g took %d segments", len(p))
	}
}

func TestMethodsNamed(t *testing.T) {
	for name, want := range map[string][]Method{
		"read": {MethodGet, MethodList}, "write": {MethodCreate, MethodUpdate, MethodDelete},
		"get": {MethodGet}, "list": {MethodList}, "create": {MethodCreate}, "update": {MethodUpdate}, "delete": {MethodDelete},
	} {
		s, ok := methodsNamed(name)
		if !ok {
			t.Fatalf("%s unknown", name)
		}
		for m := MethodGet; m <= MethodDelete; m++ {
			if s.has(m) != slices.Contains(want, m) {
				t.Errorf("%s covers %v: %v", name, m, s.has(m))
			}
		}
	}
	if s, ok := methodsNamed("foo"); ok || s != 0 {
		t.Error("foo covers something")
	}
	if all, _ := methodsNamed("read"); all.has(0) || all.has(MethodDelete+1) {
		t.Error("an invalid method is covered")
	}
}
