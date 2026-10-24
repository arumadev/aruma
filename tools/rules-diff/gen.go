package main

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
)

// node is one expression of the rules language, as the generator builds it. It is printed with
// every compound in parentheses, so precedence never decides what it means.
type node struct {
	kind string // lit, ref, bin, un, is, cond, call, method, index, field, list, map
	text string // the literal, the reference, the operator, the function, the method or the field
	kids []*node
}

func (n *node) String() string {
	var b strings.Builder
	n.write(&b)
	return b.String()
}

func (n *node) write(b *strings.Builder) {
	args := func(from int) {
		for i, k := range n.kids[from:] {
			if i > 0 {
				b.WriteString(", ")
			}
			k.write(b)
		}
	}
	switch n.kind {
	case "lit", "ref":
		b.WriteString(n.text)
	case "bin":
		b.WriteString("(")
		n.kids[0].write(b)
		b.WriteString(" " + n.text + " ")
		n.kids[1].write(b)
		b.WriteString(")")
	case "un":
		b.WriteString(n.text + "(")
		n.kids[0].write(b)
		b.WriteString(")")
	case "is":
		b.WriteString("(")
		n.kids[0].write(b)
		b.WriteString(" is " + n.text + ")")
	case "cond":
		b.WriteString("(")
		n.kids[0].write(b)
		b.WriteString(" ? ")
		n.kids[1].write(b)
		b.WriteString(" : ")
		n.kids[2].write(b)
		b.WriteString(")")
	case "call":
		b.WriteString(n.text + "(")
		args(0)
		b.WriteString(")")
	case "method":
		receiver(b, n.kids[0])
		b.WriteString("." + n.text + "(")
		args(1)
		b.WriteString(")")
	case "index":
		receiver(b, n.kids[0])
		b.WriteString("[")
		n.kids[1].write(b)
		b.WriteString("]")
	case "field":
		receiver(b, n.kids[0])
		b.WriteString("." + n.text)
	case "list":
		b.WriteString("[")
		args(0)
		b.WriteString("]")
	case "map":
		b.WriteString("{")
		for i := 0; i+1 < len(n.kids); i += 2 {
			if i > 0 {
				b.WriteString(", ")
			}
			n.kids[i].write(b)
			b.WriteString(": ")
			n.kids[i+1].write(b)
		}
		b.WriteString("}")
	default:
		panic("unknown node " + n.kind)
	}
}

// receiver writes the left side of a.f, a.f() or a[i], in parentheses where it would otherwise
// bind differently: a unary operator, or a number, whose '.' the lexer would read as a fraction.
func receiver(b *strings.Builder, n *node) {
	if n.kind == "un" || n.kind == "lit" && strings.ContainsAny(n.text[:1], "-0123456789") {
		b.WriteString("(")
		n.write(b)
		b.WriteString(")")
		return
	}
	n.write(b)
}

// size counts the nodes of the tree.
func (n *node) size() int {
	s := 1
	for _, k := range n.kids {
		s += k.size()
	}
	return s
}

// clone copies the tree.
func (n *node) clone() *node {
	c := &node{kind: n.kind, text: n.text, kids: make([]*node, len(n.kids))}
	for i, k := range n.kids {
		c.kids[i] = k.clone()
	}
	return c
}

func lit(s string) *node              { return &node{kind: "lit", text: s} }
func ref(s string) *node              { return &node{kind: "ref", text: s} }
func bin(op string, a, b *node) *node { return &node{kind: "bin", text: op, kids: []*node{a, b}} }

// gen builds random conditions and requests from a seed.
type gen struct{ r *rand.Rand }

func newGen(seed uint64) *gen { return &gen{r: rand.New(rand.NewPCG(seed, 0x61727561))} }

func (g *gen) pick(xs ...string) string { return xs[g.r.IntN(len(xs))] }

func (g *gen) chance(p float64) bool { return g.r.Float64() < p }

