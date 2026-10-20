package core

import (
	"errors"
	"strings"
	"testing"
)

func mustParse(t *testing.T, s string) *Node {
	t.Helper()
	n, err := ParseData([]byte(s), nil, 10<<20)
	if err != nil {
		t.Fatalf("ParseData(%s): %v", s, err)
	}
	return n
}

func TestParseData(t *testing.T) {
	cases := []struct {
		in, export string
	}{
		{`null`, `null`},
		{`{}`, `null`},
		{`{"a":null}`, `null`},
		{`true`, `true`},
		{`1`, `1`},
		{`1.0`, `1`},
		{`-0`, `0`},
		{`1e3`, `1000`},
		{`1e21`, `1e+21`},
		{`0.0000001`, `1e-7`},
		{`12345678901234567890`, `12345678901234567000`},
		{`"<a&b>\u0001\"\\"`, `"<a&b>\u0001\"\\"`},
		{`"مرحبا 😀"`, `"مرحبا 😀"`},
		{`{"b":2,"a":1,"10":0,"9":0}`, `{"9":0,"10":0,"a":1,"b":2}`},
		{`[1,2]`, `{"0":1,"1":2}`},
		{`["a",null,"c"]`, `{"0":"a","2":"c"}`},
		{`{"a":{"b":null}}`, `null`},
		{`{"a":1,"a":2}`, `{"a":2}`},
		{`{".value":"v",".priority":7}`, `{".value":"v",".priority":7}`},
		{`{".value":{"x":1},".priority":"p"}`, `{".priority":"p","x":1}`},
		{`{".value":null,".priority":1}`, `null`},
		{`{".priority":2,"a":1}`, `{".priority":2,"a":1}`},
		{`{".priority":null,"a":1}`, `{"a":1}`},
		{`{".priority":1}`, `null`}, // an empty object has no priority
		{`{".sv":"timestamp"}`, `{".sv":"timestamp"}`},
		{`{".sv":{"increment":-2.5}}`, `{".sv":{"increment":-2.5}}`},
		{`{".value":1,".priority":{".sv":"timestamp"}}`, `{".value":1,".priority":{".sv":"timestamp"}}`},
	}
	for _, c := range cases {
		n, err := ParseData([]byte(c.in), nil, 1<<20)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if got := string(n.ExportJSON()); got != c.export {
			t.Errorf("%s: export %s, want %s", c.in, got, c.export)
		}
	}
}

func TestParseDataRejects(t *testing.T) {
	deep := strings.Repeat(`{"k":`, 32) + "1" + strings.Repeat("}", 32)
	cases := []struct {
		in  string
		at  string
		err error
	}{
		{``, "", ErrInvalidData},
		{`{`, "", ErrInvalidData},
		{`1 2`, "", ErrInvalidData},
		{`1e400`, "", ErrInvalidData},
		{`{"a.b":1}`, "", ErrInvalidKey},
		{`{"a/b":1}`, "", ErrInvalidKey},
		{`{"":1}`, "", ErrInvalidKey},
		{`{"$x":1}`, "", ErrInvalidKey},
		{`{".other":1}`, "", ErrInvalidKey},
		{`{"x":{"\u0007":1}}`, "", ErrInvalidKey},
		{`{".value":1,"a":2}`, "", ErrInvalidData},
		{`{".sv":"timestamp","a":1}`, "", ErrInvalidData},
		{`{".sv":"unknown"}`, "", ErrInvalidServerValue},
		{`{".sv":{"increment":"x"}}`, "", ErrInvalidServerValue},
		{`{".sv":{"increment":1,"x":2}}`, "", ErrInvalidServerValue},
		{`{".value":1,".priority":true}`, "", ErrInvalidPriority},
		{`{".value":1,".priority":{"a":1}}`, "", ErrInvalidPriority},
		{`"` + strings.Repeat("x", 11) + `"`, "", ErrTooBig},
		// Depth counts the location and the data together (emulator: 32 is the limit).
		{deep, "", nil},
		{deep, "a", ErrPathTooDeep},
		{`{"` + strings.Repeat("k", 768) + `":1}`, "a", ErrPathTooLong},
		{`{"` + strings.Repeat("k", 766) + `":1}`, "gg", nil},
	}
	for _, c := range cases {
		at, _ := ParsePath(c.at)
		_, err := ParseData([]byte(c.in), at, 10)
		if c.err == nil && strings.Contains(c.in, "k") {
			_, err = ParseData([]byte(c.in), at, 1<<20)
		}
		if !errors.Is(err, c.err) {
			t.Errorf("%.40s at %q: err %v, want %v", c.in, c.at, err, c.err)
		}
	}
}

