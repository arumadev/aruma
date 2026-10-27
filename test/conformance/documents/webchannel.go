package documents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf16"
)

// The WebChannel step (MUST 21): one session of the browser transport, the smallest the full web
// SDK needs of a stream. A step of Method MethodWebChannel is sent to Path (the channel path, not
// under /v1/) and plays, in order: the handshake POST carrying the first request, the streamed
// backchannel GET, a forward POST for each further request, and TYPE=terminate. Its Body is a list
// made by Channel; the answer's Body is the list of the stream's responses (the objects of the
// arrays, noops left out), which the checks read with Lookup paths such as "0.targetChange".

// MethodWebChannel marks a request as a WebChannel session.
const MethodWebChannel = "WEBCHANNEL"

// The channel paths.
const (
	ListenChannel = "/google.firestore.v1.Firestore/Listen/channel"
	WriteChannel  = "/google.firestore.v1.Firestore/Write/channel"
)

// ChannelSend is one request of the stream and the number of responses to wait for after it.
// Message is the JSON value of a ListenRequest or WriteRequest; Token adds to a WriteRequest the
// streamToken of the last response that carried one, as a Write stream's client does.
type ChannelSend struct {
	Message map[string]any
	Token   bool
	Read    int
}

// Channel is the request of a session on path that makes the sends in order.
func Channel(path string, sends ...ChannelSend) Request {
	body := make([]any, len(sends))
	for i, s := range sends {
		body[i] = map[string]any{"send": s.Message, "token": s.Token, "read": s.Read}
	}
	return Request{Method: MethodWebChannel, Path: path, Body: body}
}

// channelTimeout bounds a whole session.
const channelTimeout = 30 * time.Second

func (h *HTTPTarget) webChannel(ctx context.Context, req Request) (Response, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, channelTimeout, errors.New("the WebChannel session timed out"))
	defer cancel()
	sends, _ := req.Body.([]any)
	if len(sends) == 0 {
		return Response{}, errors.New("a WebChannel session needs a request")
	}
	s := &channelClient{h: h, ctx: ctx, base: strings.TrimSuffix(h.BaseURL, "/") + req.Path,
		database: "projects/" + h.ProjectID + "/databases/(default)", msgs: make(chan any, 256), failed: make(chan error, 1)}
	var got []any
	for i, x := range sends {
		step, _ := x.(map[string]any)
		msg, _ := step["send"].(map[string]any)
		if t, _ := step["token"].(bool); t {
			msg = maps.Clone(msg)
			msg["streamToken"] = lastToken(got)
		}
		var err error
		if i == 0 {
			err = s.handshake(msg)
		} else {
			err = s.forward(i, msg)
		}
		if err != nil {
			return Response{}, err
		}
		n, _ := step["read"].(int)
		for range n {
			select {
			case m := <-s.msgs:
				got = append(got, m)
			case err := <-s.failed:
				return Response{}, fmt.Errorf("the backchannel ended after %d responses: %w", len(got), err)
			case <-ctx.Done():
				return Response{}, fmt.Errorf("%d responses of %d: %w", len(got), n, context.Cause(ctx))
			}
		}
	}
	if err := s.terminate(len(sends)); err != nil {
		return Response{}, err
	}
	return Response{HTTP: http.StatusOK, Body: got}, nil
}

// lastToken is the streamToken of the last response that has one.
func lastToken(got []any) string {
	for _, m := range slices.Backward(got) {
		if o, ok := m.(map[string]any); ok {
			if t, ok := o["streamToken"].(string); ok {
				return t
			}
		}
	}
	return ""
}

// channelClient is the client side of one session.
type channelClient struct {
	h        *HTTPTarget
	ctx      context.Context
	base     string
	database string
	sid      string
	gsid     string
	lastID   atomic.Int64 // the id of the last array received
	msgs     chan any
	failed   chan error
}

// query is the URL query every request of the session carries.
func (c *channelClient) query(rid string, more url.Values) string {
	q := url.Values{"VER": {"8"}, "database": {c.database}, "RID": {rid}, "t": {"1"}}
	if c.sid != "" {
		q.Set("SID", c.sid)
		q.Set("gsessionid", c.gsid)
	}
	for k, v := range more {
		q[k] = v
	}
	return c.base + "?" + q.Encode()
}

