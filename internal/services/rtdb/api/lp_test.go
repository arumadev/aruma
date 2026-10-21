package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/arumadev/aruma/internal/services/rtdb/core"
	"github.com/arumadev/aruma/internal/services/rtdb/wire"
)

const lpNS = "chat-app-default-rtdb"

// lpClient speaks long polling as BrowserPollConnection does, one request at a time unless a
// test runs requests side by side. Batches are delivered in their numbered order, as the
// SDK's PacketReceiver does.
type lpClient struct {
	t      *testing.T
	base   string // http://host
	id, pw string
	hello  string
	ser    int64
	seg    int64

	mu         sync.Mutex
	batches    map[int64][]string
	next       int64
	msgs       []string // delivered, in order
	closeAfter int64    // -1 until a close command came; then the batch after which to stop
	closed     bool     // a close command came
}

var (
	lpBatchLine   = regexp.MustCompile(`^pRTLPCB\((\d+),(.*)\);$`)
	lpCloseLine   = regexp.MustCompile(`^pLPCommand\('close'(?:,(\d+))?\);$`)
	lpStartLine   = regexp.MustCompile(`pLPCommand\('start','([^']*)','([^']*)'\);`)
	lpSDKSegments = 1840 // MAX_URL_DATA_SIZE - SEG_HEADER_SIZE
)

func startLP(t *testing.T, base string) *lpClient {
	t.Helper()
	c := &lpClient{t: t, base: base, batches: map[int64][]string{}, closeAfter: -1, ser: 1000}
	code, body := c.get("start=t&ser=31415926&cb=1&v=5&ns=" + lpNS)
	m := lpStartLine.FindStringSubmatch(body)
	if code != 200 || m == nil {
		t.Fatalf("start: %d %s", code, body)
	}
	c.id, c.pw = m[1], m[2]
	c.take(body)
	c.hello = c.msgs[0]
	c.msgs = nil
	return c
}

func (c *lpClient) get(query string) (int, string) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/.lp?"+query, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Errorf("GET %s: %v", query, err)
		return 0, ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// take reads an answer's batches and commands.
func (c *lpClient) take(body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if m := lpBatchLine.FindStringSubmatch(line); m != nil {
			n, _ := strconv.ParseInt(m[1], 10, 64)
			var raw []json.RawMessage
			if err := json.Unmarshal([]byte(m[2]), &raw); err != nil {
				c.t.Errorf("batch %s: %v", line, err)
			}
			msgs := []string{}
			for _, r := range raw {
				msgs = append(msgs, string(r))
			}
			c.batches[n] = msgs
		} else if m := lpCloseLine.FindStringSubmatch(line); m != nil {
			c.closed = true
			if m[1] != "" {
				c.closeAfter, _ = strconv.ParseInt(m[1], 10, 64)
			}
		}
	}
	for {
		b, ok := c.batches[c.next]
		if !ok {
			return
		}
		delete(c.batches, c.next)
		c.msgs = append(c.msgs, b...)
		c.next++
	}
}

// segments encodes msgs as the SDK does: web-safe base64 with "." padding, in pieces.
func (c *lpClient) segments(msgs ...string) string {
	var q strings.Builder
	i := 0
	for _, m := range msgs {
		enc := strings.ReplaceAll(base64.URLEncoding.EncodeToString([]byte(m)), "=", ".")
		var pieces []string
		for len(enc) > lpSDKSegments {
			pieces = append(pieces, enc[:lpSDKSegments])
			enc = enc[lpSDKSegments:]
		}
		pieces = append(pieces, enc)
		for _, p := range pieces {
			q.WriteString("&seg" + strconv.Itoa(i) + "=" + strconv.FormatInt(c.seg, 10) +
				"&ts" + strconv.Itoa(i) + "=" + strconv.Itoa(len(pieces)) + "&d" + strconv.Itoa(i) + "=" + p)
			c.seg++
			i++
		}
	}
	return q.String()
}

// poll sends one request, with extra appended to its query, and takes its answer.
func (c *lpClient) poll(extra string) string {
	c.t.Helper()
	c.mu.Lock()
	c.ser++
	ser := c.ser
	c.mu.Unlock()
	code, body := c.get("id=" + c.id + "&pw=" + c.pw + "&ser=" + strconv.FormatInt(ser, 10) + "&ns=" + lpNS + extra)
	if code != 200 {
		c.t.Errorf("poll: %d %s", code, body)
	}
	c.take(body)
	return body
}