var (
	ints    = []string{"0", "1", "2", "3", "-1", "7", "10", "100", "1000", "9223372036854775807", "-9223372036854775807"}
	floats  = []string{"0.0", "-0.0", "1.5", "2.0", "-2.5", "0.1", "3.25"}
	strs    = []string{"''", "'a'", "'abc'", "'Abc'", "'a/b'", "'alice'", "'editor'", "'é'", "'😀'", "' x '", "'1'", "'1.5'", "'true'", `'a\'b'`}
	types   = []string{"int", "float", "number", "string", "bool", "list", "map", "timestamp", "duration", "path", "bytes", "latlng"}
	binops  = []string{"==", "!=", "<", "<=", ">", ">=", "&&", "||", "+", "-", "*", "/", "%", "in"}
	refs    = []string{"resource.data.n", "resource.data.f", "resource.data.s", "resource.data.b", "resource.data.nul", "resource.data.arr", "resource.data.tags", "resource.data.m", "resource.data.m.x", "resource.data.missing", "resource.id", "resource", "request.auth", "request.auth.uid", "request.auth.token.email", "request.auth.token.admin", "request.auth.token.groups", "request.auth.token.missing", "request.method", "request.path", "request.resource.data.n", "request.resource.data.s", "request.resource.data", "doc", "database"}
	methods = map[string][]string{ // method: the kinds of argument it takes
		"size": nil, "lower": nil, "upper": nil, "trim": nil, "keys": nil, "values": nil, "toSet": nil,
		"matches": {"pattern"}, "split": {"str"}, "replace": {"str", "str"}, "hasAny": {"list"}, "hasAll": {"list"},
		"hasOnly": {"list"}, "get": {"str", "any"}, "concat": {"list"}, "join": {"str"}, "removeAll": {"list"},
		"toUtf8": nil, "diff": {"map"},
	}
	functions = map[string][]string{
		"int": {"any"}, "float": {"any"}, "string": {"any"}, "bool": {"any"}, "math.abs": {"num"}, "math.floor": {"num"},
		"math.ceil": {"num"}, "math.round": {"num"}, "math.isNaN": {"num"}, "math.isInfinite": {"num"}, "math.sqrt": {"num"},
		"math.pow": {"num", "num"}, "timestamp.date": {"year", "small", "small"}, "duration.value": {"small", "unit"},
		"path": {"pathstr"}, "exists": {"doc"}, "get": {"doc"}, "hashing.sha256": {"any"}, "latlng.value": {"num", "num"},
	}
	methodNames   = sortedKeys(methods)
	functionNames = sortedKeys(functions)
	patterns      = []string{"'a.*'", "'.*'", "'[a-z]+'", "'^a'", "'(a|b)c'", "'\\\\d+'", "'é'", "'[[:alpha:]]+'"}
	docPaths      = []string{"/databases/$(database)/documents/u/alice", "/databases/(default)/documents/u/bob", "/databases/$(database)/documents/u/$(request.auth.uid)"}
)

func sortedKeys(m map[string][]string) []string { return slices.Sorted(maps.Keys(m)) }

func (g *gen) literal() *node {
	switch g.r.IntN(6) {
	case 0:
		return lit(g.pick(ints...))
	case 1:
		return lit(g.pick(floats...))
	case 2:
		return lit(g.pick(strs...))
	case 3:
		return lit(g.pick("true", "false"))
	case 4:
		return lit("null")
	}
	return &node{kind: "list", kids: []*node{lit(g.pick(ints...)), lit(g.pick(strs...))}}
}

func (g *gen) arg(kind string, depth int) *node {
	switch kind {
	case "pattern":
		return lit(g.pick(patterns...))
	case "str":
		return lit(g.pick(strs...))
	case "list":
		n := &node{kind: "list"}
		for range g.r.IntN(3) {
			n.kids = append(n.kids, g.literal())
		}
		return n
	case "map":
		return &node{kind: "map", kids: []*node{lit("'x'"), lit(g.pick(ints...)), lit("'n'"), g.literal()}}
	case "num":
		if g.chance(0.5) {
			return lit(g.pick(floats...))
		}
		return lit(g.pick(ints...))
	case "year":
		return lit(g.pick("2020", "1", "9999", "0", "10000"))
	case "small":
		return lit(g.pick("1", "2", "12", "29", "30", "31", "0", "-1"))
	case "unit":
		return lit(g.pick("'s'", "'m'", "'h'", "'d'", "'w'", "'ms'", "'ns'", "'x'"))
	case "pathstr":
		return lit(g.pick("'/a/b'", "'a/b'", "'/databases/x/documents/c/d'", "'/a%20b/c'", "''"))
	case "doc":
		return lit(g.pick(docPaths...))
	}
	return g.expr(depth)
}

