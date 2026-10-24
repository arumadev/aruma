package rules

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// issuesOf checks f and returns its issues as "E description" and "W description".
func issuesOf(svc Service, f *file) ([]string, *Ruleset) {
	rs, issues := check(f, svc, DefaultLimits())
	var out []string
	for _, is := range issues {
		sev := "W"
		if is.Severity == SeverityError {
			sev = "E"
		}
		out = append(out, sev+" "+is.Description)
	}
	return out, rs
}

// grantGet is a block that grants get when cond holds.
func grantGet(path string, cond expr, fns ...*function) *match {
	return matchb(path, block{functions: fns, allows: []*allow{allowb("get", cond)}})
}

func TestCheckIssues(t *testing.T) {
	chain := func(n int) []*function {
		var fns []*function
		for i := range n {
			var ret expr = tru()
			if i < n-1 {
				ret = call(fmt.Sprintf("f%d", i+1))
			}
			fns = append(fns, fn(fmt.Sprintf("f%d", i), nil, nil, ret))
		}
		return fns
	}
	lets := func(n int) []*let {
		var ls []*let
		for i := range n {
			ls = append(ls, letb(fmt.Sprintf("x%d", i), tru()))
		}
		return ls
	}
	readsAll := func(n int) expr {
		var e expr = ident("x0")
		for i := 1; i < n; i++ {
			e = and(e, ident(fmt.Sprintf("x%d", i)))
		}
		return e
	}
	docs := func(ms ...*match) block { return docsBlock(block{matches: ms}) }
	withFns := func(fns []*function, ms ...*match) block { return docsBlock(block{functions: fns, matches: ms}) }

	for _, c := range []struct {
		name    string
		version string
		svc     Service
		body    block
		want    []string // the issues, in order; nil for none
	}{
		{"no version", "", Firestore, docs(grantGet("/{d=**}", nil)), []string{"W " + msgOldVersion}},
		{"version 1", "1", Firestore, docs(grantGet("/{d=**}", nil)), []string{"W " + msgOldVersion}},
		{"version 3", "3", Firestore, docs(grantGet("/{d=**}", nil)), []string{"E Unsupported rules version 3."}},
		{"another service", "2", "", docs(grantGet("/{d=**}", nil)), []string{"E Invalid service name: cloud.firestore."}},
		{"no service", "2", "cloud.firestorex", docs(grantGet("/{d=**}", nil)), []string{"W Invalid service name: cloud.firestorex."}},

		// Functions and lets.
		{"let in version 1", "1", Firestore,
			withFns([]*function{fn("f", nil, []*let{letb("x", tru())}, ident("x"))}, grantGet("/{d=**}", call("f"))),
			[]string{"W " + msgOldVersion, "E " + msgLetVersion1}},
		{"eleven lets", "2", Firestore,
			withFns([]*function{fn("f", nil, lets(11), readsAll(11))}, grantGet("/{d=**}", call("f"))), nil},
		{"twelve lets", "2", Firestore,
			withFns([]*function{fn("f", nil, lets(12), readsAll(12))}, grantGet("/{d=**}", call("f"))),
			[]string{"E Maximum allowed variable count of 10 for a given function has been reached."}},
		{"eight parameters", "2", Firestore,
			withFns([]*function{fn("f", strings.Split("a,b,c,d,e,f,g,h", ","), nil, tru())},
				grantGet("/{d=**}", call("f", tru(), tru(), tru(), tru(), tru(), tru(), tru(), tru()))),
			[]string{"E Maximum allowed function arguments of 7 for function f is reached.",
				"W Unused variable: a.", "W Unused variable: b.", "W Unused variable: c.", "W Unused variable: d.",
				"W Unused variable: e.", "W Unused variable: f.", "W Unused variable: g.", "W Unused variable: h."}},
		{"duplicate function", "2", Firestore,
			withFns([]*function{fn("f", nil, nil, tru()), fn("f", nil, nil, fls())}, grantGet("/{d=**}", call("f"))),
			[]string{"E Function f is already defined."}},
		{"duplicate parameter", "2", Firestore,
			withFns([]*function{fn("f", []string{"a", "a"}, nil, ident("a"))}, grantGet("/{d=**}", call("f", tru(), tru()))),
			[]string{"E Variable a is already defined.", "W Unused variable: a."}},
		{"duplicate let", "2", Firestore,
			withFns([]*function{fn("f", nil, []*let{letb("a", tru()), letb("a", fls())}, ident("a"))}, grantGet("/{d=**}", call("f"))),
			[]string{"E The identifier [a] is already bound in the current scope.", "W Unused variable: a."}},
		{"let shadowing a parameter", "2", Firestore,
			withFns([]*function{fn("f", []string{"a"}, []*let{letb("a", tru())}, ident("a"))}, grantGet("/{d=**}", call("f", tru()))),
			[]string{"E The identifier [a] is already bound in the current scope.", "W Unused variable: a."}},
		{"let before its use", "2", Firestore,
			withFns([]*function{fn("f", nil, []*let{letb("a", ident("b")), letb("b", tru())}, ident("a"))}, grantGet("/{d=**}", call("f"))),
			[]string{"W Invalid variable name: b.", "W Unused variable: b."}},
		{"unused function and let", "2", Firestore,
			withFns([]*function{fn("f", nil, []*let{letb("a", tru())}, tru())}, grantGet("/{d=**}", nil)),
			[]string{"W Unused variable: a.", "W Unused function: f."}},
		{"recursion", "2", Firestore,
			withFns([]*function{fn("f", []string{"n"}, nil, or(ident("n"), call("f", ident("n"))))}, grantGet("/{d=**}", call("f", tru()))),
			[]string{"E " + msgRecursive}},
		{"unused recursion", "2", Firestore,
			withFns([]*function{fn("f", nil, nil, call("f"))}, grantGet("/{d=**}", nil)),
			[]string{"E " + msgRecursive}},
		{"a cycle of two", "2", Firestore,
			withFns([]*function{fn("f", nil, nil, call("g")), fn("g", nil, nil, call("f"))}, grantGet("/{d=**}", call("f"))),
			[]string{"E Detected a call cycle between [f,g] functions."}},
		{"a cycle of three, unused", "2", Firestore,
			withFns([]*function{fn("h", nil, nil, call("f")), fn("f", nil, nil, call("g")), fn("g", nil, nil, call("h"))}, grantGet("/{d=**}", nil)),
			[]string{"E Detected a call cycle between [h,f,g] functions."}},
		{"a chain of 21 functions", "2", Firestore, withFns(chain(21), grantGet("/{d=**}", call("f0"))), nil},
		{"a chain of 22 functions", "2", Firestore, withFns(chain(22), grantGet("/{d=**}", call("f0"))),
			[]string{"E Maximum allowed call depth of 20 is reached for [f0->f1->f2->f3->f4->f5->f6->f7->f8->f9->f10->f11->f12->f13->f14->f15->f16->f17->f18->f19->f20] call stack."}},
		{"an unused chain of 23 functions", "2", Firestore, withFns(chain(23), grantGet("/{d=**}", nil)),
			[]string{"E Maximum allowed call depth of 20 is reached for [f0->f1->f2->f3->f4->f5->f6->f7->f8->f9->f10->f11->f12->f13->f14->f15->f16->f17->f18->f19->f20] call stack.",
				"W Unused function: f0."}},
		{"the wrong number of arguments", "2", Firestore,
			withFns([]*function{fn("f", []string{"a"}, nil, ident("a"))}, grantGet("/{d=**}", call("f"))),
			[]string{"W Incorrect number of arguments supplied to function: f."}},

		// Names.
		{"an unknown function", "2", Firestore, docs(grantGet("/{d=**}", call("foo", tru()))), []string{"W Invalid function name: foo."}},
		{"an unknown variable", "2", Firestore, docs(grantGet("/{d=**}", ident("foo"))), []string{"W Invalid variable name: foo."}},
		{"a namespaced unknown function", "2", Firestore, docs(grantGet("/{d=**}", nscall("math", "foo", tru()))), []string{"W Invalid function name: math.foo."}},
		{"a package as a variable", "2", Firestore, docs(grantGet("/{d=**}", ident("math"))), []string{"E math is a package and cannot be used as variable name."}},
		{"a library function", "2", Firestore, docs(grantGet("/{d=**}", nscall("math", "abs", call("int", tru())))), nil},
		{"Firestore's lookups", "2", Firestore, docs(grantGet("/{d=**}", and(call("get", ident("d")), call("existsAfter", ident("d"))))), nil},
		{"a Firestore lookup in Storage", "2", Storage, block{matches: []*match{grantGet("/{d=**}", call("get", ident("d")))}},
			[]string{"W Invalid function name: get."}},
		{"Storage's lookups", "2", Storage, block{matches: []*match{grantGet("/{d=**}", nscall("firestore", "get", ident("d")))}}, nil},
		{"a Storage lookup in Firestore", "2", Firestore, docs(grantGet("/{d=**}", nscall("firestore", "get", ident("d")))),
			[]string{"W Invalid variable name: firestore."}},
		{"firestore as a variable in Storage", "2", Storage, block{matches: []*match{grantGet("/{d=**}", ident("firestore"))}},
			[]string{"E firestore is a package and cannot be used as variable name."}},
		{"request and resource", "2", Storage, block{matches: []*match{grantGet("/{d=**}", and(ident("request"), ident("resource")))}}, nil},
		{"a function seeing its block's variable", "2", Firestore,
			docs(grantGet("/a/{b}", call("g"), fn("g", nil, nil, ident("b")))), nil},
		{"a service function using a wildcard", "2", Firestore,
			block{functions: []*function{fn("f", nil, nil, ident("database"))}, matches: []*match{matchb("/databases/{database}/documents", block{matches: []*match{grantGet("/b/{y}", call("f"))}})}},
			[]string{"W Invalid variable name: database."}},
		{"a function in a sibling block", "2", Firestore,
			docs(grantGet("/a/{x}", call("g"), fn("g", nil, nil, ident("x"))), grantGet("/b/{y}", call("g"))),
			[]string{"W Invalid function name: g."}},
		{"an inner function shadowing an outer one", "2", Firestore,
			block{functions: []*function{fn("g", nil, nil, tru())}, matches: []*match{matchb("/databases/{database}/documents",
				block{functions: []*function{fn("g", nil, nil, fls())}, matches: []*match{grantGet("/b/{y}", call("g"))}})}},
			[]string{"W Unused function: g."}},
		{"a function named like a lookup", "2", Firestore,
			withFns([]*function{fn("get", []string{"x"}, nil, tru())}, grantGet("/{d=**}", call("get", tru()))),
			[]string{"W Unused variable: x."}},
		{"an unknown method name", "2", Storage,
			block{matches: []*match{matchb("/b/{bucket}/o", block{allows: []*allow{allowb("read, write, get, list, create, update, delete, foo", nil)}})}},
			[]string{"W Invalid method name: foo."}},

		// Match paths.
		{"a non-final {x=**} in version 1", "1", Firestore, docs(grantGet("/a/{rest=**}/b", nil)),
			[]string{"W " + msgOldVersion, "E " + msgGlobLast}},
		{"two {x=**} in version 1", "1", Firestore, docs(grantGet("/{a=**}/{b=**}", nil)),
			[]string{"W " + msgOldVersion, "E " + msgGlobLast}},
		{"a non-final {x=**} in version 2", "2", Firestore, docs(grantGet("/a/{rest=**}/b", nil)), nil},
		{"two {x=**} in version 2", "2", Firestore, docs(grantGet("/{a=**}/{b=**}", nil)), []string{"E " + msgGlobOnce}},
		{"{x=**} in a block and in one nested, version 2", "2", Firestore,
			docs(matchb("/x/{a=**}", block{matches: []*match{grantGet("/y/{b=**}", nil)}})), []string{"E " + msgGlobOnce}},
		{"{x=**} in a block and in one nested, version 1", "1", Firestore,
			docs(matchb("/x/{a=**}", block{matches: []*match{grantGet("/y/{b=**}", nil)}})),
			[]string{"W " + msgOldVersion, "W " + msgGlobChildren}},
		{"a non-final {x=**} with a block nested, version 2", "2", Firestore,
			docs(matchb("/{p=**}/groups/{g}", block{matches: []*match{grantGet("/members/{m}", nil)}})), nil},
		{"a variable twice in a statement", "2", Firestore, docs(grantGet("/{a}/{a}", nil)),
			[]string{"E Variable a already exists in the PathTemplate."}},
		{"a variable of the block around, again", "2", Firestore,
			docs(matchb("/x/{a}", block{matches: []*match{grantGet("/y/{a}", nil)}})), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := rulesFile(c.version, c.svc, c.body)
			switch c.svc {
			case "": // a Firestore file checked for Storage
				f.service, c.svc = string(Firestore), Storage
			case Firestore, Storage:
			default: // a name that is no service, checked for Firestore
				f.service, c.svc = string(c.svc), Firestore
			}
			got, rs := issuesOf(c.svc, f)
			if !slices.Equal(got, c.want) {
				t.Errorf("issues:\n got  %q\n want %q", got, c.want)
			}
			hasError := slices.ContainsFunc(c.want, func(s string) bool { return strings.HasPrefix(s, "E ") })
			if (rs == nil) != hasError {
				t.Errorf("ruleset %v with errors %v", rs != nil, hasError)
			}
		})
	}
}

