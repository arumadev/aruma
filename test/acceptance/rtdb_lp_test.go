package acceptance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/osama300/aurma-project/internal/services/rtdb/wire"
)

// lpClient is a raw long-poll client, speaking /.lp as BrowserPollConnection does: the
// messages it sends ride in the URL as web-safe base64 segments, and it delivers the
// numbered batches it receives in order.
type lpClient struct {
	t      *testing.T
	base   string
	ns     string
	id, pw string
	hello  wire.Hello

	mu      sync.Mutex
	ser     int64
	seg     int64
	rid     int64
	batches map[int64][]string
	next    int64
	msgs    []string
	closed  bool
}

var (
	lpStartRE = regexp.MustCompile(`pLPCommand\('start','([^']*)','([^']*)'\);`)
	lpBatchRE = regexp.MustCompile(`(?m)^pRTLPCB\((\d+),(.*)\);$`)
	lpCloseRE = regexp.MustCompile(`(?m)^pLPCommand\('close'(?:,\d+)?\);$`)
)

func (e *Env) startLP(ns string) *lpClient {
	e.t.Helper()
	c := &lpClient{t: e.t, base: e.URL, ns: ns, ser: 27182818, batches: map[int64][]string{}}
	code, body := c.get("start=t&ser=31415926&cb=1&v=5&ns=" + ns)
	m := lpStartRE.FindStringSubmatch(body)
	if code != 200 || m == nil {
		e.t.Fatalf("start: %d %s", code, body)
	}
	c.id, c.pw = m[1], m[2]
	c.take(body)
	var hello struct {
		D struct {
			D wire.Hello `json:"d"`
		} `json:"d"`
	}
	if len(c.msgs) != 1 || json.Unmarshal([]byte(c.msgs[0]), &hello) != nil {
		e.t.Fatalf("batch 0: %q", c.msgs)
	}
	c.hello, c.msgs = hello.D.D, nil
	return c
}

func (c *lpClient) get(query string) (int, string) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/.lp?"+query, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Errorf("GET /.lp?%s: %v", query, err)
		return 0, ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (c *lpClient) take(body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range lpBatchRE.FindAllStringSubmatch(body, -1) {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		var raw []json.RawMessage
		if err := json.Unmarshal([]byte(m[2]), &raw); err != nil {
			c.t.Errorf("batch %q: %v", m[0], err)
		}
		msgs := []string{}
		for _, r := range raw {
			msgs = append(msgs, string(r))
		}
		c.batches[n] = msgs
	}
	if lpCloseRE.MatchString(body) {
		c.closed = true
	}
	for b, ok := c.batches[c.next]; ok; b, ok = c.batches[c.next] {
		delete(c.batches, c.next)
		c.msgs = append(c.msgs, b...)
		c.next++
	}
}

// poll sends one request carrying msgs and takes its answer.
func (c *lpClient) poll(msgs ...string) string {
	c.t.Helper()
	c.mu.Lock()
	c.ser++
	q := "id=" + c.id + "&pw=" + c.pw + "&ser=" + strconv.FormatInt(c.ser, 10) + "&ns=" + c.ns
	i := 0
	for _, m := range msgs {
		enc := strings.ReplaceAll(base64.URLEncoding.EncodeToString([]byte(m)), "=", ".")
		var pieces []string
		for len(enc) > 1840 {
			pieces, enc = append(pieces, enc[:1840]), enc[1840:]
		}
		pieces = append(pieces, enc)
		for _, p := range pieces {
			n := strconv.Itoa(i)
			q += "&seg" + n + "=" + strconv.FormatInt(c.seg, 10) + "&ts" + n + "=" + strconv.Itoa(len(pieces)) + "&d" + n + "=" + p
			c.seg++
			i++
		}
	}
	c.mu.Unlock()
	code, body := c.get(q)
	if code != 200 {
		c.t.Errorf("poll: %d %s", code, body)
	}
	c.take(body)
	return body
}

// await polls until a message satisfies ok and returns it.
func (c *lpClient) await(what string, ok func(string) bool) string {
	c.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; {
		c.mu.Lock()
		for i, m := range c.msgs {
			if ok(m) {
				c.msgs = append(c.msgs[:i:i], c.msgs[i+1:]...)
				c.mu.Unlock()
				return m
			}
		}
		have := c.msgs
		c.mu.Unlock()
		if time.Now().After(deadline) {
			c.t.Fatalf("%s: not received; have %q", what, have)
		}
		c.poll()
	}
}

// call sends a request over the long poll and returns its reply.
func (c *lpClient) call(action string, body any) string {
	c.t.Helper()
	c.mu.Lock()
	c.rid++
	r := c.rid
	c.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"t": "d", "d": map[string]any{"r": r, "a": action, "b": body}})
	c.poll(string(b))
	return c.await(action+" reply", isReplyTo(r))
}

