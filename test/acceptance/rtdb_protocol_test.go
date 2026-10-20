package acceptance

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Realtime Database over the raw protocol (R1-R2). Each test notes where the emulator 4.11.2
// behaves differently (internal/services/rtdb/wire/testdata/golden); rtdb_golden_test.go
// replays the emulator's scenarios frame by frame.

func TestRTDBDisabledByDefault(t *testing.T) {
	e := newEnv(t, nil)
	e.owner().project("chat-app")
	if _, resp, err := e.tryDial("chat-app-default-rtdb", dialOpts{}); err == nil || resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("RTDB answered while disabled: %v", err)
	}
}

func TestRTDBHandshake(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("chat-app")
	before := time.Now().UnixMilli()
	c := e.dial(ns, dialOpts{})
	h := c.hello
	if h.Version != "5" || h.Timestamp < before-1000 || h.Timestamp > time.Now().UnixMilli()+1000 {
		t.Errorf("hello: %+v", h)
	}
	// h is the host the client used, port included: the SDKs reconnect there.
	if h.Host != strings.TrimPrefix(e.URL, "http://") {
		t.Errorf("h = %q, want %q", h.Host, strings.TrimPrefix(e.URL, "http://"))
	}
	if !regexp.MustCompile(`^[A-Za-z0-9]{32}$`).MatchString(h.Session) {
		t.Errorf("session id %q", h.Session)
	}
	// Stats are acked; a ping gets a pong; a 0 frame is a keepalive and gets nothing.
	if r := c.call("s", map[string]any{"c": map[string]int{"sdk.js.10-0-0": 1}}); r.Status != "ok" || string(r.Data) != `""` {
		t.Errorf("stats: %+v", r)
	}
	c.sendRaw("0")
	c.sendRaw(`{"t":"c","d":{"t":"p","d":{}}}`)
	if m := c.next(); m != `{"t":"c","d":{"t":"o","d":null}}` {
		t.Errorf("pong: %s", m)
	}
	// Each connection has its own session id.
	if d := e.dial(ns, dialOpts{}); d.hello.Session == h.Session {
		t.Error("session ids repeat")
	}
}

// The handshake names the host the client connected to, whatever it is: a database host
// under rtdb.domain, with its port.
func TestRTDBHandshakeHostUnderDomain(t *testing.T) {
	e := rtdbEnv(t, false, map[string]string{"ARUMA_RTDB_DOMAIN": "rtdb.aruma.test"})
	e.owner().project("chat-app")
	port := e.URL[strings.LastIndex(e.URL, ":"):]
	host := "chat-app-default-rtdb.rtdb.aruma.test" + port
	c := e.dial("", dialOpts{host: host, query: "v=5"})
	if c.hello.Host != host {
		t.Errorf("h = %q, want %q", c.hello.Host, host)
	}
	c.put("/x", 1)
	// The same database through ?ns= on the main host.
	if got := e.dial("chat-app-default-rtdb", dialOpts{}).get("/x"); got != "1" {
		t.Errorf("same database on the main host: %s", got)
	}
	// Host and ?ns= naming the same database is fine; naming different ones is not.
	e.dial("", dialOpts{host: host, query: "v=5&ns=chat-app-default-rtdb"})
	e.owner2().project("other-app")
	_, resp, err := e.tryDial("", dialOpts{host: host, query: "v=5&ns=other-app-default-rtdb"})
	if err == nil || resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("host and ?ns= disagree: %v", err)
	}
	// An unknown database, or none named at all.
	for _, o := range []dialOpts{{query: "v=5&ns=missing-default-rtdb"}, {host: "missing-default-rtdb.rtdb.aruma.test" + port, query: "v=5"}} {
		if _, resp, err := e.tryDial("", o); err == nil || resp == nil || resp.StatusCode != http.StatusNotFound {
			t.Errorf("%+v: %v", o, err)
		}
	}
	if _, resp, err := e.tryDial("", dialOpts{query: "v=5"}); err == nil || resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no namespace: %v", err)
	}
}