var (
	numRefs  = []string{"resource.data.n", "resource.data.f", "resource.data.m.x", "request.resource.data.n", "resource.data.missing"}
	strRefs  = []string{"resource.data.s", "request.auth.uid", "request.auth.token.email", "request.method", "doc", "database", "resource.id", "request.resource.data.s"}
	listRefs = []string{"resource.data.arr", "resource.data.tags", "request.auth.token.groups"}
	mapRefs  = []string{"resource.data", "resource.data.m", "request.resource.data", "request.auth.token", "request.auth"}
	boolRefs = []string{"resource.data.b", "request.auth.token.admin", "resource.data.nul", "resource.data.missing"}
	kinds    = []string{"bool", "num", "str", "list", "map"}
)

func method(name string, recv *node, args ...*node) *node {
	return &node{kind: "method", text: name, kids: append([]*node{recv}, args...)}
}

func call(name string, args ...*node) *node { return &node{kind: "call", text: name, kids: args} }

// expr returns an expression of any type, of at most depth levels.
func (g *gen) expr(depth int) *node {
	if g.chance(0.15) {
		return g.wild(depth)
	}
	return g.typed(g.pick(kinds...), depth)
}

// typed returns an expression that has type t when its parts have the types they usually have:
// most conditions then decide true or false instead of failing on a type error, which says
// little about the language.
func (g *gen) typed(t string, depth int) *node {
	leaf := depth <= 0 || g.chance(0.3)
	d := depth - 1
	switch t {
	case "bool":
		if leaf {
			switch g.r.IntN(3) {
			case 0:
				return lit(g.pick("true", "false"))
			case 1:
				return ref(g.pick(boolRefs...))
			}
			return bin(g.pick("==", "!=", "<", ">="), g.typed("num", 0), g.typed("num", 0))
		}
		switch g.r.IntN(12) {
		case 0, 1:
			return bin(g.pick("&&", "||"), g.typed("bool", d), g.typed("bool", d))
		case 2:
			return &node{kind: "un", text: "!", kids: []*node{g.typed("bool", d)}}
		case 3, 4:
			return bin(g.pick("==", "!=", "<", "<=", ">", ">="), g.typed("num", d), g.typed("num", d))
		case 5:
			return bin(g.pick("==", "!=", "<", ">"), g.typed("str", d), g.typed("str", d))
		case 6:
			return &node{kind: "is", text: g.pick(types...), kids: []*node{g.expr(d)}}
		case 7:
			if g.chance(0.5) {
				return bin("in", g.typed(g.pick("num", "str"), d), g.typed("list", d))
			}
			return bin("in", g.typed("str", d), g.typed("map", d))
		case 8:
			if g.chance(0.5) {
				return method("matches", g.typed("str", d), lit(g.pick(patterns...)))
			}
			return method(g.pick("hasAny", "hasAll", "hasOnly"), g.typed("list", d), g.typed("list", d))
		case 9:
			doc := lit(g.pick(docPaths...))
			if g.chance(0.5) {
				return call("exists", doc)
			}
			return bin("==", &node{kind: "field", text: "data.role", kids: []*node{call("get", doc)}}, lit("'editor'"))
		case 10:
			return &node{kind: "cond", kids: []*node{g.typed("bool", d), g.typed("bool", d), g.typed("bool", d)}}
		}
		return bin(g.pick("==", "!="), g.expr(d), g.literal())
	case "num":
		if leaf {
			if g.chance(0.5) {
				return ref(g.pick(numRefs...))
			}
			return lit(g.pick(append(ints, floats...)...))
		}
		switch g.r.IntN(6) {
		case 0, 1:
			return bin(g.pick("+", "-", "*", "/", "%"), g.typed("num", d), g.typed("num", d))
		case 2:
			return &node{kind: "un", text: "-", kids: []*node{g.typed("num", d)}}
		case 3:
			return method("size", g.typed(g.pick("str", "list", "map"), d))
		case 4:
			name := g.pick("int", "float", "math.abs", "math.floor", "math.ceil", "math.round")
			if name == "int" || name == "float" {
				return call(name, g.typed(g.pick("num", "str"), d))
			}
			return call(name, g.typed("num", d))
		}
		return &node{kind: "index", kids: []*node{g.typed("list", d), lit(g.pick("0", "1", "2", "-1", "5"))}}
	case "str":
		if leaf {
			if g.chance(0.5) {
				return ref(g.pick(strRefs...))
			}
			return lit(g.pick(strs...))
		}
		switch g.r.IntN(5) {
		case 0:
			return bin("+", g.typed("str", d), g.typed("str", d))
		case 1:
			return method(g.pick("lower", "upper", "trim"), g.typed("str", d))
		case 2:
			return method("replace", g.typed("str", d), lit(g.pick(strs...)), lit(g.pick(strs...)))
		case 3:
			return call("string", g.expr(d))
		}
		return &node{kind: "index", kids: []*node{g.typed("str", d), lit(g.pick("0", "1", "-1"))}}
	case "list":
		if leaf {
			if g.chance(0.5) {
				return ref(g.pick(listRefs...))
			}
			return g.arg("list", 0)
		}
		switch g.r.IntN(4) {
		case 0:
			return method("split", g.typed("str", d), lit(g.pick("'/'", "'a'", "''", "' '")))
		case 1:
			return method(g.pick("keys", "values"), g.typed("map", d))
		case 2:
			return method(g.pick("concat", "removeAll"), g.typed("list", d), g.typed("list", d))
		}
		return &node{kind: "list", kids: []*node{g.expr(d), g.expr(d)}}
	}
	if leaf || g.chance(0.5) {
		if g.chance(0.5) {
			return ref(g.pick(mapRefs...))
		}
		return g.arg("map", 0)
	}
	return &node{kind: "map", kids: []*node{lit(g.pick("'x'", "'n'", "'s'")), g.expr(d)}}
}

