package main

import "testing"

func obj(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func TestSetAt(t *testing.T) {
	tests := []struct {
		name string
		root any
		path string
		v    any
		want any
	}{
		{"into nothing", nil, "a/b", 1.0, obj("a", obj("b", 1.0))},
		{"replace a leaf", obj("a", 1.0), "a", "x", obj("a", "x")},
		{"below a leaf turns it into an object", obj("a", 1.0), "a/b", true, obj("a", obj("b", true))},
		{"delete below a leaf changes nothing", obj("a", 1.0), "a/b", nil, obj("a", 1.0)},
		{"delete prunes empty parents", obj("a", obj("b", obj("c", 1.0)), "d", 2.0), "a/b/c", nil, obj("d", 2.0)},
		{"delete the last child is null", obj("a", obj("b", 1.0)), "a/b", nil, nil},
		{"an empty object is a delete", obj("a", 1.0, "b", 2.0), "a", map[string]any{}, obj("b", 2.0)},
		{"empty objects inside a value vanish", nil, "a", obj("b", map[string]any{}, "c", 1.0), obj("a", obj("c", 1.0))},
		{"root", obj("a", 1.0), "/", obj("z", 1.0), obj("z", 1.0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := clone(tt.root)
			got := setAt(tt.root, split(tt.path), tt.v)
			if !equal(got, tt.want) {
				t.Errorf("got %s, want %s", short(got), short(tt.want))
			}
			if !equal(tt.root, before) {
				t.Errorf("setAt changed its input: %s", short(tt.root))
			}
		})
	}
}

func TestDecodeAndDiff(t *testing.T) {
	v, err := decode([]byte(`{"a":{"b":{}},"c":[1,2]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !equal(v, obj("c", []any{1.0, 2.0})) {
		t.Errorf("decode: %s", short(v))
	}
	if d := diff(obj("a", 1.0), obj("a", 2.0, "b", true)); d != `/a: want 1, got 2; /b: want null, got true` {
		t.Errorf("diff: %s", d)
	}
}
