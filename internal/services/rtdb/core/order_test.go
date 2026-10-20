package core

import (
	"strings"
	"testing"
)

func TestCompareUTF16(t *testing.T) {
	// Go compares UTF-8 bytes, which is code point order; JavaScript compares UTF-16 code
	// units. They disagree when a character above U+FFFF meets one in U+E000-U+FFFF.
	cases := []struct {
		a, b string
		want int
	}{
		{"a", "b", -1},
		{"B", "a", -1},
		{"", "a", -1},
		{"ab", "a", 1},
		{"￿", "😀", 1}, // 0xFFFF > 0xD83D
		{"", "😀", 1},
		{"퟿", "😀", -1},
		{"😀", "😁", -1},
		{"日本", "日本語", -1},
	}
	for _, c := range cases {
		if got := sign(compareUTF16(c.a, c.b)); got != c.want {
			t.Errorf("compareUTF16(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := sign(compareUTF16(c.b, c.a)); got != -c.want {
			t.Errorf("compareUTF16(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
	if strings.Compare("￿", "😀") != -1 {
		t.Fatal("the case above should differ from Go's order")
	}
}

func TestKeyInt(t *testing.T) {
	for k, want := range map[string]int64{"0": 0, "-0": 0, "00": 0, "007": 7, "-12": -12, "2147483647": 2147483647, "-2147483648": -2147483648, "0000000000001": 1} {
		if v, ok := keyInt(k); !ok || v != want {
			t.Errorf("keyInt(%q) = %d, %v; want %d", k, v, ok, want)
		}
	}
	for _, k := range []string{"", "-", "+1", "1a", "1.5", "2147483648", "-2147483649", "12345678901", " 1", "1e3"} {
		if _, ok := keyInt(k); ok {
			t.Errorf("keyInt(%q) should not be an integer key", k)
		}
	}
}

func TestSortedKeys(t *testing.T) {
	n := mustParse(t, `{
		"c": {".value": 1, ".priority": "b"},
		"a": {".value": 2, ".priority": 10},
		"b": {".value": 3, ".priority": 2},
		"d": 0,
		"e": {"s": 5},
		"f": {"s": -1, ".priority": "a"}
	}`)
	for _, c := range []struct {
		ix   Index
		want string
	}{
		{ByKey, "a b c d e f"},
		{ByPriority, "d e b a f c"}, // none, then numbers, then strings; ties by key
		{ByValue, "d c a b e f"},    // 0, 1, 2, 3, then the objects, tied, by key
		{ByChild(Path{"s"}), "a b c d f e"},
	} {
		got := strings.Join(n.SortedKeys(c.ix), " ")
		if got != c.want {
			t.Errorf("%+v: %s, want %s", c.ix, got, c.want)
		}
	}
}

func TestHashKnownValues(t *testing.T) {
	// Values whose hash the emulator reported (golden/datastale.json, B-emulator-oracle.md).
	for in, want := range map[string]string{
		`null`:    "",
		`{}`:      "",
		`{"s":1}`: "8n5kEPKa77xiDo6xsHQrcom3mJ4=",
		`"new"`:   "ECFapkKpb5PK+HS0unEQfPlwgQg=",
	} {
		if got := Hash(mustParse(t, in)); got != want {
			t.Errorf("Hash(%s) = %q, want %q", in, got, want)
		}
	}
	// Children are hashed in priority order, so the order of the input does not matter.
	if Hash(mustParse(t, `{"a":1,"b":2}`)) != Hash(mustParse(t, `{"b":2,"a":1}`)) {
		t.Error("hash depends on input order")
	}
	if Hash(mustParse(t, `{"a":1}`)) == Hash(mustParse(t, `{"a":{".value":1,".priority":1}}`)) {
		t.Error("priority must change the hash")
	}
}
