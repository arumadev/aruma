package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// state is what a client knows of one of its objects: absent, or a version with its custom
// metadata m ("" for none).
type state struct {
	exists bool
	v      int64
	m      string
}

func (s state) String() string {
	if !s.exists {
		return "absent"
	}
	if s.m == "" {
		return fmt.Sprintf("v%d", s.v)
	}
	return fmt.Sprintf("v%d m=%s", s.v, s.m)
}

// client is one writer. It owns the objects o/c<i>/<j>, so after each operation, once a lost
// reply is settled by reading back, it knows exactly what each of them must hold.
type client struct {
	r     *run
	i     int
	rng   *rand.Rand
	n     int64
	names []string
	want  map[string]state
	stats map[string]int
}

func (c *client) count(k string) { c.stats[k]++ }

func (c *client) objURL(name string) string { return c.r.base + "/" + url.QueryEscape(name) }

func (c *client) uploadURL(name string) string { return c.r.base + "?name=" + url.QueryEscape(name) }

// size picks an object size: mostly small, some of a few MB.
func (c *client) size(big bool) int64 {
	switch x := c.rng.IntN(100); {
	case big && x < 15:
		return 4<<20 + c.rng.Int64N(8<<20)
	case x < 25:
		return 1<<20 + c.rng.Int64N(3<<20)
	default:
		return headerLen + c.rng.Int64N(512<<10)
	}
}

// loop runs operations until stop ends. Each operation runs to its end under the run's own
// context: a request cancelled by the client could still complete on the server after the
// client read the object back, which would look like a write applied after it was settled.
func (c *client) loop(stop context.Context) {
	ctx := c.r.bg
	for stop.Err() == nil {
		if _, err := c.r.WaitUp(stop); err != nil {
			return
		}
		name := c.names[c.rng.IntN(len(c.names))]
		switch x := c.rng.IntN(100); {
		case x < 22:
			c.simple(ctx, name)
		case x < 40:
			c.multipart(ctx, name)
		case x < 62:
			c.resumable(ctx, name)
		case x < 74:
			c.delete(ctx, name)
		case x < 90:
			c.patch(ctx, name)
		default:
			c.read(ctx, name, "read")
		}
	}
}

// do sends a request; a nil error means the server answered, whatever the status.
func (c *client) do(ctx context.Context, method, u string, hdr map[string]string, body []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if debug {
		fmt.Fprintf(os.Stderr, "c%d %s %s %v %d bytes\n", c.i, method, u, hdr, len(body))
	}
	resp, err := c.r.hc.Do(req)
	if debug {
		fmt.Fprintf(os.Stderr, "c%d  -> %v %v\n", c.i, resp != nil && err == nil, err)
	}
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, b, nil
}

// lost reports whether a reply leaves the outcome open: no reply, or a 5xx.
func lost(code int, err error) bool { return err != nil || code >= 500 }

type objectJSON struct {
	Name     string            `json:"name"`
	Size     string            `json:"size"`
	MD5      string            `json:"md5Hash"`
	Metadata map[string]string `json:"metadata"`
}

// checkReply checks an upload's reply against what was sent.
func (c *client) checkReply(what, name string, v, size int64, b []byte) {
	var o objectJSON
	if err := json.Unmarshal(b, &o); err != nil {
		c.r.Violation("%s %s v%d: reply is not an object: %.200s", what, name, v, b)
		return
	}
	if o.Size != strconv.FormatInt(size, 10) || o.MD5 != md5Of(name, v, size) {
		c.r.Violation("%s %s v%d: acknowledged with size %s md5 %s, sent %d bytes md5 %s", what, name, v, o.Size, o.MD5, size, md5Of(name, v, size))
	}
}

// settle reads an object back after a lost reply and records what it holds; ok reports whether
// that is one of the states the operation may have left: before (not applied) or after.
func (c *client) settle(label, name string, ep int64, before, after state) {
	c.afterLoss(ep)
	got := c.readBack(name)
	switch {
	case got == after:
		c.count(label + " lost, applied")
	case got == before:
		c.count(label + " lost, not applied")
	default:
		c.r.Violation("%s %s: after a lost reply it holds %s; before it held %s, the operation would leave %s", label, name, got, before, after)
	}
	c.want[name] = got
}

