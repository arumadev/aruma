package grpc_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	"github.com/arumadev/aruma/internal/services/documents/listen"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
)

// The streams' limits, their tokens and their closing (ADR-026 §3, §4): each limit reached is
// told to the client, counted and logged, never a silent drop.

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(apitest.ListenWait)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func wantEnd(t *testing.T, what string, l *apitest.ListenClient, code connect.Code) {
	t.Helper()
	if err := l.Ended(); connect.CodeOf(err) != code {
		t.Errorf("%s: the stream ended with %v, want %v", what, err, code)
	}
}

func randomBytes(t *testing.T, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func removed(l *apitest.ListenClient, id int32) []*pb.ListenResponse {
	return l.Until(func(r *pb.ListenResponse) bool { return apitest.IsTargetChange(r, pb.TargetChange_REMOVE, id) })
}

// A target past targets_per_stream is removed alone with RESOURCE_EXHAUSTED; the stream stays.
func TestListenTargetsPerStream(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{TargetsPerStream: 2})
	tok := x.env.JWT(t)
	x.commit(t, tok, update("tp/a", rankDoc(1)))
	l := apitest.OpenListen(t, x.client, tok)
	l.Add(docTarget(1, "tp/a"))
	l.UntilGlobal()
	l.Add(docTarget(2, "tp/a"))
	l.UntilGlobal()
	l.Add(docTarget(3, "tp/a"))
	wantShapes(t, "a third target", removed(l, 3),
		`REMOVE[3] 8 "The stream has 2 targets, the most this server allows. Remove one first."`)
	l.Remove(1)
	removed(l, 1)
	l.Add(docTarget(4, "tp/a"))
	wantShapes(t, "after a remove", l.UntilGlobal(), "ADD[4]", "change tp/a [4]", "CURRENT[4]", "global")
	if n := x.metrics.Overflows("targets_per_stream"); n != 1 {
		t.Errorf("targets_per_stream overflows: %v", n)
	}
}

// A target whose read goes past snapshot_rows is removed alone with RESOURCE_EXHAUSTED.
func TestListenSnapshotRows(t *testing.T) {
	x := newHarness(t, apitest.Options{Hub: listen.Options{SnapshotRows: 2}}, docgrpc.Limits{})
	tok := x.env.JWT(t)
	x.commit(t, tok, update("sr/a", rankDoc(1)), update("sr/b", rankDoc(2)), update("sr/c", rankDoc(3)))
	l := apitest.OpenListen(t, x.client, tok)
	l.Add(queryTarget(1, byRank("sr", 0, 0, 0)))
	got := shapes(removed(l, 1))
	if last := got[len(got)-1]; !strings.HasPrefix(last, "REMOVE[1] 8 ") {
		t.Errorf("past snapshot_rows: %q", got)
	}
	l.Add(docTarget(2, "sr/a"))
	wantShapes(t, "the stream stays", l.UntilGlobal(), "ADD[2]", "change sr/a [2]", "CURRENT[2]", "global")
	if n := x.metrics.Overflows("snapshot_rows"); n != 1 {
		t.Errorf("snapshot_rows overflows: %v", n)
	}
}

// A Listen stream without a target for stream_idle ends UNAVAILABLE; one with a target does not.
func TestListenIdle(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{StreamIdle: 300 * time.Millisecond})
	tok := x.env.JWT(t)
	l := apitest.OpenListen(t, x.client, tok)
	l.Add(docTarget(1, "li/a"))
	l.UntilGlobal()
	l.Quiet(600 * time.Millisecond)
	l.Remove(1)
	removed(l, 1)
	start := time.Now()
	wantEnd(t, "no target", l, connect.CodeUnavailable)
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("closed after %v", d)
	}
}

// A client that stops reading fills the outbox; when nothing leaves it for stream_write_stall,
// the stream ends, counted as stream_outbox_bytes.
func TestListenOutbox(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{StreamOutboxBytes: 1 << 20, StreamWriteStall: 300 * time.Millisecond})
	tok := x.env.JWT(t)
	slow := firestorepbconnect.NewFirestoreClient(h2c(64<<10), x.url, connect.WithGRPC())
	l := apitest.OpenListen(t, slow, tok)
	l.Add(queryTarget(1, byRank("ob", 0, 0, 0)))
	l.UntilGlobal()
	l.Hold()
	// Random bytes: the responses are compressed.
	for i := range 40 {
		x.commit(t, tok, update(fmt.Sprintf("ob/d%02d", i), map[string]*pb.Value{
			"rank": intValue(1), "pad": {ValueType: &pb.Value_BytesValue{BytesValue: randomBytes(t, 100<<10)}}}))
	}
	waitFor(t, "the outbox's overflow", func() bool { return x.metrics.Overflows("stream_outbox_bytes") == 1 })
	l.Release()
	// Its write was stuck on the client's window: cutting it resets the stream, so the client
	// sees INTERNAL, not the RESOURCE_EXHAUSTED status the server ends it with.
	if err := l.Ended(); err == nil {
		t.Error("a client that did not read: the stream ended OK")
	}
	waitFor(t, "the stream's end", func() bool { return x.h.OpenStreams() == 0 })
}

