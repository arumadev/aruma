package rules

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// The fuzz targets check that no rules document, however hostile, panics the loader or the
// engine, and that loading stays fast. Run them longer with, for example:
//
//	go test ./internal/services/rtdb/rules -run '^$' -fuzz FuzzParse -fuzztime 60s

// fuzzBudget bounds one load or check. A document is at most MaxSize bytes, so the bound is
// generous; it catches a quadratic or exponential path, not a slow machine.
const fuzzBudget = 2 * time.Second

func seedDocuments(f *testing.F) {
	raw, err := os.ReadFile("testdata/targaryen/fixtures.json")
	if err != nil {
		f.Fatal(err)
	}
	var fx targaryenFixtures
	if err := json.Unmarshal(raw, &fx); err != nil {
		f.Fatal(err)
	}
	for _, tc := range fx.Tests {
		doc, _ := json.Marshal(map[string]any{"rules": map[string]any{"$color": map[string]any{".read": tc.Rule, ".write": tc.Rule}}})
		f.Add(string(doc))
	}
	gf, err := os.ReadFile("testdata/emulator/matrix.golden.json")
	if err != nil {
		f.Fatal(err)
	}
	var g goldenFile
	if err := json.Unmarshal(gf, &g); err != nil {
		f.Fatal(err)
	}
	for _, l := range g.Loads {
		f.Add(l.Doc)
	}
	for _, gr := range g.Groups[len(g.Groups)-30:] {
		f.Add(string(gr.Rules))
	}
}

func FuzzParse(f *testing.F) {
	seedDocuments(f)
	data, err := NewMemorySource(map[string]any{"a": map[string]any{"b": "x", "n": 1.0}, "blue": true})
	if err != nil {
		f.Fatal(err)
	}
	auth, _ := User("u1", map[string]any{"email": "a@b.c", "arr": []any{"x"}})
	f.Fuzz(func(t *testing.T, doc string) {
		start := time.Now()
		rs, err := Parse([]byte(doc))
		if d := time.Since(start); d > fuzzBudget {
			t.Fatalf("Parse took %v", d)
		}
		if err != nil {
			if err.Error() == "" {
				t.Fatal("an error without a message")
			}
			return
		}
		ctx := context.Background()
		for _, env := range []Env{{Data: data}, {Auth: auth, Data: data}} {
			for _, p := range [][]string{nil, {"a"}, {"a", "b"}, {"blue", "x", "y"}} {
				if _, err := rs.CanRead(ctx, env, p, PlainListen); err != nil {
					t.Fatal(err)
				}
				if _, err := rs.CanWrite(ctx, env, Change{Path: p, Value: map[string]any{"b": "y", "c": []any{1.0}}}); err != nil {
					t.Fatal(err)
				}
				if _, err := rs.CanWrite(ctx, env, Change{Path: p}); err != nil {
					t.Fatal(err)
				}
			}
		}
		rs.IndexOn([]string{"a", "b"})
		if d := time.Since(start); d > fuzzBudget {
			t.Fatalf("checks took %v", d)
		}
	})
}

// FuzzExpression fuzzes one expression inside a fixed document, where most of the grammar lives.
func FuzzExpression(f *testing.F) {
	raw, err := os.ReadFile("testdata/targaryen/fixtures.json")
	if err != nil {
		f.Fatal(err)
	}
	var fx targaryenFixtures
	if err := json.Unmarshal(raw, &fx); err != nil {
		f.Fatal(err)
	}
	for _, tc := range fx.Tests {
		f.Add(tc.Rule)
	}
	data, _ := NewMemorySource(map[string]any{"foo": map[string]any{"bar": true}, "str": "bar"})
	f.Fuzz(func(t *testing.T, e string) {
		for _, kind := range []string{".read", ".write", ".validate"} {
			doc, _ := json.Marshal(map[string]any{"rules": map[string]any{"$color": map[string]any{kind: e}}})
			rs, err := Parse(doc)
			if err != nil {
				continue
			}
			ctx := context.Background()
			if _, err := rs.CanRead(ctx, Env{Data: data}, []string{"foo"}, PlainREST); err != nil {
				t.Fatal(err)
			}
			if _, err := rs.CanWrite(ctx, Env{Data: data}, Change{Path: []string{"foo", "bar"}, Value: "blue"}); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func FuzzRegex(f *testing.F) {
	for _, s := range []string{"/abc/", "/^a{2,3}$/i", `/[\d-z]/`, "/(a|b)*/", "/a**/", `/\{foo}/`, "/[]a]/", "/^(foo|)$/", "/()/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		re, err := translateRegex("/" + body + "/")
		if err == nil {
			re.MatchString("some text to match, with ünïcode and 😀")
		}
	})
}