// nest is n match blocks one in another, each with path, the innermost granting.
func nest(n int, path string) *match {
	inner := grantGet(path, nil)
	for range n - 1 {
		inner = matchb(path, block{matches: []*match{inner}})
	}
	return inner
}

// The static limits on match blocks (the emulator counts the first block of a service at
// depth 0, so eleven nest; rules_compile.json), each reported once along a path.
func TestMatchLimits(t *testing.T) {
	long := "/" + strings.Repeat("a/", 100) + "a"
	vars := func(prefix string, n int) string {
		var s []string
		for i := range n {
			s = append(s, fmt.Sprintf("{%s%d}", prefix, i))
		}
		return "/" + strings.Join(s, "/")
	}
	for _, c := range []struct {
		name string
		body block
		want []string
	}{
		{"eleven deep", block{matches: []*match{nest(11, "/a")}}, nil},
		{"twelve deep", block{matches: []*match{nest(12, "/a")}}, []string{"E Maximum allowed resource rule depth of 10 is reached."}},
		{"sixty-four deep", block{matches: []*match{nest(64, "/a")}},
			[]string{"E Maximum allowed resource rule depth of 10 is reached.", "E " + msgHierarchy}},
		{"100 segments", block{matches: []*match{grantGet(long[:len(long)-2], nil)}}, nil},
		{"101 segments", block{matches: []*match{grantGet(long, nil)}}, []string{"E Maximum allowed path length of 100 is reached."}},
		{"20 variables", block{matches: []*match{matchb(vars("a", 15), block{matches: []*match{grantGet(vars("b", 5), nil)}})}}, nil},
		{"21 variables", block{matches: []*match{matchb(vars("a", 15), block{matches: []*match{grantGet(vars("b", 6), nil)}})}},
			[]string{"E Maximum allowed amount of 20 match field variables is reached."}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, _ := issuesOf(Firestore, rulesFile("2", Firestore, c.body))
			if !slices.Equal(got, c.want) {
				t.Errorf("issues:\n got  %q\n want %q", got, c.want)
			}
		})
	}
}

