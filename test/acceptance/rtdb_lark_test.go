package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/arumadev/aruma/internal/app"
	"github.com/arumadev/aruma/internal/services/rtdb/wire"
)

// The tests of this file check Aruma against the production bugs that Lark, another server
// that speaks the Firebase protocol, found and fixed in its 0.3.0 release (its CHANGELOG,
// "Fixed"; ideas only, no code). Aruma had none of them; the tests stay as regression guards.

// units counts a string's UTF-16 code units, as the JS SDK measures frames.
func units(s string) int { return len(utf16.Encode([]rune(s))) }

// A message over one frame is split at character boundaries in both directions: every frame
// the server sends is valid UTF-8 on its own (a browser fails the whole WebSocket with 1007 on
// one that is not, and the SDK reconnects and asks again, forever), and the server takes
// frames of 16384 code units however many bytes they hold, up to 49152 for three-byte
// characters. Arabic, CJK and emoji text is placed so that multi-byte characters fall on
// every frame boundary.
func TestRTDBNonASCIIFramesAtBoundaries(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("chat-app")
	writer := e.dial(ns, dialOpts{})
	reader := e.dial(ns, dialOpts{})
	writer.put("/text/seed", "بذرة")
	reader.call("q", map[string]any{"p": "/text", "h": ""}).mustOK(t)
	if m := reader.next(); !strings.Contains(m, `"d":{"seed":"بذرة"}`) {
		t.Fatalf("first data: %s", m)
	}

	var sentBig, gotBig bool
	for _, run := range []struct{ name, unit string }{
		{"arabic", "مرحبا"},
		{"cjk", "中文字"},
		{"emoji", "😀🎉"},
		{"mixed", "aب中😀"},
	} {
		for pad := range 4 {
			key := fmt.Sprintf("%s%d", run.name, pad)
			value := strings.Repeat("x", pad) + strings.Repeat(run.unit, 40000/units(run.unit))
			msg, _ := json.Marshal(map[string]any{"t": "d", "d": map[string]any{"r": 1000 + pad, "a": "p", "b": map[string]any{"p": "/text/" + key, "d": value}}})
			for _, f := range wire.Split(msg) {
				sentBig = sentBig || len(f) > wire.MaxFrameChars
			}
			writer.send(string(msg))
			if r := writer.await(int64(1000 + pad)); r.Status != "ok" {
				t.Fatalf("%s: write refused: %+v", key, r)
			}

			// The listener's push, frame by frame.
			reader.frames = nil
			push := reader.next()
			var got struct {
				D struct {
					B struct {
						P string `json:"p"`
						D string `json:"d"`
					} `json:"b"`
				} `json:"d"`
			}
			if err := json.Unmarshal([]byte(push), &got); err != nil || got.D.B.P != "text/"+key || got.D.B.D != value {
				t.Fatalf("%s: push differs (%v, path %q, %d bytes)", key, err, got.D.B.P, len(got.D.B.D))
			}
			if len(reader.frames) < 3 {
				t.Fatalf("%s: %d frames, want a multi-frame message", key, len(reader.frames))
			}
			for i, f := range reader.frames {
				if !utf8.ValidString(f) {
					t.Errorf("%s: frame %d is not valid UTF-8", key, i)
				}
				if n := units(f); n > wire.MaxFrameChars {
					t.Errorf("%s: frame %d holds %d code units", key, i, n)
				}
				gotBig = gotBig || len(f) > wire.MaxFrameChars
			}
			// The same value as a fresh connection's first data, and over REST.
			if got := e.dial(ns, dialOpts{}).get("/text/" + key); got != string(mustJSON(value)) {
				t.Errorf("%s: get on a new connection differs", key)
			}
			if r := restAs(t, e, "GET", "/text/"+key+".json?ns="+ns, "", "", nil); r.status != 200 || r.body != string(mustJSON(value)) {
				t.Errorf("%s: REST GET differs: %d", key, r.status)
			}
		}
	}
	if !sentBig || !gotBig {
		t.Fatalf("frames over 16384 bytes: sent %v, received %v", sentBig, gotBig)
	}
	// A frame of 16384 three-byte characters, the largest an SDK sends: 49152 bytes in one
	// frame, accepted.
	big := strings.Repeat("中", wire.MaxFrameChars-53) // 53: the envelope's characters
	msg := `{"t":"d","d":{"r":1,"a":"p","b":{"p":"/one","d":"` + big + `"}}}`
	if units(msg) != wire.MaxFrameChars || len(msg) < 49000 {
		t.Fatalf("test frame: %d units, %d bytes", units(msg), len(msg))
	}
	c := e.dial(ns, dialOpts{})
	c.sendRaw(msg)
	if r := c.await(1); r.Status != "ok" {
		t.Fatalf("one frame of %d bytes: %+v", len(msg), r)
	}
}