func (c *client) afterLoss(ep int64) { _, _ = c.r.WaitNext(c.r.bg, ep) }

// readBack reads an object's state, its metadata and its bytes, across restarts.
func (c *client) readBack(name string) state {
	for {
		ep, err := c.r.WaitUp(c.r.bg)
		if err != nil {
			return state{}
		}
		s, err := c.fetch(c.r.bg, name)
		if err == nil {
			return s
		}
		c.afterLoss(ep)
	}
}

// fetch reads an object's metadata and bytes; a failed read is an error, a wrong object a
// violation.
func (c *client) fetch(ctx context.Context, name string) (state, error) {
	code, _, b, err := c.do(ctx, http.MethodGet, c.objURL(name), nil, nil)
	if err != nil || code >= 500 {
		return state{}, fmt.Errorf("metadata: %d %v", code, err)
	}
	if code == http.StatusNotFound {
		return state{}, nil
	}
	if code != http.StatusOK {
		return state{}, fmt.Errorf("metadata: %d %.100s", code, b)
	}
	var o objectJSON
	if err := json.Unmarshal(b, &o); err != nil {
		return state{}, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.objURL(name)+"?alt=media", nil)
	resp, err := c.r.hc.Do(req)
	if err != nil {
		return state{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// Deleted between the two reads by nobody but this client: the row is there without
		// its bytes, or the metadata read and the download disagree.
		c.r.Violation("%s: metadata found, download 404", name)
		return state{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return state{}, fmt.Errorf("download: %d", resp.StatusCode)
	}
	v, verr := verify(resp.Body, name)
	if verr != nil {
		var ne interface{ Timeout() bool }
		if errors.As(verr, &ne) || strings.Contains(verr.Error(), "read at") {
			return state{}, verr // the connection broke: read again
		}
		c.r.Violation("%s: served bytes are wrong: %v", name, verr)
		return state{}, verr
	}
	if sz, _ := strconv.ParseInt(o.Size, 10, 64); o.MD5 != md5Of(name, v, sz) {
		c.r.Violation("%s: metadata says size %s md5 %s, the bytes are version %d", name, o.Size, o.MD5, v)
	}
	return state{exists: true, v: v, m: o.Metadata["m"]}, nil
}

// read checks that an object serves what the client knows of it.
func (c *client) read(ctx context.Context, name, label string) {
	s, err := c.fetch(ctx, name)
	if err != nil {
		return
	}
	c.count(label)
	if s != c.want[name] {
		c.r.Violation("%s: serves %s, the client knows %s", name, s, c.want[name])
		c.want[name] = s
	}
}

func (c *client) simple(ctx context.Context, name string) {
	c.n++
	v, size := c.n, c.size(false)
	before, after := c.want[name], state{exists: true, v: c.n}
	ep := c.r.Epoch()
	code, _, b, err := c.do(ctx, http.MethodPost, c.uploadURL(name), map[string]string{"Content-Type": "application/octet-stream"}, body(name, v, size, 0, size))
	switch {
	case lost(code, err):
		c.settle("simple", name, ep, before, after)
	case code == http.StatusOK:
		c.checkReply("simple", name, v, size, b)
		c.want[name] = after
		c.count("simple acked")
	default:
		c.count(fmt.Sprintf("simple refused %d", code))
	}
}

func (c *client) multipart(ctx context.Context, name string) {
	c.n++
	v, size := c.n, c.size(false)
	m := strconv.FormatInt(v, 10)
	before, after := c.want[name], state{exists: true, v: v, m: m}
	meta, _ := json.Marshal(map[string]any{"name": name, "contentType": "application/octet-stream", "metadata": map[string]string{"m": m}})
	const boundary = "chaos-boundary-7c1f"
	var buf bytes.Buffer
	buf.WriteString("--" + boundary + "\r\nContent-Type: application/json; charset=utf-8\r\n\r\n")
	buf.Write(meta)
	buf.WriteString("\r\n--" + boundary + "\r\nContent-Type: application/octet-stream\r\n\r\n")
	buf.Write(body(name, v, size, 0, size))
	buf.WriteString("\r\n--" + boundary + "--")
	ep := c.r.Epoch()
	code, _, b, err := c.do(ctx, http.MethodPost, c.uploadURL(name), map[string]string{
		"X-Goog-Upload-Protocol": "multipart", "Content-Type": "multipart/related; boundary=" + boundary}, buf.Bytes())
	switch {
	case lost(code, err):
		c.settle("multipart", name, ep, before, after)
	case code == http.StatusOK:
		c.checkReply("multipart", name, v, size, b)
		c.want[name] = after
		c.count("multipart acked")
	default:
		c.count(fmt.Sprintf("multipart refused %d", code))
	}
}

func (c *client) delete(ctx context.Context, name string) {
	before := c.want[name]
	if !before.exists {
		return
	}
	ep := c.r.Epoch()
	code, _, _, err := c.do(ctx, http.MethodDelete, c.objURL(name), nil, nil)
	switch {
	case lost(code, err):
		c.settle("delete", name, ep, before, state{})
	case code == http.StatusNoContent || code == http.StatusOK:
		c.want[name] = state{}
		c.count("delete acked")
	default:
		c.count(fmt.Sprintf("delete refused %d", code))
		if code == http.StatusNotFound {
			c.r.Violation("delete %s: 404, the client knows %s", name, before)
			c.want[name] = state{}
		}
	}
}

func (c *client) patch(ctx context.Context, name string) {
	before := c.want[name]
	if !before.exists {
		return
	}
	c.n++
	m := "p" + strconv.FormatInt(c.n, 10)
	after := before
	after.m = m
	meta, _ := json.Marshal(map[string]any{"metadata": map[string]string{"m": m}})
	ep := c.r.Epoch()
	code, _, b, err := c.do(ctx, http.MethodPatch, c.objURL(name), map[string]string{"Content-Type": "application/json"}, meta)
	switch {
	case lost(code, err):
		c.settle("patch", name, ep, before, after)
	case code == http.StatusOK:
		var o objectJSON
		_ = json.Unmarshal(b, &o)
		if o.Metadata["m"] != m {
			c.r.Violation("patch %s: acknowledged, reply holds m=%q, sent %q", name, o.Metadata["m"], m)
		}
		c.want[name] = after
		c.count("patch acked")
	default:
		c.count(fmt.Sprintf("patch refused %d", code))
	}
}

// resumable uploads in chunks of 256 KiB multiples. When a chunk's reply is lost it waits for the
// server, asks the session how much it holds (query), checks that this is no less than what the
// server acknowledged and no more than what was sent, and resumes from there. A session the
// server no longer knows is a clean failure: the object must then be as before.
func (c *client) resumable(ctx context.Context, name string) {
	c.n++
	v, size := c.n, c.size(true)
	before, after := c.want[name], state{exists: true, v: v}
	start := func() (string, bool) {
		for attempt := 0; attempt < 5 && ctx.Err() == nil; attempt++ {
			ep := c.r.Epoch()
			meta, _ := json.Marshal(map[string]any{"name": name, "contentType": "application/octet-stream"})
			code, h, _, err := c.do(ctx, http.MethodPost, c.uploadURL(name), map[string]string{
				"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start",
				"X-Goog-Upload-Header-Content-Length": strconv.FormatInt(size, 10),
				"X-Goog-Upload-Header-Content-Type":   "application/octet-stream", "Content-Type": "application/json"}, meta)
			if lost(code, err) {
				c.count("resumable start lost")
				c.afterLoss(ep)
				continue
			}
			if code != http.StatusOK || h.Get("X-Goog-Upload-URL") == "" {
				c.count(fmt.Sprintf("resumable start refused %d", code))
				return "", false
			}
			return h.Get("X-Goog-Upload-URL"), true
		}
		return "", false
	}
	up, ok := start()
	if !ok {
		return
	}
	chunk := int64(256<<10) * (1 + c.rng.Int64N(4))
	var off, acked int64
	for fails := 0; fails < 30; {
		n := min(chunk, size-off)
		final := off+n >= size
		cmd := "upload"
		if final {
			cmd = "upload, finalize"
		}
		ep := c.r.Epoch()
		code, h, b, err := c.do(ctx, http.MethodPost, up, map[string]string{"X-Goog-Upload-Command": cmd,
			"X-Goog-Upload-Offset": strconv.FormatInt(off, 10), "Content-Type": "application/octet-stream"}, body(name, v, size, off, n))
		if err == nil && code == http.StatusOK {
			st := h.Get("X-Goog-Upload-Status")
			if final {
				if st != "final" {
					c.r.Violation("resumable %s v%d: finalize 200 with status %q", name, v, st)
				}
				c.checkReply("resumable", name, v, size, b)
				c.want[name] = after
				c.count("resumable acked")
				return
			}
			if st != "active" {
				c.r.Violation("resumable %s v%d: chunk 200 with status %q", name, v, st)
				return
			}
			if rcv := h.Get("X-Goog-Upload-Size-Received"); rcv != "" && rcv != strconv.FormatInt(off+n, 10) {
				c.r.Violation("resumable %s v%d: chunk to %d acknowledged with size received %s", name, v, off+n, rcv)
			}
			off += n
			acked = off
			continue
		}
		fails++
		if ctx.Err() != nil && err != nil {
			// Told to stop: settle the object as for any lost reply.
			c.count("resumable stopped")
			c.settleSession(name, up, ep, v, size, acked, off+n, before, after)
			return
		}
		if lost(code, err) {
			c.count("resumable chunk lost")
			c.afterLoss(ep)
		} else {
			c.count(fmt.Sprintf("resumable chunk refused %d", code))
		}
		// Ask the session where it stands.
		next, done := c.query(name, up, v, size, acked, off+n, before, after)
		if done {
			return
		}
		if next != off {
			c.count("resumable resumed at another offset")
		}
		c.count("resumable resumed")
		off = next
		acked = max(acked, next)
	}
	c.count("resumable gave up")
	c.settle("resumable gave up", name, c.r.Epoch(), before, before)
}

// query asks a session how much it holds. done means the upload ended, its state settled.
func (c *client) query(name, up string, v, size, acked, sent int64, before, after state) (next int64, done bool) {
	for {
		ep, err := c.r.WaitUp(c.r.bg)
		if err != nil {
			return 0, true
		}
		code, h, _, err := c.do(c.r.bg, http.MethodPost, up, map[string]string{"X-Goog-Upload-Command": "query"}, nil)
		if lost(code, err) {
			c.afterLoss(ep)
			continue
		}
		st := h.Get("X-Goog-Upload-Status")
		rcv, perr := strconv.ParseInt(h.Get("X-Goog-Upload-Size-Received"), 10, 64)
		switch {
		case code == http.StatusOK && st == "active" && perr == nil:
			if rcv < acked {
				c.r.Violation("resumable %s v%d: the session holds %d bytes, %d were acknowledged", name, v, rcv, acked)
			}
			if rcv > sent || rcv > size {
				c.r.Violation("resumable %s v%d: the session holds %d bytes, %d were sent", name, v, rcv, sent)
			}
			return rcv, false
		case code == http.StatusOK && st == "final":
			c.count("resumable final at query")
			got := c.readBack(name)
			if got != after && got != before {
				c.r.Violation("resumable %s v%d: final at query, the object holds %s", name, v, got)
			}
			c.want[name] = got
			return 0, true
		default:
			// The session is gone or cancelled: a clean failure, the object as before.
			c.count(fmt.Sprintf("resumable session ended %d %s", code, st))
			got := c.readBack(name)
			if got != before {
				c.r.Violation("resumable %s v%d: the session ended (%d %s), the object holds %s, before %s", name, v, code, st, got, before)
			}
			c.want[name] = got
			return 0, true
		}
	}
}

// settleSession settles an upload stopped by the end of the run.
func (c *client) settleSession(name, up string, ep, v, size, acked, sent int64, before, after state) {
	c.afterLoss(ep)
	_, done := c.query(name, up, v, size, acked, sent, before, after)
	if !done {
		// Still active: the object is as before.
		got := c.readBack(name)
		if got != before {
			c.r.Violation("resumable %s v%d: the session is active, the object holds %s, before %s", name, v, got, before)
		}
		c.want[name] = got
	}
}

var debug = os.Getenv("CHAOS_DEBUG") != ""