func isReplyTo(r int64) func(string) bool {
	prefix := `{"t":"d","d":{"r":` + strconv.FormatInt(r, 10) + `,`
	return func(m string) bool { return strings.HasPrefix(m, prefix) }
}

func isDataAt(path string) func(string) bool {
	return func(m string) bool { return strings.Contains(m, `"a":"d","b":{"p":"`+path+`"`) }
}

func TestRTDBLongPollHandshake(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("lp-app")
	c := &lpClient{t: t, base: e.URL, batches: map[int64][]string{}}
	resp, err := http.Get(e.URL + "/.lp?start=t&ser=31415926&cb=4&v=5&ns=" + ns)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/javascript; charset=utf-8" {
		t.Fatalf("start: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	// The app's page loads the answers as scripts from another origin.
	if corp := resp.Header.Get("Cross-Origin-Resource-Policy"); corp != "cross-origin" {
		t.Errorf("Cross-Origin-Resource-Policy: %q", corp)
	}
	// The emulator's script (golden lp_handshake.json): callbacks forwarded to the parent
	// window under the client's callback id, then start and the hello as batch 0, whose h is
	// the host the client used.
	host := strings.TrimPrefix(e.URL, "http://")
	want := regexp.MustCompile(`^\nfunction pLPCommand\(c, a1, a2, a3, a4\) \{\nparent\.window\["pLPCommand4"\] && parent\.window\["pLPCommand4"\]\(c, a1, a2, a3, a4\);\n\}\n` +
		`function pRTLPCB\(pN, data\) \{\nparent\.window\["pRTLPCB4"\] && parent\.window\["pRTLPCB4"\]\(pN, data\);\n\}\n` +
		`         pLPCommand\('start','\d+','[A-Za-z0-9]+'\);\n` +
		`pRTLPCB\(0,\[\{"t":"c","d":\{"t":"h","d":\{"ts":\d+,"v":"5","h":"` + regexp.QuoteMeta(host) + `","s":"[A-Za-z0-9]{32}"\}\}\}\]\);\n$`)
	if !want.Match(b) {
		t.Errorf("start answer:\n%q", b)
	}
	m := lpStartRE.FindStringSubmatch(string(b))
	// The dframe page sends disconn for the session when it unloads.
	resp, err = http.Get(e.URL + "/.lp?dframe=t&id=" + m[1] + "&pw=" + m[2] + "&ns=" + ns)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `EnvSendPing("/.lp?disconn=t&id=`+m[1]+`&ns=`+ns+`&pw=`+m[2]+`");`) {
		t.Errorf("dframe page: %s", b)
	}
	// It is framed by the app's page and runs an inline script.
	if h := resp.Header; h.Get("X-Frame-Options") != "" || !strings.Contains(h.Get("Content-Security-Policy"), "script-src 'unsafe-inline'") ||
		strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors") {
		t.Errorf("dframe headers: %v", h)
	}
	// A poll for no session: back to the host to start again.
	if _, body := c.get("id=1&pw=nope&ser=1&ns=" + ns); body != `pRTLPCB(0,[{"t":"c","d":{"t":"r","d":"`+host+`"}}]);`+"\npLPCommand('close');\n" {
		t.Errorf("unknown session: %q", body)
	}
}

// Data both ways: requests and replies over the long poll, and pushes of writes another
// client makes over a WebSocket.
func TestRTDBLongPollData(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("lp-app")
	c := e.startLP(ns)
	ws := e.dial(ns, dialOpts{})
	ws.call("q", map[string]any{"p": "/chat", "h": ""}).mustOK(t)

	if m := c.call("s", map[string]any{"c": map[string]any{"sdk.js.12-0-0": 1}}); !strings.Contains(m, `"s":"ok"`) {
		t.Errorf("stats: %s", m)
	}
	// A message of several segments.
	long := strings.Repeat("é", 1000) + strings.Repeat("x", 3000)
	if m := c.call("p", map[string]any{"p": "/chat/a", "d": long}); m != `{"t":"d","d":{"r":2,"b":{"s":"ok","d":""}}}` {
		t.Errorf("put: %s", m)
	}
	if m := ws.next(); !strings.Contains(m, long) {
		t.Errorf("push to the WebSocket: %.100s", m)
	}
	if m := c.call("q", map[string]any{"p": "/chat", "h": ""}); m != `{"t":"d","d":{"r":3,"b":{"s":"ok","d":{}}}}` {
		t.Errorf("listen: %s", m)
	}
	c.await("listen data", isDataAt("chat"))
	ws.put("/chat/b", "hi")
	if m := c.await("push", isDataAt("chat/b")); m != `{"t":"d","d":{"a":"d","b":{"p":"chat/b","d":"hi"}}}` {
		t.Errorf("push: %s", m)
	}
	if m := c.call("g", map[string]any{"p": "/chat/b", "q": map[string]any{}}); m != `{"t":"d","d":{"r":4,"b":{"s":"ok","d":"hi"}}}` {
		t.Errorf("get: %s", m)
	}
}