// wild returns an expression of any shape, with no regard for types.
func (g *gen) wild(depth int) *node {
	if depth <= 0 || g.chance(0.25) {
		if g.chance(0.5) {
			return ref(g.pick(refs...))
		}
		return g.literal()
	}
	d := depth - 1
	switch g.r.IntN(10) {
	case 0, 1, 2:
		return bin(g.pick(binops...), g.expr(d), g.expr(d))
	case 3:
		return &node{kind: "un", text: g.pick("!", "-"), kids: []*node{g.expr(d)}}
	case 4:
		return &node{kind: "is", text: g.pick(types...), kids: []*node{g.expr(d)}}
	case 5:
		return &node{kind: "cond", kids: []*node{g.expr(d), g.expr(d), g.expr(d)}}
	case 6:
		name := g.pick(methodNames...)
		n := &node{kind: "method", text: name, kids: []*node{g.expr(d)}}
		for _, a := range methods[name] {
			n.kids = append(n.kids, g.arg(a, d))
		}
		return n
	case 7:
		name := g.pick(functionNames...)
		n := &node{kind: "call", text: name}
		for _, a := range functions[name] {
			n.kids = append(n.kids, g.arg(a, d))
		}
		if name == "get" && g.chance(0.7) {
			return &node{kind: "field", text: g.pick("data.role", "data", "id"), kids: []*node{n}}
		}
		return n
	case 8:
		return &node{kind: "index", kids: []*node{g.expr(d), g.pick2(lit(g.pick("0", "1", "-1", "'x'", "'n'")), g.expr(d))}}
	}
	return bin(g.pick("==", "!=", "<", ">="), g.expr(d), g.literal())
}