// owner2 is a second account in the same instance.
func (e *Env) owner2() *Client { return e.account("second@example.com") }

func TestRTDBPutGet(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	c := e.dial(e.owner().projectNS("chat-app"), dialOpts{})
	// Shapes as in golden/put_get.json.
	if r := c.call("p", map[string]any{"p": "/golden/pg", "d": map[string]any{"a": 1, "b": "hi", "n": map[string]any{"c": true}}}); r.Status != "ok" || string(r.Data) != `""` {
		t.Fatalf("put: %+v", r)
	}
	if got := c.get("/golden/pg"); !sameJSON(got, `{"a":1,"b":"hi","n":{"c":true}}`) {
		t.Errorf("get: %s", got)
	}
	if got := c.get("/golden/pgmissing"); got != "null" {
		t.Errorf("missing: %s", got)
	}
	if got := c.get("golden/pg/n"); got != `{"c":true}` { // Android and iOS send no leading slash
		t.Errorf("child: %s", got)
	}
	c.put("/golden/pg/pr", map[string]any{".value": "v", ".priority": 7})
	if got := c.get("/golden/pg/pr"); got != `{".value":"v",".priority":7}` { // emulator: 7.0
		t.Errorf("priority: %s", got)
	}
	c.put("/golden/pg/arr", []int{1, 2})
	if got := c.get("/golden/pg/arr"); got != `{"0":1,"1":2}` {
		t.Errorf("array: %s", got)
	}
	// Priorities through .priority paths, and deletion by null and by {}.
	c.put("/golden/pg/n/.priority", "p")
	if got := c.get("/golden/pg/n"); got != `{".priority":"p","c":true}` {
		t.Errorf("set priority: %s", got)
	}
	c.put("/golden/pg/arr", nil)
	c.put("/golden/pg/n", map[string]any{})
	if got := c.get("/golden/pg"); got != `{"a":1,"b":"hi","pr":{".value":"v",".priority":7}}` {
		t.Errorf("after deletes: %s", got)
	}
	// Data survives the connection.
	if got := e.dial("chat-app-default-rtdb", dialOpts{}).get("/golden/pg/b"); got != `"hi"` {
		t.Errorf("second connection: %s", got)
	}
}

func TestRTDBMultiPathUpdate(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	c := e.dial(e.owner().projectNS("chat-app"), dialOpts{})
	c.put("/golden/up", map[string]any{"a": 1, "b": map[string]any{"c": 2}, "d": 3})
	// golden/update_multipath.json.
	if r := c.call("m", map[string]any{"p": "/golden/up", "d": map[string]any{"b/c": 20, "d": nil, "e/f": 5}}); r.Status != "ok" || string(r.Data) != `""` {
		t.Fatalf("update: %+v", r)
	}
	if got := c.get("/golden/up"); got != `{"a":1,"b":{"c":20},"e":{"f":5}}` {
		t.Errorf("after update: %s", got)
	}
	// Atomic: one bad child and nothing is written.
	for _, bad := range []map[string]any{
		{"a": 100, "x/y": map[string]any{"bad.key": 1}},
		{"a": 100, "x": map[string]any{"y": map[string]any{".sv": "nope"}}},
		{"a": 100, "a/b": 1}, // a path and its descendant
	} {
		if r := c.call("m", map[string]any{"p": "/golden/up", "d": bad}); r.Status != "invalid_parameters" {
			t.Errorf("%v: %+v", bad, r)
		}
	}
	if got := c.get("/golden/up"); got != `{"a":1,"b":{"c":20},"e":{"f":5}}` {
		t.Errorf("a failed update wrote: %s", got)
	}
	// An update at the root, and one that empties a branch.
	c.call("m", map[string]any{"p": "/", "d": map[string]any{"golden/up/a": nil, "golden/up/b": nil, "golden/up/e/f": nil, "top": true}}).mustOK(t)
	if got := c.get("/"); got != `{"top":true}` {
		t.Errorf("root update: %s", got)
	}
}

