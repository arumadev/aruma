package acceptance

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// Listens, queries and their pushes over a raw WebSocket (R3). rtdb_golden_test.go compares
// the same frames with the emulator's.

func TestRTDBListen(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("chat-app")
	a := e.dial(ns, dialOpts{})
	b := e.dial(ns, dialOpts{})
	a.put("/room", map[string]any{"m1": "hi"})

	// The data come before the ok.
	r := b.request("q", map[string]any{"p": "/room", "h": ""})
	if m := b.next(); m != `{"t":"d","d":{"a":"d","b":{"p":"room","d":{"m1":"hi"}}}}` {
		t.Fatalf("listen data: %s", m)
	}
	if rep := b.await(r); rep.Status != "ok" || string(rep.Data) != `{}` {
		t.Fatalf("listen reply: %+v", rep)
	}
	// Another connection's write reaches it; the writer's own listen gets its push before
	// its ack.
	a.call("q", map[string]any{"p": "/room", "h": ""})
	a.next() // a's own listen data
	r = a.request("p", map[string]any{"p": "/room/m2", "d": "yo"})
	if m := a.next(); m != `{"t":"d","d":{"a":"d","b":{"p":"room/m2","d":"yo"}}}` {
		t.Errorf("writer's own push: %s", m)
	}
	if rep := a.await(r); rep.Status != "ok" {
		t.Errorf("ack: %+v", rep)
	}
	if m := b.next(); m != `{"t":"d","d":{"a":"d","b":{"p":"room/m2","d":"yo"}}}` {
		t.Errorf("other listener: %s", m)
	}
	// After unlisten, nothing.
	b.call("n", map[string]any{"p": "/room"}).mustOK(t)
	a.put("/room/m3", "x")
	a.next()
	if got := b.get("/room/m3"); got != `"x"` { // the next message is this reply, not a push
		t.Errorf("get after unlisten: %s", got)
	}
}

func TestRTDBQueryListen(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("chat-app")
	a := e.dial(ns, dialOpts{})
	b := e.dial(ns, dialOpts{})
	a.put("/msgs", map[string]any{"a": 1, "b": 2, "c": 3})

	// limitToLast(2): filtered and tagged.
	r := b.request("q", map[string]any{"p": "/msgs", "h": "", "q": map[string]any{"l": 2, "vf": "r"}, "t": 1})
	if m := b.next(); !sameJSON(m, `{"t":"d","d":{"a":"d","b":{"p":"msgs","d":{"b":2,"c":3},"t":1}}}`) {
		t.Fatalf("window: %s", m)
	}
	b.await(r)
	a.put("/msgs/d", 4) // enters, b leaves silently
	if m := b.next(); !sameJSON(m, `{"t":"d","d":{"a":"d","b":{"p":"msgs/d","d":4,"t":1}}}`) {
		t.Errorf("entering: %s", m)
	}
	// Ordered by a child without .indexOn: everything, untagged, with no_index.
	r = b.request("q", map[string]any{"p": "/msgs", "h": "", "q": map[string]any{"i": "s", "l": 1, "vf": "l"}, "t": 2})
	if m := b.next(); !sameJSON(m, `{"t":"d","d":{"a":"d","b":{"p":"msgs","d":{"a":1,"b":2,"c":3,"d":4}}}}`) {
		t.Errorf("unindexed: %s", m)
	}
	if rep := b.await(r); string(rep.Data) != `{"w":["no_index"]}` {
		t.Errorf("unindexed reply: %+v", rep)
	}
	if rep := b.call("g", map[string]any{"p": "/msgs", "q": map[string]any{"i": "s", "l": 1, "vf": "l"}}); rep.Status != "no_index" ||
		string(rep.Data) != `"Index not defined, add \".indexOn\": \"s\", for path \"/msgs\", to the rules"` {
		t.Errorf("unindexed get: %+v", rep)
	}
	if rep := b.call("g", map[string]any{"p": "/msgs", "q": map[string]any{"i": ".key", "l": 1, "vf": "l"}}); rep.mustOK(t) != `{"a":1}` {
		t.Errorf("key get: %+v", rep)
	}
}