// await polls until a delivered message satisfies ok, and returns it.
func (c *lpClient) await(ok func(string) bool) string {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		for i, m := range c.msgs {
			if ok(m) {
				c.msgs = append(c.msgs[:i:i], c.msgs[i+1:]...)
				c.mu.Unlock()
				return m
			}
		}
		closed := c.closed
		c.mu.Unlock()
		if closed || time.Now().After(deadline) {
			c.t.Fatalf("not received; have %q", c.msgs)
		}
		c.poll("")
	}
}

// call sends a request in a poll and returns its reply.
func (c *lpClient) call(r int64, action string, body any) string {
	c.t.Helper()
	b, _ := json.Marshal(map[string]any{"t": "d", "d": map[string]any{"r": r, "a": action, "b": body}})
	c.poll(c.segments(string(b)))
	return c.await(isReply(r))
}

func isReply(r int64) func(string) bool {
	return func(m string) bool { return strings.HasPrefix(m, `{"t":"d","d":{"r":`+strconv.FormatInt(r, 10)+`,`) }
}

func lpServer(t *testing.T) (*API, *memStore, string) {
	t.Helper()
	a, store, _ := newTestAPI(t, Options{}, core.Options{})
	ts := httptest.NewServer(a)
	t.Cleanup(ts.Close)
	return a, store, ts.URL
}

func TestLPStart(t *testing.T) {
	_, _, base := lpServer(t)
	c := &lpClient{t: t, base: base, batches: map[int64][]string{}, closeAfter: -1}
	resp, err := http.Get(base + "/.lp?start=t&ser=31415926&cb=17&v=5&ns=" + lpNS)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(b)
	if ct := resp.Header.Get("Content-Type"); ct != "application/javascript; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	// The emulator's answer, with this server's id, password and hello (golden lp_handshake.json).
	want := regexp.MustCompile(`^\nfunction pLPCommand\(c, a1, a2, a3, a4\) \{\nparent\.window\["pLPCommand17"\] && parent\.window\["pLPCommand17"\]\(c, a1, a2, a3, a4\);\n\}\n` +
		`function pRTLPCB\(pN, data\) \{\nparent\.window\["pRTLPCB17"\] && parent\.window\["pRTLPCB17"\]\(pN, data\);\n\}\n` +
		`         pLPCommand\('start','1','[A-Za-z0-9]{20}'\);\n` +
		`pRTLPCB\(0,\[\{"t":"c","d":\{"t":"h","d":\{"ts":\d+,"v":"5","h":"127\.0\.0\.1:\d+","s":"[A-Za-z0-9]{32}"\}\}\}\]\);\n$`)
	if !want.MatchString(body) {
		t.Errorf("start answer:\n%q", body)
	}
	c.take(body)
	if len(c.msgs) != 1 {
		t.Errorf("batch 0: %q", c.msgs)
	}

	// Without cb, the functions are called directly; a cb that is not a number is refused, as
	// it would go into the script.
	if _, body := c.get("start=t&ser=1&v=5&ns=" + lpNS); !strings.HasPrefix(body, "pLPCommand('start','2','") {
		t.Errorf("without cb: %q", body)
	}
	if code, _ := c.get("start=t&ser=1&cb=1%22%5D(alert(1))&v=5&ns=" + lpNS); code != http.StatusBadRequest {
		t.Errorf("script in cb: %d", code)
	}
	// An unknown session, or a wrong password, gets the emulator's reset to this host.
	for _, q := range []string{"id=99&pw=x&ser=1", "id=1&pw=wrong&ser=1"} {
		if _, body := c.get(q + "&ns=" + lpNS); !regexp.MustCompile(`^pRTLPCB\(0,\[\{"t":"c","d":\{"t":"r","d":"127\.0\.0\.1:\d+"\}\}\]\);\npLPCommand\('close'\);\n$`).MatchString(body) {
			t.Errorf("%s: %q", q, body)
		}
	}
}