func (g *gen) pick2(a, b *node) *node {
	if g.chance(0.7) {
		return a
	}
	return b
}

// genCase is one generated case: a condition and a request.
type genCase struct {
	Index     int            `json:"index"`
	Condition string         `json:"condition"`
	Method    string         `json:"method"`
	Auth      bool           `json:"auth"`     // signed in as alice
	Existing  map[string]any `json:"existing"` // the document's data before the request; nil when it does not exist
	Incoming  map[string]any `json:"incoming"` // request.resource.data on create and update
	Alice     bool           `json:"alice"`    // u/alice exists, for get() and exists()

	tree *node
}

// doc is the document the case reads or writes, one per case: after some cases the emulator
// keeps the lock of the document they used for longer than a write waits ("409 Transaction lock
// timeout.", seeds 3 and 7 on 2026-10-24), so no case reuses another's.
func (c *genCase) doc() string { return fmt.Sprintf("c/d%d", c.Index) }

var aliceToken = map[string]any{"email": "alice@example.com", "admin": true, "role": "editor", "groups": []any{"g1", "g2"}}

func (g *gen) value() any {
	switch g.r.IntN(7) {
	case 0:
		return g.r.IntN(20) - 5
	case 1:
		return 1.5
	case 2:
		return g.pick("", "abc", "alice", "é", "a/b")
	case 3:
		return g.chance(0.5)
	case 4:
		return nil
	case 5:
		return []any{1, "a"}
	}
	return map[string]any{"x": 1, "y": "z"}
}

func (g *gen) document() map[string]any {
	d := map[string]any{"n": 1, "f": 1.5, "s": "abc", "b": true, "nul": nil, "arr": []any{1, 2, 3}, "tags": []any{"a", "b"}, "m": map[string]any{"x": 1, "y": "z"}}
	for _, k := range []string{"n", "s", "m"} {
		if g.chance(0.3) {
			d[k] = g.value()
		}
	}
	return d
}

// next returns the i-th case of the stream.
func (g *gen) next(i, depth int) *genCase {
	tree := g.typed("bool", depth)
	c := &genCase{Index: i, Method: g.pick("get", "get", "create", "update", "delete"), Auth: g.chance(0.7), Alice: g.chance(0.6), tree: tree}
	switch c.Method {
	case "create":
		c.Incoming = g.document()
	case "update":
		c.Existing, c.Incoming = g.document(), g.document()
	default:
		if g.chance(0.8) {
			c.Existing = g.document()
		}
	}
	c.Condition = tree.String()
	return c
}

// ruleset returns the rules file of one case: the condition on its method, on /c/{doc}.
func ruleset(method, condition string) string {
	return fmt.Sprintf(`rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /c/{doc} {
      allow %s: if %s;
    }
  }
}
`, method, condition)
}

// shrinks returns the trees one step smaller than t, larger steps first: each node, from the
// root down, replaced by one of its children or by a plain literal.
func shrinks(t *node) []*node {
	var out []*node
	var walk func(path []int)
	walk = func(path []int) {
		n := at(t, path)
		for _, k := range n.kids {
			out = append(out, replaced(t, path, k))
		}
		if n.kind != "lit" {
			for _, l := range []string{"true", "false", "1", "null"} {
				out = append(out, replaced(t, path, lit(l)))
			}
		}
		for i := range n.kids {
			walk(append(append([]int{}, path...), i))
		}
	}
	walk(nil)
	return out
}

func at(t *node, path []int) *node {
	for _, i := range path {
		t = t.kids[i]
	}
	return t
}

// replaced returns a copy of t with the node at path replaced by a copy of with.
func replaced(t *node, path []int, with *node) *node {
	if len(path) == 0 {
		return with.clone()
	}
	c := &node{kind: t.kind, text: t.text, kids: append([]*node{}, t.kids...)}
	c.kids[path[0]] = replaced(t.kids[path[0]], path[1:], with)
	return c
}