// slowConn is a client's end of a slow link: it reads at most rate bytes per second, and
// asks the kernel for a small receive buffer, so the server soon has to wait for it.
type slowConn struct {
	net.Conn
	rate int
}

func newSlowConn(c net.Conn, rate int) net.Conn {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(16 << 10)
	}
	return &slowConn{Conn: c, rate: rate}
}

func (c *slowConn) Read(p []byte) (int, error) {
	if chunk := max(c.rate/50, 1); len(p) > chunk {
		p = p[:chunk]
	}
	n, err := c.Conn.Read(p)
	time.Sleep(time.Duration(n) * time.Second / time.Duration(c.rate))
	return n, err
}

// A large first sync reaches a client on a slow link whole: the write timeout bounds each
// frame, at most 49152 bytes, never the whole message, so a client that keeps reading is not
// dropped however long the message takes. Lark 0.3.0 dropped such clients with a flat
// deadline. Here the timeout is 1 s and the client reads at 1 MB/s: a frame takes about 30 ms
// at most, the 4 MB message about 4 s, far past what the kernel's buffers hold.
func TestRTDBSlowClientLargeSync(t *testing.T) {
	const timeout = time.Second
	e := newEnv(t, rtdbSettings(false, nil), app.WithRTDBWriteTimeout(timeout))
	ns := e.owner().projectNS("chat-app")
	value := strings.Repeat("abcdefghij", 300_000) + strings.Repeat("مرحبا بكم ", 50_000)
	if r := restAs(t, e, "PUT", "/sync.json?ns="+ns, string(mustJSON(value)), "", nil); r.status != 200 {
		t.Fatalf("seed: %d %s", r.status, r.body)
	}
	c := e.dial(ns, dialOpts{readRate: 1 << 20})
	start := time.Now()
	got := c.get("/sync")
	took := time.Since(start)
	if got != string(mustJSON(value)) {
		t.Fatal("the sync differs")
	}
	if took < 3*timeout {
		t.Fatalf("the sync took %v: the server never waited long for the client", took)
	}
	t.Logf("%d bytes in %d frames at 1 MB/s in %v, with a %v write timeout", len(got), len(c.frames), took.Round(time.Millisecond), timeout)
	// Still the same connection.
	c.put("/after", true)
}

// Over a thousand pushes queued behind a large message do not cut the client off: the outbox
// is bounded in bytes (rtdb.limits.outbox_bytes), never in messages. Lark 0.3.0 dropped a
// client at 1000 queued messages, which a large first sync reached on its own, and the client
// reconnected and asked again forever. The client here reads nothing until a burst of writes
// is acked, so every push of the burst waits in its outbox at once, behind a 6 MB sync that
// the kernel's buffers cannot hold.
func TestRTDBManyQueuedPushes(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("chat-app")
	value := strings.Repeat("abcdefghij", 400_000) + strings.Repeat("مرحبا بكم ", 100_000)
	if r := restAs(t, e, "PUT", "/sync.json?ns="+ns, string(mustJSON(value)), "", nil); r.status != 200 {
		t.Fatalf("seed: %d %s", r.status, r.body)
	}
	c := e.dial(ns, dialOpts{})
	listen := c.request("q", map[string]any{"p": "/sync", "h": ""})
	c.request("q", map[string]any{"p": "/burst", "h": ""})
	// The listens are registered before the burst starts: a read on another connection,
	// answered by the database's writer, follows them.
	w := e.dial(ns, dialOpts{})
	w.call("g", map[string]any{"p": "/burst", "q": map[string]any{}}).mustOK(t)
	const burst = 1200
	ids := make([]int64, burst)
	for i := range ids {
		ids[i] = w.request("p", map[string]any{"p": fmt.Sprintf("/burst/k%04d", i), "d": "ب"})
	}
	for _, id := range ids {
		if r, err := w.awaitQuiet(id, time.Minute); err != nil || r.Status != "ok" {
			t.Fatalf("burst write %d: %+v %v", id, r, err)
		}
	}

	// Now the client reads: the sync, then every push.
	c.await(listen).mustOK(t)
	var keys map[string]string
	if err := json.Unmarshal([]byte(c.get("/burst")), &keys); err != nil || len(keys) != burst {
		t.Fatalf("burst: %d keys, %v", len(keys), err)
	}
	synced, pushes := 0, 0
	want := string(mustJSON(value))
	for _, m := range c.queue {
		switch {
		case strings.Contains(m, `"p":"sync"`) && strings.Contains(m, want):
			synced++
		case strings.Contains(m, `"p":"burst/k`):
			pushes++
		}
	}
	if synced != 1 || pushes != burst {
		t.Fatalf("received the sync %d times and %d pushes, want 1 and %d", synced, pushes, burst)
	}
}