func TestLPRoundTrip(t *testing.T) {
	_, _, base := lpServer(t)
	c := startLP(t, base)
	if m := c.call(1, "auth", map[string]any{"cred": sat}); !strings.Contains(m, `"s":"ok"`) {
		t.Fatalf("auth: %s", m)
	}
	// A message of several segments, with characters outside ASCII.
	big := strings.Repeat("é", 800) + strings.Repeat("y", 3000)
	if m := c.call(2, "p", map[string]any{"p": "/x", "d": big}); m != `{"t":"d","d":{"r":2,"b":{"s":"ok","d":""}}}` {
		t.Fatalf("big put: %s", m)
	}
	if m := c.call(3, "q", map[string]any{"p": "/x", "h": ""}); m != `{"t":"d","d":{"r":3,"b":{"s":"ok","d":{}}}}` {
		t.Fatalf("listen: %s", m)
	}
	if m := c.await(func(m string) bool { return strings.Contains(m, `"a":"d"`) }); !strings.Contains(m, big) {
		t.Fatalf("listen data: %.80s", m)
	}

	// A write over a WebSocket reaches the long poll's listen.
	ws := dial(t, "ws"+strings.TrimPrefix(base, "http")+"/.ws?v=5&ns="+lpNS)
	ws.call("auth", map[string]any{"cred": sat})
	ws.call("p", map[string]any{"p": "/x", "d": 7})
	if m := c.await(func(m string) bool { return strings.Contains(m, `"a":"d"`) }); m != `{"t":"d","d":{"a":"d","b":{"p":"x","d":7}}}` {
		t.Fatalf("push: %s", m)
	}

	// Segments out of order, over two requests: the message waits for its first piece.
	msg := `{"t":"d","d":{"r":4,"a":"g","b":{"p":"/x","q":{}}}}` + strings.Repeat(" ", 4000)
	q := c.segments(msg) // three segments, seg0..seg2
	segs := regexp.MustCompile(`&seg(\d)=(\d+)&ts\d=(\d+)&d\d=([^&]*)`).FindAllStringSubmatch(q, -1)
	if len(segs) != 3 {
		t.Fatalf("segments: %d", len(segs))
	}
	one := func(s []string) string { return "&seg0=" + s[2] + "&ts0=" + s[3] + "&d0=" + s[4] }
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.poll(one(segs[2]))
	}()
	c.poll(one(segs[0]) + strings.Replace(one(segs[1]), "0=", "1=", 3))
	<-done
	if m := c.await(isReply(4)); m != `{"t":"d","d":{"r":4,"b":{"s":"ok","d":7}}}` {
		t.Fatalf("out of order: %s", m)
	}
	// A ping over the long poll.
	c.poll(c.segments(`{"t":"c","d":{"t":"p","d":{}}}`))
	c.await(func(m string) bool { return m == `{"t":"c","d":{"t":"o","d":null}}` })
}

func TestLPHold(t *testing.T) {
	defer func(h time.Duration) { lpHold = h }(lpHold)
	lpHold = 300 * time.Millisecond
	_, _, base := lpServer(t)
	c := startLP(t, base)

	// Nothing to send: the poll is answered empty once lpHold passes.
	start := time.Now()
	if body := c.poll(""); body != "pRTLPCB(1,[]);\n" || time.Since(start) < lpHold {
		t.Errorf("idle poll: %q after %v", body, time.Since(start))
	}

	// A newer request releases the one held, at once.
	lpHold = 5 * time.Second
	first := make(chan time.Duration, 1)
	go func() {
		t0 := time.Now()
		c.poll("")
		first <- time.Since(t0)
	}()
	time.Sleep(200 * time.Millisecond)
	second := make(chan struct{})
	go func() {
		c.poll("")
		close(second)
	}()
	if d := <-first; d > 2*time.Second {
		t.Errorf("held request released after %v", d)
	}
	// A request overtaken by one with a higher serial is answered at once, and the newer one
	// stays held.
	t0 := time.Now()
	code, _ := c.get("id=" + c.id + "&pw=" + c.pw + "&ser=1&ns=" + lpNS)
	if code != 200 || time.Since(t0) > time.Second {
		t.Errorf("overtaken request: %d after %v", code, time.Since(t0))
	}
	select {
	case <-second:
		t.Error("the newest request was released")
	case <-time.After(300 * time.Millisecond):
	}
	// It is answered when there is something to send: released by the request that brings
	// the ping, which then holds the pong or waits for lpHold.
	lpHold = 300 * time.Millisecond
	b, _ := json.Marshal(map[string]any{"t": "c", "d": map[string]any{"t": "p", "d": map[string]any{}}})
	c.poll(c.segments(string(b)))
	select {
	case <-second:
	case <-time.After(2 * time.Second):
		t.Error("the held request was not answered")
	}
}

