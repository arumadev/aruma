package core

import (
	"errors"
	"sort"
	"testing"
	"time"
)

func TestPushIDs(t *testing.T) {
	var g PushIDs
	// The emulator 4.11.2 answered a POST of {".sv":"timestamp"} with -P4RaVTEA8evcxceazHd
	// and stored 1792554960785 under it (probe of 2026-10-21): the id's time part is that of
	// 1792554960783, two milliseconds before the value was resolved.
	at := time.UnixMilli(1792554960783)
	id := g.Next(at)
	if len(id) != 20 || id[:8] != "-P4RaVTE" {
		t.Fatalf("id %q, want 20 characters starting -P4RaVTE", id)
	}
	// Ids from one generator sort in the order they were made: in the same millisecond, a
	// later one, and with a clock that steps back.
	ids := []string{id}
	for _, ms := range []int64{0, 0, 0, 1, 1, -5, 2} {
		ids = append(ids, g.Next(at.Add(time.Duration(ms)*time.Millisecond)))
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("ids out of order: %q", ids)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Errorf("id %q made twice", id)
		}
		seen[id] = true
		for _, c := range id {
			if !ValidKey(string(c)) {
				t.Errorf("id %q holds %q, which no key may", id, c)
			}
		}
	}
	// The random part carries over within a millisecond: all 64^12 used moves to the next.
	g.last = at.UnixMilli()
	for i := range g.rand {
		g.rand[i] = 63
	}
	if next := g.Next(at); next[:8] != pushTime(at.UnixMilli()+1) {
		t.Errorf("after the last id of a millisecond, %q does not move to the next", next)
	}
}

func pushTime(ms int64) string {
	var b [8]byte
	for i := 7; i >= 0; i-- {
		b[i] = pushChars[ms%64]
		ms /= 64
	}
	return string(b[:])
}

func TestKeyErrorPosition(t *testing.T) {
	for _, c := range []struct {
		data      string
		line, col int
	}{
		// The emulator's answers (testdata rest.golden.json, "errors").
		{`{"a.b":1}`, 1, 2},
		{`{"ok":{"b.c":1}}`, 1, 8},
		{`{"a":1, "":2}`, 1, 9},
		{"{\n  \"ok\": 1,\n  \"a$b\": 2}", 3, 3},
		{`{"é":1,"a#":2}`, 1, 8},
	} {
		_, err := ParseData([]byte(c.data), nil, 1<<20)
		var ke *KeyError
		if !errors.As(err, &ke) || ke.Line != c.line || ke.Column != c.col || !errors.Is(err, ErrInvalidKey) {
			t.Errorf("%s: %v %+v, want key error at %d:%d", c.data, err, ke, c.line, c.col)
		}
		if AsError(err) != ErrInvalidKey {
			t.Errorf("%s: AsError is %v", c.data, AsError(err))
		}
	}
}

func TestEchoJSON(t *testing.T) {
	for _, c := range []struct{ in, echo string }{
		// The emulator echoes a PUT with arrays as arrays and priorities kept (rest.golden.json).
		{`["a","b","c"]`, `["a","b","c"]`},
		{`{"0":"a","2":"c"}`, `["a",null,"c"]`},
		{`{"x":{".priority":1,"y":2},"z":{".value":true,".priority":"a"}}`, `{"x":{".priority":1,"y":2},"z":{".value":true,".priority":"a"}}`},
		{`{".priority":1,"0":"a"}`, `{".priority":1,"0":"a"}`},
		{`{"5":"a"}`, `{"5":"a"}`},
	} {
		n, err := ParseData([]byte(c.in), nil, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(n.EchoJSON()); got != c.echo {
			t.Errorf("%s: echo %s, want %s", c.in, got, c.echo)
		}
	}
}