// The checker binds every name: a parameter, a let, a path variable by slot, request and
// resource by name; calls to the file's functions, the library, lookups and methods.
func TestBindings(t *testing.T) {
	x, lt, pv, rq := ident("x"), ident("l"), ident("b"), ident("request")
	userCall := call("f", pv)
	lib := nscall("math", "abs", rq)
	method := &callExpr{fn: &memberExpr{x: ident("resource"), name: "size"}}
	look := call("exists", ident("request"))
	f := fn("f", []string{"x"}, []*let{letb("l", x)}, lt)
	body := docsBlock(block{matches: []*match{matchb("/a/{b}", block{functions: []*function{f}, allows: []*allow{
		allowb("get", and(and(userCall, lib), and(method, look))),
	}})}})
	compile(t, Firestore, rulesFile("2", Firestore, body))
	for _, c := range []struct {
		got  *identExpr
		kind identKind
		slot int
	}{{x, identParam, 0}, {lt, identLet, 0}, {pv, identPathVar, 1}, {rq, identGlobal, 0}} {
		if c.got.kind != c.kind || c.got.slot != c.slot {
			t.Errorf("%s: kind %d slot %d, want %d %d", c.got.name, c.got.kind, c.got.slot, c.kind, c.slot)
		}
	}
	for _, c := range []struct {
		e    expr
		kind callKind
		name string
	}{{userCall, callsUser, "f"}, {lib, callsFunction, "math.abs"}, {method, callsMethod, "size"}, {look, callsLookup, "exists"}} {
		if e := c.e.(*callExpr); e.kind != c.kind || e.name != c.name {
			t.Errorf("%s: kind %d, want %d", e.name, e.kind, c.kind)
		}
	}
	if userCall.(*callExpr).user != f {
		t.Error("the call does not reach its function")
	}
}