func TestRTDBDataStale(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	c := e.dial(e.owner().projectNS("chat-app"), dialOpts{})
	// golden/datastale.json: the emulator's ETag of {"s":1} is the SDK hash the client sends.
	c.put("/golden/tx", map[string]any{"s": 1})
	if r := c.call("p", map[string]any{"p": "/golden/tx", "d": map[string]any{"s": 2}, "h": "wronghash"}); r.Status != "datastale" || string(r.Data) != `"Transaction ETag does not match"` {
		t.Errorf("wrong hash: %+v", r)
	}
	if got := c.get("/golden/tx"); got != `{"s":1}` {
		t.Errorf("datastale wrote: %s", got)
	}
	if r := c.call("p", map[string]any{"p": "/golden/tx", "d": map[string]any{"s": 2}, "h": "8n5kEPKa77xiDo6xsHQrcom3mJ4="}); r.Status != "ok" || string(r.Data) != `{"s":2}` {
		t.Errorf("right hash: %+v", r)
	}
	// The hash of nothing is "".
	if r := c.call("p", map[string]any{"p": "/golden/txmissing", "d": "new", "h": ""}); r.Status != "ok" || string(r.Data) != `"new"` {
		t.Errorf("empty hash on a missing path: %+v", r)
	}
	if r := c.call("p", map[string]any{"p": "/golden/txmissing", "d": "newer", "h": ""}); r.Status != "datastale" {
		t.Errorf("empty hash on an existing path: %+v", r)
	}
}

func TestRTDBServerValues(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	c := e.dial(e.owner().projectNS("chat-app"), dialOpts{})
	before := time.Now().UnixMilli()
	// golden/server_values.json.
	c.put("/golden/sv", map[string]any{"ts": map[string]any{".sv": "timestamp"}, "n": map[string]any{".sv": map[string]any{"increment": 5}}})
	c.put("/golden/sv/n", map[string]any{".sv": map[string]any{"increment": 2.5}})
	c.put("/golden/sv/m", map[string]any{".sv": map[string]any{"increment": 1}})
	c.put("/golden/sv/s", "str")
	c.put("/golden/sv/s", map[string]any{".sv": map[string]any{"increment": 3}})
	var got struct {
		M, N, S float64
		TS      int64
	}
	if err := json.Unmarshal([]byte(c.get("/golden/sv")), &got); err != nil {
		t.Fatal(err)
	}
	if got.M != 1 || got.N != 7.5 || got.S != 3 || got.TS < before || got.TS > time.Now().UnixMilli() {
		t.Errorf("server values: %+v", got)
	}
	// Unknown server values are refused; the emulator answers them with its error frame.
	for _, sv := range []any{"unknown", map[string]any{"increment": "x"}} {
		if r := c.call("p", map[string]any{"p": "/golden/sv/u", "d": map[string]any{".sv": sv}}); r.Status != "invalid_parameters" {
			t.Errorf("%v: %+v", sv, r)
		}
	}
	// In an update too, each against the value before it.
	c.call("m", map[string]any{"p": "/golden/sv", "d": map[string]any{"n": map[string]any{".sv": map[string]any{"increment": 1}}, "k/l": map[string]any{".sv": "timestamp"}}}).mustOK(t)
	if v := c.get("/golden/sv/n"); v != "8.5" {
		t.Errorf("update increment: %s", v)
	}
}

