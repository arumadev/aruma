package rules_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/platform/rules/rulestest"
)

// The fuzz targets check that no rules file or request, however hostile, panics the engine or
// runs long, and that a decision does not depend on anything but its input. They are seeded
// from the development oracle only: the held-out set is not on this branch, and the reader
// never reads a heldout directory. Run them longer with, for example:
//
//	go test ./internal/platform/rules -run '^$' -fuzz FuzzParse -fuzztime 10m
//
// While Compile is the contract's stub they skip.

// fuzzBudget bounds one compile or one evaluation. It catches a quadratic or exponential path,
// not a slow machine: the limits keep an evaluation far below it.
const fuzzBudget = 2 * time.Second

// maxSeed leaves the size cases (up to 1 MiB) out of the seeds: they test a limit, not a
// shape, and would slow every run of the seed corpus.
const maxSeed = 16 << 10

func skipIfStub(f *testing.F) {
	if s := stub(); s != "" {
		f.Skip("Compile is the contract's stub: " + s)
	}
}

// seedRulesets returns the distinct rules files of the development oracle, smallest first.
func seedRulesets(f *testing.F) []string {
	set := loadOracle(f)
	seen := map[string]bool{}
	var out []string
	for _, c := range set.Cases {
		if c.Ruleset == "" || len(c.Ruleset) > maxSeed || seen[c.Ruleset] {
			continue
		}
		seen[c.Ruleset] = true
		out = append(out, c.Ruleset)
	}
	return out
}

// serviceOf guesses the service a fuzzed file is for from its declaration.
func serviceOf(src string) rules.Service {
	if strings.Contains(src, "firebase.storage") {
		return rules.Storage
	}
	return rules.Firestore
}

func FuzzParse(f *testing.F) {
	skipIfStub(f)
	for _, src := range seedRulesets(f) {
		f.Add(src)
	}
	lim := rules.DefaultLimits()
	f.Fuzz(func(t *testing.T, src string) {
		lines := strings.Count(src, "\n") + 1
		runes := utf8.RuneCountInString(src)
		for _, svc := range []rules.Service{rules.Firestore, rules.Storage} {
			start := time.Now()
			rs, issues := rules.Compile(svc, rules.File{Name: "fuzz.rules", Content: src}, lim)
			if d := time.Since(start); d > fuzzBudget {
				t.Fatalf("%s: Compile took %v", svc, d)
			}
			if (rs == nil) != (issues.Err() != nil) {
				t.Fatalf("%s: ruleset %v with issues %v", svc, rs != nil, issues)
			}
			for _, is := range issues {
				if is.Description == "" {
					t.Fatalf("%s: an issue without a description: %+v", svc, is)
				}
				p := is.Position
				if p == nil {
					continue
				}
				if p.Line < 1 || p.Line > lines || p.Column < 1 || p.CurrentOffset < 0 ||
					p.CurrentOffset > p.EndOffset || p.EndOffset > runes {
					t.Fatalf("%s: issue %q at %+v, outside %d lines and %d code points", svc, is.Description, *p, lines, runes)
				}
			}
		}
	})
}

// evalSeed is one request of a seed of FuzzEvaluate.
type evalSeed struct {
	method   uint8
	path     string
	resource string // the existing resource, JSON
	data     string // request.resource, JSON
}

var fuzzMethods = []rules.Method{rules.MethodGet, rules.MethodList, rules.MethodCreate, rules.MethodUpdate, rules.MethodDelete}

