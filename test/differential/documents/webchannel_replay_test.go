package documents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	"github.com/arumadev/aruma/internal/services/documents/rest"
	"github.com/arumadev/aruma/internal/services/documents/webchannel"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
	conformance "github.com/arumadev/aruma/test/conformance/documents"
)

// webDiffers are the f6 exchanges Aruma does not answer as production, each with its parity
// exception (parity-exceptions.json). A listed exchange that matches fails the test.
var webDiffers = map[string]string{
	"f6-cors.json cors.preflights/REST preflight asking for an arbitrary header":                                           "cors-unknown-request-header",
	"f6-webchannel-errors.json raw.channel-errors/Listen: handshake with a malformed Authorization and an invalid API key": "webchannel-api-key-validated",
	"f6-webchannel-errors.json raw.channel-errors/Write: handshake with a malformed Authorization and an invalid API key":  "webchannel-api-key-validated",
	"f6-webchannel-errors.json raw.channel-errors/Listen: no VER and no database parameter":                                "webchannel-probe-cookie",
	"f6-webchannel-errors.json raw.channel-errors/Write: no VER and no database parameter":                                 "webchannel-probe-cookie",
	"f6-webchannel-errors.json listen.invalid-api-key/backchannel":                                                         "webchannel-api-key-validated",
	"f6-webchannel-errors.json listen.invalid-api-key/terminate":                                                           "webchannel-api-key-validated",
	"f6-webchannel-errors.json listen.invalid-api-key/handshake":                                                           "webchannel-api-key-validated",
}

// The browser transports' recordings (record-f6-web.mjs) are HTTP exchanges: hand-made requests
// with their answers (requests), and the SDK's sessions in the order it sent them (entries). The
// replay sends each request to an Aruma in this process, mounted as internal/server mounts it,
// and compares the answer with production's after the random parts are normalised: SID,
// gsessionid, RID, zx, times, resume and stream tokens. A session's backchannel is compared array
// by array, noops aside (their ids and times are the clock's), and a forward POST is sent once
// Aruma has sent the arrays the recorded AID acknowledges. The documents a recording shows are
// written before its case, or, for a change the recorder made during a session, when the replay
// reaches it.

type webRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    *string           `json:"body"`
}

type webResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type webExchange struct {
	Label    string      `json:"label"`
	Request  webRequest  `json:"request"`
	Response webResponse `json:"response"`
}

type webCase struct {
	ID       string        `json:"id"`
	Entries  []webExchange `json:"entries"`
	Requests []webExchange `json:"requests"`
}

// webHeaders are the answer headers compared: what a browser or the SDK reads.
var webHeaders = []string{
	"access-control-allow-origin", "access-control-allow-credentials", "access-control-allow-methods",
	"access-control-allow-headers", "access-control-max-age", "access-control-expose-headers",
	"vary", "content-type", "cache-control",
}

const webKey = "aruma-replay-key"

type webKeys struct{}

func (webKeys) ProjectForKey(_ context.Context, key string) (tcore.KeyProject, error) {
	if key == webKey {
		return tcore.KeyProject{ID: apitest.Project}, nil
	}
	return tcore.KeyProject{}, errors.New("unknown key")
}

var (
	webTime      = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z|N{4}-NN-NNTNN:NN:NN(\.N+)?Z`)
	webToken     = regexp.MustCompile(`"(resumeToken|streamToken)": "[^"]*"`)
	webRecordedK = regexp.MustCompile(`AIzaSyA+`)
)

// webNorm normalises a message or body: times and tokens.
func webNorm(s string) string {
	s = webTime.ReplaceAllString(s, "<time>")
	return webToken.ReplaceAllString(s, `"$1": "<token>"`)
}

type webReplay struct {
	t      *testing.T
	base   string
	admin  *conformance.HTTPTarget
	user   string
	client *http.Client
	diffs  map[string][]string
}