func TestLPDisconnect(t *testing.T) {
	_, store, base := lpServer(t)
	c := startLP(t, base)
	c.call(1, "auth", map[string]any{"cred": sat})
	c.call(2, "o", map[string]any{"p": "/gone", "d": true})

	// The page the client keeps in an iframe sends disconn when it unloads; ns rides along.
	_, page := c.get("dframe=t&id=" + c.id + "&pw=" + c.pw + "&ns=" + lpNS)
	m := regexp.MustCompile(`EnvSendPing\("([^"]+)"\)`).FindStringSubmatch(page)
	if m == nil || m[1] != "/.lp?disconn=t&id="+c.id+"&ns="+lpNS+"&pw="+c.pw {
		t.Fatalf("dframe page: %s", page)
	}
	held := make(chan string, 1)
	go func() { _, body := c.get("id=" + c.id + "&pw=" + c.pw + "&ser=5000&ns=" + lpNS); held <- body }()
	time.Sleep(100 * time.Millisecond)
	if code, body := c.get(strings.TrimPrefix(m[1], "/.lp?")); code != 200 || body != "<!-- -->" {
		t.Errorf("disconn: %d %q", code, body)
	}
	if body := <-held; !strings.HasSuffix(body, "pLPCommand('close');\n") {
		t.Errorf("held request at disconn: %q", body)
	}
	waitFor(t, func() bool { return string(store.value(lpNS).Get(core.Path{"gone"}).ExportJSON()) == "true" })
	// The session is gone.
	if _, body := c.get("id=" + c.id + "&pw=" + c.pw + "&ser=6000&ns=" + lpNS); !strings.Contains(body, `"t":"r"`) {
		t.Errorf("after disconn: %q", body)
	}
}

// A long-poll connection belongs to the database it was started on. Its id and password, or
// its session id, sent with another database's ns are an unknown connection there: the poll
// gets the reset and its messages are not handled, disconn ends nothing, and a WebSocket
// that comes to take the session over is reset and dropped.
func TestLPStaysOnItsDatabase(t *testing.T) {
	_, store, base := lpServer(t)
	const other = "other-app-default-rtdb"
	c := startLP(t, base)
	c.call(1, "auth", map[string]any{"cred": sat})
	seg := c.seg
	write, _ := json.Marshal(map[string]any{"t": "d", "d": map[string]any{"r": 2, "a": "p", "b": map[string]any{"p": "/x", "d": 1}}})
	if code, body := c.get("id=" + c.id + "&pw=" + c.pw + "&ser=2000&ns=" + other + c.segments(string(write))); code != 200 || !strings.Contains(body, `"t":"r"`) {
		t.Errorf("poll through another database: %d %q", code, body)
	}
	if code, body := c.get("disconn=t&id=" + c.id + "&pw=" + c.pw + "&ns=" + other); code != 200 || body != "<!-- -->" {
		t.Errorf("disconn through another database: %d %q", code, body)
	}

	var hello struct {
		D struct {
			D wire.Hello `json:"d"`
		} `json:"d"`
	}
	json.Unmarshal([]byte(c.hello), &hello)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sec, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/.ws?v=5&ns="+other+"&s="+hello.D.D.Session, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sec.CloseNow()
	if _, m, err := sec.Read(ctx); err != nil || !strings.Contains(string(m), `"t":"r"`) {
		t.Errorf("upgrade through another database: %q %v", m, err)
	}
	if _, _, err := sec.Read(ctx); err == nil {
		t.Error("the WebSocket stays open")
	}

	// The connection carries on on its own database, which the message did not reach.
	c.seg = seg
	if r := c.call(3, "g", map[string]any{"p": "/x", "q": map[string]any{}}); !strings.Contains(r, `"d":null`) {
		t.Errorf("read on its own database: %s", r)
	}
	if v := store.value(other); v != nil {
		t.Errorf("data on the other database: %s", v.ExportJSON())
	}
}

func TestLPAbandoned(t *testing.T) {
	defer func(d time.Duration) { lpAbandon = d }(lpAbandon)
	lpAbandon = 300 * time.Millisecond
	_, store, base := lpServer(t)
	c := startLP(t, base)
	c.call(1, "auth", map[string]any{"cred": sat})
	c.call(2, "o", map[string]any{"p": "/gone", "d": "abandoned"})
	// No request comes any more: the connection closes and its onDisconnect write runs.
	waitFor(t, func() bool { return string(store.value(lpNS).Get(core.Path{"gone"}).ExportJSON()) == `"abandoned"` })
}

func TestLPShutdown(t *testing.T) {
	a, _, base := lpServer(t)
	c := startLP(t, base)
	held := make(chan string, 1)
	go func() { held <- c.poll("") }()
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// The client is told to close, and reconnects; no s message, as on a WebSocket.
	if body := <-held; body != "pLPCommand('close');\n" {
		t.Errorf("held request at shutdown: %q", body)
	}
	if code, _ := c.get("start=t&ser=1&cb=1&v=5&ns=" + lpNS); code != http.StatusServiceUnavailable {
		t.Errorf("start during shutdown: %d", code)
	}
}

