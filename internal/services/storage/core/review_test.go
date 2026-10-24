package core

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// The S2-D review's findings, pinned.

// Bad metadata is 400 on every path, never a database error, and the tokens count in
// metadata_bytes as S2-C counts them.
func TestUploadMetadataIsInvalidNotUnavailable(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	bad := string([]byte{0xff, 'x'})
	big := strings.Repeat("t", 4000)
	tokens := []string{big, big}
	for _, c := range []struct {
		name string
		sp   UploadSpec
	}{
		{"a Content-Type not UTF-8", UploadSpec{Name: "a", Surface: SurfaceFirebase, Declared: -1,
			Attrs: Patch{Fields: map[string]*string{FieldContentType: &bad}}}},
		{"a metadata key not UTF-8", UploadSpec{Name: "a", Surface: SurfaceFirebase, Declared: -1,
			Attrs: Patch{Metadata: map[string]*string{bad: strPtr("v")}}}},
		{"a metadata value not UTF-8", UploadSpec{Name: "a", Surface: SurfaceFirebase, Declared: -1,
			Attrs: Patch{Metadata: map[string]*string{"k": &bad}}}},
		{"tokens and metadata over metadata_bytes", UploadSpec{Name: "a", Surface: SurfaceGCS, Declared: -1,
			Attrs: Patch{Tokens: &tokens, Metadata: map[string]*string{"k": strPtr(strings.Repeat("v", 300))}}}},
	} {
		var inv *InvalidError
		if _, err := e.s.Insert(ctx, e.a, asAdmin(), c.sp, strings.NewReader("x"), 1); !errors.As(err, &inv) || errors.Is(err, ErrUnavailable) {
			t.Errorf("%s, Insert: %v", c.name, err)
		}
		if _, err := e.s.StartUpload(ctx, e.a, asAdmin(), c.sp); !errors.As(err, &inv) || errors.Is(err, ErrUnavailable) {
			t.Errorf("%s, StartUpload: %v", c.name, err)
		}
	}
	if e.count(`SELECT count(*) FROM storage.upload_sessions`)+e.count(`SELECT count(*) FROM storage.objects`) != 0 {
		t.Error("bad metadata made a row")
	}
}

// On GCS a precondition that fails at finalize is terminal: kept, replayed, and the staged bytes
// and their reservation go at once.
func TestGCSPreconditionAtFinalizeIsFinal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.insert(e.a, "a", "exists")
	zero := int64(0)
	sp := uploadSpec("a", SurfaceGCS)
	sp.Pre.IfGenerationMatch = &zero
	id := e.start(e.a, asAdmin(), sp)
	got, err := e.chunk(e.a, "a", id, 0, "new", true)
	if !errors.Is(err, ErrPreconditionFailed) || got.Status != UploadFinal || got.Err != ErrPreconditionFailed {
		t.Fatalf("finalize: %+v, %v", got, err)
	}
	for _, f := range []func() (UploadState, error){
		func() (UploadState, error) { return e.chunk(e.a, "a", id, 3, "", true) },
		func() (UploadState, error) { return e.s.QueryUpload(ctx, e.a, "a", id) },
	} {
		if got, err := f(); err != ErrPreconditionFailed || got.Status != UploadFinal {
			t.Errorf("a repeat: %+v, %v", got, err)
		}
	}
	if _, reserved, _, _ := e.usage(projectA); reserved != 0 || e.count(`SELECT count(*) FROM storage.blob_gc`) != 1 {
		t.Errorf("%d bytes still reserved, or the staging file is not queued", reserved)
	}
	if e.read(e.a, "a") != "exists" {
		t.Error("the refused upload changed the object")
	}
	e.checkUsage(projectA)
}

// The caps hold however sessions get there: empty sessions count at start against the address
// and the uid, and a session's first byte is checked again under the project's lock, so starting
// many empty sessions and then sending a byte to each makes no more live sessions than allowed.
func TestSessionCapsAtTheFirstByte(t *testing.T) {
	e := newEnv(t, func(o *Options) {
		o.Limits.UploadSessionsPerIP = 2
		o.Limits.UploadSessionsPerProject = 3
		o.Limits.UploadSessionsPerUser = 2
	})
	ctx := context.Background()
	ip := netip.MustParseAddr("192.0.2.1")
	at := func(addr netip.Addr) UploadSpec {
		sp := uploadSpec("a", SurfaceFirebase)
		sp.IP = addr
		return sp
	}
	anon := Principal{Kind: PrincipalAnonymous}
	e.start(e.a, anon, at(ip))
	e.start(e.a, anon, at(ip))
	if _, err := e.s.StartUpload(ctx, e.a, anon, at(ip)); !errors.Is(err, ErrTooManyUploads) {
		t.Errorf("a third session from one address, empty ones counted: %v", err)
	}
	e.start(e.a, asUser("u1"), at(netip.Addr{}))
	e.start(e.a, asUser("u1"), at(netip.Addr{}))
	if _, err := e.s.StartUpload(ctx, e.a, asUser("u1"), at(netip.Addr{})); !errors.Is(err, ErrTooManyUploads) {
		t.Errorf("a third session of one uid: %v", err)
	}
	// Six sessions from six addresses, all empty, then a byte to each, at once: three become
	// live, the project's cap, and the others end final.
	var ids []string
	for i := range 6 {
		ids = append(ids, e.start(e.a, anon, at(netip.AddrFrom4([4]byte{198, 51, 100, byte(i + 1)}))))
	}
	errs := make(chan error, len(ids))
	for _, id := range ids {
		go func() {
			_, err := e.chunk(e.a, "a", id, 0, "x", false)
			errs <- err
		}()
	}
	refused := 0
	for range ids {
		switch err := <-errs; {
		case err == nil:
		case errors.Is(err, ErrQuotaExceeded):
			refused++
		default:
			t.Error(err)
		}
	}
	if live := e.count(`SELECT count(*) FROM storage.upload_sessions WHERE state = 'active' AND received > 0`); live != 3 || refused != 3 {
		t.Errorf("%d live sessions and %d refused, against a cap of 3", live, refused)
	}
	e.checkUsage(projectA)
}

// Close refuses new writes, waits for those in flight, and only then releases the lock; past
// its wait it keeps the lock and says so.
func TestCloseWaitsForWriters(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.start(e.a, asUser("u1"), uploadSpec("a", SurfaceFirebase))
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := e.s.WriteUpload(ctx, e.a, "a", id, nil, Chunk{Body: pr, Length: -1, Total: -1})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // the chunk is in flight
	closed := make(chan error, 1)
	go func() { closed <- e.s.Close() }()
	time.Sleep(100 * time.Millisecond)
	if _, err := e.s.StartUpload(ctx, e.a, asUser("u1"), uploadSpec("b", SurfaceFirebase)); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a start during Close: %v", err)
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned with a write in flight: %v", err)
	default:
	}
	if e.s.lock == nil {
		t.Fatal("the lock went with a write in flight")
	}
	_, _ = pw.Write([]byte("abc"))
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil || e.s.lock != nil {
		t.Fatalf("Close after the write: %v", err)
	}
	// Past the wait, the lock stays.
	e2 := newEnv(t)
	leave, err := e2.s.enter()
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	old := closeWait
	closeWait = 50 * time.Millisecond
	defer func() { closeWait = old }()
	if err := e2.s.Close(); err == nil || e2.s.lock == nil {
		t.Errorf("Close past its wait: %v, lock %v", err, e2.s.lock)
	}
}
