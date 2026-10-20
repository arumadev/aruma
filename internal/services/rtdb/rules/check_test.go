package rules

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// semantic is one read or write against a rules document. The first group restates, as data,
// the behaviours targaryen's specs check (test/spec/lib/database/index.js, ISC): how .read and
// .write cascade, which rules a check evaluates and in what order, how .validate walks a
// write, and what newData and now hold. The rest cover what targaryen does not.
type semantic struct {
	name  string
	rules string
	data  string // JSON; empty for none
	auth  Auth
	now   int64 // ms since the epoch; 0 for the real clock
	op    string
	path  string
	value string // JSON, for set and update
	want  bool
	// trace lists the rules evaluated, as "rule@path", when set.
	trace []string
}

func bobAuth(claims map[string]any) Auth {
	a, err := Variable(claims)
	if err != nil {
		panic(err)
	}
	return a
}

var semantics = []semantic{
	// Reads.
	{name: "a grant on a sibling grants nothing above it", rules: `{"rules":{"foo":{"bar":{".read":"false"},"baz":{".read":"true"}}}}`,
		op: "read", path: "foo", want: false},
	{name: "a false .read denies", rules: `{"rules":{"foo":{"bar":{".read":"false"},"baz":{".read":"true"}}}}`,
		op: "read", path: "foo/bar", want: false},
	{name: "a true .read grants", rules: `{"rules":{"foo":{"bar":{".read":"false"},"baz":{".read":"true"}}}}`,
		op: "read", path: "foo/baz", want: true},
	{name: "wildcards bind along the path (equal keys)", rules: `{"rules":{"$a":{"$b":{".read":"false","$c":{".read":"$a != $b && $b != $c"}}}}}`,
		op: "read", path: "foo/foo/bar", want: false},
	{name: "wildcards bind along the path (distinct keys)", rules: `{"rules":{"$a":{"$b":{".read":"false","$c":{".read":"$a != $b && $b != $c"}}}}}`,
		op: "read", path: "foo/bar/baz", want: true},
	{name: "data is the location's stored value", rules: `{"rules":{".read":"false","foo":{"bar":{".read":"data.val() == true"},"baz":{".read":"data.val() == true"}}}}`,
		data: `{"foo":{"bar":false,"baz":true}}`, op: "read", path: "foo/baz", want: true},
	{name: "data is false where stored false", rules: `{"rules":{".read":"false","foo":{"bar":{".read":"data.val() == true"}}}}`,
		data: `{"foo":{"bar":false,"baz":true}}`, op: "read", path: "foo/bar", want: false},
	{name: "root reads anywhere", rules: `{"rules":{"foo":{"qux":{".read":"root.child('foo/baz').val() == true"}}}}`,
		data: `{"foo":{"bar":false,"baz":true}}`, op: "read", path: "foo/qux", want: true},
	{name: "auth fields of an override", rules: `{"rules":{".read":"auth.isAdmin == true","foo":{"bar":{".read":"auth.isUser == true"}}}}`,
		auth: bobAuth(map[string]any{"isUser": true}), op: "read", path: "foo/bar", want: true},
	{name: "auth fields of an override, above", rules: `{"rules":{".read":"auth.isAdmin == true","foo":{"bar":{".read":"auth.isUser == true"}}}}`,
		auth: bobAuth(map[string]any{"isUser": true}), op: "read", path: "foo", want: false},
	{name: "an admin override reads everywhere below", rules: `{"rules":{".read":"auth.isAdmin == true","foo":{"bar":{".read":"auth.isUser == true"}}}}`,
		auth: bobAuth(map[string]any{"isAdmin": true}), op: "read", path: "foo/bar", want: true},
	{name: "a denied read evaluates every .read on the way", rules: `{"rules":{".read":"false","$a":{".read":"false","$b":{".read":"false","$c":{".read":"false"}}}}}`,
		op: "read", path: "foo/bar/baz", want: false,
		trace: []string{"rules/.read@", "rules/$a/.read@foo", "rules/$a/$b/.read@foo/bar", "rules/$a/$b/$c/.read@foo/bar/baz"}},
	{name: "a read stops at the first grant", rules: `{"rules":{".read":"false","$a":{".read":"true","$b":{".read":"true","$c":{".read":"true"}}}}}`,
		op: "read", path: "foo/bar/baz", want: true, trace: []string{"rules/.read@", "rules/$a/.read@foo"}},
	{name: "a read evaluates only .read", rules: `{"rules":{".read":"false","$a":{".write":"true","$b":{".write":"true","$c":{".read":"true"}}}}}`,
		op: "read", path: "foo/bar/baz", want: true, trace: []string{"rules/.read@", "rules/$a/$b/$c/.read@foo/bar/baz"}},
	{name: "now is the clock in ms", rules: `{"rules":{".read":"now == 1234"}}`, now: 1234, op: "read", path: "foo", want: true},
	{name: "now is not another time", rules: `{"rules":{".read":"now == 1234"}}`, now: 1235, op: "read", path: "foo", want: false},
	{name: "a read below the rules uses the deepest grant above", rules: `{"rules":{"a":{".read":true}}}`, op: "read", path: "a/b/c/d", want: true},

	// Writes.
	{name: "a write grant on a child does not grant the parent", rules: `{"rules":{"foo":{"bar":{".write":"false"},"baz":{".write":"true"}}}}`,
		op: "set", path: "foo", value: "true", want: false},
	{name: "a write grant applies at its level", rules: `{"rules":{"foo":{"bar":{".write":"false"},"baz":{".write":"true"}}}}`,
		op: "set", path: "foo/baz", value: "true", want: true},
	{name: "write wildcards bind along the path", rules: `{"rules":{"$a":{"$b":{".write":"false","$c":{".write":"$a != $b && $b != $c"}}}}}`,
		op: "set", path: "foo/bar/baz", value: "true", want: true},
	{name: "write wildcards that clash", rules: `{"rules":{"$a":{"$b":{".write":"false","$c":{".write":"$a != $b && $b != $c"}}}}}`,
		op: "set", path: "foo/foo/foo", value: "true", want: false},
	{name: "a set replaces the node, so newData has only the new children", rules: `{"rules":{".write":true,"foo":{".validate":"!newData.hasChild('bar') && newData.hasChild('qux')"}}}`,
		data: `{"foo":{"bar":true,"baz":true}}`, op: "set", path: "foo", value: `{"qux":true}`, want: true},
	{name: "a denied write evaluates every .write on the way, not below", rules: `{"rules":{".write":"false","$a":{".write":"false","$b":{".write":"false","$c":{".write":"false","d":{".write":"false"}}}}}}`,
		op: "set", path: "foo/bar/baz", value: "true", want: false,
		trace: []string{"rules/.write@", "rules/$a/.write@foo", "rules/$a/$b/.write@foo/bar", "rules/$a/$b/$c/.write@foo/bar/baz"}},
	{name: "a write stops evaluating .write at the first grant", rules: `{"rules":{".write":"false","$a":{".write":"true","$b":{".write":"true"}}}}`,
		op: "set", path: "foo/bar/baz", value: "true", want: true, trace: []string{"rules/.write@", "rules/$a/.write@foo"}},
	{name: "a write evaluates only .write and .validate", rules: `{"rules":{".write":"false","$a":{".read":"false","$b":{".read":"true","$c":{".write":"true"}}}}}`,
		op: "set", path: "foo/bar/baz", value: "true", want: true, trace: []string{"rules/.write@", "rules/$a/$b/$c/.write@foo/bar/baz"}},
	{name: ".validate runs on the way and on every written node, in order", rules: `{"rules":{".validate":"true",".write":"true","$a":{".validate":"true","$b":{".validate":"true","$c":{".validate":"true","d":{".validate":"true"},"e":{".validate":"true","f":{".validate":"true"}}}}}}}`,
		op: "set", path: "foo/bar/baz", value: `{"d":true,"e":{"f":true}}`, want: true,
		trace: []string{"rules/.write@", "rules/.validate@", "rules/$a/.validate@foo", "rules/$a/$b/.validate@foo/bar",
			"rules/$a/$b/$c/.validate@foo/bar/baz", "rules/$a/$b/$c/d/.validate@foo/bar/baz/d",
			"rules/$a/$b/$c/e/.validate@foo/bar/baz/e", "rules/$a/$b/$c/e/f/.validate@foo/bar/baz/e/f"}},
	{name: "a failing .validate below denies", rules: `{"rules":{".write":"true","$a":{"$b":{"$c":{"d":{".validate":"true"},"e":{".validate":"false","f":{".validate":"true"}}}}}}}`,
		op: "set", path: "foo/bar/baz", value: `{"d":true,"e":{"f":true}}`, want: false},
	{name: ".validate runs only where a value is written", rules: `{"rules":{".write":true,"$a":{"$b":{"$c":{"d":{".validate":"true"},"e":{".validate":"false","f":{".validate":"false"}}}}}}}`,
		op: "set", path: "foo/bar/baz", value: `{"d":true}`, want: true, trace: []string{"rules/.write@", "rules/$a/$b/$c/d/.validate@foo/bar/baz/d"}},
	{name: "a granted delete skips .validate", rules: `{"rules":{"$a":{".write":"true","$b":{".write":"true","$c":{".validate":"false"}}}}}`,
		op: "set", path: "foo/bar/baz", value: "null", want: true, trace: []string{"rules/$a/.write@foo"}},
	{name: "a .write that fails denies", rules: `{"rules":{"a":{".write":"newData.val().contains('one') === true"}}}`,
		op: "set", path: "a", value: "2", want: false},
	{name: "a .validate that fails denies", rules: `{"rules":{".write":true,"a":{".validate":"newData.val().contains('one') === true"}}}`,
		op: "set", path: "a", value: "2", want: false},
	{name: "now in a write rule", rules: `{"rules":{"foo":{".write":"newData.val() == now"}}}`, now: 12345000,
		op: "set", path: "foo", value: "12345000", want: true},

	// Updates.
	{name: "an update needs a grant for each child", rules: `{"rules":{".write":"false","bar":{".write":true},"$other":{".write":false}}}`,
		op: "update", path: "", value: `{"bar":true}`, want: true},
	{name: "an update with one ungranted child is denied", rules: `{"rules":{".write":"false","bar":{".write":true},"$other":{".write":false}}}`,
		op: "update", path: "", value: `{"bar":true,"baz":true}`, want: false},
	{name: "an update validates each child", rules: `{"rules":{".write":"true","bar":{".validate":true},"$other":{".validate":false}}}`,
		op: "update", path: "", value: `{"bar":true,"baz":true}`, want: false},
	{name: "an update sees now", rules: `{"rules":{".write":"false","$key":{".write":"newData.val() == now"}}}`, now: 12345000,
		op: "update", path: "", value: `{"foo":12345000,"bar":12345000}`, want: true},
	{name: "an update where one child is not now", rules: `{"rules":{".write":"false","$key":{".write":"newData.val() == now"}}}`, now: 12345000,
		op: "update", path: "", value: `{"foo":12345000,"baz":1}`, want: false},
	{name: "deep update keys bind wildcards", rules: `{"rules":{".write":"false","$a":{"$b":{"$c":{".write":"$a != $b && $b != $c"}}}}}`,
		op: "update", path: "", value: `{"foo/bar/baz":true,"foo/bar/qux":true}`, want: true},
	{name: "deep update keys that clash", rules: `{"rules":{".write":"false","$a":{"$b":{"$c":{".write":"$a != $b && $b != $c"}}}}}`,
		op: "update", path: "", value: `{"foo/bar/baz":true,"foo/bar/bar":true}`, want: false},
	{name: "an empty update is allowed", rules: `{"rules":{".write":"false"}}`, data: `{"foo":1}`, op: "update", path: "", value: `{}`, want: true},
	{name: "a trailing slash in an update key", rules: `{"rules":{"invites":{".write":true,"$key":{".validate":"newData.hasChildren(['createdAt'])","createdAt":{".validate":"newData.isNumber()"}}}}}`,
		op: "update", path: "", value: `{"invites/":{"createdAt":1508598138982}}`, want: false},
	{name: "an update key with a nested value", rules: `{"rules":{"invites":{".write":true,"$key":{".validate":"newData.hasChildren(['createdAt'])","createdAt":{".validate":"newData.isNumber()"}}}}}`,
		op: "update", path: "", value: `{"invites/someKey":{"createdAt":"some time ago"}}`, want: false},

	// newData, deletes and the overlay.
	{name: "newData of an ancestor survives a partial delete", rules: `{"rules":{".write":true,"a":{".validate":"newData.exists()"}}}`,
		data: `{"a":{"x":1,"y":2}}`, op: "set", path: "a/x", value: "null", want: true},
	{name: "deleting the last child removes the ancestor", rules: `{"rules":{".write":true,"a":{".write":"!newData.exists()"}}}`,
		data: `{"a":{"x":1}}`, op: "update", path: "a", value: `{"x":null}`, want: true},
	{name: "deleting below a leaf keeps the leaf", rules: `{"rules":{".write":true,"s":{".validate":"newData.val() == 'x'"}}}`,
		data: `{"s":"x"}`, op: "set", path: "s/child", value: "null", want: true},
	{name: "writing below a leaf replaces the leaf", rules: `{"rules":{".write":true,"s":{".validate":"newData.isString()"}}}`,
		data: `{"s":"x"}`, op: "set", path: "s/child", value: "1", want: false},
	{name: "an update's children see each other in newData", rules: `{"rules":{"a":{".write":"newData.parent().child('b').val() == 2"},"b":{".write":true}}}`,
		op: "update", path: "", value: `{"a":1,"b":2}`, want: true},
	{name: "a priority wrapper is the value", rules: `{"rules":{".write":true,".validate":"newData.child('a').isNumber()"}}`,
		op: "set", path: "", value: `{"a":{".value":5,".priority":1}}`, want: true},
	{name: "an empty object deletes", rules: `{"rules":{".write":"!newData.exists()"}}`, data: `{"a":1}`, op: "set", path: "a", value: `{"x":{}}`, want: true},
}