// Issues have positions in the file, and the ones about the whole file none.
func TestCheckPositions(t *testing.T) {
	src := "service cloud.firestore {\n  match /a/{x}/{x} {\n    allow get: if foo();\n  }\n}\n"
	at := func(s string, nth int) span {
		i := -1
		for range nth {
			i += 1 + strings.Index(src[i+1:], s)
		}
		return span{i, i + len(s)}
	}
	m := &match{span: at("match", 1), path: []pattern{
		{span: at("/a", 1), kind: patternLiteral, text: "a"},
		{span: at("/{x}", 1), kind: patternVariable, text: "x"},
		{span: at("/{x}", 2), kind: patternVariable, text: "x"},
	}}
	cond := &callExpr{span: at("foo()", 1), fn: &identExpr{span: at("foo", 1), name: "foo"}}
	m.body.allows = []*allow{{span: at("allow", 1), methods: []name{{span: at("get", 1), text: "get"}}, cond: cond}}
	f := &file{name: "firestore.rules", src: src, service: "cloud.firestore", serviceAt: at("cloud.firestore", 1), body: block{matches: []*match{m}}}
	_, issues := check(f, Firestore, DefaultLimits())
	var got []string
	for _, is := range sortIssues(issues) {
		if is.Position == nil {
			got = append(got, "- "+is.Description)
			continue
		}
		got = append(got, fmt.Sprintf("%d:%d %s", is.Position.Line, is.Position.Column, is.Description))
	}
	want := []string{
		"- " + msgOldVersion,
		"2:17 Variable x already exists in the PathTemplate.",
		"3:19 Invalid function name: foo.",
	}
	if !slices.Equal(got, want) {
		t.Errorf("issues:\n got  %q\n want %q", got, want)
	}
}

