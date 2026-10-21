package rtdbkit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LPConn is a long-poll connection to /.lp, as the JS SDK's BrowserPollConnection keeps one:
// one request is always held open for the server to answer with what it has, and each
// message the client sends rides in the URL of a new request, which releases the one held.
// Batches are numbered and delivered in order, whatever order their responses arrive in.
type LPConn struct {
	*router
	hc      *http.Client
	base    string
	ns      string
	id, pw  string
	ctx     context.Context
	cancel  context.CancelFunc
	pollers sync.WaitGroup

	mu      sync.Mutex
	ser     int64
	seg     int64
	batches map[int64][][]byte
	next    int64
}

var (
	lpStartRE = regexp.MustCompile(`pLPCommand\('start','([^']*)','([^']*)'\);`)
	lpBatchRE = regexp.MustCompile(`(?m)^pRTLPCB\((\d+),(.*)\);$`)
	lpCloseRE = regexp.MustCompile(`(?m)^pLPCommand\('close'(?:,-?\d+)?\);$`)
)

// DialLP starts a long-poll session on base for namespace ns and keeps a poll open.
func DialLP(ctx context.Context, hc *http.Client, base, ns string, onPush func(Push)) (*LPConn, error) {
	c := &LPConn{router: newRouter(onPush), hc: hc, base: base, ns: ns, ser: 1, batches: map[int64][][]byte{}}
	body, err := c.get(ctx, "start=t&ser=0&cb=1&v=5&ns="+url.QueryEscape(ns))
	if err != nil {
		return nil, err
	}
	m := lpStartRE.FindStringSubmatch(body)
	if m == nil {
		return nil, fmt.Errorf("rtdbkit: long-poll start: %q", body)
	}
	c.id, c.pw = m[1], m[2]
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.take(body) // batch 0, the handshake, which handle skips
	c.pollers.Add(1)
	go c.pollLoop()
	return c, nil
}

func (c *LPConn) get(ctx context.Context, query string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/.lp?"+query, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("rtdbkit: /.lp: %s: %s", resp.Status, b)
	}
	return string(b), nil
}

// take delivers the batches of a response in order, and ends the connection on close.
func (c *LPConn) take(body string) {
	at := time.Now()
	c.mu.Lock()
	for _, m := range lpBatchRE.FindAllStringSubmatch(body, -1) {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		var raw []json.RawMessage
		_ = json.Unmarshal([]byte(m[2]), &raw)
		msgs := make([][]byte, len(raw))
		for i, r := range raw {
			msgs[i] = r
		}
		c.batches[n] = msgs
	}
	var ready [][]byte
	for b, ok := c.batches[c.next]; ok; b, ok = c.batches[c.next] {
		delete(c.batches, c.next)
		ready = append(ready, b...)
		c.next++
	}
	c.mu.Unlock()
	for _, m := range ready {
		c.handle(m, at)
	}
	if lpCloseRE.MatchString(body) {
		c.finish(errors.New("rtdbkit: long poll closed by the server"))
		c.cancel()
	}
}

// query builds the URL of the next request, carrying msgs.
func (c *LPConn) query(msgs ...[]byte) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ser++
	var q strings.Builder
	q.WriteString("id=" + c.id + "&pw=" + c.pw + "&ser=" + strconv.FormatInt(c.ser, 10) + "&ns=" + url.QueryEscape(c.ns))
	i := 0
	for _, m := range msgs {
		enc := strings.ReplaceAll(base64.URLEncoding.EncodeToString(m), "=", ".")
		var pieces []string
		for len(enc) > 1840 {
			pieces, enc = append(pieces, enc[:1840]), enc[1840:]
		}
		pieces = append(pieces, enc)
		for _, p := range pieces {
			n := strconv.Itoa(i)
			fmt.Fprintf(&q, "&seg%s=%d&ts%s=%d&d%s=%s", n, c.seg, n, len(pieces), n, p)
			c.seg++
			i++
		}
	}
	return q.String()
}

// pollLoop keeps a request open until the connection ends.
func (c *LPConn) pollLoop() {
	defer c.pollers.Done()
	for c.ctx.Err() == nil {
		body, err := c.get(c.ctx, c.query())
		if err != nil {
			c.finish(err)
			c.cancel()
			return
		}
		c.take(body)
	}
}

// Send sends a request in a new poll and returns the channel its reply arrives on.
func (c *LPConn) Send(action string, body any) (<-chan Reply, error) {
	msg, id, ch, err := c.request(action, body)
	if err != nil {
		return nil, err
	}
	q := c.query(msg)
	c.pollers.Add(1)
	go func() {
		defer c.pollers.Done()
		resp, err := c.get(c.ctx, q)
		if err != nil {
			c.abandon(id)
			return
		}
		c.take(resp)
	}()
	return ch, nil
}

// Call sends a request and waits for its reply.
func (c *LPConn) Call(ctx context.Context, action string, body any) (Reply, error) {
	ch, err := c.Send(action, body)
	if err != nil {
		return Reply{}, err
	}
	return Await(ctx, ch)
}

// Close ends the session with disconn=t, as a page that unloads does.
func (c *LPConn) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = c.get(ctx, "disconn=t&id="+c.id+"&pw="+c.pw+"&ns="+url.QueryEscape(c.ns))
	c.cancel()
	c.pollers.Wait()
	c.finish(nil)
}
