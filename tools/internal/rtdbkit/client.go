package rtdbkit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/arumadev/aruma/internal/services/rtdb/wire"
)

// Reply is the server's answer to a request: its status ("ok", "datastale", …) and data.
type Reply struct {
	Status string          `json:"s"`
	Data   json.RawMessage `json:"d"`
}

// OK reports whether the request succeeded.
func (r Reply) OK() bool { return r.Status == wire.StatusOK }

// Push is a message the server sent unasked: data (d, m) or a cancellation (c, ac).
type Push struct {
	Action string
	Path   string
	Data   json.RawMessage
	Tag    *int64
	// At is when the client read it.
	At time.Time
}

// ErrClosed is the error of a request whose connection ended before its reply: the request
// may or may not have taken effect.
var ErrClosed = errors.New("rtdbkit: connection closed before the reply")

// router matches replies to requests and hands pushes to onPush, for either transport.
type router struct {
	onPush func(Push)

	mu      sync.Mutex
	rid     int64
	waiting map[int64]chan Reply
	closed  bool
	done    chan struct{}
	err     error
}

func newRouter(onPush func(Push)) *router {
	return &router{onPush: onPush, waiting: map[int64]chan Reply{}, done: make(chan struct{})}
}

type incoming struct {
	T string `json:"t"`
	D struct {
		R *int64          `json:"r"`
		A string          `json:"a"`
		B json.RawMessage `json:"b"`
	} `json:"d"`
}

// handle routes one message from the server, read at at.
func (r *router) handle(msg []byte, at time.Time) {
	var in incoming
	if json.Unmarshal(msg, &in) != nil || in.T != "d" {
		return // control messages: pong, error frames
	}
	if in.D.R != nil {
		var rep Reply
		_ = json.Unmarshal(in.D.B, &rep)
		r.mu.Lock()
		ch := r.waiting[*in.D.R]
		delete(r.waiting, *in.D.R)
		r.mu.Unlock()
		if ch != nil {
			ch <- rep
		}
		return
	}
	if r.onPush == nil {
		return
	}
	var b struct {
		P string          `json:"p"`
		D json.RawMessage `json:"d"`
		T *int64          `json:"t"`
	}
	_ = json.Unmarshal(in.D.B, &b)
	r.onPush(Push{Action: in.D.A, Path: b.P, Data: b.D, Tag: b.T, At: at})
}

// request numbers a request and registers its reply channel; ErrClosed once the connection
// has ended.
func (r *router) request(action string, body any) (msg []byte, id int64, ch chan Reply, err error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, 0, nil, err
	}
	ch = make(chan Reply, 1)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, 0, nil, ErrClosed
	}
	r.rid++
	id = r.rid
	r.waiting[id] = ch
	r.mu.Unlock()
	msg, _ = json.Marshal(map[string]any{"t": "d", "d": map[string]any{"r": id, "a": action, "b": json.RawMessage(b)}})
	return msg, id, ch, nil
}

// abandon gives up on request id after its message could not be sent: its outcome is
// unknown, which a closed channel says.
func (r *router) abandon(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.waiting[id]; ok {
		delete(r.waiting, id)
		close(ch)
	}
}

// finish fails every request still waiting: their outcome is unknown.
func (r *router) finish(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed, r.err = true, err
	for id, ch := range r.waiting {
		close(ch)
		delete(r.waiting, id)
	}
	close(r.done)
}

// Conn is one WebSocket connection speaking the Firebase wire protocol, version 5, as the
// SDKs do: requests are numbered, replies are matched to them, and pushes go to OnPush.
type Conn struct {
	*router
	ws    *websocket.Conn
	Hello wire.Hello
	wmu   sync.Mutex // serialises writes
}

// DialOptions configure a connection.
type DialOptions struct {
	// OnPush receives every push, on the reader goroutine, in order; it must not block for
	// long.
	OnPush func(Push)
	// HTTPClient dials; nil uses a fresh transport.
	HTTPClient *http.Client
}