// Listen and Write count together against streams_per_project and streams_per_ip; one more is
// refused RESOURCE_EXHAUSTED, and a stream that ends frees its place.
func TestStreamsPerProjectAndIP(t *testing.T) {
	for _, c := range []struct {
		name   string
		limits docgrpc.Limits
		msg    string
	}{
		{"streams_per_project", docgrpc.Limits{StreamsPerProject: 2}, "Too many streams are open on this project."},
		{"streams_per_ip", docgrpc.Limits{StreamsPerIP: 2}, "Too many streams are open from this address."},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := newHarness(t, apitest.Options{}, c.limits)
			tok := x.env.JWT(t)
			w, _ := openWrite(t, x, tok)
			l := apitest.OpenListen(t, x.client, tok)
			l.Add(docTarget(1, "pp/a"))
			l.UntilGlobal()

			third := apitest.OpenListen(t, x.client, tok)
			third.Add(docTarget(1, "pp/a"))
			if err := third.Ended(); connect.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), c.msg) {
				t.Errorf("a third stream: %v", err)
			}
			st := x.client.Write(t.Context())
			st.RequestHeader().Set("Authorization", "Bearer "+tok)
			_ = st.Send(&pb.WriteRequest{Database: db})
			if _, err := st.Receive(); connect.CodeOf(err) != connect.CodeResourceExhausted {
				t.Errorf("a third stream, Write: %v", err)
			}
			_ = st.CloseRequest()
			_ = st.CloseResponse()
			if n := x.metrics.Overflows(c.name); n != 2 {
				t.Errorf("%s overflows: %v", c.name, n)
			}

			_ = w.CloseRequest()
			_ = w.CloseResponse()
			waitFor(t, "the write stream's end", func() bool { return x.h.OpenStreams() == 1 })
			again := apitest.OpenListen(t, x.client, tok)
			again.Add(docTarget(1, "pp/a"))
			again.UntilGlobal()
		})
	}
}

// A service account's stream closes UNAUTHENTICATED at its token's exp.
func TestStreamServiceAccountExpiry(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	tok := x.env.ShortJWT(t, 2*time.Second)
	l := apitest.OpenListen(t, x.client, tok)
	l.Add(docTarget(1, "ex/a"))
	l.UntilGlobal()
	w, _ := openWrite(t, x, tok)
	wantEnd(t, "Listen at exp", l, connect.CodeUnauthenticated)
	if _, err := w.Receive(); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("Write at exp: %v", err)
	}
}

// users is an idtoken.Verifier of the end users' test tokens, all of Project's user-1: each
// token's claims expire at exp, or the verification fails with err; gate, when set, holds every
// verification until it is closed.
type users struct {
	mu   sync.Mutex
	exp  time.Time
	err  error
	gate chan struct{}
	in   chan struct{} // told of each verification that waits on gate
}