// Paths reach the database in their canonical form whatever the spelling: with or without the
// leading slash, with a trailing or doubled one, in a request's path and in an update's keys.
// No spelling reads data it should not see (a/b against a/bc), and the listener of one
// spelling hears the writes of another.
func TestRTDBPathSpellings(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("chat-app")
	c := e.dial(ns, dialOpts{})
	l := e.dial(ns, dialOpts{})
	c.put("/a/b", map[string]any{"w": 0})
	l.call("q", map[string]any{"p": "a/b/", "h": ""}).mustOK(t)
	if m := l.next(); !sameJSON(m, `{"t":"d","d":{"a":"d","b":{"p":"a/b","d":{"w":0}}}}`) {
		t.Fatalf("first data: %s", m)
	}
	// A sibling whose key starts with b reaches no listener of a/b, set or updated from a,
	// where a/b's listener is below the write and only the children decide: the next push it
	// gets is the write to a/b itself.
	c.put("/a/bc", "other")
	c.call("m", map[string]any{"p": "/a", "d": map[string]any{"bc": "other", "bcd/e": 1}}).mustOK(t)
	c.call("m", map[string]any{"p": "/", "d": map[string]any{"a/bc": "other"}}).mustOK(t)
	c.put("a//b/", map[string]any{"x": 1})
	if m := l.next(); !sameJSON(m, `{"t":"d","d":{"a":"d","b":{"p":"a/b","d":{"x":1}}}}`) {
		t.Fatalf("after the write: %s", m)
	}
	c.call("m", map[string]any{"p": "/a/", "d": map[string]any{"/b/y/": 2, "b//z": 3}}).mustOK(t)
	// One push per child the update wrote, as from the emulator.
	if m := l.next() + l.next(); !strings.Contains(m, `"p":"a/b/y","d":2`) || !strings.Contains(m, `"p":"a/b/z","d":3`) {
		t.Fatalf("after the update: %s", m)
	}
	for _, p := range []string{"/a/b", "a/b", "a/b/", "//a//b//"} {
		if got := c.get(p); !sameJSON(got, `{"x":1,"y":2,"z":3}`) {
			t.Errorf("get %q: %s", p, got)
		}
	}
	if got := c.get("/a"); !sameJSON(got, `{"b":{"x":1,"y":2,"z":3},"bc":"other","bcd":{"e":1}}`) {
		t.Errorf("get /a: %s", got)
	}
	for _, p := range []string{"/a/b.json", "/a/b/.json"} {
		if r := restAs(t, e, "GET", p+"?ns="+ns, "", "", nil); r.status != 200 || !sameJSON(r.body, `{"x":1,"y":2,"z":3}`) {
			t.Errorf("REST GET %s: %d %s", p, r.status, r.body)
		}
	}
	c.put("a/b//x/", nil)
	if m := l.next(); !sameJSON(m, `{"t":"d","d":{"a":"d","b":{"p":"a/b/x","d":null}}}`) {
		t.Errorf("after the delete: %s", m)
	}
}