func TestRTDBLimits(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	c := e.dial(e.owner().projectNS("chat-app"), dialOpts{})
	expect := func(r reply, status, msg string) {
		t.Helper()
		if r.Status != status || (msg != "" && !strings.Contains(string(r.Data), msg)) {
			t.Errorf("got %s %s, want %s %q", r.Status, r.Data, status, msg)
		}
	}
	// A forbidden character in a key: the emulator sends its error frame and no reply; the
	// SDKs never send this.
	expect(c.call("p", map[string]any{"p": "/golden/limits/a", "d": map[string]any{"a.b": 1}}), "invalid_parameters", "Key value")
	expect(c.call("p", map[string]any{"p": "/golden/bad/x.y", "d": 1}), "invalid_parameters", "Invalid token in path")
	// Path length: the bytes of the keys, 768 at most (separators not counted).
	long := "/golden/len/" + strings.Repeat("y", 760) // 6+3+760 = 769
	expect(c.call("p", map[string]any{"p": long, "d": 1}), "invalid_parameters", "maximum length that can be written (768 bytes)")
	c.call("p", map[string]any{"p": "/golden/len/" + strings.Repeat("d", 759), "d": 1}).mustOK(t)
	expect(c.call("p", map[string]any{"p": "/gg", "d": map[string]any{strings.Repeat("e", 767): 1}}), "invalid_parameters", "768 bytes")
	// Depth: path keys and data nesting together, 32 at most.
	deep := "/golden/depth" + strings.Repeat("/s", 30) // 32 keys
	c.call("p", map[string]any{"p": deep, "d": 32}).mustOK(t)
	expect(c.call("p", map[string]any{"p": deep + "/s", "d": 33}), "invalid_parameters", "maximum depth that can be written (32)")
	var nested any = 1
	for range 30 {
		nested = map[string]any{"k": nested}
	}
	expect(c.call("p", map[string]any{"p": "/golden/deep/l30", "d": nested}), "invalid_parameters", "maximum depth")
	c.call("p", map[string]any{"p": "/gdeep", "d": nested}).mustOK(t) // 1 + 30 + ... = 31

	// A 60 KB write travels in frames both ways (golden/limits.json: count, then 16384-char
	// pieces).
	big := strings.Repeat("x", 60000)
	c.put("/golden/big", big)
	c.frames = nil
	if got := c.get("/golden/big"); got != `"`+big+`"` {
		t.Fatalf("big value came back with %d bytes", len(got))
	}
	if len(c.frames) != 5 || c.frames[0] != "4" || len(c.frames[1]) != 16384 || len(c.frames[2]) != 16384 || len(c.frames[3]) != 16384 {
		var lens []int
		for _, f := range c.frames {
			lens = append(lens, len(f))
		}
		t.Errorf("reply frames: %v", lens)
	}
	// Multi-byte text splits on character boundaries, which the WebSocket requires.
	arabic := strings.Repeat("مرحبا😀", 5000)
	c.put("/golden/ar", arabic)
	if got := c.get("/golden/ar"); got != `"`+arabic+`"` {
		t.Error("multi-byte value did not round-trip")
	}
	// A message over the limit is dropped with the emulator's error frame; the connection
	// lives on.
	f := rtdbEnv(t, false, map[string]string{"ARUMA_RTDB_MESSAGE_BYTES": "1048576", "ARUMA_RTDB_STRING_BYTES": "1048576"})
	d := f.dial(f.owner().projectNS("chat-app"), dialOpts{})
	d.request("p", map[string]any{"p": "/huge", "d": strings.Repeat("z", 1100000)})
	if m := d.next(); m != `{"t":"c","d":{"t":"e","d":"Error on incoming message"}}` {
		t.Errorf("oversized message: %s", m)
	}
	if got := d.get("/huge"); got != "null" {
		t.Errorf("oversized message was written: %.40s", got)
	}
	// A single frame above the frame limit closes the connection (the emulator: 1002 at
	// about 100 KB; here 1009, message too big).
	d.sendRaw(`{"t":"d","d":{"r":99,"a":"p","b":{"p":"/x","d":"` + strings.Repeat("z", 120000) + `"}}}`)
	if code := d.closedWith(10 * time.Second); code != websocket.StatusMessageTooBig {
		t.Errorf("close code %d", code)
	}
}