func TestSemantics(t *testing.T) {
	for _, c := range semantics {
		t.Run(c.name, func(t *testing.T) {
			rs, err := Parse([]byte(c.rules))
			if err != nil {
				t.Fatal(err)
			}
			var data any
			if c.data != "" {
				mustJSON(t, c.data, &data)
			}
			src, err := NewMemorySource(data)
			if err != nil {
				t.Fatal(err)
			}
			env := Env{Auth: c.auth, Data: src}
			if c.now != 0 {
				env.Now = time.UnixMilli(c.now)
			}
			var trace []string
			tr := func(rule string, at []string) { trace = append(trace, rule+"@"+strings.Join(at, "/")) }
			ctx := context.Background()
			path := splitPath(c.path)
			var got bool
			switch c.op {
			case "read":
				got, err = rs.canRead(ctx, env, path, PlainListen, tr)
			case "set":
				var v any
				mustJSON(t, c.value, &v)
				got, err = rs.canWrite(ctx, env, tr, []Change{{Path: path, Value: v}})
			case "update":
				var v map[string]any
				mustJSON(t, c.value, &v)
				got, err = rs.canWrite(ctx, env, tr, Update(path, v))
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("%s %s = %v, want %v (evaluated %v)", c.op, c.path, got, c.want, trace)
			}
			if c.trace != nil && !reflect.DeepEqual(trace, c.trace) {
				t.Fatalf("evaluated %v, want %v", trace, c.trace)
			}
		})
	}
}