// Two auth messages back to back, before the first is answered, are taken in order: the
// second decides who the session is, both get their replies, and nothing breaks, on many
// connections at once (go test -race).
func TestRTDBConcurrentAuth(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	ns, users, pat, adm := rulesSetUp(t, e)
	users.setUp("chat-app")
	tokens := map[string]string{}
	for _, uid := range []string{"ann", "bob"} {
		users.create("chat-app", uid)
		tokens[uid] = users.signIn("chat-app", uid).str("idToken")
		adm.put("/users/"+uid, map[string]any{"name": uid})
	}
	const conns = 16
	clients := make([]*wsClient, conns)
	for i := range clients {
		clients[i] = e.dial(ns, dialOpts{})
	}
	var wg sync.WaitGroup
	errs := make(chan error, conns)
	for i, c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- authRace(c, i, tokens, pat)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

// authRace pipelines sign-ins on one connection and checks the replies. It reports instead of
// failing, since it runs off the test's goroutine.
func authRace(c *wsClient, i int, tokens map[string]string, pat string) error {
	first, second := tokens["ann"], tokens["bob"]
	want, deny := "bob", "ann"
	if i%2 == 1 {
		first, second, want, deny = second, first, deny, want
	}
	for round := range 5 {
		var creds []string
		switch round {
		case 0:
			creds = []string{first, second}
		case 1:
			creds = []string{"not-a-token", first, second}
		case 2:
			creds = []string{pat, second}
		case 3:
			creds = []string{first, first, first, second}
		default:
			creds = []string{second, "not-a-token", second}
		}
		ids := make([]int64, 0, len(creds)+2)
		for _, cred := range creds {
			ids = append(ids, c.requestQuiet("auth", map[string]any{"cred": cred}))
		}
		ids = append(ids, c.requestQuiet("g", map[string]any{"p": "/users/" + want, "q": map[string]any{}}))
		ids = append(ids, c.requestQuiet("g", map[string]any{"p": "/users/" + deny, "q": map[string]any{}}))
		var got []string
		for _, id := range ids {
			r, err := c.awaitQuiet(id, 10*time.Second)
			if err != nil {
				return fmt.Errorf("connection %d round %d: %v", i, round, err)
			}
			got = append(got, r.Status)
		}
		n := len(creds)
		if got[n-1] != "ok" || got[n] != "ok" || got[n+1] != "permission_denied" {
			return fmt.Errorf("connection %d round %d: replies %v", i, round, got)
		}
		if round == 1 && got[0] != "invalid_token" {
			return fmt.Errorf("connection %d: a bad token got %s", i, got[0])
		}
	}
	return nil
}

// requestQuiet is request for another goroutine than the test's.
func (c *wsClient) requestQuiet(action string, body any) int64 {
	c.rid++
	b, _ := json.Marshal(map[string]any{"t": "d", "d": map[string]any{"r": c.rid, "a": action, "b": body}})
	for _, f := range wire.Split(b) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = c.ws.Write(ctx, websocket.MessageText, f)
		cancel()
	}
	return c.rid
}

// awaitQuiet is await for another goroutine than the test's, waiting up to timeout for each
// message.
func (c *wsClient) awaitQuiet(r int64, timeout time.Duration) (reply, error) {
	for {
		m, err := c.read(timeout)
		if err != nil {
			return reply{}, err
		}
		var env struct {
			T string `json:"t"`
			D struct {
				R *int64 `json:"r"`
				B struct {
					S string          `json:"s"`
					D json.RawMessage `json:"d"`
				} `json:"b"`
			} `json:"d"`
		}
		if json.Unmarshal([]byte(m), &env) == nil && env.T == "d" && env.D.R != nil && *env.D.R == r {
			return reply{env.D.B.S, env.D.B.D}, nil
		}
	}
}