func TestRTDBNamespacesIsolated(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	owner := e.owner()
	a := e.dial(owner.projectNS("chat-app"), dialOpts{})
	b := e.dial(e.owner2().projectNS("other-app"), dialOpts{})
	a.put("/golden/iso", "A")
	b.put("/golden/iso", "B")
	if got := a.get("/golden/iso"); got != `"A"` {
		t.Errorf("a: %s", got)
	}
	if got := b.get("/golden/iso"); got != `"B"` {
		t.Errorf("b: %s", got)
	}
	b.put("/", nil)
	if got := a.get("/"); got != `{"golden":{"iso":"A"}}` {
		t.Errorf("deleting b's root touched a: %s", got)
	}
}

func TestRTDBOrigin(t *testing.T) {
	// Firebase checks no Origin; by default neither does Aruma (lead decision): credentials
	// travel inside the messages, never in cookies.
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("chat-app")
	for _, origin := range []string{"", e.URL, "https://any-app.example", "null"} {
		if _, _, err := e.tryDial(ns, dialOpts{origin: origin}); err != nil {
			t.Errorf("origin %q refused: %v", origin, err)
		}
	}

	// rtdb.allowed_origins narrows it for operators who want that.
	f := rtdbEnv(t, false, map[string]string{"ARUMA_RTDB_ALLOWED_ORIGINS": "app.example.com"})
	ns = f.owner().projectNS("chat-app")
	for origin, ok := range map[string]bool{
		"":                        true, // Android, iOS and the Admin SDKs send none
		f.URL:                     true, // a page on the database's own host
		"https://app.example.com": true,
		"https://evil.example":    false,
		"null":                    false,
	} {
		e := f
		_, resp, err := e.tryDial(ns, dialOpts{origin: origin})
		switch {
		case ok && err != nil:
			t.Errorf("origin %q refused: %v", origin, err)
		case !ok && (err == nil || resp == nil || resp.StatusCode != http.StatusForbidden):
			t.Errorf("origin %q: want 403, got %v", origin, err)
		}
	}
}

