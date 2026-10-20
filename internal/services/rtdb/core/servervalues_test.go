package core

import (
	"testing"
)

func TestResolveServerValues(t *testing.T) {
	const now = 1792507857299
	cases := []struct {
		name, in string
		existing map[string]string
		want     string
	}{
		{"timestamp", `{".sv":"timestamp"}`, nil, `1792507857299`},
		{"nested", `{"ts":{".sv":"timestamp"},"n":{".sv":{"increment":5}},"x":"y"}`, nil,
			`{"n":5,"ts":1792507857299,"x":"y"}`},
		// The emulator's answers (golden/server_values.json): 5 + 2.5, 0 + 1, "str" -> 3.
		{"increment a number", `{".sv":{"increment":2.5}}`, map[string]string{"": `5`}, `7.5`},
		{"increment nothing", `{".sv":{"increment":1}}`, nil, `1`},
		{"increment a string", `{".sv":{"increment":3}}`, map[string]string{"": `"str"`}, `3`},
		{"increment an object", `{".sv":{"increment":3}}`, map[string]string{"": `{"a":1}`}, `3`},
		{"increment a child", `{"c":{".sv":{"increment":-1}}}`, map[string]string{"c": `10`}, `{"c":9}`},
		{"keeps its priority", `{".value":{".sv":"timestamp"},".priority":4}`, nil, `{".value":1792507857299,".priority":4}`},
		{"priority timestamp", `{".value":1,".priority":{".sv":"timestamp"}}`, nil, `{".value":1,".priority":1792507857299}`},
		{"priority increment", `{".value":1,".priority":{".sv":{"increment":1}}}`, map[string]string{".priority": `4`}, `{".value":1,".priority":5}`},
		{"none", `{"a":1}`, nil, `{"a":1}`},
	}
	for _, c := range cases {
		n := mustParse(t, c.in)
		existing := map[string]*Node{}
		for k, v := range c.existing {
			existing[k] = mustParse(t, v)
		}
		paths := n.IncrementPaths()
		for _, p := range paths {
			if _, ok := c.existing[p.String()]; !ok && len(c.existing) > 0 {
				t.Errorf("%s: asks for %q, which the case does not provide", c.name, p)
			}
		}
		got := ResolveServerValues(n, now, existing)
		if s := string(got.ExportJSON()); s != c.want {
			t.Errorf("%s: %s, want %s", c.name, s, c.want)
		}
		if got.HasServerValues() {
			t.Errorf("%s: placeholders left", c.name)
		}
	}
}
