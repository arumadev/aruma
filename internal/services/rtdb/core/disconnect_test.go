package core

import (
	"strings"
	"testing"
)

func TestOnDisconnect(t *testing.T) {
	type op struct{ kind, path, value string } // kind: set or cancel
	cases := []struct {
		name string
		ops  []op
		want string // "path=value" of the resulting write, or the update's children
	}{
		{"nothing", nil, ""},
		{"one set", []op{{"set", "od/put", `"gone"`}}, `set od/put="gone"`},
		// The emulator's case (golden/ondisconnect.json): one update at the common path.
		{"emulator", []op{
			{"set", "od/put", `"gone"`},
			{"set", "od/m1", `1`},
			{"set", "od/m2", `{".sv":"timestamp"}`},
			{"set", "od/cancelme", `"x"`},
			{"cancel", "od/cancelme", ""},
			{"set", "od/inc", `{".sv":{"increment":1}}`},
		}, `update od: put="gone" m1=1 m2={".sv":"timestamp"} inc={".sv":{"increment":1}}`},
		{"later set replaces below", []op{{"set", "a/b", `1`}, {"set", "a", `{"c":2}`}}, `set a={"c":2}`},
		{"set folds into one above", []op{{"set", "a", `{"c":2}`}, {"set", "a/b", `1`}}, `set a={"b":1,"c":2}`},
		{"cancel below a set splits it", []op{{"set", "a", `{"b":1,"c":2}`}, {"cancel", "a/b", ""}}, `set a/c=2`},
		{"cancel below a leaf", []op{{"set", "a", `1`}, {"cancel", "a/b", ""}}, `set a=1`},
		{"cancel below a delete", []op{{"set", "a", `null`}, {"cancel", "a/b", ""}}, ""},
		{"cancel above", []op{{"set", "a/b", `1`}, {"set", "a/c", `2`}, {"set", "d", `3`}, {"cancel", "a", ""}}, `set d=3`},
		{"entry at the common path", []op{{"set", "a", `1`}, {"set", "b/c", `2`}}, `update : a=1 b/c=2`},
		{"siblings deep", []op{{"set", "x/y/a", `1`}, {"set", "x/y/b", `2`}}, `update x/y: a=1 b=2`},
		{"nested under common", []op{{"set", "x/y", `1`}, {"set", "x/z/w", `2`}}, `update x: y=1 z/w=2`},
	}
	for _, c := range cases {
		var o onDisconnect
		for _, op := range c.ops {
			p, _ := ParsePath(op.path)
			if op.kind == "cancel" {
				o.forget(p)
			} else {
				o.remember(p, mustParse(t, op.value))
			}
		}
		if got := describe(o.write()); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}
}

func describe(w *Write) string {
	if w == nil {
		return ""
	}
	if !w.IsUpdate() {
		return "set " + w.Path.String() + "=" + string(w.Value.ExportJSON())
	}
	parts := []string{"update " + w.Path.String() + ":"}
	for _, c := range w.Update {
		parts = append(parts, c.Path.String()+"="+string(c.Value.ExportJSON()))
	}
	return strings.Join(parts, " ")
}