func mustJSON(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("%s: %v", s, err)
	}
}

// failingSource fails every read, as a database that is down.
type failingSource struct{ calls int }

var errDown = errors.New("database down")

func (f *failingSource) Get(context.Context, []string) (Node, error) {
	f.calls++
	return Node{}, errDown
}

func (f *failingSource) HasChildExcept(context.Context, []string, []string) (bool, error) {
	f.calls++
	return false, errDown
}

func TestSourceErrorsAreNotDenials(t *testing.T) {
	rs, err := Parse([]byte(`{"rules":{".read":"data.exists() || true",".write":"root.child('x').exists() || true"}}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	env := Env{Data: &failingSource{}}
	if _, err := rs.CanRead(ctx, env, nil, PlainListen); !errors.Is(err, errDown) {
		t.Fatalf("read: %v", err)
	}
	if _, err := rs.CanWrite(ctx, env, Change{Path: []string{"a"}, Value: 1.0}); !errors.Is(err, errDown) {
		t.Fatalf("write: %v", err)
	}
	// A rule that reads nothing needs no data.
	open, _ := Parse([]byte(`{"rules":{".read":true}}`))
	if ok, err := open.CanRead(ctx, env, []string{"a"}, PlainListen); !ok || err != nil {
		t.Fatalf("open read: %v %v", ok, err)
	}
}

// countingSource records what the engine asks.
type countingSource struct {
	*MemorySource
	gets, except int
	exceptKeys   [][]string
}

func (c *countingSource) Get(ctx context.Context, p []string) (Node, error) {
	c.gets++
	return c.MemorySource.Get(ctx, p)
}

func (c *countingSource) HasChildExcept(ctx context.Context, p []string, keys []string) (bool, error) {
	c.except++
	c.exceptKeys = append(c.exceptKeys, keys)
	return c.MemorySource.HasChildExcept(ctx, p, keys)
}

func TestSourceIsAskedLittle(t *testing.T) {
	rs, err := Parse([]byte(`{"rules":{".write":true,"a":{".validate":"newData.exists() && newData.exists() && data.exists() && data.exists()"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	mem, _ := NewMemorySource(map[string]any{"a": map[string]any{"x": 1.0, "y": 2.0, "z": 3.0}})
	src := &countingSource{MemorySource: mem}
	ok, err := rs.CanWrite(context.Background(), Env{Data: src}, Update([]string{"a"}, map[string]any{"x": nil, "y": nil})...)
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if src.except != 1 || !reflect.DeepEqual(src.exceptKeys[0], []string{"x", "y"}) {
		t.Fatalf("HasChildExcept asked %d times with %v", src.except, src.exceptKeys)
	}
	if src.gets > 4 {
		t.Fatalf("Get asked %d times", src.gets)
	}
}

func TestAdminBypassesRules(t *testing.T) {
	rs, _ := Parse([]byte(`{"rules":{".read":false,".write":false,".validate":false}}`))
	ctx := context.Background()
	env := Env{Auth: Admin(), Data: &failingSource{}}
	if ok, err := rs.CanRead(ctx, env, nil, PlainREST); !ok || err != nil {
		t.Fatalf("read %v %v", ok, err)
	}
	if ok, err := rs.CanWrite(ctx, env, Change{Value: "x"}); !ok || err != nil {
		t.Fatalf("write %v %v", ok, err)
	}
	if !Admin().IsAdmin() || Unauthenticated().IsAdmin() {
		t.Fatal("IsAdmin")
	}
}

func TestAuthConstructors(t *testing.T) {
	rs, _ := Parse([]byte(`{"rules":{
		"user":{".read":"auth.uid == 'u1' && auth.token.email == 'a@b.c' && auth.token.roles[0] == 'admin' && auth.token.n == 3"},
		"anon":{".read":"auth == null"},
		"over":{".read":"auth.uid == 'svc' && auth.extra.k == true"}}}`))
	ctx := context.Background()
	src, _ := NewMemorySource(nil)
	u, err := User("u1", map[string]any{"email": "a@b.c", "roles": []any{"admin"}, "n": 3})
	if err != nil {
		t.Fatal(err)
	}
	over, _ := Variable(map[string]any{"uid": "svc", "extra": map[string]any{"k": true}})
	none, _ := Variable(nil)
	for _, c := range []struct {
		auth Auth
		path string
		want bool
	}{
		{u, "user", true}, {Unauthenticated(), "user", false}, {Unauthenticated(), "anon", true},
		{none, "anon", true}, {u, "anon", false}, {over, "over", true}, {u, "over", false},
	} {
		ok, err := rs.CanRead(ctx, Env{Auth: c.auth, Data: src}, []string{c.path}, PlainListen)
		if err != nil || ok != c.want {
			t.Errorf("%s: %v %v, want %v", c.path, ok, err, c.want)
		}
	}
	if _, err := User("u", map[string]any{"bad": make(chan int)}); err == nil {
		t.Error("a claim that is not JSON was accepted")
	}
}

func TestIndexOn(t *testing.T) {
	rs, err := Parse([]byte(`{"rules":{".indexOn":"top","users":{".indexOn":["age","meta/created"],"$uid":{"posts":{".indexOn":".value"}}},"named":{"$id":{".indexOn":"w"},"special":{}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		want []string
	}{
		{"", []string{"top"}},
		{"users", []string{"age", "meta/created"}},
		{"users/u1", nil},
		{"users/u1/posts", []string{".value"}},
		{"users/u2/posts", []string{".value"}},
		{"users/u1/posts/p1", nil},
		{"named/anything", []string{"w"}},
		{"named/special", nil},
		{"missing/deep", nil},
	} {
		if got := rs.IndexOn(splitPath(c.path)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("IndexOn(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestUpdateExpands(t *testing.T) {
	got := Update([]string{"a"}, map[string]any{"b/c": 1, "/d/": 2, "e": nil})
	want := []Change{{Path: []string{"a", "d"}, Value: 2}, {Path: []string{"a", "b", "c"}, Value: 1}, {Path: []string{"a", "e"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestWriteOfUnsupportedValue(t *testing.T) {
	rs, _ := Parse([]byte(`{"rules":{".write":true}}`))
	src, _ := NewMemorySource(nil)
	if _, err := rs.CanWrite(context.Background(), Env{Data: src}, Change{Value: struct{}{}}); err == nil {
		t.Fatal("a struct value was accepted")
	}
	deep := any(1.0)
	for i := 0; i < 100; i++ {
		deep = map[string]any{"k": deep}
	}
	if _, err := rs.CanWrite(context.Background(), Env{Data: src}, Change{Value: deep}); err == nil {
		t.Fatal("a value nested 100 deep was accepted")
	}
}

func TestMemorySourceNormalizes(t *testing.T) {
	src, err := NewMemorySource(map[string]any{
		"arr":   []any{"a", nil, "c"},
		"empty": map[string]any{},
		"pri":   map[string]any{".value": 5, ".priority": 1},
		"num":   json.Number("2.5"),
		"int":   7,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for path, want := range map[string]Node{
		"arr":   {HasChildren: true},
		"arr/0": {Value: "a"},
		"arr/1": {},
		"arr/2": {Value: "c"},
		"empty": {},
		"pri":   {Value: 5.0},
		"num":   {Value: 2.5},
		"int":   {Value: 7.0},
	} {
		if got, _ := src.Get(ctx, splitPath(path)); got != want {
			t.Errorf("%s = %+v, want %+v", path, got, want)
		}
	}
}