func FuzzEvaluate(f *testing.F) {
	skipIfStub(f)
	set := loadOracle(f)
	seen := map[string]bool{}
	for _, c := range set.Cases {
		if c.Ruleset == "" || len(c.Ruleset) > maxSeed || c.Request == nil || c.Provenance != rulestest.TestAPI {
			continue
		}
		m, ok := rulestest.Method(c.Request.Method)
		if !ok {
			continue
		}
		s := evalSeed{method: uint8(m - 1), path: c.Request.Path, resource: "null", data: "null"}
		if c.Resource.Present() {
			s.resource = string(c.Resource.Raw())
		}
		if c.Request.Resource.Present() {
			s.data = string(c.Request.Resource.Raw())
		}
		k := c.Ruleset + "\x00" + s.path + "\x00" + s.resource + "\x00" + s.data
		if seen[k] {
			continue
		}
		seen[k] = true
		f.Add(c.Ruleset, s.method, s.path, s.resource, s.data)
	}
	lim := rules.DefaultLimits()
	inflight := rules.NewInflight(lim.InflightBytes)
	auth, _ := rulestest.TestAPIValue([]byte(`{"uid": "alice", "token": {"sub": "alice", "admin": true, "groups": ["g1"]}}`))
	when := time.Date(2026, 1, 15, 10, 20, 30, 123e6, time.UTC)
	f.Fuzz(func(t *testing.T, src string, method uint8, path, resource, data string) {
		svc := serviceOf(src)
		rs, _ := rules.Compile(svc, rules.File{Name: "fuzz.rules", Content: src}, lim)
		if rs == nil {
			return
		}
		m := fuzzMethods[int(method)%len(fuzzMethods)]
		segs := rulestest.Segments(path)
		if len(segs) > lim.RequestSegments {
			segs = segs[:lim.RequestSegments]
		}
		build := func() *rules.Request {
			req := map[string]rules.Value{
				"method": rules.String(m.String()),
				"path":   rules.PathOf(segs),
				"auth":   auth,
				"time":   rules.Timestamp(when),
			}
			vars := map[string]rules.Value{"request": rules.Map(req)}
			if v, err := rulestest.TestAPIValue([]byte(resource)); err == nil && json.Valid([]byte(resource)) {
				vars["resource"] = v
			}
			if v, err := rulestest.TestAPIValue([]byte(data)); err == nil && json.Valid([]byte(data)) {
				req["resource"] = v
			}
			docs := rulestest.NewMemoryDocuments()
			docs.Missing = rules.ErrNoDocuments
			return &rules.Request{
				Project:   rulestest.Project,
				Method:    m,
				Path:      rules.Literals(segs...),
				Variables: vars,
				Documents: docs,
				Budget:    rules.NewBudget(),
				Inflight:  inflight,
				Trace:     rules.TraceStatements,
			}
		}
		var first rules.Decision
		for i := range 2 {
			start := time.Now()
			d, err := rs.Evaluate(context.Background(), build())
			if el := time.Since(start); el > fuzzBudget {
				t.Fatalf("Evaluate took %v", el)
			}
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if i == 0 {
				first = d
				continue
			}
			if d.Allow != first.Allow || len(d.Statements) != len(first.Statements) {
				t.Fatalf("two evaluations differ: %+v, then %+v", first, d)
			}
			for j := range d.Statements {
				if d.Statements[j].Outcome != first.Statements[j].Outcome {
					t.Fatalf("statement %d: %v, then %v", j, first.Statements[j].Outcome, d.Statements[j].Outcome)
				}
			}
		}
	})
}

// FuzzRegex checks that matches() on a pattern built while evaluating, whatever the pattern and
// the text, ends within the budget and decides the same twice: the cost of a pattern is
// bounded before it is compiled (ADR-021 §4).
func FuzzRegex(f *testing.F) {
	skipIfStub(f)
	for _, s := range [][2]string{
		{`a.*b`, "axxb"}, {`^[a-z]+@example\.com$`, "alice@example.com"}, {`(a|b)*c`, strings.Repeat("ab", 500)},
		{`\pL{1000}`, "x"}, {`(x+x+)+y`, strings.Repeat("x", 64)}, {`[`, ""}, {`a{1001}`, "a"}, {`(?i)ÄRUMA`, "äruma"},
	} {
		f.Add(s[0], s[1])
	}
	const src = `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /r/{doc} {
      allow get: if request.resource.data.s.matches(request.resource.data.p);
    }
  }
}
`
	lim := rules.DefaultLimits()
	rs, issues := rules.Compile(rules.Firestore, rules.File{Name: "firestore.rules", Content: src}, lim)
	if rs == nil {
		f.Fatal(issues.Err())
	}
	inflight := rules.NewInflight(lim.InflightBytes)
	f.Fuzz(func(t *testing.T, pattern, text string) {
		var first *rules.Decision
		for range 2 {
			data := rules.Map(map[string]rules.Value{"p": rules.String(pattern), "s": rules.String(text)})
			req := &rules.Request{
				Project: rulestest.Project,
				Method:  rules.MethodGet,
				Path:    rules.Literals("databases", "(default)", "documents", "r", "d"),
				Variables: map[string]rules.Value{
					"request": rules.Map(map[string]rules.Value{"resource": rules.Map(map[string]rules.Value{"data": data})}),
				},
				Documents: rules.NoDocuments{},
				Budget:    rules.NewBudget(),
				Inflight:  inflight,
			}
			start := time.Now()
			d, err := rs.Evaluate(context.Background(), req)
			if el := time.Since(start); el > fuzzBudget {
				t.Fatalf("matches(%q) on %d bytes took %v", pattern, len(text), el)
			}
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if first != nil && d.Allow != first.Allow {
				t.Fatalf("matches(%q) decided %v, then %v", pattern, first.Allow, d.Allow)
			}
			first = &d
		}
	})
}