func (c *channelClient) post(u string, form url.Values) (*http.Response, error) {
	r, err := http.NewRequestWithContext(c.ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.h.Client.Do(r)
}

// mapForm is the form of a forward request carrying msg as map number ofs.
func mapForm(ofs int, msg map[string]any) (url.Values, error) {
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return url.Values{"count": {"1"}, "ofs": {strconv.Itoa(ofs)}, "req0___data__": {string(b)}}, nil
}

// handshake opens the session with msg as its first request and starts reading the backchannel.
func (c *channelClient) handshake(msg map[string]any) error {
	form, err := mapForm(0, msg)
	if err != nil {
		return err
	}
	headers := "Content-Type:text/plain\r\n"
	if c.h.Token != "" {
		headers += "Authorization:Bearer " + c.h.Token + "\r\n"
	}
	form.Set("headers", headers)
	res, err := c.post(c.query("1000", url.Values{"CVER": {"22"}, "X-HTTP-Session-Id": {"gsessionid"}}), form)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("handshake: HTTP %d %.200s", res.StatusCode, b)
	}
	br := bufio.NewReader(res.Body)
	first := true
	for {
		text, err := readChunk(br)
		if err != nil {
			break
		}
		for _, a := range parseArrays(text) {
			if first {
				first = false
				hello, _ := a.payload.([]any)
				if len(hello) < 2 || hello[0] != "c" {
					return fmt.Errorf("handshake: the first array is %v, not [\"c\",SID,…]", a.payload)
				}
				c.sid, _ = hello[1].(string)
				continue
			}
			c.deliver(a) // an error the server sent with the handshake
		}
	}
	if c.sid == "" {
		return errors.New("handshake: no session id")
	}
	if c.gsid = res.Header.Get("X-HTTP-Session-Id"); c.gsid == "" {
		return errors.New("handshake: no X-HTTP-Session-Id")
	}
	return c.backchannel()
}

// backchannel opens the streamed backchannel (CI=0) and reads it in the background.
func (c *channelClient) backchannel() error {
	r, err := http.NewRequestWithContext(c.ctx, http.MethodGet,
		c.query("rpc", url.Values{"AID": {"0"}, "CI": {"0"}, "TYPE": {"xmlhttp"}}), nil)
	if err != nil {
		return err
	}
	res, err := c.h.Client.Do(r)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return fmt.Errorf("backchannel: HTTP %d %.200s", res.StatusCode, b)
	}
	go func() {
		defer res.Body.Close()
		br := bufio.NewReader(res.Body)
		for {
			text, err := readChunk(br)
			if err != nil {
				c.failed <- err
				return
			}
			for _, a := range parseArrays(text) {
				c.deliver(a)
			}
		}
	}()
	return nil
}

// deliver hands the objects of an array to the session, a noop to nobody.
func (c *channelClient) deliver(a channelArray) {
	c.lastID.Store(int64(a.id))
	list, _ := a.payload.([]any)
	for _, m := range list {
		if _, noop := m.(string); !noop {
			c.msgs <- m
		}
	}
}

// forward sends msg as the session's map number n.
func (c *channelClient) forward(n int, msg map[string]any) error {
	form, err := mapForm(n, msg)
	if err != nil {
		return err
	}
	res, err := c.post(c.query(strconv.Itoa(1000+n), url.Values{"AID": {strconv.FormatInt(c.lastID.Load(), 10)}}), form)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("forward request %d: HTTP %d %.200s", n, res.StatusCode, b)
	}
	return nil
}

func (c *channelClient) terminate(n int) error {
	r, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.query(strconv.Itoa(1000+n), url.Values{"TYPE": {"terminate"}}), nil)
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	res, err := c.h.Client.Do(r)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("terminate: HTTP %d", res.StatusCode)
	}
	return nil
}

type channelArray struct {
	id      int
	payload any
}

// readChunk reads one chunk of a channel body: its length in UTF-16 units, a newline, its text.
func readChunk(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		return "", fmt.Errorf("chunk length %q: %w", line, err)
	}
	var b strings.Builder
	for units := 0; units < n; {
		r, _, err := br.ReadRune()
		if err != nil {
			return "", err
		}
		b.WriteRune(r)
		units += len(utf16.Encode([]rune{r}))
	}
	return b.String(), nil
}

// parseArrays reads a chunk's text: [[id,payload],…].
func parseArrays(text string) []channelArray {
	var raw [][]json.RawMessage
	if json.Unmarshal([]byte(text), &raw) != nil {
		return nil
	}
	var out []channelArray
	for _, a := range raw {
		if len(a) != 2 {
			continue
		}
		var id int
		var payload any
		if json.Unmarshal(a[0], &id) == nil && json.Unmarshal(a[1], &payload) == nil {
			out = append(out, channelArray{id, payload})
		}
	}
	return out
}