// One request is held; a newer one releases it, as the SDK's 25-second keepalive does, and
// an idle one is answered within that time.
func TestRTDBLongPollKeepalive(t *testing.T) {
	if testing.Short() {
		t.Skip("waits 20 seconds")
	}
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("lp-app")
	c := e.startLP(ns)
	first := make(chan time.Duration, 1)
	go func() {
		t0 := time.Now()
		c.poll()
		first <- time.Since(t0)
	}()
	time.Sleep(500 * time.Millisecond)
	t0 := time.Now()
	c.poll()
	if d := <-first; d > 2*time.Second {
		t.Errorf("the held request was answered after %v", d)
	}
	if d := time.Since(t0); d < 15*time.Second || d > 25*time.Second {
		t.Errorf("an idle request was answered after %v", d)
	}
}

// disconn, which the dframe page sends when the page unloads, ends the session at once and
// runs its onDisconnect writes.
func TestRTDBLongPollDisconnect(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("lp-app")
	ws := e.dial(ns, dialOpts{})
	ws.call("q", map[string]any{"p": "/presence", "h": ""}).mustOK(t)
	c := e.startLP(ns)
	c.call("o", map[string]any{"p": "/presence/lp", "d": "offline"})
	if code, body := c.get("disconn=t&id=" + c.id + "&pw=" + c.pw + "&ns=" + ns); code != 200 || body != "<!-- -->" {
		t.Errorf("disconn: %d %q", code, body)
	}
	if m := ws.next(); m != `{"t":"d","d":{"a":"d","b":{"p":"presence/lp","d":"offline"}}}` {
		t.Errorf("onDisconnect: %s", m)
	}
}

// The upgrade as Connection.ts makes it, while another client writes: every write reaches
// the client once and in order, first over the long poll, then over the WebSocket.
func TestRTDBLongPollUpgrade(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("lp-app")
	c := e.startLP(ns)
	c.call("q", map[string]any{"p": "/n", "h": ""})

	w := e.dial(ns, dialOpts{})
	const writes = 100
	written := make(chan struct{})
	go func() {
		defer close(written)
		for i := 1; i <= writes; i++ {
			w.call("p", map[string]any{"p": "/n", "d": i})
		}
	}()

	// Some writes reach the long poll before the WebSocket joins.
	for {
		c.mu.Lock()
		n := len(c.msgs)
		c.mu.Unlock()
		if n >= 20 {
			break
		}
		c.poll()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sec, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.URL, "http")+"/.ws?v=5&ns="+ns+"&s="+c.hello.Session, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sec.CloseNow()
	read := func() string {
		_, b, err := sec.Read(ctx)
		if err != nil {
			t.Fatalf("secondary: %v", err)
		}
		return string(b)
	}
	write := func(m string) {
		if err := sec.Write(ctx, websocket.MessageText, []byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	if m := read(); m != `{"t":"c","d":{"t":"a","d":null}}` {
		t.Fatalf("greeting: %s", m)
	}
	go c.poll()
	for range 2 {
		write(`{"t":"c","d":{"t":"p","d":{}}}`)
		if m := read(); m != `{"t":"c","d":{"t":"o","d":null}}` {
			t.Fatalf("pong: %s", m)
		}
	}
	write(`{"t":"c","d":{"t":"a","d":{}}}`)
	c.poll(`{"t":"c","d":{"t":"n","d":{}}}`)
	for !c.closed {
		c.poll()
	}
	if last := c.msgs[len(c.msgs)-1]; last != `{"t":"c","d":{"t":"n","d":null}}` {
		t.Fatalf("the long poll ended with %s", last)
	}
	<-written
	var values []int
	collect := func(m string) {
		var p struct {
			D struct {
				A string `json:"a"`
				B struct {
					P string          `json:"p"`
					D json.RawMessage `json:"d"`
				} `json:"b"`
			} `json:"d"`
		}
		if json.Unmarshal([]byte(m), &p) == nil && p.D.A == "d" && p.D.B.P == "n" {
			v, _ := strconv.Atoi(string(p.D.B.D))
			values = append(values, v)
		}
	}
	for _, m := range c.msgs {
		collect(m)
	}
	overLP := len(values)
	for len(values) == 0 || values[len(values)-1] != writes {
		collect(read())
	}
	for i, v := range values {
		if v != i+1 {
			t.Fatalf("write %d arrived as %d (%d over the long poll): %v", i+1, v, overLP, values)
		}
	}
	t.Logf("%d pushes over the long poll, %d over the WebSocket", overLP, len(values)-overLP)
	// The session carries on over the WebSocket.
	write(`{"t":"d","d":{"r":9,"a":"g","b":{"p":"/n","q":{}}}}`)
	if m := read(); m != `{"t":"d","d":{"r":9,"b":{"s":"ok","d":100}}}` {
		t.Errorf("after the upgrade: %s", m)
	}
}
