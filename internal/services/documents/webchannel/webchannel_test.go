package webchannel_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	"github.com/arumadev/aruma/internal/services/documents/rest"
	"github.com/arumadev/aruma/internal/services/documents/webchannel"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// A scripted client speaks WebChannel VER=8 as closure-library's WebChannelBase does for the web
// SDK (webchannel_connection.ts): the handshake with headers= and the first request, forward
// POSTs, the backchannel streamed or long polled, AID, noop, terminate.

const (
	db   = "projects/" + apitest.Project + "/databases/(default)"
	root = db + "/documents"
)

type harness struct {
	env  *apitest.Env
	wc   *webchannel.Handler
	url  string
	text *slog.Logger
}

type options struct {
	grpc   docgrpc.Limits
	limits webchannel.Limits
	timing webchannel.Timing
	keys   webchannel.Keys
	rules  apitest.Options
}

func newHarness(t *testing.T, o options) *harness {
	t.Helper()
	env := apitest.New(t, o.rules)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	streams := docgrpc.New(docgrpc.Options{Service: env.Service, Limits: o.grpc, Metrics: docgrpc.NewMetrics(), Logger: log})
	wc := webchannel.New(webchannel.Options{Streams: streams, Keys: o.keys, Limits: o.limits, Timing: o.timing, Logger: log})
	// As internal/server mounts the surface: the common middleware, then REST's CORS.
	h := httpx.CommonWithLimit(rest.New(rest.Options{Service: env.Service, Channel: wc, Logger: log}), log, nil, 17<<20)
	ts := httptest.NewUnstartedServer(h)
	ts.Config.ReadTimeout, ts.Config.WriteTimeout = 60*time.Second, 60*time.Second
	ts.Start()
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})
	return &harness{env: env, wc: wc, url: ts.URL + webchannel.ListenPath}
}