func TestNodeSet(t *testing.T) {
	cases := []struct {
		start, path, value, want string
	}{
		{`null`, "a/b", `1`, `{"a":{"b":1}}`},
		{`{"a":1}`, "a/b", `2`, `{"a":{"b":2}}`},
		// A leaf that gains a child keeps its priority (LeafNode.updateImmediateChild).
		{`{"a":{".value":1,".priority":5}}`, "a/b", `2`, `{"a":{".priority":5,"b":2}}`},
		// Deleting below a leaf changes nothing.
		{`{"a":1}`, "a/b", `null`, `{"a":1}`},
		// An object that loses its last child is gone, priority and all.
		{`{"a":{".priority":5,"b":2},"c":1}`, "a/b", `null`, `{"c":1}`},
		{`{"a":{"b":1}}`, "a/b", `null`, `null`},
		{`{"a":1}`, "a/.priority", `3`, `{"a":{".value":1,".priority":3}}`},
		{`{"a":{".value":1,".priority":3}}`, "a/.priority", `null`, `{"a":1}`},
		{`null`, "a/.priority", `3`, `null`},
		{`{"a":1}`, "", `{"b":2}`, `{"b":2}`},
	}
	for _, c := range cases {
		p, err := ParseWritePath(c.path)
		if err != nil {
			t.Fatal(err)
		}
		got := mustParse(t, c.start).Set(p, mustParse(t, c.value))
		if s := string(got.ExportJSON()); s != c.want {
			t.Errorf("%s set %s=%s: %s, want %s", c.start, c.path, c.value, s, c.want)
		}
	}
	// Set copies: the original is unchanged.
	orig := mustParse(t, `{"a":{"b":1}}`)
	orig.Set(Path{"a", "c"}, Number(2))
	if string(orig.ExportJSON()) != `{"a":{"b":1}}` {
		t.Error("Set modified its receiver")
	}
}

func TestRESTArrays(t *testing.T) {
	// The emulator's REST answers (B-emulator-oracle.md §3) and ChildrenNode.val.
	for in, want := range map[string]string{
		`["a","b","c"]`:         `["a","b","c"]`,
		`{"0":"a","2":"c"}`:     `["a",null,"c"]`,
		`{"0":"a","1":"b"}`:     `["a","b"]`,
		`{"0":"a","3":"c"}`:     `["a",null,null,"c"]`, // 3 < 2*2
		`{"1":"a"}`:             `[null,"a"]`,
		`{"00":"x"}`:            `{"00":"x"}`,
		`{"0":"a","b":1}`:       `{"0":"a","b":1}`,
		`{"a":[1,[2,3]]}`:       `{"a":[1,[2,3]]}`,
		`{".priority":1,"0":2}`: `[2]`,
	} {
		if got := string(mustParse(t, in).JSON()); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestEqual(t *testing.T) {
	a := mustParse(t, `{"a":{".value":1,".priority":2},"b":[true,"x"]}`)
	b := mustParse(t, `{"b":{"1":"x","0":true},"a":{".priority":2,".value":1}}`)
	if !Equal(a, b) || !Equal(nil, nil) || Equal(a, nil) {
		t.Error("Equal")
	}
	if Equal(a, mustParse(t, `{"a":1,"b":[true,"x"]}`)) {
		t.Error("priorities must count")
	}
}