// Writes at the root and the deletes around them reach the database itself: a fresh server on
// the same database sees what the clients last saw. Covered: set and delete of the whole
// database over the protocol and over REST, deleting a key twice, writing below a leaf and
// then deleting what was written, deleting a node and then writing below it, and update's
// nulls, all sent back to back without waiting for acks.
func TestRTDBRootWritesSurviveRestart(t *testing.T) {
	settings := rtdbSettings(false, nil)
	e := newEnv(t, settings)
	ns := e.owner().projectNS("chat-app")

	steps := []struct {
		name string
		do   func(t *testing.T, e *Env, c *wsClient)
		want string // the root after a restart
	}{
		{"set the root", func(t *testing.T, e *Env, c *wsClient) {
			c.put("/", map[string]any{"a": 1, "b": map[string]any{"c": "ج"}})
		}, `{"a":1,"b":{"c":"ج"}}`},
		{"set the root again, smaller", func(t *testing.T, e *Env, c *wsClient) {
			c.put("", map[string]any{"z": true})
		}, `{"z":true}`},
		{"REST PUT the root", func(t *testing.T, e *Env, c *wsClient) {
			if r := restAs(t, e, "PUT", "/.json?ns="+ns, `{"r":{"s":1},"t":2}`, "", nil); r.status != 200 {
				t.Fatalf("PUT: %d %s", r.status, r.body)
			}
		}, `{"r":{"s":1},"t":2}`},
		{"REST DELETE the root", func(t *testing.T, e *Env, c *wsClient) {
			if r := restAs(t, e, "DELETE", "/.json?ns="+ns, "", "", nil); r.status != 200 {
				t.Fatalf("DELETE: %d %s", r.status, r.body)
			}
		}, `null`},
		{"delete the root", func(t *testing.T, e *Env, c *wsClient) {
			c.put("/", map[string]any{"a": 1})
			c.put("/", nil)
		}, `null`},
		{"delete a key twice", func(t *testing.T, e *Env, c *wsClient) {
			c.put("/k", map[string]any{"v": strings.Repeat("v", 5000), "w": strings.Repeat("w", 5000)})
			c.put("/k/v", "short")
			pipeline(t, c,
				[]any{"p", map[string]any{"p": "/k/v", "d": nil}},
				[]any{"p", map[string]any{"p": "/k/v", "d": nil}},
				[]any{"m", map[string]any{"p": "/k", "d": map[string]any{"v": nil}}},
				[]any{"p", map[string]any{"p": "/k/v/deeper", "d": nil}},
			)
		}, `{"k":{"w":"` + strings.Repeat("w", 5000) + `"}}`},
		{"write below a leaf, then delete it", func(t *testing.T, e *Env, c *wsClient) {
			c.put("/", map[string]any{"p": 5, "q": "old"})
			pipeline(t, c,
				[]any{"p", map[string]any{"p": "/p/x", "d": 1}},
				[]any{"p", map[string]any{"p": "/p/x", "d": nil}},
				[]any{"m", map[string]any{"p": "/", "d": map[string]any{"q/y": 1}}},
				[]any{"m", map[string]any{"p": "/", "d": map[string]any{"q/y": nil}}},
			)
		}, `null`},
		{"delete a node, then write below it", func(t *testing.T, e *Env, c *wsClient) {
			c.put("/n", map[string]any{"x": 1, "y": map[string]any{"deep": 2}})
			pipeline(t, c,
				[]any{"p", map[string]any{"p": "/n", "d": nil}},
				[]any{"p", map[string]any{"p": "/n/z", "d": 3}},
				[]any{"m", map[string]any{"p": "/", "d": map[string]any{"m": map[string]any{"old": 1}}}},
				[]any{"m", map[string]any{"p": "/", "d": map[string]any{"m/old": nil, "m/new": 2}}},
			)
		}, `{"m":{"new":2},"n":{"z":3}}`},
		{"update's nulls delete", func(t *testing.T, e *Env, c *wsClient) {
			c.call("m", map[string]any{"p": "/", "d": map[string]any{"n": nil, "u/a": 1, "u/b": 2}}).mustOK(t)
			c.call("m", map[string]any{"p": "/u", "d": map[string]any{"a": nil, "c": map[string]any{}}}).mustOK(t)
		}, `{"m":{"new":2},"u":{"b":2}}`},
	}
	for _, s := range steps {
		c := e.dial(ns, dialOpts{})
		s.do(t, e, c)
		if got := c.get("/"); !sameJSON(got, s.want) {
			t.Fatalf("%s, before the restart: %s", s.name, got)
		}
		c.close()
		e = restartRTDB(t, e, settings)
		after := e.dial(ns, dialOpts{})
		if got := after.get("/"); !sameJSON(got, s.want) {
			t.Fatalf("%s, after the restart: %s, want %s", s.name, got, s.want)
		}
		after.close()
	}
	// A shallow read after children were deleted lists the live ones only.
	if r := restAs(t, e, "GET", "/.json?shallow=true&ns="+ns, "", "", nil); r.status != 200 || !sameJSON(r.body, `{"m":true,"u":true}`) {
		t.Errorf("shallow: %d %s", r.status, r.body)
	}
}