func (x *harness) commit(t *testing.T, name string, n int64) {
	t.Helper()
	_, err := x.env.Service.Commit(t.Context(), api.Call{Authorization: "Bearer " + x.env.JWT(t), Transport: authn.Transport{Peer: netip.MustParseAddr("127.0.0.1")}}, &pb.CommitRequest{
		Database: db, Writes: []*pb.Write{{Operation: &pb.Write_Update{Update: &pb.Document{Name: root + "/" + name,
			Fields: map[string]*pb.Value{"n": {ValueType: &pb.Value_IntegerValue{IntegerValue: n}}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
}

func addDoc(id int32, paths ...string) string {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = root + "/" + p
	}
	b, _ := protojson.Marshal(&pb.ListenRequest{Database: db, TargetChange: &pb.ListenRequest_AddTarget{AddTarget: &pb.Target{
		TargetId: id, TargetType: &pb.Target_Documents{Documents: &pb.Target_DocumentsTarget{Documents: names}}}}})
	return string(b)
}

func removeTarget(id int32) string {
	b, _ := protojson.Marshal(&pb.ListenRequest{Database: db, TargetChange: &pb.ListenRequest_RemoveTarget{RemoveTarget: id}})
	return string(b)
}

// client is one session, as WebChannelBase keeps it.
type client struct {
	t    *testing.T
	url  string
	sid  string
	gsid string
	rid  int
	ofs  int
	aid  int
}

// encodeHeaders is goog.net.rpc.HttpCors.generateEncodedHttpHeadersOverwriteParam's text.
func encodeHeaders(h map[string]string) string {
	var b strings.Builder
	for k, v := range h {
		b.WriteString(k + ":" + v + "\r\n")
	}
	return b.String()
}

func (c *client) body(msgs []string) string {
	v := []string{"count=" + strconv.Itoa(len(msgs))}
	if len(msgs) > 0 {
		v = append(v, "ofs="+strconv.Itoa(c.ofs))
	}
	for i, m := range msgs {
		v = append(v, "req"+strconv.Itoa(i)+"___data__="+url.QueryEscape(m))
	}
	c.ofs += len(msgs)
	return strings.Join(v, "&")
}

func (c *client) query(extra ...string) string {
	q := url.Values{"VER": {"8"}, "database": {db}, "zx": {"abc"}, "t": {"1"}}
	if c.sid != "" {
		q.Set("SID", c.sid)
		q.Set("gsessionid", c.gsid)
		q.Set("AID", strconv.Itoa(c.aid))
	}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return c.url + "?" + q.Encode()
}

func post(t *testing.T, u, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, u, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://app.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// handshake opens a session with headers and the first requests; it returns the client and the
// arrays of the handshake's answer.
func handshake(t *testing.T, base string, headers map[string]string, msgs ...string) (*client, []warray) {
	t.Helper()
	c := &client{t: t, url: base, rid: 1}
	body := c.body(msgs)
	if headers != nil {
		body = "headers=" + url.QueryEscape(encodeHeaders(headers)) + "&" + body
	}
	res := post(t, c.query("RID", "1", "CVER", "22", "X-HTTP-Session-Id", "gsessionid"), body)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("handshake: %d %s", res.StatusCode, b)
	}
	if res.Header.Get("Access-Control-Allow-Origin") != "https://app.example" || res.Header.Get("Access-Control-Allow-Credentials") != "true" ||
		res.Header.Get("Access-Control-Expose-Headers") != "x-client-wire-protocol,x-http-session-id" {
		t.Errorf("handshake CORS: %v", res.Header)
	}
	c.gsid = res.Header.Get("X-HTTP-Session-Id")
	as := readArrays(t, bufio.NewReader(res.Body))
	var hello []any
	if err := json.Unmarshal(as[0].payload, &hello); err != nil || as[0].id != 0 || len(hello) < 4 || hello[0] != "c" || hello[3] != 8.0 {
		t.Fatalf("handshake answer: %v %s", err, as[0].payload)
	}
	c.sid = hello[1].(string)
	if c.gsid == "" || c.sid == "" {
		t.Fatalf("no session ids: %q %q", c.sid, c.gsid)
	}
	return c, as
}

// send posts requests on the forward channel and returns its answer.
func (c *client) send(msgs ...string) []int64 {
	c.t.Helper()
	c.rid++
	res := post(c.t, c.query("RID", strconv.Itoa(c.rid)), c.body(msgs))
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		c.t.Fatalf("forward: %d %s", res.StatusCode, b)
	}
	text, err := readChunk(bufio.NewReader(res.Body))
	if err != nil {
		c.t.Fatal(err)
	}
	var state []int64
	if err := json.Unmarshal(text, &state); err != nil || len(state) != 3 {
		c.t.Fatalf("forward answer %s: %v", text, err)
	}
	return state
}

// back opens a backchannel: ci "0" streamed, "1" long polled.
func (c *client) back(ci string, extra ...string) *backReader {
	c.t.Helper()
	ctx, cancel := context.WithCancel(c.t.Context())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.query(append([]string{"RID", "rpc", "CI", ci, "TYPE", "xmlhttp"}, extra...)...), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		c.t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		cancel()
		c.t.Fatalf("backchannel: %d %s", res.StatusCode, b)
	}
	b := &backReader{c: c, res: res, br: bufio.NewReader(res.Body), cancel: cancel, arrays: make(chan warray, 1024)}
	go b.run()
	c.t.Cleanup(b.close)
	return b
}

func (c *client) terminate(method string) {
	c.t.Helper()
	c.rid++
	req, _ := http.NewRequestWithContext(c.t.Context(), method, c.query("RID", strconv.Itoa(c.rid), "TYPE", "terminate"), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	res.Body.Close()
}

type warray struct {
	id      int
	payload json.RawMessage
}

type backReader struct {
	c      *client
	res    *http.Response
	br     *bufio.Reader
	cancel context.CancelFunc
	arrays chan warray
	err    error
}

func (b *backReader) run() {
	defer close(b.arrays)
	for {
		text, err := readChunk(b.br)
		if err != nil {
			b.err = err
			return
		}
		for _, a := range parseArrays(text) {
			b.arrays <- a
		}
	}
}

func (b *backReader) close() {
	b.cancel()
	b.res.Body.Close()
}

// next is the next array, the client acknowledging it as WebChannelBase does (lastArrayId_).
func (b *backReader) next(wait time.Duration) (warray, bool) {
	b.c.t.Helper()
	select {
	case a, ok := <-b.arrays:
		if ok {
			b.c.aid = a.id
		}
		return a, ok
	case <-time.After(wait):
		b.c.t.Fatalf("no array within %v", wait)
		return warray{}, false
	}
}

// response reads the next message, skipping noops, as a ListenResponse or an error status.
func (b *backReader) response(wait time.Duration) (*pb.ListenResponse, string) {
	b.c.t.Helper()
	for {
		a, ok := b.next(wait)
		if !ok {
			b.c.t.Fatalf("backchannel ended: %v", b.err)
		}
		if string(a.payload) == `["noop"]` || string(a.payload) == `["close"]` {
			continue
		}
		return decode(b.c.t, a.payload)
	}
}

func decode(t *testing.T, payload json.RawMessage) (*pb.ListenResponse, string) {
	t.Helper()
	// A stream's status: {"__sm__":{"status":[[{"error":…}]]}}, as the transport hands the SDK
	// data = [[{"error":…}]].
	var sm struct {
		SM *struct {
			Status [][]json.RawMessage `json:"status"`
		} `json:"__sm__"`
	}
	if json.Unmarshal(payload, &sm) == nil && sm.SM != nil {
		if len(sm.SM.Status) != 1 || len(sm.SM.Status[0]) != 1 {
			t.Fatalf("status %s", payload)
		}
		payload = append(append(json.RawMessage{'['}, sm.SM.Status[0][0]...), ']')
	}
	var data []json.RawMessage
	if err := json.Unmarshal(payload, &data); err != nil || len(data) != 1 {
		t.Fatalf("message %s: %v", payload, err)
	}
	var e struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(data[0], &e) == nil && e.Error != nil {
		return nil, e.Error.Status + " " + e.Error.Message
	}
	r := new(pb.ListenResponse)
	if err := protojson.Unmarshal(data[0], r); err != nil {
		t.Fatalf("ListenResponse %s: %v", data[0], err)
	}
	return r, ""
}

// readChunk reads one chunk: its length in UTF-16 code units, a newline, the text.
func readChunk(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSuffix(line, "\n"))
	if err != nil {
		return nil, err
	}
	var text []byte
	for units := 0; units < n; {
		r, _, err := br.ReadRune()
		if err != nil {
			return nil, err
		}
		text = append(text, string(r)...)
		units += len(utf16.Encode([]rune{r}))
	}
	return text, nil
}

func parseArrays(text []byte) []warray {
	var raw [][]json.RawMessage
	if err := json.Unmarshal(text, &raw); err != nil {
		panic(string(text))
	}
	out := make([]warray, len(raw))
	for i, a := range raw {
		id, _ := strconv.Atoi(string(a[0]))
		out[i] = warray{id: id, payload: a[1]}
	}
	return out
}

func readArrays(t *testing.T, br *bufio.Reader) []warray {
	t.Helper()
	text, err := readChunk(br)
	if err != nil {
		t.Fatal(err)
	}
	return parseArrays(text)
}

// shape names a response as the gRPC tests do.
func shape(r *pb.ListenResponse) string {
	switch {
	case r.GetTargetChange() != nil:
		tc := r.GetTargetChange()
		if len(tc.TargetIds) == 0 {
			return "global"
		}
		s := tc.TargetChangeType.String() + "[" + strconv.Itoa(int(tc.TargetIds[0])) + "]"
		if tc.Cause != nil {
			s += " " + strconv.Itoa(int(tc.Cause.Code))
		}
		return s
	case r.GetDocumentChange() != nil:
		return "change " + strings.TrimPrefix(r.GetDocumentChange().GetDocument().GetName(), root+"/")
	case r.GetDocumentDelete() != nil:
		return "delete " + strings.TrimPrefix(r.GetDocumentDelete().GetDocument(), root+"/")
	}
	return "other"
}

func (b *backReader) want(t *testing.T, shapes ...string) {
	t.Helper()
	for _, want := range shapes {
		r, status := b.response(10 * time.Second)
		if status != "" {
			t.Fatalf("want %s, got status %s", want, status)
		}
		if got := shape(r); got != want {
			t.Fatalf("want %s, got %s", want, got)
		}
	}
}

func (b *backReader) wantStatus(t *testing.T, prefix string) {
	t.Helper()
	r, status := b.response(10 * time.Second)
	if !strings.HasPrefix(status, prefix) {
		t.Fatalf("want status %s, got %q %v", prefix, status, r)
	}
}

func admin(t *testing.T, x *harness) map[string]string {
	return map[string]string{"Authorization": "Bearer " + x.env.JWT(t), "X-Goog-Api-Client": "gl-js/ fire/12", "Content-Type": "text/plain"}
}

func TestListenStreamed(t *testing.T) {
	x := newHarness(t, options{})
	x.commit(t, "aruma-ref-wc/a", 1)
	c, as := handshake(t, x.url, admin(t, x), addDoc(1002, "aruma-ref-wc/a"))
	if len(as) != 1 {
		t.Fatalf("handshake arrays: %d", len(as))
	}
	b := c.back("0")
	b.want(t, "ADD[1002]", "change aruma-ref-wc/a", "CURRENT[1002]", "global")
	x.commit(t, "aruma-ref-wc/a", 2)
	b.want(t, "change aruma-ref-wc/a", "global")

	// A second target on the forward channel, then its removal.
	state := c.send(addDoc(1004, "aruma-ref-wc/missing"))
	if state[0] != 1 {
		t.Errorf("forward answer %v: the backchannel is attached", state)
	}
	b.want(t, "ADD[1004]", "delete aruma-ref-wc/missing", "CURRENT[1004]", "global")
	c.send(removeTarget(1004))
	b.want(t, "REMOVE[1004]")
	// The forward answer counts the arrays sent, and production's 7 bytes.
	if state := c.send(); state[1] != int64(c.aid) || state[2] != 7 {
		t.Errorf("forward answer %v, last array %d", state, c.aid)
	}

	c.terminate(http.MethodPost)
	if _, ok := <-b.arrays; ok {
		t.Error("the backchannel goes on after terminate")
	}
	res := post(t, c.query("RID", "99"), c.body(nil))
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest || strings.Index(string(body), "Unknown SID") <= 0 {
		t.Errorf("after terminate: %d %s", res.StatusCode, body)
	}
	if n := x.wc.Sessions(); n != 0 {
		t.Errorf("%d sessions after terminate", n)
	}
}

// A backchannel that broke is sent again from the client's AID; arrays acknowledged are not.
func TestBackchannelResend(t *testing.T) {
	x := newHarness(t, options{})
	x.commit(t, "aruma-ref-wc/r", 1)
	c, _ := handshake(t, x.url, admin(t, x), addDoc(1, "aruma-ref-wc/r"))
	b := c.back("0")
	b.want(t, "ADD[1]", "change aruma-ref-wc/r", "CURRENT[1]", "global")
	last := c.aid
	b.close()

	c.aid = 0 // the client lost everything after the handshake
	b2 := c.back("0")
	b2.want(t, "ADD[1]", "change aruma-ref-wc/r", "CURRENT[1]", "global")
	if c.aid != last {
		t.Errorf("resent up to %d, sent %d", c.aid, last)
	}
	b2.close()
	b3 := c.back("0")
	x.commit(t, "aruma-ref-wc/r", 2)
	b3.want(t, "change aruma-ref-wc/r", "global")
}

func TestListenLongPoll(t *testing.T) {
	x := newHarness(t, options{timing: webchannel.Timing{LongPoll: 200 * time.Millisecond}})
	x.commit(t, "aruma-ref-wc/p", 1)
	c, _ := handshake(t, x.url, admin(t, x), addDoc(7, "aruma-ref-wc/p"))
	var got []string
	for len(got) < 4 {
		b := c.back("1")
		n := 0
		for {
			a, ok := <-b.arrays
			if !ok {
				break
			}
			c.aid = a.id
			n++
			if string(a.payload) == `["noop"]` {
				continue
			}
			r, status := decode(t, a.payload)
			if status != "" {
				t.Fatal(status)
			}
			got = append(got, shape(r))
		}
		if n == 0 {
			t.Fatal("a long poll answered nothing")
		}
	}
	if strings.Join(got, ",") != "ADD[7],change aruma-ref-wc/p,CURRENT[7],global" {
		t.Errorf("long polled %v", got)
	}
	// Nothing to send: the poll is held, then answered with a noop.
	start := time.Now()
	b := c.back("1", "TO", "100")
	var idle []string
	for a := range b.arrays {
		idle = append(idle, string(a.payload))
	}
	if strings.Join(idle, ",") != `["noop"],["noop"]` || time.Since(start) < 90*time.Millisecond {
		t.Errorf("idle poll: %v after %v", idle, time.Since(start))
	}
}

func TestNoop(t *testing.T) {
	x := newHarness(t, options{timing: webchannel.Timing{Noop: 100 * time.Millisecond}})
	c, _ := handshake(t, x.url, admin(t, x), addDoc(1, "aruma-ref-wc/n"))
	b := c.back("0")
	b.want(t, "ADD[1]", "delete aruma-ref-wc/n", "CURRENT[1]", "global")
	a, _ := b.next(5 * time.Second)
	if string(a.payload) != `["noop"]` {
		t.Errorf("idle backchannel: %s", a.payload)
	}
}

// The token in headers= is verified as on gRPC, and the rules judge each target as on gRPC.
func TestIdentity(t *testing.T) {
	x := newHarness(t, options{})
	c, _ := handshake(t, x.url, map[string]string{"Authorization": "Bearer not-a-token"}, addDoc(1, "aruma-ref-wc/i"))
	c.back("0").wantStatus(t, "UNAUTHENTICATED ")

	x.env.PublishFirestoreRules(t, `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /aruma-ref-wc-pub/{doc} { allow read: if request.auth != null; }
  }
}`)
	user := map[string]string{"Authorization": "Bearer " + apitest.UserToken(t, "u1", nil)}
	c, _ = handshake(t, x.url, user, addDoc(1, "aruma-ref-wc-priv/a"))
	b := c.back("0")
	b.want(t, "ADD[1]", "REMOVE[1] 7")
	c.send(addDoc(2, "aruma-ref-wc-pub/a"))
	b.want(t, "ADD[2]", "delete aruma-ref-wc-pub/a", "CURRENT[2]", "global")

	// No token: the rules see no auth.
	c, _ = handshake(t, x.url, nil, addDoc(3, "aruma-ref-wc-pub/a"))
	c.back("0").want(t, "ADD[3]", "REMOVE[3] 7")
}

type fakeKeys map[string]tcore.KeyProject

func (k fakeKeys) ProjectForKey(_ context.Context, key string) (tcore.KeyProject, error) {
	if p, ok := k[key]; ok {
		return p, nil
	}
	return tcore.KeyProject{}, errors.New("unknown key")
}

func TestAPIKey(t *testing.T) {
	x := newHarness(t, options{keys: fakeKeys{"good": {ID: apitest.Project}, "other": {ID: apitest.OtherProject}}})
	h := admin(t, x)
	h["x-goog-api-key"] = "good"
	c, _ := handshake(t, x.url, h, addDoc(1, "aruma-ref-wc/k"))
	c.back("0").want(t, "ADD[1]")
	for key, want := range map[string]string{"bad": "400 API key not valid. Please pass a valid API key.", "other": "403 Missing or insufficient permissions."} {
		h["x-goog-api-key"] = key
		res := post(t, x.url+"?VER=8&RID=1&database="+url.QueryEscape(db), "headers="+url.QueryEscape(encodeHeaders(h))+"&count=0")
		var e struct{ Error struct{ Message string } }
		_ = json.NewDecoder(res.Body).Decode(&e)
		res.Body.Close()
		if got := strconv.Itoa(res.StatusCode) + " " + e.Error.Message; got != want {
			t.Errorf("key %s: %s", key, got)
		}
	}
}

func TestLimits(t *testing.T) {
	x := newHarness(t, options{
		grpc:   docgrpc.Limits{StreamFirstMessage: 300 * time.Millisecond, StreamWriteStall: 300 * time.Millisecond, StreamOutboxBytes: 1},
		limits: webchannel.Limits{SessionsPerIP: 2, SessionBytes: 1},
		timing: webchannel.Timing{Expiry: 4 * time.Second},
	})
	// No first request within stream_first_message.
	c1, _ := handshake(t, x.url, admin(t, x))
	c1.back("0").wantStatus(t, "DEADLINE_EXCEEDED ")

	// A client that never reads: one response is held past session_bytes, then the stream ends
	// RESOURCE_EXHAUSTED once stream_write_stall passed, as on gRPC.
	x.commit(t, "aruma-ref-wc/l", 1)
	c2, _ := handshake(t, x.url, admin(t, x), addDoc(1, "aruma-ref-wc/l"))
	// The stall, then the cut of the send in progress (grpc's cutGrace).
	time.Sleep(2 * time.Second)
	b := c2.back("0")
	b.want(t, "ADD[1]")
	b.wantStatus(t, "RESOURCE_EXHAUSTED ")

	// streams_per_ip: the handshake's answer carries the refusal.
	_, as := handshake(t, x.url, admin(t, x), addDoc(1, "aruma-ref-wc/l"))
	if len(as) != 3 || string(as[2].payload) != `["close"]` {
		t.Fatalf("refused handshake: %d arrays", len(as))
	}
	if _, status := decode(t, as[1].payload); !strings.HasPrefix(status, "RESOURCE_EXHAUSTED ") {
		t.Errorf("refused handshake: %s", status)
	}

	// Sessions without a backchannel expire.
	deadline := time.Now().Add(10 * time.Second)
	for x.wc.Sessions() > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := x.wc.Sessions(); n != 0 {
		t.Errorf("%d sessions left", n)
	}
}

func TestProtocolErrors(t *testing.T) {
	x := newHarness(t, options{})
	for _, tc := range []struct{ name, query, body string }{
		{"version", "?VER=7&RID=1", "count=0"},
		{"too many maps", "?VER=8&RID=1", "count=1001&ofs=0"},
		{"bad map", "?VER=8&RID=1", "count=1&ofs=0&req0_type=_badmap"},
		{"unknown SID", "?VER=8&RID=2&SID=nope&gsessionid=x&AID=0", "count=0"},
	} {
		res := post(t, x.url+tc.query, tc.body)
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", tc.name, res.StatusCode, b)
		}
	}
	// A message that is not a ListenRequest ends the stream with the binding's error.
	c, _ := handshake(t, x.url, admin(t, x), `{"database":"`+db+`","addTarget":{"targetId":"x"}}`)
	c.back("0").wantStatus(t, "INVALID_ARGUMENT ")

	// The preflight of a channel path, as production's (f6-cors.json).
	req, _ := http.NewRequest(http.MethodOptions, x.url, nil)
	req.Header.Set("Origin", "https://app.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type,authorization")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Access-Control-Allow-Origin") != "https://app.example" ||
		res.Header.Get("Access-Control-Allow-Credentials") != "true" || res.Header.Get("Access-Control-Allow-Methods") != "GET,POST" ||
		res.Header.Get("Access-Control-Allow-Headers") != "content-type,authorization" || res.Header.Get("Access-Control-Max-Age") != "" {
		t.Errorf("preflight: %d %v", res.StatusCode, res.Header)
	}

	// A request without a version is production's empty 200; it sets no cookie, and no answer
	// names the wire protocol (parity-exceptions.json webchannel-probe-cookie, -wire-protocol).
	res = post(t, x.url+"?RID=30000&zx=a", "count=0")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || len(b) != 0 || res.Header.Get("Set-Cookie") != "" || res.Header.Get("X-HTTP-Session-Id") != "" ||
		res.Header.Get("X-Client-Wire-Protocol") != "" {
		t.Errorf("no version: %d %v %q", res.StatusCode, res.Header, b)
	}
}

// writeReq is a WriteRequest in proto3 JSON: the handshake when token is empty, else a batch of
// an update per document carrying the stream's last token.
func writeReq(token string, docs ...string) string {
	r := &pb.WriteRequest{Database: db}
	if token != "" {
		r = new(pb.WriteRequest)
		if err := protojson.Unmarshal([]byte(`{"streamToken":"`+token+`"}`), r); err != nil {
			panic(err)
		}
	}
	for _, d := range docs {
		r.Writes = append(r.Writes, &pb.Write{Operation: &pb.Write_Update{Update: &pb.Document{Name: root + "/" + d,
			Fields: map[string]*pb.Value{"n": {ValueType: &pb.Value_IntegerValue{IntegerValue: 1}}}}}})
	}
	b, _ := protojson.Marshal(r)
	return string(b)
}

// writeResponse reads the next message of a Write session, skipping noops, as a JSON object; nil
// for a payload that is not a message, such as a status. The array is returned too.
func (b *backReader) writeResponse(wait time.Duration) (map[string]any, warray) {
	b.c.t.Helper()
	for {
		a, ok := b.next(wait)
		if !ok {
			b.c.t.Fatalf("backchannel ended: %v", b.err)
		}
		if string(a.payload) == `["noop"]` {
			continue
		}
		var data []map[string]any
		if json.Unmarshal(a.payload, &data) != nil || len(data) != 1 {
			return nil, a
		}
		return data[0], a
	}
}

// Write over WebChannel is gRPC's Write stream: the handshake's stream id and token, one batch per
// request carrying the last token, answered with its results and commit time; a batch the rules
// deny ends the session with the status and a close, as production does
// (f6-webchannel-write.json, f6-webchannel-errors.json).
func TestWrite(t *testing.T) {
	x := newHarness(t, options{})
	wurl := strings.Replace(x.url, webchannel.ListenPath, webchannel.WritePath, 1)
	c, _ := handshake(t, wurl, admin(t, x), writeReq(""))
	b := c.back("0")
	hello, _ := b.writeResponse(10 * time.Second)
	token, _ := hello["streamToken"].(string)
	if hello["streamId"] != "0" || token == "" || len(hello) != 2 {
		t.Fatalf("handshake response %v", hello)
	}
	for i, docs := range [][]string{{"aruma-ref-wc-w/a"}, {"aruma-ref-wc-w/b", "aruma-ref-wc-w/c"}} {
		c.send(writeReq(token, docs...))
		res, a := b.writeResponse(10 * time.Second)
		results, _ := res["writeResults"].([]any)
		if res["commitTime"] == nil || res["streamToken"] == nil || res["streamToken"] == token || len(results) != len(docs) {
			t.Fatalf("batch %d: %s", i, a.payload)
		}
		token = res["streamToken"].(string)
	}

	// A user the rules deny: the status, then the close, in production's form.
	x.env.PublishFirestoreRules(t, `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /aruma-ref-wc-w/{doc} { allow read; }
  }
}`)
	user := map[string]string{"Authorization": "Bearer " + apitest.UserToken(t, "u1", nil)}
	c, _ = handshake(t, wurl, user, writeReq(""))
	b = c.back("0")
	hello, _ = b.writeResponse(10 * time.Second)
	c.send(writeReq(hello["streamToken"].(string), "aruma-ref-wc-w/d"))
	_, a := b.writeResponse(10 * time.Second)
	if want := `{"__sm__":{"status":[[{"error":{"code":403,"message":"Missing or insufficient permissions.","status":"PERMISSION_DENIED"}}]]}}`; string(a.payload) != want {
		t.Errorf("denied batch: %s", a.payload)
	}
	if a, _ := b.next(5 * time.Second); string(a.payload) != `["close"]` {
		t.Errorf("after the status: %s", a.payload)
	}
}
