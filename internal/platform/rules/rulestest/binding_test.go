package rulestest

import (
	"context"
	"testing"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// TestEmulatorDocumentsFollowTheRecorder: the recorder deletes what getAfter() and a false
// exists() name, and stores the request's own document after the mocks; it writes raw numbers
// into documents' data alone.
func TestEmulatorDocumentsFollowTheRecorder(t *testing.T) {
	c := testAPICase(`{"method": "create", "path": "/databases/(default)/documents/u/new",
		"auth": {"uid": "alice", "token": {"n": 777001}}, "resource": {"data": {"x": 777001}}}`, "")
	c.Provenance = Emulator
	c.RawNumbers = map[string]string{"777001": "2.0"}
	docs := mocked(t, c, `[
	 {"function": "getAfter", "args": [{"exactValue": "/databases/(default)/documents/u/a"}], "result": {"value": {"data": {"n": 1}}}},
	 {"function": "get", "args": [{"exactValue": "/databases/(default)/documents/u/new"}], "result": {"value": {"data": {"n": 1}}}}
	]`)
	for _, after := range []bool{false, true} {
		if _, found, _ := docs.Get(context.Background(), Project, Segments("/databases/(default)/documents/u/a"), after); found {
			t.Errorf("getAfter() mock stored (after %v)", after)
		}
	}
	// The create's own document: absent before (the request's state wins over the mock), the
	// incoming document after.
	if _, found, _ := get(t, docs, "new"); found {
		t.Error("the created document exists before the create")
	}
	doc, found, _ := docs.Get(context.Background(), Project, Segments("/databases/(default)/documents/u/new"), true)
	if k := field(t, doc, "data", "x").Kind(); !found || k != rules.KindFloat {
		t.Errorf("after the create: found %v, data.x %v", found, k)
	}
	req := build(t, c)
	if k := field(t, req.Variables["request"], "auth", "token", "n").Kind(); k != rules.KindInt {
		t.Errorf("a raw number in the emulator's auth is %v; the recorder did not write it raw", k)
	}

	// The Test API saw the whole request as written.
	c.Provenance = TestAPI
	req = build(t, c)
	if k := field(t, req.Variables["request"], "auth", "token", "n").Kind(); k != rules.KindInt {
		t.Errorf("Test API auth number %v", k)
	}
	if v, _ := field(t, req.Variables["request"], "auth", "token", "n").AsInt(); v != 2 {
		t.Errorf("Test API auth number %d, want 2 (written 2.0)", v)
	}
}