// Dial opens /.ws on base (http://host:port) for namespace ns and reads the handshake.
func Dial(ctx context.Context, base, ns string, o DialOptions) (*Conn, error) {
	u := "ws" + strings.TrimPrefix(base, "http") + "/.ws?v=5&ns=" + ns
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: o.HTTPClient, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(1 << 20)
	c := &Conn{router: newRouter(o.OnPush), ws: ws}
	asm := &wire.Assembler{Max: 256 << 20}
	first, err := c.readMessage(ctx, asm)
	if err != nil {
		ws.CloseNow()
		return nil, err
	}
	var hello struct {
		T string `json:"t"`
		D struct {
			T string     `json:"t"`
			D wire.Hello `json:"d"`
		} `json:"d"`
	}
	if err := json.Unmarshal(first, &hello); err != nil || hello.T != "c" || hello.D.T != "h" {
		ws.CloseNow()
		return nil, errors.New("rtdbkit: first message is not the handshake")
	}
	c.Hello = hello.D.D
	go c.readLoop(asm)
	return c, nil
}

func (c *Conn) readMessage(ctx context.Context, asm *wire.Assembler) ([]byte, error) {
	for {
		typ, frame, err := c.ws.Read(ctx)
		if err != nil {
			return nil, err
		}
		if typ != websocket.MessageText {
			continue
		}
		msg, err := asm.Feed(frame)
		if err != nil {
			return nil, err
		}
		if msg != nil {
			return msg, nil
		}
	}
}

func (c *Conn) readLoop(asm *wire.Assembler) {
	var err error
	defer func() { c.finish(err) }()
	for {
		var msg []byte
		if msg, err = c.readMessage(context.Background(), asm); err != nil {
			return
		}
		c.handle(msg, time.Now())
	}
}

// Done is closed once the connection has ended.
func (r *router) Done() <-chan struct{} { return r.done }

// Send sends a request and returns the channel its reply arrives on. The channel is closed
// without a value if the connection ends first.
func (c *Conn) Send(action string, body any) (<-chan Reply, error) {
	msg, id, ch, err := c.request(action, body)
	if err != nil {
		return nil, err
	}
	if err := c.write(msg); err != nil {
		c.abandon(id) // sent or not, the outcome is unknown: the channel says so
	}
	return ch, nil
}

func (c *Conn) write(msg []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	for _, f := range wire.Split(msg) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := c.ws.Write(ctx, websocket.MessageText, f)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

// Call sends a request and waits for its reply. ErrClosed means the connection ended first.
func (c *Conn) Call(ctx context.Context, action string, body any) (Reply, error) {
	ch, err := c.Send(action, body)
	if err != nil {
		return Reply{}, err
	}
	return Await(ctx, ch)
}

// Await waits for a reply sent with Send.
func Await(ctx context.Context, ch <-chan Reply) (Reply, error) {
	select {
	case r, ok := <-ch:
		if !ok {
			return Reply{}, ErrClosed
		}
		return r, nil
	case <-ctx.Done():
		return Reply{}, ctx.Err()
	}
}

// Ping sends a keepalive frame "0" as the SDKs do after 45 seconds of silence.
func (c *Conn) Ping() error { return c.write([]byte("0")) }

// Close closes the connection normally, as an SDK going offline does.
func (c *Conn) Close() error {
	err := c.ws.Close(websocket.StatusNormalClosure, "")
	<-c.done
	return err
}

// CloseNow drops the connection without a close handshake.
func (c *Conn) CloseNow() {
	_ = c.ws.CloseNow()
	<-c.done
}

// Bodies of the requests, in the protocol's field names.

// Put is the body of p, o: a set of Data at Path, conditional on Hash when set.
func Put(path string, data any, hash *string) map[string]any {
	b := map[string]any{"p": path, "d": data}
	if hash != nil {
		b["h"] = *hash
	}
	return b
}

// Listen is the body of q and n for a default listen.
func Listen(path string) map[string]any { return map[string]any{"p": path, "h": ""} }

// ListenQuery is the body of q and n for a query listen with its tag.
func ListenQuery(path string, q map[string]any, tag int64) map[string]any {
	return map[string]any{"p": path, "q": q, "t": tag, "h": ""}
}

// Get is the body of g.
func Get(path string) map[string]any { return map[string]any{"p": path} }