// The rules document loads over REST with an admin credential; its .indexOn makes a child
// query filtered and tagged.
func TestRTDBRulesIndexOn(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	owner := e.owner()
	ns := owner.projectNS("chat-app")
	_, key := owner.serviceAccountKey("chat-app", "backend")
	sat, _, err := exchange(t, key)
	if err != nil {
		t.Fatal(err)
	}
	url := e.URL + "/.settings/rules.json?ns=" + ns
	do := func(method, auth, body string) (int, string) {
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	// Without a credential the emulator answers 403 (rest.golden.json, "rules endpoint").
	if code, _ := do("GET", "", ""); code != http.StatusForbidden {
		t.Errorf("GET without credential: %d", code)
	}
	if code, body := do("GET", sat, ""); code != 200 || !sameJSON(body, `{"rules":{".read":false,".write":false}}`) {
		t.Errorf("default rules: %d %s", code, body)
	}
	doc := `{"rules": {".read": true, ".write": true, "msgs": {".indexOn": ["s"]}}}`
	if code, body := do("PUT", sat, doc); code != 200 || body != `{"status":"ok"}` {
		t.Fatalf("PUT: %d %s", code, body)
	}
	if code, body := do("GET", owner.pat(), ""); code != 200 || body != doc {
		t.Errorf("GET: %d %s", code, body)
	}
	if code, _ := do("PUT", sat, `{"rules": {".read": "auth !="}}`); code != http.StatusBadRequest {
		t.Errorf("invalid rules: %d", code)
	}

	c := e.dial(ns, dialOpts{})
	c.put("/msgs", map[string]any{"a": map[string]any{"s": 3}, "b": map[string]any{"s": 1}, "c": map[string]any{"s": 2}})
	r := c.request("q", map[string]any{"p": "/msgs", "h": "", "q": map[string]any{"i": "s", "l": 2, "vf": "l"}, "t": 1})
	if m := c.next(); !sameJSON(m, `{"t":"d","d":{"a":"d","b":{"p":"msgs","d":{"b":{"s":1},"c":{"s":2}},"t":1}}}`) {
		t.Errorf("indexed window: %s", m)
	}
	if rep := c.await(r); string(rep.Data) != `{}` {
		t.Errorf("indexed reply: %+v", rep)
	}
}

// After unauth, under locked rules, the server cancels the listens with c before its reply.
func TestRTDBUnauthRevokesListens(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	owner := e.owner()
	ns := owner.projectNS("chat-app")
	c := e.dial(ns, dialOpts{})
	if rep := c.call("auth", map[string]any{"cred": owner.pat()}); rep.Status != "ok" {
		t.Fatal(rep)
	}
	c.call("q", map[string]any{"p": "/x", "h": ""}).mustOK(t)
	c.call("q", map[string]any{"p": "/x", "h": "", "q": map[string]any{"l": 1, "vf": "l"}, "t": 1}).mustOK(t)
	r := c.request("unauth", map[string]any{})
	// A query comes back as a list of its query object with sin and ein filled in, as
	// production and the emulator send it (goldens revoke_unauth.json and
	// production/cancel.json).
	want := map[string]bool{
		`{"t":"d","d":{"a":"c","b":{"p":"x","q":null}}}`:                                     true,
		`{"t":"d","d":{"a":"c","b":{"p":"x","q":[{"sin":true,"ein":true,"l":1,"vf":"l"}]}}}`: true,
	}
	for range 2 {
		if m := c.next(); !want[m] {
			t.Errorf("revoke: %s", m)
		}
	}
	if rep := c.await(r); string(rep.Data) != `"unauthenticated"` {
		t.Errorf("unauth: %+v", rep)
	}
	if rep := c.call("q", map[string]any{"p": "/x", "h": ""}); rep.Status != "permission_denied" {
		t.Errorf("listen after unauth: %+v", rep)
	}
}
