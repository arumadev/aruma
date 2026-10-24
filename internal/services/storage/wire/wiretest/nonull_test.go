package wiretest

import "testing"

func TestFindNull(t *testing.T) {
	for _, c := range []struct {
		body, path string
		err        bool
	}{
		{body: ``},
		{body: "  \n"},
		{body: `{}`},
		{body: `{"name":"null","items":[],"metadata":{"k":"v"}}`},
		{body: `null`, path: "$"},
		{body: `{"nextPageToken":null}`, path: "$.nextPageToken"},
		{body: `{"items":[{"name":"a"},{"name":"b","bucket":null}]}`, path: "$.items[1].bucket"},
		{body: `{"a":{"b":[1,[null]]}}`, path: "$.a.b[1][0]"},
		{body: `{"a":1,"b":null,"c":`, path: "$.b"}, // the first null, before the cut
		{body: `{"a":`, err: true},
		{body: `OK`, err: true}, // the emulator's body on a Go chunk (B §9-B2)
		{body: `{} {}`, err: true},
	} {
		path, err := FindNull([]byte(c.body))
		if (err != nil) != c.err || path != c.path {
			t.Errorf("%q: %q %v, want %q (error %v)", c.body, path, err, c.path, c.err)
		}
	}
}

// AssertNoNull fails on a null and on a body that is not JSON, and passes the rest.
func TestAssertNoNull(t *testing.T) {
	for body, fails := range map[string]bool{
		`{"prefixes":[],"items":[]}`: false,
		``:                           false,
		`{"downloadTokens":null}`:    true,
		`not json`:                   true,
	} {
		ft := &fakeT{}
		AssertNoNull(ft, []byte(body))
		if ft.failed != fails {
			t.Errorf("%q: failed %v, want %v", body, ft.failed, fails)
		}
	}
}

type fakeT struct {
	testing.TB
	failed bool
}

func (f *fakeT) Helper()               {}
func (f *fakeT) Errorf(string, ...any) { f.failed = true }