func TestRTDBOnDisconnect(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("chat-app")
	a := e.dial(ns, dialOpts{})
	b := e.dial(ns, dialOpts{})
	// golden/ondisconnect.json: acked at once, applied only when a goes.
	for _, r := range []reply{
		a.call("o", map[string]any{"p": "/golden/od/put", "d": "gone"}),
		a.call("om", map[string]any{"p": "/golden/od", "d": map[string]any{"m1": 1, "m2": map[string]any{".sv": "timestamp"}}}),
		a.call("o", map[string]any{"p": "/golden/od/cancelme", "d": "x"}),
		a.call("oc", map[string]any{"p": "/golden/od/cancelme"}),
		a.call("o", map[string]any{"p": "/golden/od/inc", "d": map[string]any{".sv": map[string]any{"increment": 1}}}),
	} {
		if r.Status != "ok" || string(r.Data) != `""` {
			t.Errorf("registration: %+v", r)
		}
	}
	if got := b.get("/golden/od"); got != "null" {
		t.Fatalf("applied before the close: %s", got)
	}
	closed := time.Now().UnixMilli()
	a.close()
	var got map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := b.get("/golden/od"); s != "null" {
			json.Unmarshal([]byte(s), &got)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got["put"] != "gone" || got["m1"] != 1.0 || got["inc"] != 1.0 || got["cancelme"] != nil {
		t.Fatalf("after close: %v", got)
	}
	if ts, _ := got["m2"].(float64); int64(ts) < closed-1000 {
		t.Errorf("timestamp resolved at registration, not at disconnection: %v", got["m2"])
	}
}

// Locked rules (ADR-020): without dev_open_rules only admin credentials reach the data.
func TestRTDBLockedUntilAdmin(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	owner := e.owner()
	ns := owner.projectNS("chat-app")
	c := e.dial(ns, dialOpts{})
	for _, r := range []reply{
		c.call("p", map[string]any{"p": "/a", "d": 1}),
		c.call("g", map[string]any{"p": "/a", "q": map[string]any{}}),
		c.call("m", map[string]any{"p": "/", "d": map[string]any{"a": 1}}),
		c.call("o", map[string]any{"p": "/a", "d": 1}),
	} {
		if r.Status != "permission_denied" || string(r.Data) != `"Permission denied"` {
			t.Errorf("unauthenticated: %+v", r)
		}
	}
	// A token that is no Aruma ID token is refused.
	if r := c.call("auth", map[string]any{"cred": "eyJhbGciOiJSUzI1NiJ9.e30.c2ln"}); r.Status != "invalid_token" {
		t.Errorf("forged user token: %+v", r)
	}
	if r := c.call("p", map[string]any{"p": "/a", "d": 1}); r.Status != "permission_denied" {
		t.Errorf("user token wrote: %+v", r)
	}

	// A service account's access token for this project, as the Admin SDKs send it.
	_, key := owner.serviceAccountKey("chat-app", "backend")
	sat, _, err := exchange(t, key)
	if err != nil {
		t.Fatal(err)
	}
	if r := c.call("gauth", map[string]any{"cred": sat}); r.Status != "ok" {
		t.Fatalf("service account: %+v", r)
	}
	c.put("/a", 1)
	if got := c.get("/a"); got != "1" {
		t.Errorf("admin read: %s", got)
	}
	// unauth drops it again.
	if r := c.call("unauth", map[string]any{}); r.Status != "ok" || string(r.Data) != `"unauthenticated"` {
		t.Errorf("unauth: %+v", r)
	}
	if r := c.call("g", map[string]any{"p": "/a", "q": map[string]any{}}); r.Status != "permission_denied" {
		t.Errorf("after unauth: %+v", r)
	}
	// The owner's CLI token works too; a bad admin token is invalid_token.
	if r := c.call("auth", map[string]any{"cred": owner.pat()}); r.Status != "ok" {
		t.Errorf("owner token: %+v", r)
	}
	if r := c.call("gauth", map[string]any{"cred": "aruma_sat_" + strings.Repeat("A", 43)}); r.Status != "invalid_token" {
		t.Errorf("made-up token: %+v", r)
	}

	// Another project's service account gets nothing here.
	other := e.owner2()
	other.project("other-app")
	_, otherKey := other.serviceAccountKey("other-app", "backend")
	otherSat, _, err := exchange(t, otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if r := c.call("gauth", map[string]any{"cred": otherSat}); r.Status != "invalid_token" {
		t.Errorf("other project's token: %+v", r)
	}
	if r := c.call("g", map[string]any{"p": "/a", "q": map[string]any{}}); r.Status != "permission_denied" {
		t.Errorf("after a refused token: %+v", r)
	}
	// And another account's CLI token neither.
	if r := c.call("auth", map[string]any{"cred": other.pat()}); r.Status != "invalid_token" {
		t.Errorf("other account's token: %+v", r)
	}
}

// Malformed messages get the emulator's error frame and the connection stays usable.
func TestRTDBMalformed(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	c := e.dial(e.owner().projectNS("chat-app"), dialOpts{})
	for _, m := range []string{`{"t":"d","d":{"r":50,"a":"zzz","b":{}}}`, "hello", `{"t":"d","d":{"r":51,"a":"p"}}`} {
		c.send(m)
		if got := c.next(); got != `{"t":"c","d":{"t":"e","d":"Error on incoming message"}}` {
			t.Errorf("%s: %s", m, got)
		}
	}
	if got := c.get("/x"); got != "null" {
		t.Errorf("connection unusable after errors: %s", got)
	}
	// Replies keep the order of the requests, writes and immediate answers mixed.
	ids := []int64{
		c.request("p", map[string]any{"p": "/o/a", "d": 1}),
		c.request("s", map[string]any{"c": map[string]int{}}),
		c.request("g", map[string]any{"p": "/o", "q": map[string]any{}}),
		c.request("o", map[string]any{"p": "/o/b", "d": 2}),
	}
	for _, want := range ids {
		var env struct {
			D struct {
				R int64 `json:"r"`
			} `json:"d"`
		}
		json.Unmarshal([]byte(c.next()), &env)
		if env.D.R != want {
			t.Errorf("reply %d, want %d", env.D.R, want)
		}
	}
}

// Behind a trusted proxy, the database and the hello's h come from X-Forwarded-Host, so the
// client reconnects to the public host it used (review #11).
func TestRTDBBehindProxy(t *testing.T) {
	e := rtdbEnv(t, false, map[string]string{"ARUMA_RTDB_DOMAIN": "rtdb.aruma.test", "ARUMA_TRUSTED_PROXIES": "127.0.0.1/32"})
	e.owner().project("chat-app")
	public := "chat-app-default-rtdb.rtdb.aruma.test"
	c := e.dial("", dialOpts{query: "v=5", forwardedHost: public})
	if c.hello.Host != public {
		t.Errorf("h = %q, want %q", c.hello.Host, public)
	}
	c.put("/x", 1)
	if got := e.dial("chat-app-default-rtdb", dialOpts{}).get("/x"); got != "1" {
		t.Errorf("the forwarded host named another database: %s", got)
	}
}

// Anonymous clients cannot exhaust the server (review #3): they may send only small
// messages, and they take none of the project's connection slots.
func TestRTDBAnonymousClientsAreBounded(t *testing.T) {
	e := rtdbEnv(t, true, map[string]string{"ARUMA_RTDB_CONNECTIONS_PER_PROJECT": "1"})
	owner := e.owner()
	ns := owner.projectNS("chat-app")
	_, key := owner.serviceAccountKey("chat-app", "backend")
	sat, _, err := exchange(t, key)
	if err != nil {
		t.Fatal(err)
	}
	var anons []*wsClient
	for range 3 {
		c := e.dial(ns, dialOpts{})
		if r := c.call("p", map[string]any{"p": "/a", "d": 1}); r.Status != "permission_denied" {
			t.Fatalf("anonymous write: %+v", r)
		}
		anons = append(anons, c)
	}
	// 2 MB before signing in: dropped with the error frame, past the 1 MiB default.
	anons[0].request("p", map[string]any{"p": "/a", "d": strings.Repeat("z", 2<<20)})
	if m := anons[0].next(); m != `{"t":"c","d":{"t":"e","d":"Error on incoming message"}}` {
		t.Errorf("large anonymous message: %.80s", m)
	}
	// The one slot is still free for the admin.
	admin := e.dial(ns, dialOpts{})
	if r := admin.call("gauth", map[string]any{"cred": sat}); r.Status != "ok" {
		t.Fatalf("admin at the limit: %+v", r)
	}
	admin.put("/a", strings.Repeat("z", 2<<20)) // signed in: the full limit
	second := e.dial(ns, dialOpts{})
	if r := second.call("gauth", map[string]any{"cred": sat}); r.Status != "unavailable" {
		t.Errorf("second admin past the limit: %+v", r)
	}
}

// A purged project's databases go with it (review #17).
func TestRTDBPurgedProjectLosesItsData(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	c := e.owner()
	ns := c.projectNS("chat-app")
	e.dial(ns, dialOpts{}).put("/x", 1)
	keep := e.owner2().projectNS("other-app")
	e.dial(keep, dialOpts{}).put("/y", 1)
	c.mustOK(c.do("POST", "/console/v1/projects/chat-app:delete", map[string]any{"confirmProjectId": "chat-app"}))
	ctx := t.Context()
	if _, err := e.Pool.Exec(ctx, "UPDATE control.projects SET expire_time = now() - interval '1 second' WHERE project_id = 'chat-app'"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.App.Service.PurgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	var dbs, nodes int
	e.Pool.QueryRow(ctx, "SELECT count(*) FROM rtdb.databases WHERE name = $1", ns).Scan(&dbs)
	e.Pool.QueryRow(ctx, "SELECT count(*) FROM rtdb.nodes").Scan(&nodes)
	if dbs != 0 || nodes != 1 {
		t.Errorf("after purge: %d databases of the project, %d nodes in all (want 0 and the other project's 1)", dbs, nodes)
	}
}