// TestLPUpgrade moves a session from the long poll to a WebSocket while another client
// writes without pause, as Connection.ts does it: nothing may be lost, repeated or reordered.
func TestLPUpgrade(t *testing.T) {
	_, _, base := lpServer(t)
	wsBase := "ws" + strings.TrimPrefix(base, "http") + "/.ws?v=5&ns=" + lpNS
	c := startLP(t, base)
	c.call(1, "auth", map[string]any{"cred": sat})
	c.call(2, "q", map[string]any{"p": "/n", "h": ""})

	// The writer.
	w := dial(t, wsBase)
	w.call("auth", map[string]any{"cred": sat})
	const writes = 300
	written := make(chan struct{})
	go func() {
		defer close(written)
		for i := 1; i <= writes; i++ {
			w.call("p", map[string]any{"p": "/n", "d": i})
		}
	}()

	// The secondary joins with s=<session id>; the server greets it with a.
	var hello struct {
		D struct {
			D wire.Hello `json:"d"`
		} `json:"d"`
	}
	json.Unmarshal([]byte(c.hello), &hello)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sec, _, err := websocket.Dial(ctx, wsBase+"&s="+hello.D.D.Session, nil)
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
	// The long poll keeps carrying the stream meanwhile.
	go func() {
		for range 3 {
			c.poll("")
		}
	}()
	for range 2 {
		write(`{"t":"c","d":{"t":"p","d":{}}}`)
		if m := read(); m != `{"t":"c","d":{"t":"o","d":null}}` {
			t.Fatalf("pong: %s", m)
		}
	}
	// The switch: a on the secondary, then a request that goes over it at once, then n on
	// the long poll.
	write(`{"t":"c","d":{"t":"a","d":{}}}`)
	write(`{"t":"d","d":{"r":3,"a":"g","b":{"p":"/n","q":{}}}}`)
	time.Sleep(50 * time.Millisecond)
	c.poll(c.segments(`{"t":"c","d":{"t":"n","d":{}}}`))
	for {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			break
		}
		c.poll("")
	}
	c.mu.Lock()
	lpMsgs := c.msgs
	c.mu.Unlock()
	if len(lpMsgs) == 0 || lpMsgs[len(lpMsgs)-1] != `{"t":"c","d":{"t":"n","d":null}}` {
		t.Fatalf("the long poll did not end with n: %q", lpMsgs)
	}
	<-written
	w.call("p", map[string]any{"p": "/done", "d": true})

	// Every write arrives once, in order, first over the long poll, then over the WebSocket;
	// the reply to r=3 comes over the WebSocket.
	values := []int{}
	collect := func(m string) {
		var p struct {
			D struct {
				A string `json:"a"`
				B struct {
					P string `json:"p"`
					D json.RawMessage
				} `json:"b"`
			} `json:"d"`
		}
		json.Unmarshal([]byte(m), &p)
		if p.D.A == "d" && p.D.B.P == "n" {
			v, _ := strconv.Atoi(string(p.D.B.D))
			values = append(values, v)
		}
	}
	for _, m := range lpMsgs[:len(lpMsgs)-1] {
		collect(m)
	}
	fromLP := len(values)
	replied := false
	for len(values) == 0 || values[len(values)-1] != writes || !replied {
		m := read()
		if isReply(3)(m) {
			replied = true
		}
		collect(m)
	}
	for i, v := range values {
		if v != i+1 {
			t.Fatalf("value %d is %d (%d over the long poll): %v", i, v, fromLP, values)
		}
	}
	t.Logf("%d pushes over the long poll, %d over the WebSocket", fromLP, len(values)-fromLP)

	// The long poll is gone; the session lives on over the WebSocket.
	if _, body := c.get("id=" + c.id + "&pw=" + c.pw + "&ser=9999&ns=" + lpNS); !strings.Contains(body, `"t":"r"`) {
		t.Errorf("old long poll: %q", body)
	}
	write(`{"t":"d","d":{"r":4,"a":"g","b":{"p":"/n","q":{}}}}`)
	if m := read(); m != `{"t":"d","d":{"r":4,"b":{"s":"ok","d":300}}}` {
		t.Errorf("after the switch: %s", m)
	}

	// A WebSocket for an unknown session is sent back to the host.
	x, _, err := websocket.Dial(ctx, wsBase+"&s=unknownsession0000000000000000", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer x.CloseNow()
	if _, b, err := x.Read(ctx); err != nil || !strings.HasPrefix(string(b), `{"t":"c","d":{"t":"r","d":"127.0.0.1:`) {
		t.Errorf("unknown session: %s %v", b, err)
	}
	if _, _, err := x.Read(ctx); err == nil {
		t.Error("the socket stayed open")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
	}
}
