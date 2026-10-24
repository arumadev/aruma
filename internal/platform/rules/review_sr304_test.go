package rules

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// functionsFile returns a Firestore file of the functions bodies[i] returns, f0 granting get.
func functionsFile(n int, body func(i int) string) string {
	var b strings.Builder
	b.WriteString("rules_version = '2';\nservice cloud.firestore {\n  match /databases/{database}/documents {\n")
	for i := range n {
		fmt.Fprintf(&b, "function f%d(){return %s;}\n", i, body(i))
	}
	b.WriteString("    match /c/{id} {\n      allow get: if f0();\n    }\n  }\n}\n")
	return b.String()
}

// SR3-04: every edge back to a function on the stack made an error naming the whole stack. 6,200
// functions each calling f0 and the next made 6,200 errors and 104 MiB of text, 433 MiB at peak,
// in 345 ms. Each cycle is now reported once, naming at most maxCycleNames functions, and at
// most maxCycleErrors cycles.
func TestCallCycleErrorsAreBounded(t *testing.T) {
	const n = 6200
	for _, c := range []struct {
		name   string
		n      int
		body   func(i int) string
		errors int
		first  string
	}{
		{"each calls f0 and the next", n, func(i int) string {
			if i == n-1 {
				return "f0()"
			}
			return fmt.Sprintf("f0()||f%d()", i+1)
		}, 1, msgRecursive},
		{"one cycle through all", n, func(i int) string { return fmt.Sprintf("f%d()", (i+1)%n) }, 1,
			"Detected a call cycle between [f0,f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13,f14,f15,f16,f17,f18,f19,f20,f21,f22,f23,f24,f25,f26,f27,f28,f29,f30,f31 and 6168 more] functions."},
		{"each calls the one before and the next", 5000, func(i int) string {
			switch i {
			case 0:
				return "f1()"
			case 5000 - 1:
				return fmt.Sprintf("f%d()", i-1)
			}
			return fmt.Sprintf("f%d()||f%d()", i-1, i+1)
		}, maxCycleErrors, "Detected a call cycle between [f0,f1] functions."},
	} {
		src := functionsFile(c.n, c.body)
		if len(src) > firebaseSourceBytes {
			t.Fatalf("%s: %d bytes", c.name, len(src))
		}
		var rs *Ruleset
		var issues Issues
		start := time.Now()
		mem := allocated(func() { rs, issues = Compile(Firestore, File{Name: "firestore.rules", Content: src}, DefaultLimits()) })
		d := time.Since(start)
		var errs []Issue
		text := 0
		for _, is := range issues {
			text += len(is.Description)
			if is.Severity == SeverityError {
				errs = append(errs, is)
			}
		}
		t.Logf("%s: %v, allocated %d MiB, %d errors, %d bytes of text", c.name, d, mem>>20, len(errs), text)
		if rs != nil || len(errs) != c.errors || errs[0].Description != c.first {
			t.Errorf("%s: %d errors, the first %q; want %d, %q", c.name, len(errs), errs[0].Description, c.errors, c.first)
		}
		if text > 1<<20 {
			t.Errorf("%s: %d bytes of issues", c.name, text)
		}
		if d > within(2*time.Second) {
			t.Errorf("%s: took %v", c.name, d)
		}
		if !raceEnabled && mem > 256<<20 {
			t.Errorf("%s: allocated %d MiB", c.name, mem>>20)
		}
	}
}