// pipeline sends requests back to back, then requires every reply to be ok.
func pipeline(t *testing.T, c *wsClient, reqs ...[]any) {
	t.Helper()
	ids := make([]int64, len(reqs))
	for i, r := range reqs {
		ids[i] = c.request(r[0].(string), r[1])
	}
	for i, id := range ids {
		if r := c.await(id); r.Status != "ok" {
			t.Fatalf("request %d: %+v", i, r)
		}
	}
}

// restartRTDB stops e's Realtime Database, as a shutdown does once its connections are gone,
// and starts a new server on the same database.
func restartRTDB(t *testing.T, e *Env, settings map[string]string) *Env {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.App.RTDB.Close(ctx); err != nil {
		t.Fatal(err)
	}
	return e.restart(settings)
}

// .validate is skipped at every node a write leaves null and runs everywhere else, through
// the server as in the rules engine (rules/testdata/emulator/nulls.golden.json holds the
// emulator's verdicts for these and more): nulling a validated child in a multi-path update
// is allowed, removing a child the parent requires is denied, over the protocol and REST.
func TestRTDBValidateSkipsNulls(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	owner := e.owner()
	ns := owner.projectNS("chat-app")
	pat := owner.pat()
	e.putRules(pat, ns, `{"rules": {"items": {"$id": {
		".write": true,
		".read": true,
		".validate": "newData.hasChildren(['a', 'b'])",
		"a": {".validate": "newData.isString()"},
		"b": {".validate": "newData.isNumber()"},
		"c": {".validate": "newData.isBoolean()"},
		"$other": {".validate": false}
	}}}}`)
	adm := e.dial(ns, dialOpts{})
	adm.call("auth", map[string]any{"cred": pat}).mustOK(t)
	seed := map[string]any{"x": map[string]any{"a": "s", "b": 1, "c": true}, "y": map[string]any{"a": "t", "b": 2, "c": false}}
	c := e.dial(ns, dialOpts{})
	for _, w := range []struct {
		action string
		path   string
		data   any
		want   string
	}{
		{"m", "/", map[string]any{"items/x/c": nil, "items/y/c": nil}, "ok"},
		{"m", "/items/x", map[string]any{"a": "new", "c": nil}, "ok"},
		{"m", "/", map[string]any{"/items/x/c/": nil}, "ok"},
		{"p", "/items/x/c", nil, "ok"},
		{"m", "/", map[string]any{"items/x/a": nil}, "permission_denied"},
		{"m", "/", map[string]any{"items/x/a": nil, "items/y/c": true}, "permission_denied"},
		{"p", "/items/x/a", nil, "permission_denied"},
		{"p", "/items/x", map[string]any{"a": nil, "b": 1}, "permission_denied"},
		{"m", "/", map[string]any{"items/x/a": nil, "items/x/b": nil, "items/x/c": nil}, "ok"},
		{"m", "/", map[string]any{"items/x": nil, "items/z": map[string]any{"a": "u"}}, "permission_denied"},
		{"o", "/items/x/c", nil, "ok"},
		{"o", "/items/x/a", nil, "permission_denied"},
	} {
		adm.put("/items", seed)
		if r := c.call(w.action, map[string]any{"p": w.path, "d": w.data}); r.Status != w.want {
			t.Errorf("%s %s %v: %s, want %s", w.action, w.path, w.data, r.Status, w.want)
		}
		rest := map[string]string{"m": "PATCH", "p": "PUT"}[w.action]
		if rest == "" {
			continue
		}
		adm.put("/items", seed)
		body := string(mustJSON(w.data))
		status := map[string]int{"ok": 200, "permission_denied": 401}[w.want]
		if r := restAs(t, e, rest, "/"+strings.Trim(w.path, "/")+".json?ns="+ns, body, "", nil); r.status != status {
			t.Errorf("REST %s %s %s: %d, want %d", rest, w.path, body, r.status, status)
		}
	}
	// The allowed writes did what they said.
	adm.put("/items", seed)
	c.call("m", map[string]any{"p": "/", "d": map[string]any{"items/x/c": nil, "items/y/a": "u"}}).mustOK(t)
	if got := adm.get("/items"); !sameJSON(got, `{"x":{"a":"s","b":1},"y":{"a":"u","b":2,"c":false}}`) {
		t.Errorf("after the update: %s", got)
	}
}
