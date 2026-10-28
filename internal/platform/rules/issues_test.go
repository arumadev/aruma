package rules

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A source at the size limit made of unused functions gives one warning per function, about
// 9,400 of them, and still compiles fast: the checker places its issues in one ordered pass
// (checker.positions), not with a scan from the start per issue, which took seconds. Each one
// is where position puts it, and the warnings come in source order.
func TestManyIssuesCompileFast(t *testing.T) {
	lim := DefaultLimits()
	var b strings.Builder
	b.WriteString("rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n")
	for i := 0; b.Len() < lim.SourceBytes-200; i++ {
		fmt.Fprintf(&b, "function a%d(){return 1;}", i)
	}
	b.WriteString("\n  }\n}\n")
	src := b.String()
	start := time.Now()
	rs, issues := Compile(Storage, File{Name: "storage.rules", Content: src}, lim)
	took := time.Since(start)
	if rs == nil || len(issues) < 9000 {
		t.Fatalf("compiled %v with %d issues: %v", rs != nil, len(issues), issues.Err())
	}
	// Generous for -race on a loaded machine; the placement from the start per issue took 5 s.
	if took > 2*time.Second {
		t.Errorf("%d issues took %v", len(issues), took)
	}
	// A sample, each placed from the start of the file as position does.
	for i := 0; i < len(issues); i += max(1, i/4) {
		is, name := issues[i], fmt.Sprintf("a%d", i)
		at := strings.Index(src, "function "+name+"(") + len("function ")
		if want := position(src, span{at, at + len(name)}); is.Position == nil || *is.Position != want ||
			is.Description != fmt.Sprintf(msgUnusedFunction, name) {
			t.Fatalf("issue %d: %+v, want %+v", i, is, want)
		}
	}
}

// Positions count code points, lines end at \n alone, and EndOffset is the span's last code
// point, as the emulator reports them (ADR-021 §2.1).
func TestPosition(t *testing.T) {
	src := "ab\nنص 🙂 x\r\ny\rz"
	x := strings.Index(src, "x") // after the emoji: four bytes, one code point
	y := strings.Index(src, "y")
	z := strings.Index(src, "z")
	for _, c := range []struct {
		at   span
		want Position
	}{
		{span{0, 0}, Position{Line: 1, Column: 1}},
		{span{0, 1}, Position{Line: 1, Column: 1}},
		{span{1, 2}, Position{Line: 1, Column: 2, CurrentOffset: 1, EndOffset: 1}},
		{span{0, 2}, Position{Line: 1, Column: 1, EndOffset: 1}},
		{span{2, 3}, Position{Line: 1, Column: 3, CurrentOffset: 2, EndOffset: 2}},
		// The Arabic word for "text" is two code points of two bytes.
		{span{3, 7}, Position{Line: 2, Column: 1, CurrentOffset: 3, EndOffset: 4}},
		{span{x, x + 1}, Position{Line: 2, Column: 6, CurrentOffset: 8, EndOffset: 8}},
		// CRLF ends one line; the CR is the line's last column.
		{span{x + 1, x + 2}, Position{Line: 2, Column: 7, CurrentOffset: 9, EndOffset: 9}},
		{span{y, y + 1}, Position{Line: 3, Column: 1, CurrentOffset: 11, EndOffset: 11}},
		// A lone CR is a character, as ANTLR counts it.
		{span{z, z + 1}, Position{Line: 3, Column: 3, CurrentOffset: 13, EndOffset: 13}},
		// The end of the file, and offsets outside it, clamped.
		{span{len(src), len(src)}, Position{Line: 3, Column: 4, CurrentOffset: 14, EndOffset: 14}},
		{span{-5, 1 << 20}, Position{Line: 1, Column: 1, EndOffset: 13}},
		{span{5, 2}, Position{Line: 2, Column: 2, CurrentOffset: 4, EndOffset: 4}},
	} {
		if got := position(src, c.at); got != c.want {
			t.Errorf("position(%v) = %+v, want %+v", c.at, got, c.want)
		}
	}
	// Invalid UTF-8 counts a code point per byte.
	if got := position("\xff\xfex", span{2, 3}); got != (Position{Line: 1, Column: 3, CurrentOffset: 2, EndOffset: 2}) {
		t.Errorf("invalid UTF-8: %+v", got)
	}
}

// Issues come out in order, and the cursor gives the same positions as position, in order or
// not.
func TestPosCursor(t *testing.T) {
	src := strings.Repeat("ab 🙂\r\n", 50)
	cur := newPosCursor(src)
	for _, at := range []span{{0, 1}, {7, 9}, {120, 130}, {5, 6}, {len(src), len(src)}, {3, 7}, {3, 7}} {
		if got, want := cur.at(at), position(src, at); got != want {
			t.Errorf("cursor at %v = %+v, position %+v", at, got, want)
		}
	}
}

// An issue about the whole file has no position: an empty file.
func TestWholeFileIssue(t *testing.T) {
	f, issues := parse("storage.rules", "", DefaultLimits())
	if f != nil || len(issues) != 1 || issues[0].Position != nil || issues[0].File != "storage.rules" {
		t.Fatalf("parse of an empty file: %v %+v", f != nil, issues)
	}
	if got := issues[0].String(); got != "[E] Rules content empty. Compilation aborted." {
		t.Errorf("printed %q", got)
	}
	if err := Issues(issues).Err(); err == nil {
		t.Error("an empty file compiles")
	}
}