func TestReplayWebChannelInProcess(t *testing.T) {
	env := apitest.New(t, apitest.Options{})
	env.PublishFirestoreRules(t, `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /aruma-ref-f6-open/{d=**} { allow read, write: if true; }
    match /aruma-ref-f6-auth/{d=**} { allow read, write: if request.auth != null; }
    match /aruma-ref-f6-deny/{d=**} { allow read, write: if false; }
  }
}`)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	streams := docgrpc.New(docgrpc.Options{Service: env.Service, Metrics: docgrpc.NewMetrics(), Logger: log})
	wc := webchannel.New(webchannel.Options{Streams: streams, Keys: webKeys{}, Logger: log})
	ts := httptest.NewServer(httpx.CommonWithLimit(rest.New(rest.Options{Service: env.Service, Channel: wc, Logger: log}), log, nil, 17<<20))
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})
	r := &webReplay{t: t, base: ts.URL, user: apitest.UserToken(t, "u1", nil), client: ts.Client(), diffs: map[string][]string{},
		admin: &conformance.HTTPTarget{BaseURL: ts.URL, ProjectID: apitest.Project, Token: env.JWT(t), Client: ts.Client()}}

	files, _ := filepath.Glob(filepath.Join(conformance.CorpusDir, "f6-*.json"))
	exchanges := 0
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Cases []webCase `json:"cases"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(path)
		for _, c := range f.Cases {
			r.setup(c)
			for _, x := range c.Requests {
				exchanges++
				r.exchange(name+" "+c.ID+"/"+x.Label, x, nil)
			}
			if len(c.Entries) > 0 {
				exchanges += r.session(name+" "+c.ID, c.Entries)
			}
		}
	}
	if exchanges < 80 {
		t.Errorf("%d exchanges replayed: the f6 corpus has more", exchanges)
	}
	for id, exception := range webDiffers {
		if d, ok := r.diffs[id]; !ok {
			t.Errorf("%s: not replayed, but listed as differing (%s)", id, exception)
		} else if len(d) == 0 {
			t.Errorf("%s: matches production, but is listed as differing (%s)", id, exception)
		}
	}
	for id, d := range r.diffs {
		if _, listed := webDiffers[id]; !listed && len(d) > 0 {
			t.Errorf("%s differs from production:\n  %s", id, strings.Join(d, "\n  "))
		}
	}
}

// ---- fixtures ----

// setup leaves in the recorder's collections the documents the case shows as they were when it
// began: those of the REST answers, and those a target's initial set carries.
func (r *webReplay) setup(c webCase) {
	ctx := r.t.Context()
	for _, col := range []string{"aruma-ref-f6-open", "aruma-ref-f6-auth", "aruma-ref-f6-deny"} {
		res, err := r.admin.Send(ctx, conformance.Request{Method: http.MethodGet, Path: "projects/" + apitest.Project + "/databases/(default)/documents/" + col + "?pageSize=300"})
		if err != nil {
			r.t.Fatal(err)
		}
		docs, _ := res.Body.(map[string]any)["documents"].([]any)
		for _, d := range docs {
			name, _ := d.(map[string]any)["name"].(string)
			if _, err := r.admin.Send(ctx, conformance.Request{Method: http.MethodDelete, Path: name}); err != nil {
				r.t.Fatal(err)
			}
		}
	}
	var docs []map[string]any
	for _, x := range slices.Concat(c.Requests, c.Entries) {
		if !strings.Contains(x.Request.URL, "/channel?") {
			docs = append(docs, restDocs(x.Response.Body)...)
		}
	}
	docs = append(docs, initialDocs(c.Entries)...)
	for _, d := range docs {
		r.put(d)
	}
}

// put writes a recorded document, unless it holds a masked time.
func (r *webReplay) put(d map[string]any) {
	name, _ := d["name"].(string)
	fields, _ := d["fields"].(map[string]any)
	if b, _ := json.Marshal(fields); name == "" || strings.Contains(string(b), "NNNN-") {
		return
	}
	res, err := r.admin.Send(r.t.Context(), conformance.Request{Method: http.MethodPatch, Path: name, Body: map[string]any{"fields": fields}})
	if err != nil || res.HTTP != http.StatusOK {
		r.t.Fatalf("put %s: %v %v", name, err, res.Body)
	}
}

// restDocs are the documents of a REST answer: a document, found, or a query's documents.
func restDocs(body string) []map[string]any {
	var v any
	if json.Unmarshal([]byte(body), &v) != nil {
		return nil
	}
	var out []map[string]any
	items, ok := v.([]any)
	if !ok {
		items = []any{v}
	}
	for _, it := range items {
		m, _ := it.(map[string]any)
		for _, k := range []string{"found", "document"} {
			if d, ok := m[k].(map[string]any); ok {
				out = append(out, d)
			}
		}
		if _, ok := m["name"]; ok && m["fields"] != nil {
			out = append(out, m)
		}
	}
	return out
}

// initialDocs are the documents a target's initial set carries: the changes before its CURRENT,
// each at its first version.
func initialDocs(entries []webExchange) []map[string]any {
	seen := map[string]bool{}
	var out []map[string]any
	for _, sess := range recordedSessions(entries) {
		current := map[float64]bool{}
		for _, m := range sess {
			var data []map[string]any
			if json.Unmarshal([]byte(m.payload), &data) != nil || len(data) != 1 {
				continue
			}
			if tc, ok := data[0]["targetChange"].(map[string]any); ok && tc["targetChangeType"] == "CURRENT" {
				for _, id := range tc["targetIds"].([]any) {
					current[id.(float64)] = true
				}
			}
			dc, ok := data[0]["documentChange"].(map[string]any)
			if !ok {
				continue
			}
			doc := dc["document"].(map[string]any)
			ids, _ := dc["targetIds"].([]any)
			if name := doc["name"].(string); !seen[name] && len(ids) > 0 && !current[ids[0].(float64)] {
				seen[name] = true
				out = append(out, doc)
			}
		}
	}
	return out
}

// recordedSessions are the non-noop messages of each recorded session, in array order.
func recordedSessions(entries []webExchange) [][]wcArray {
	var out [][]wcArray
	for _, x := range entries {
		u := x.Request.URL
		switch {
		case isHandshake(u):
			out = append(out, nil)
		case strings.Contains(u, "RID=rpc") && len(out) > 0:
			for _, a := range bodyArrays(x.Response.Body) {
				if a.payload != `["noop"]` {
					out[len(out)-1] = append(out[len(out)-1], a)
				}
			}
		}
	}
	return out
}

func isHandshake(u string) bool { return strings.Contains(u, "X-HTTP-Session-Id=gsessionid") }

type wcArray struct {
	id      int
	payload string
}

// bodyArrays parses a backchannel body: chunks of a UTF-16 length, a newline and [[id,payload],…].
func bodyArrays(body string) []wcArray {
	var out []wcArray
	br := bufio.NewReader(strings.NewReader(body))
	for {
		text, err := readWebChunk(br)
		if err != nil {
			return out
		}
		out = append(out, parseWebArrays(text)...)
	}
}

func readWebChunk(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for units := 0; units < n; {
		c, _, err := br.ReadRune()
		if err != nil {
			return "", err
		}
		b.WriteRune(c)
		units += len(utf16.Encode([]rune{c}))
	}
	return b.String(), nil
}

func parseWebArrays(text string) []wcArray {
	var raw [][]json.RawMessage
	if json.Unmarshal([]byte(text), &raw) != nil {
		return nil
	}
	out := make([]wcArray, 0, len(raw))
	for _, a := range raw {
		id, _ := strconv.Atoi(string(a[0]))
		out = append(out, wcArray{id: id, payload: string(a[1])})
	}
	return out
}

// ---- exchanges ----

// live is the live session a recorded request names.
type live struct {
	sid, gsid string
	path      string
	recorded  []wcArray // the recorded non-noop messages, in order
	mu        sync.Mutex
	got       []wcArray // the non-noop arrays received
	lastID    int
	changed   chan struct{}
	tokens    map[string]string // recorded stream token → live one
	backs     []*http.Response
	poll      *webRequest // a long-polled session's last backchannel request
	open      int         // its backchannels open
}

// prepare rewrites a recorded request for the live server and session.
func (r *webReplay) prepare(x webRequest, s *live) (*http.Request, error) {
	u, err := url.Parse(x.URL)
	if err != nil {
		return nil, err
	}
	q := u.RawQuery
	q = strings.NewReplacer("<zx>", "zx", "<api-key>", webKey).Replace(q)
	q = regexp.MustCompile(`RID=<rid0(\+\d+)?>`).ReplaceAllStringFunc(q, func(m string) string {
		n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(m, "RID=<rid0+"), ">"))
		return "RID=" + strconv.Itoa(1000+n)
	})
	if s != nil {
		q = regexp.MustCompile(`SID=S+`).ReplaceAllString(q, "SID="+s.sid)
		q = regexp.MustCompile(`gsessionid=G+`).ReplaceAllString(q, "gsessionid="+s.gsid)
		s.mu.Lock()
		q = regexp.MustCompile(`AID=\d+`).ReplaceAllString(q, "AID="+strconv.Itoa(s.lastID))
		s.mu.Unlock()
	}
	var body io.Reader
	if x.Body != nil {
		b := strings.NewReplacer("%3C", "<", "%3E", ">").Replace(*x.Body)
		b = strings.NewReplacer("<api-key>", webKey, "<id-token>", r.user).Replace(b)
		if s != nil {
			s.mu.Lock()
			for rec, mine := range s.tokens {
				b = strings.ReplaceAll(b, url.QueryEscape(`"`+rec+`"`), url.QueryEscape(`"`+mine+`"`))
			}
			s.mu.Unlock()
		}
		body = strings.NewReader(b)
	}
	req, err := http.NewRequestWithContext(r.t.Context(), x.Method, r.base+u.EscapedPath()+"?"+q, body)
	if err != nil {
		return nil, err
	}
	for k, v := range x.Headers {
		if strings.HasPrefix(k, ":") || slices.Contains([]string{"content-length", "accept-encoding", "host", "connection"}, strings.ToLower(k)) {
			continue
		}
		req.Header.Set(k, webRecordedK.ReplaceAllString(v, "AIzaSyA"))
	}
	if x.Body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return req, nil
}

// exchange replays one request and compares its whole answer; a backchannel's body is the
// session's (stream).
func (r *webReplay) exchange(id string, x webExchange, s *live) {
	if x.Request.Method == http.MethodOptions && x.Response.Status == http.StatusNoContent {
		return // Chromium's own view of a preflight it cached, not the wire (f6 research note)
	}
	req, err := r.prepare(x.Request, s)
	if err != nil {
		r.t.Fatal(err)
	}
	res, err := r.client.Do(req)
	if err != nil {
		r.t.Fatalf("%s: %v", id, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	r.compare(id, x.Response, res, string(b), s)
}

func (r *webReplay) compare(id string, want webResponse, res *http.Response, body string, s *live) {
	var d []string
	if res.StatusCode != want.Status {
		d = append(d, fmt.Sprintf("status %d, production %d", res.StatusCode, want.Status))
	}
	got := map[string]string{}
	for _, k := range webHeaders {
		if vs := res.Header.Values(k); len(vs) > 0 {
			got[k] = webHeader(k, strings.Join(vs, ", "))
		}
	}
	for _, k := range webHeaders {
		// A header of several lines: CDP joins them with newlines, Node's fetch with commas.
		w := webHeader(k, strings.ReplaceAll(want.Headers[k], "\n", ", "))
		if got[k] != w {
			d = append(d, fmt.Sprintf("header %s %q, production %q", k, got[k], w))
		}
	}
	if (want.Headers["x-http-session-id"] != "") != (res.Header.Get("X-HTTP-Session-Id") != "") ||
		len(want.Headers["x-http-session-id"]) != len(res.Header.Get("X-HTTP-Session-Id")) {
		d = append(d, fmt.Sprintf("x-http-session-id %q, production %q", res.Header.Get("X-HTTP-Session-Id"), want.Headers["x-http-session-id"]))
	}
	if res.Header.Get("Set-Cookie") != "" {
		d = append(d, "a cookie is set")
	}
	if want.Headers["set-cookie"] != "" {
		d = append(d, "production sets a cookie")
	}
	wb, gb := webNorm(want.Body), webNorm(webSID.ReplaceAllStringFunc(body, func(m string) string {
		return `["c","` + strings.Repeat("S", len(m)-7) + `"`
	}))
	switch {
	case jsonEqual(wb, gb):
		// A REST answer: production pretty-prints it, Aruma does not (parity-exceptions.json
		// rest-json-compact); the order of a document's fields is production's own.
	case strings.Contains(wb, "Unknown SID"):
		// The client looks for the text past index 0; production's page is Google's own.
		if i := strings.Index(gb, "Unknown SID"); i <= 0 {
			d = append(d, "the body does not name Unknown SID")
		}
	case strings.HasPrefix(wb, "<!DOCTYPE"):
		if !strings.HasPrefix(gb, "<!DOCTYPE") {
			d = append(d, fmt.Sprintf("body %.200q, production an HTML page", gb))
		}
	case isForwardAnswer(wb):
		if !isForwardAnswer(gb) {
			d = append(d, fmt.Sprintf("forward answer %q, production %q", gb, wb))
		}
	case gb != wb:
		d = append(d, fmt.Sprintf("body %.400q, production %.400q", gb, wb))
	}
	r.diffs[id] = append(r.diffs[id], d...)
}

var webSID = regexp.MustCompile(`\["c","[A-Za-z0-9_-]+"`)

// webHeader normalises what a header's value depends on besides the server: the headers
// production lists as exposed are those of its answer, transfer-encoding for a chunked one.
func webHeader(k, v string) string {
	if k == "access-control-expose-headers" {
		v = strings.NewReplacer(",transfer-encoding", "", ",content-length", "").Replace(v)
	}
	return v
}

// jsonEqual reports whether two bodies are the same JSON value, and JSON at all.
func jsonEqual(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}

var forwardAnswer = regexp.MustCompile(`^(\d+)\n(\[1,\d+,7\]\n)$`)

// isForwardAnswer: [1, the last array sent, 7] and its length; the last array's id counts noops,
// the clock's.
func isForwardAnswer(s string) bool {
	m := forwardAnswer.FindStringSubmatch(s)
	return m != nil && m[1] == strconv.Itoa(len(m[2]))
}

// session replays an SDK's sessions in their recorded order and compares the messages each
// sends, array by array.
func (r *webReplay) session(id string, entries []webExchange) int {
	recorded := recordedSessions(entries)
	var s *live
	n, sessions := 0, 0
	defer func() { r.closeBacks(s) }()
	for _, x := range entries {
		u := x.Request.URL
		if !strings.Contains(u, "/channel?") {
			n++
			label := x.Request.Method + " " + u[strings.LastIndex(u, ":"):]
			if i := strings.Index(label, "?"); i > 0 {
				label = label[:i]
			}
			r.exchange(id+"/"+label, x, nil)
			continue
		}
		n++
		switch {
		case isHandshake(u):
			r.closeBacks(s)
			s = &live{path: u, changed: make(chan struct{}), tokens: map[string]string{}, recorded: recorded[sessions]}
			sessions++
			r.handshake(id+"/handshake", x, s)
		case strings.Contains(u, "RID=rpc"):
			r.back(id+"/backchannel", x, s)
		case strings.Contains(u, "TYPE=terminate"):
			r.awaitAll(id, s)
			r.exchange(id+"/terminate", x, s)
		default:
			r.await(id, s, x)
			r.exchange(id+"/forward", x, s)
		}
	}
	r.awaitAll(id, s)
	return n
}

func (r *webReplay) handshake(id string, x webExchange, s *live) {
	req, err := r.prepare(x.Request, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	res, err := r.client.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if as := bodyArrays(string(b)); len(as) > 0 {
		var hello []any
		if json.Unmarshal([]byte(as[0].payload), &hello) == nil && len(hello) > 1 {
			s.sid, _ = hello[1].(string)
		}
	}
	s.gsid = res.Header.Get("X-HTTP-Session-Id")
	r.compare(id, x.Response, res, string(b), s)
}

// back opens a backchannel and reads its arrays into the session as they come; its headers are
// compared at once, its messages by awaitAll.
func (r *webReplay) back(id string, x webExchange, s *live) {
	// The client opens a backchannel once the last one ended, acknowledging what it carried.
	if id != "" {
		r.await(strings.TrimSuffix(id, "/backchannel"), s, x)
	}
	req, err := r.prepare(x.Request, s)
	if err != nil {
		r.t.Fatal(err)
	}
	res, err := r.client.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	if id != "" {
		want := x.Response
		want.Body = ""
		r.compare(id, want, res, "", nil)
	}
	s.backs = append(s.backs, res)
	s.mu.Lock()
	s.open++
	if strings.Contains(x.Request.URL, "CI=1") {
		s.poll = &x.Request
	}
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.open--
			close(s.changed)
			s.changed = make(chan struct{})
			s.mu.Unlock()
		}()
		br := bufio.NewReader(res.Body)
		for {
			text, err := readWebChunk(br)
			if err != nil {
				return
			}
			s.mu.Lock()
			for _, a := range parseWebArrays(text) {
				if a.id <= s.lastID && a.id > 0 {
					continue // sent again on the next backchannel, a race of the replay's
				}
				s.lastID = a.id
				if a.payload != `["noop"]` {
					s.got = append(s.got, a)
				}
			}
			close(s.changed)
			s.changed = make(chan struct{})
			s.mu.Unlock()
		}
	}()
}

func (r *webReplay) closeBacks(s *live) {
	if s == nil {
		return
	}
	for _, res := range s.backs {
		res.Body.Close()
	}
	s.backs = nil
}

// await waits until the session received the messages the forward request's recorded AID
// acknowledges.
func (r *webReplay) await(id string, s *live, x webExchange) {
	m := regexp.MustCompile(`AID=(\d+)`).FindStringSubmatch(x.Request.URL)
	if m == nil {
		return
	}
	aid, _ := strconv.Atoi(m[1])
	k := 0
	for k < len(s.recorded) && s.recorded[k].id <= aid {
		k++
	}
	r.waitFor(id, s, k)
}

// awaitAll waits for every recorded message of the session.
func (r *webReplay) awaitAll(id string, s *live) {
	if s != nil {
		r.waitFor(id, s, len(s.recorded))
	}
}

// waitFor waits until s received k messages, comparing each with production's; a message the
// recorder caused from outside (a document written during the session) is written when reached,
// and a global snapshot production sent on its own clock is let go.
func (r *webReplay) waitFor(id string, s *live, k int) {
	for i := 0; i < k; i++ {
		s.mu.Lock()
		have := len(s.got) > i
		s.mu.Unlock()
		if !have {
			r.cause(s.recorded[i].payload)
			if !r.receive(s, i, 5*time.Second) {
				if isGlobal(s.recorded[i].payload) || isDelete(s.recorded[i].payload) {
					// A global snapshot on production's clock (listen-heartbeats), or the delete
					// of a missing document the recorder deleted again during the session.
					s.recorded = slices.Delete(s.recorded, i, i+1)
					i--
					k--
					continue
				}
				r.diffs[id+"/backchannel"] = append(r.diffs[id+"/backchannel"], fmt.Sprintf("message %d not sent: production sent %.300q", i+1, s.recorded[i].payload))
				s.recorded = s.recorded[:i]
				return
			}
		}
		s.mu.Lock()
		got := s.got[i].payload
		if strings.Contains(got, `"streamToken"`) {
			s.tokens[tokenOf(s.recorded[i].payload)] = tokenOf(got)
		}
		s.mu.Unlock()
		// A document's fields are a map, whose order is the server's own.
		if gw, ww := webNorm(got), webNorm(s.recorded[i].payload); gw != ww && !(strings.Contains(ww, `"fields"`) && jsonEqual(gw, ww)) {
			if isGlobal(s.recorded[i].payload) && !isGlobal(got) {
				// Production's global snapshot of its own clock, which Aruma does not send.
				s.recorded = slices.Delete(s.recorded, i, i+1)
				i--
				k--
				continue
			}
			r.diffs[id+"/backchannel"] = append(r.diffs[id+"/backchannel"], fmt.Sprintf("message %d %.400q, production %.400q", i+1, gw, ww))
		} else if _, ok := r.diffs[id+"/backchannel"]; !ok {
			r.diffs[id+"/backchannel"] = nil
		}
	}
}

func (r *webReplay) receive(s *live, i int, wait time.Duration) bool {
	deadline := time.After(wait)
	for {
		s.mu.Lock()
		have, ch, poll := len(s.got) > i, s.changed, s.poll != nil && s.open == 0
		s.mu.Unlock()
		if have {
			return true
		}
		if poll {
			// A long poll answers as soon as it has an array, where production's carried what the
			// stream had at once: the client polls again.
			r.back("", webExchange{Request: *s.poll}, s)
			continue
		}
		select {
		case <-ch:
		case <-deadline:
			return false
		}
	}
}

// cause writes the document a recorded change shows, when the recorder wrote it during the
// session; a change Aruma already holds is written again to the same fields, which a listener
// sees as no change, so it is written only when not received in time.
func (r *webReplay) cause(msg string) {
	var data []map[string]any
	if json.Unmarshal([]byte(msg), &data) != nil || len(data) != 1 {
		return
	}
	if dc, ok := data[0]["documentChange"].(map[string]any); ok {
		// Let a change of the stream's own come first.
		time.Sleep(300 * time.Millisecond)
		r.put(dc["document"].(map[string]any))
	}
}

func isDelete(msg string) bool { return strings.HasPrefix(msg, "[{\n  \"documentDelete\"") }

func isGlobal(msg string) bool {
	var data []map[string]any
	if json.Unmarshal([]byte(msg), &data) != nil || len(data) != 1 {
		return false
	}
	tc, ok := data[0]["targetChange"].(map[string]any)
	return ok && len(data[0]) == 1 && tc["targetIds"] == nil && tc["targetChangeType"] == nil
}

func tokenOf(msg string) string {
	m := regexp.MustCompile(`"streamToken": "([^"]*)"`).FindStringSubmatch(msg)
	if m == nil {
		return ""
	}
	return m[1]
}