// Weight counts the tree as well as the text and the pattern cache.
// A name that is no service compiles, as in production, and the ruleset grants nothing.
func TestUnknownServiceGrantsNothing(t *testing.T) {
	f := rulesFile("2", Firestore, docsBlock(block{matches: []*match{grantGet("/{d=**}", nil)}}))
	f.service = "cloud.firestorex"
	rs, issues := check(f, Firestore, DefaultLimits())
	if rs == nil || Issues(issues).Err() != nil {
		t.Fatalf("does not compile: %v", issues)
	}
	if evaluate(t, rs, request(MethodGet, docPath("c", "d"), nil)).Allow {
		t.Error("granted under an unknown service")
	}
}

// The Rules API counts a source's content, its name and some framing: for firestore.rules,
// 262,119 bytes of content compile and 262,120 do not.
func TestSourceSizeBoundary(t *testing.T) {
	lim := DefaultLimits()
	for n, want := range map[int]bool{262_119: false, 262_120: true} {
		f := File{Name: "firestore.rules", Content: strings.Repeat("x", n)}
		if got := sourceTooLarge(f, &lim); got != want {
			t.Errorf("%d bytes: too large %v, want %v", n, got, want)
		}
	}
	// The largest content each name length compiles with, as the S1-E probe measured: the
	// name's length counts, and one byte more from 128, where its length takes two bytes.
	for name, largest := range map[int]int{1: 262_133, 15: 262_119, 100: 262_034, 127: 262_007, 128: 262_005, 129: 262_004, 300: 261_833} {
		for n, want := range map[int]bool{largest: false, largest + 1: true} {
			f := File{Name: strings.Repeat("n", name), Content: strings.Repeat("x", n)}
			if got := sourceTooLarge(f, &lim); got != want {
				t.Errorf("name of %d bytes, %d bytes of content: too large %v, want %v", name, n, got, want)
			}
		}
	}
	rs, issues := Compile(Firestore, File{Name: "firestore.rules", Content: strings.Repeat("/", 262_120)}, lim)
	if rs != nil || len(issues) != 1 || issues[0].Description != "Total size of sources must be smaller than 262144." {
		t.Errorf("Compile = %v, %v", rs, issues)
	}
}

func TestWeightCountsTheTree(t *testing.T) {
	rs := compile(t, Firestore, rulesFile("2", Firestore, docsBlock(block{matches: []*match{grantGet("/{d=**}", and(tru(), tru()))}})))
	if rs.Weight() <= rs.lim.RegexCacheBytes {
		t.Errorf("Weight() = %d counts no tree", rs.Weight())
	}
}