func (u *users) Verify(ctx context.Context, _ int64, _ string) (*idtoken.Claims, error) {
	u.mu.Lock()
	gate := u.gate
	u.mu.Unlock()
	if gate != nil {
		u.in <- struct{}{}
		<-gate
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.err != nil {
		return nil, u.err
	}
	return &idtoken.Claims{UID: "user-1", AuthTime: time.Now().Add(-time.Minute), Expires: u.exp}, nil
}

func (u *users) set(exp time.Time, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.exp, u.err = exp, err
}

// endUsers is a harness whose end users reach the data (dev_open_rules) and whose tokens u
// verifies, with revocations from rev.
func endUsers(t *testing.T, u *users, rev idtoken.Revocations) *harness {
	t.Helper()
	return newHarnessWith(t, apitest.Options{DevOpenRules: true}, docgrpc.Options{IDTokens: u, Revocations: rev})
}

// An end user's stream closes UNAUTHENTICATED at its token's exp; a token refused at open is
// UNAUTHENTICATED.
func TestStreamEndUserExpiry(t *testing.T) {
	u := &users{exp: time.Now().Add(time.Second)}
	x := endUsers(t, u, idtoken.NewHub(0))
	tok := apitest.IDToken(t)
	l := apitest.OpenListen(t, x.client, tok)
	l.Add(docTarget(1, "eu/a"))
	l.UntilGlobal()
	wantEnd(t, "at exp", l, connect.CodeUnauthenticated)
	if n := x.metrics.Closes("token_expired"); n != 1 {
		t.Errorf("token_expired closes: %v", n)
	}

	u.set(time.Now().Add(time.Hour), idtoken.ErrDisabled)
	refused := apitest.OpenListen(t, x.client, tok)
	refused.Add(docTarget(1, "eu/a"))
	wantEnd(t, "a disabled user's token", refused, connect.CodeUnauthenticated)
}

// A revocation closes the user's open Listen and Write streams at once, and not another user's.
func TestStreamRevocation(t *testing.T) {
	u := &users{exp: time.Now().Add(time.Hour)}
	rev := idtoken.NewHub(0)
	x := endUsers(t, u, rev)
	tok := apitest.IDToken(t)
	l := apitest.OpenListen(t, x.client, tok)
	l.Add(docTarget(1, "rv/a"))
	l.UntilGlobal()
	w, _ := openWrite(t, x, tok)
	if rev.Subscribers(x.env.Number) != 1 {
		t.Fatalf("subscriptions: %d, want one for the project", rev.Subscribers(x.env.Number))
	}

	rev.Publish(idtoken.Revocation{Project: x.env.Number, UID: "user-2", Cause: idtoken.Disabled})
	l.Quiet(200 * time.Millisecond)
	rev.Publish(idtoken.Revocation{Project: x.env.Number, UID: "user-1", Cause: idtoken.Disabled})
	wantEnd(t, "Listen of a revoked user", l, connect.CodeUnauthenticated)
	if _, err := w.Receive(); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("Write of a revoked user: %v", err)
	}
	if n := x.metrics.Closes("token_revoked"); n != 2 {
		t.Errorf("token_revoked closes: %v", n)
	}
	waitFor(t, "the subscription's end", func() bool { return rev.Subscribers(x.env.Number) == 0 })
}

// A revocation that lands while a stream's token is being verified refuses the stream: the hold
// keeps it from being lost between the verification and the stream's registration.
func TestStreamRevocationRacesOpen(t *testing.T) {
	u := &users{exp: time.Now().Add(time.Hour), gate: make(chan struct{}), in: make(chan struct{}, 1)}
	rev := idtoken.NewHub(0)
	x := endUsers(t, u, rev)
	l := apitest.OpenListen(t, x.client, apitest.IDToken(t))
	l.Add(docTarget(1, "rr/a"))
	<-u.in // verifying, after the hold began
	rev.Publish(idtoken.Revocation{Project: x.env.Number, UID: "user-1", Cause: idtoken.TokensRevoked, ValidAfter: time.Now()})
	time.Sleep(50 * time.Millisecond) // delivered to the watch
	close(u.gate)
	wantEnd(t, "revoked as it opened", l, connect.CodeUnauthenticated)
}

// Revocations lost to a subscription that fell behind verify every end user's token again.
func TestStreamRevocationsOverflow(t *testing.T) {
	u := &users{exp: time.Now().Add(time.Hour)}
	rev := idtoken.NewHub(1)
	x := endUsers(t, u, rev)
	l := apitest.OpenListen(t, x.client, apitest.IDToken(t))
	l.Add(docTarget(1, "ro/a"))
	l.UntilGlobal()
	u.set(time.Now().Add(time.Hour), idtoken.ErrRevoked)
	// None names user-1: only the overflow's verification can close the stream.
	deadline := time.Now().Add(apitest.ListenWait)
	for x.metrics.Closes("token_revoked") == 0 && time.Now().Before(deadline) {
		for range 1000 {
			rev.Publish(idtoken.Revocation{Project: x.env.Number, UID: "someone-else", Cause: idtoken.Disabled})
		}
		time.Sleep(time.Millisecond)
	}
	wantEnd(t, "after an overflow", l, connect.CodeUnauthenticated)
}

// The lost instance lock closes every Listen and Write stream UNAVAILABLE and refuses new ones
// until it is taken again.
func TestStreamsInstanceLost(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	tok := x.env.JWT(t)
	l := apitest.OpenListen(t, x.client, tok)
	l.Add(docTarget(1, "il/a"))
	l.UntilGlobal()
	w, _ := openWrite(t, x, tok)
	x.h.InstanceLost()
	wantEnd(t, "Listen", l, connect.CodeUnavailable)
	if _, err := w.Receive(); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("Write: %v", err)
	}
	refused := apitest.OpenListen(t, x.client, tok)
	refused.Add(docTarget(1, "il/a"))
	wantEnd(t, "while lost", refused, connect.CodeUnavailable)
	x.h.InstanceRegained()
	again := apitest.OpenListen(t, x.client, tok)
	again.Add(docTarget(1, "il/a"))
	again.UntilGlobal()
	if n := x.metrics.Closes("instance_lost"); n != 2 {
		t.Errorf("instance_lost closes: %v", n)
	}
}
