package main

import (
	"strings"
	"testing"
)

func TestGeneratorIsSeeded(t *testing.T) {
	a, b, c := newGen(7), newGen(7), newGen(8)
	same, differs := true, false
	for i := range 50 {
		x, y, z := a.next(i, 4), b.next(i, 4), c.next(i, 4)
		if x.Condition != y.Condition || x.Method != y.Method || x.Auth != y.Auth || x.Alice != y.Alice {
			same = false
		}
		if x.Condition != z.Condition {
			differs = true
		}
		if x.Condition == "" || !strings.Contains(ruleset(x.Method, x.Condition), "allow "+x.Method+": if "+x.Condition+";") {
			t.Fatalf("case %d: %q", i, x.Condition)
		}
		if (x.Method == "create" || x.Method == "update") != (x.Incoming != nil) || x.Method == "create" && x.Existing != nil ||
			x.Method == "update" && x.Existing == nil {
			t.Errorf("case %d: %s with existing %v, incoming %v", i, x.Method, x.Existing, x.Incoming)
		}
	}
	if !same || !differs {
		t.Errorf("the same seed gives the same cases: %v; another seed others: %v", same, differs)
	}
}

func TestPrinting(t *testing.T) {
	for _, c := range []struct {
		n    *node
		want string
	}{
		{bin("&&", lit("true"), bin("==", ref("resource.data.n"), lit("1"))), "(true && (resource.data.n == 1))"},
		{method("size", lit("-1")), "(-1).size()"},
		{method("size", &node{kind: "un", text: "-", kids: []*node{ref("a")}}), "(-(a)).size()"},
		{method("lower", lit("'A'")), "'A'.lower()"},
		{method("hasAny", ref("l"), &node{kind: "list", kids: []*node{lit("1"), lit("'a'")}}), "l.hasAny([1, 'a'])"},
		{&node{kind: "index", kids: []*node{lit("2"), lit("0")}}, "(2)[0]"},
		{&node{kind: "field", text: "data.role", kids: []*node{call("get", lit("/databases/(default)/documents/u/a"))}}, "get(/databases/(default)/documents/u/a).data.role"},
		{&node{kind: "cond", kids: []*node{lit("true"), lit("1"), lit("2")}}, "(true ? 1 : 2)"},
		{&node{kind: "is", text: "int", kids: []*node{lit("1")}}, "(1 is int)"},
		{&node{kind: "map", kids: []*node{lit("'x'"), lit("1"), lit("'y'"), lit("2")}}, "{'x': 1, 'y': 2}"},
	} {
		if got := c.n.String(); got != c.want {
			t.Errorf("%s, want %s", got, c.want)
		}
	}
}

func TestShrinks(t *testing.T) {
	tree := bin("&&", bin("==", ref("resource.data.n"), lit("1")), method("matches", ref("doc"), lit("'a.*'")))
	cands := shrinks(tree)
	if len(cands) == 0 || cands[0].String() != "(resource.data.n == 1)" {
		t.Fatalf("the first shrink is %v, want the root's first child", cands)
	}
	seen := map[string]bool{}
	for _, c := range cands {
		if c.size() > tree.size() {
			t.Errorf("%s is larger than %s", c, tree)
		}
		seen[c.String()] = true
	}
	for _, want := range []string{"true", "(true && doc.matches('a.*'))", "((resource.data.n == 1) && true)", "(1 && doc.matches('a.*'))"} {
		if !seen[want] {
			t.Errorf("no shrink to %s", want)
		}
	}
	if tree.String() != "((resource.data.n == 1) && doc.matches('a.*'))" {
		t.Errorf("shrinking changed the tree: %s", tree)
	}
}

func TestShrink(t *testing.T) {
	tree := bin("||", bin("&&", lit("false"), method("matches", ref("doc"), lit("'a.*'"))), bin("==", ref("resource.data.n"), lit("1")))
	tries := 0
	got, err := shrink(tree, 100, func(cond string) (bool, error) {
		tries++
		return strings.Contains(cond, "doc"), nil
	})
	if err != nil || got != "doc" {
		t.Errorf("shrunk to %q (%v) in %d tries, want doc", got, err, tries)
	}
	if got, _ := shrink(tree, 0, func(string) (bool, error) { return true, nil }); got != tree.String() {
		t.Errorf("no tries shrank to %q", got)
	}
}

// TestEngineFailureDisagrees: an engine that panicked or failed disagrees with any verdict of
// the emulator, a compile error and a deny included.
func TestEngineFailureDisagrees(t *testing.T) {
	compileError := verdict{Outcome: "compile-error"}
	deny := verdict{Compiled: true, Outcome: "deny"}
	for _, c := range []struct {
		emu, eng verdict
		want     bool
	}{
		{compileError, verdict{Outcome: "panic", Message: "nil map"}, true},
		{deny, verdict{Compiled: true, Outcome: "outage", Message: "rules: Firestore documents are not available"}, true},
		{deny, verdict{Compiled: true, Outcome: "error"}, false},
		{compileError, verdict{Outcome: "compile-error"}, false},
		{deny, verdict{Compiled: true, Allow: true, Outcome: "allow"}, true},
	} {
		if got := disagree(c.emu, c.eng); got != c.want {
			t.Errorf("emulator %s, engine %s: disagree %v, want %v", c.emu.Outcome, c.eng.Outcome, got, c.want)
		}
	}
}
