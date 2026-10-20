package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/osama300/aurma-project/internal/services/rtdb/wire"
)

// The Realtime Database tests speak the wire protocol over a raw WebSocket, as the SDKs do,
// so every frame is visible. rtdbEnv starts a server with the database enabled; open rules
// unless locked is set.
func rtdbEnv(t *testing.T, locked bool, extra map[string]string) *Env {
	t.Helper()
	env := map[string]string{"ARUMA_RTDB_ENABLED": "true"}
	if !locked {
		env["ARUMA_RTDB_DEV_OPEN_RULES"] = "true"
	}
	for k, v := range extra {
		env[k] = v
	}
	return newEnv(t, env)
}

// wsClient is one raw protocol connection.
type wsClient struct {
	t     *testing.T
	ws    *websocket.Conn
	asm   wire.Assembler
	rid   int64
	hello wire.Hello
	// frames holds every text frame received, in order, for tests that look at framing.
	frames []string
	// queue holds messages read while waiting for something else.
	queue []string
}

type dialOpts struct {
	host   string // the URL's host; the connection still goes to the test server
	origin string
	query  string // replaces ?v=5&ns=...
}

// dial opens /.ws on the test server for namespace ns and reads the handshake.
func (e *Env) dial(ns string, o dialOpts) *wsClient {
	e.t.Helper()
	c, resp, err := e.tryDial(ns, o)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		e.t.Fatalf("dial %s: %v (HTTP %d)", ns, err, status)
	}
	return c
}

func (e *Env) tryDial(ns string, o dialOpts) (*wsClient, *http.Response, error) {
	addr := strings.TrimPrefix(e.URL, "http://")
	host := addr
	if o.host != "" {
		host = o.host
	}
	query := o.query
	if query == "" {
		query = "v=5&ns=" + ns
	}
	header := http.Header{}
	if o.origin != "" {
		header.Set("Origin", o.origin)
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, "ws://"+host+"/.ws?"+query, &websocket.DialOptions{HTTPClient: client, HTTPHeader: header})
	if err != nil {
		return nil, resp, err
	}
	ws.SetReadLimit(1 << 20)
	c := &wsClient{t: e.t, ws: ws, asm: wire.Assembler{Max: 64 << 20}}
	e.t.Cleanup(func() { ws.CloseNow() })
	var hello struct {
		T string `json:"t"`
		D struct {
			T string     `json:"t"`
			D wire.Hello `json:"d"`
		} `json:"d"`
	}
	if err := json.Unmarshal([]byte(c.next()), &hello); err != nil || hello.T != "c" || hello.D.T != "h" {
		e.t.Fatalf("first frame is not the handshake: %v", err)
	}
	c.hello = hello.D.D
	return c, resp, nil
}

// sendRaw sends one text frame.
func (c *wsClient) sendRaw(frame string) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

// send sends a message, split into frames as the SDKs split it.
func (c *wsClient) send(msg string) {
	c.t.Helper()
	for _, f := range wire.Split([]byte(msg)) {
		c.sendRaw(string(f))
	}
}

// request sends a request and returns its number.
func (c *wsClient) request(action string, body any) int64 {
	c.t.Helper()
	c.rid++
	b, err := json.Marshal(map[string]any{"t": "d", "d": map[string]any{"r": c.rid, "a": action, "b": body}})
	if err != nil {
		c.t.Fatal(err)
	}
	c.send(string(b))
	return c.rid
}

// read returns the next message, reassembled, or "" after timeout.
func (c *wsClient) read(timeout time.Duration) (string, error) {
	if len(c.queue) > 0 {
		m := c.queue[0]
		c.queue = c.queue[1:]
		return m, nil
	}
	deadline := time.Now().Add(timeout)
	for {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		typ, frame, err := c.ws.Read(ctx)
		cancel()
		if err != nil {
			return "", err
		}
		if typ != websocket.MessageText {
			c.t.Fatalf("binary frame from the server")
		}
		c.frames = append(c.frames, string(frame))
		msg, err := c.asm.Feed(frame)
		if err != nil {
			c.t.Fatal(err)
		}
		if msg != nil {
			return string(msg), nil
		}
	}
}

// next returns the next message or fails.
func (c *wsClient) next() string {
	c.t.Helper()
	m, err := c.read(10 * time.Second)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return m
}

// reply is a decoded response.
type reply struct {
	Status string
	Data   json.RawMessage
}

// await reads until the reply to request r, keeping other messages for later.
func (c *wsClient) await(r int64) reply {
	c.t.Helper()
	var others []string
	defer func() { c.queue = append(others, c.queue...) }()
	for {
		m := c.next()
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
			return reply{env.D.B.S, env.D.B.D}
		}
		others = append(others, m)
	}
}

// call sends a request and waits for its reply.
func (c *wsClient) call(action string, body any) reply {
	c.t.Helper()
	return c.await(c.request(action, body))
}

// mustOK fails unless the reply is ok, and returns its data.
func (r reply) mustOK(t *testing.T) string {
	t.Helper()
	if r.Status != "ok" {
		t.Fatalf("status %s: %s", r.Status, r.Data)
	}
	return string(r.Data)
}

// put sets a value and requires ok.
func (c *wsClient) put(path string, value any) {
	c.t.Helper()
	c.call("p", map[string]any{"p": path, "d": value}).mustOK(c.t)
}

// get reads a value with the default query.
func (c *wsClient) get(path string) string {
	c.t.Helper()
	return c.call("g", map[string]any{"p": path, "q": map[string]any{}}).mustOK(c.t)
}

// close closes the connection normally.
func (c *wsClient) close() { _ = c.ws.Close(websocket.StatusNormalClosure, "") }

// closedWith waits for the server to close the connection and returns the close code.
func (c *wsClient) closedWith(timeout time.Duration) websocket.StatusCode {
	for {
		_, err := c.read(timeout)
		if err == nil {
			continue
		}
		var ce websocket.CloseError
		if errors.As(err, &ce) {
			return ce.Code
		}
		return -1
	}
}

// sameJSON compares JSON texts by value: 7.0 equals 7.
func sameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}

// projectNS creates a project and returns its database name.
func (c *Client) projectNS(id string) string {
	c.project(id)
	return id + "-default-rtdb"
}

func itoa(n int) string { return strconv.Itoa(n) }
