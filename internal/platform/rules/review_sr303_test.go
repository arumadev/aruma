package rules

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// SR3-03: errors copied their values whole into their messages, uncharged, and the operands
// kept every one: a list of a hundred int(s) of a 2 MiB string held 250 MiB (409 MiB
// allocated) after one denied get, the meter charged 4 MiB. A message now carries the first
// messageValueBytes of a value, the meter is charged for it, and the operands keep one error.
func TestErrorMessagesAreBounded(t *testing.T) {
	ints := make([]string, 100)
	for i := range ints {
		ints[i] = "int(s)"
	}
	src := "rules_version = '2';\nservice cloud.firestore {\n  match /databases/{database}/documents {\n" +
		"    function h(s) { return [" + strings.Join(ints, ", ") + "] == []; }\n" +
		"    function g(a) { let b = a + a; let c = b + b; let d = c + c; let e = d + d; let f = e + e; return h(f); }\n" +
		"    match /c/{id} {\n      allow get: if g('" + strings.Repeat("x", 64<<10) + "');\n    }\n  }\n}\n"
	rs, _ := compileSource(t, src)
	var d Decision
	start := time.Now()
	mem := allocated(func() { d = evaluate(t, rs, request(MethodGet, docPath("c", "d"), nil)) })
	t.Logf("100 errors on 2 MiB: %v, allocated %d MiB", time.Since(start), mem>>20)
	if d.Allow || len(d.Statements) != 1 || d.Statements[0].Outcome != OutcomeError {
		t.Fatalf("decision %+v", d)
	}
	want := "Type conversion error. Argument: [" + strings.Repeat("x", messageValueBytes) + "...], From type: [string], To type: [int]."
	if got := d.Statements[0].Message; got != want {
		t.Errorf("message of %d bytes %.80q…, want %.80q…", len(got), got, want)
	}
	// The strings themselves are 4 MiB, charged; the errors are no more than their messages.
	if !raceEnabled && mem > 32<<20 {
		t.Errorf("allocated %d MiB", mem>>20)
	}

	// The message is charged where the error is placed.
	rs, _ = compileSource(t, grantingSource("int('"+strings.Repeat("y", 1000)+"') == 1"))
	req := request(MethodGet, docPath("c", "d"), nil)
	m := newMeter(&rs.lim, req.Inflight, rs.regexes)
	ev := &evaluator{ctx: t.Context(), rs: rs, req: req, m: m}
	_, err := ev.condition(rs.blocks[0].allows[0].a.cond)
	ee, ok := err.(*evalError)
	if !ok || len(ee.msg) > 300 {
		t.Fatalf("int of 1,000 bytes: %v", err)
	}
	if m.allocated < errorBytes+int64(len(ee.msg)) {
		t.Errorf("charged %d for an error of %d bytes", m.allocated, len(ee.msg))
	}
	m.release()

	// A value is cut at a character's start; short ones are whole.
	for _, c := range []struct{ in, want string }{
		{"short", "short"},
		{strings.Repeat("a", messageValueBytes), strings.Repeat("a", messageValueBytes)},
		{strings.Repeat("a", messageValueBytes-1) + "é", strings.Repeat("a", messageValueBytes-1) + "..."},
		{strings.Repeat("é", messageValueBytes), strings.Repeat("é", messageValueBytes/2) + "..."},
	} {
		if got := clip(c.in); got != c.want || !utf8.ValidString(got) {
			t.Errorf("clip of %d bytes = %q", len(c.in), got)
		}
	}
}
