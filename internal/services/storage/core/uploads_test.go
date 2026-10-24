package core

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"io"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// The upload session's state machine (ADR-022 §6, §7).

func (e *env) start(b Bucket, p Principal, sp UploadSpec) string {
	e.t.Helper()
	id, err := e.s.StartUpload(context.Background(), b, p, sp)
	if err != nil {
		e.t.Fatalf("start %s: %v", sp.Name, err)
	}
	return id
}

// chunk sends data at offset, with its length, finalizing when asked.
func (e *env) chunk(b Bucket, name, id string, offset int64, data string, finalize bool) (UploadState, error) {
	return e.s.WriteUpload(context.Background(), b, name, id, nil,
		Chunk{Offset: offset, Body: strings.NewReader(data), Length: int64(len(data)), Finalize: finalize, Total: -1})
}

func TestUploadOffsets(t *testing.T) {
	e := newEnv(t)
	id := e.start(e.a, user("u1"), spec("a.txt", SurfaceFirebase))
	steps := []struct {
		name     string
		offset   int64
		data     string
		received int64
		err      error
	}{
		{"append", 0, "hello", 5, nil},
		{"append again", 5, " wor", 9, nil},
		{"an overlap is ignored and the rest appended", 6, "world", 11, nil},
		{"a full duplicate", 0, "hello", 11, nil},
		{"a gap", 20, "x", 11, &OffsetError{Received: 11}},
		{"an empty chunk at received", 11, "", 11, nil},
	}
	for _, st := range steps {
		got, err := e.chunk(e.a, "a.txt", id, st.offset, st.data, false)
		var oe *OffsetError
		switch {
		case st.err == nil && err != nil, st.err != nil && (!errors.As(err, &oe) || oe.Received != 11):
			t.Fatalf("%s: %v", st.name, err)
		case got.Status != UploadActive || got.Received != st.received:
			t.Fatalf("%s: %+v, want %d received", st.name, got, st.received)
		}
	}
	if _, err := e.s.WriteUpload(context.Background(), e.a, "a.txt", id, nil, Chunk{Offset: -1, Total: -1, Length: -1}); err == nil {
		t.Error("a negative offset is accepted")
	}
	got, err := e.chunk(e.a, "a.txt", id, 11, "!", true)
	if err != nil || got.Status != UploadFinal || got.Object == nil || got.Object.Size != 12 {
		t.Fatalf("finalize: %+v, %v", got, err)
	}
	if e.read(e.a, "a.txt") != "hello world!" {
		t.Errorf("stored %q", e.read(e.a, "a.txt"))
	}
	e.checkUsage(projectA)
}

// A finalize repeated, a query, and a chunk after the end answer with the object the first
// reply showed, even once it was overwritten.
func TestUploadFinalReplays(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.start(e.a, user("u1"), spec("a.txt", SurfaceFirebase))
	first, err := e.chunk(e.a, "a.txt", id, 0, "data", true)
	if err != nil || first.Object == nil || len(first.Object.DownloadTokens) != 1 {
		t.Fatalf("finalize: %+v, %v", first, err)
	}
	e.insert(e.a, "a.txt", "overwritten")
	for _, f := range []func() (UploadState, error){
		func() (UploadState, error) { return e.chunk(e.a, "a.txt", id, 0, "data", true) },
		func() (UploadState, error) { return e.chunk(e.a, "a.txt", id, 4, "more", false) },
		func() (UploadState, error) { return e.s.QueryUpload(ctx, e.a, "a.txt", id) },
	} {
		got, err := f()
		if err != nil || got.Status != UploadFinal || got.Object == nil || got.Object.Generation != first.Object.Generation ||
			got.Object.Size != 4 || got.Object.DownloadTokens[0] != first.Object.DownloadTokens[0] || got.Object.Bucket != e.a.Name {
			t.Errorf("a repeat: %+v, %v", got, err)
		}
	}
	if _, err := e.s.CancelUpload(ctx, e.a, "a.txt", id); !errors.Is(err, ErrUploadFinal) {
		t.Errorf("cancel after the end: %v", err)
	}
	// The snapshot keeps its tokens sealed.
	if n := e.count(`SELECT count(*) FROM storage.upload_sessions WHERE result_object::text LIKE '%' || $1 || '%'`, first.Object.DownloadTokens[0]); n != 0 {
		t.Error("the session keeps a download token in the clear")
	}
}

// Every terminal refusal makes the session final, is replayed on a repeat and on a query, and
// gives the session's files and reservation back.
func TestUploadTerminalRefusals(t *testing.T) {
	ctx := context.Background()
	md5Of := func(s string) *[16]byte { v := md5.Sum([]byte(s)); return &v }
	wrong := uint32(7)
	cases := []struct {
		name  string
		sp    UploadSpec
		p     Principal
		hook  func(context.Context, Bucket, Principal, ruleOp) error
		setup func(e *env)
		chunk Chunk
		want  error
	}{
		{"rules", spec("a", SurfaceFirebase), user("u1"),
			func(_ context.Context, _ Bucket, _ Principal, op ruleOp) error { return ErrPermissionDenied }, nil,
			Chunk{Length: 3, Finalize: true, Total: -1}, ErrPermissionDenied},
		{"declared size", UploadSpec{Name: "a", Surface: SurfaceFirebase, Declared: 4}, user("u1"), nil, nil,
			Chunk{Length: 3, Finalize: true, Total: -1}, ErrSizeMismatch},
		{"past the declared size", UploadSpec{Name: "a", Surface: SurfaceFirebase, Declared: 2}, user("u1"), nil, nil,
			Chunk{Length: 3, Total: -1}, ErrSizeMismatch},
		{"md5Hash on /v0", UploadSpec{Name: "a", Surface: SurfaceFirebase, Declared: -1, Want: Checksums{MD5: md5Of("xyz")}}, user("u1"), nil, nil,
			Chunk{Length: 3, Finalize: true, Total: -1}, ErrChecksumMismatch},
		{"crc32c on the last request", spec("a", SurfaceGCS), admin(), nil, nil,
			Chunk{Length: 3, Finalize: true, Total: 3, Want: Checksums{CRC32C: &wrong}}, ErrChecksumMismatch},
		{"claims that contradict", UploadSpec{Name: "a", Surface: SurfaceGCS, Declared: -1, Want: Checksums{MD5: md5Of("abc")}}, admin(), nil, nil,
			Chunk{Length: 3, Finalize: true, Total: 3, Want: Checksums{MD5: md5Of("abd")}}, ErrChecksumMismatch},
		{"a disabled user", spec("a", SurfaceFirebase), user("u1"), nil, func(e *env) { e.users.disable("u1") },
			Chunk{Length: 3, Finalize: true, Total: -1}, ErrUnauthenticated},
		{"a total other than declared", UploadSpec{Name: "a", Surface: SurfaceGCS, Declared: 3}, admin(), nil, nil,
			Chunk{Length: 3, Total: 9}, ErrSizeMismatch},
		{"over object_bytes", spec("a", SurfaceGCS), admin(), nil, func(e *env) { e.s.limits.ObjectBytes = 2 },
			Chunk{Length: -1, Total: -1}, ErrTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if c.hook != nil {
				e.s.authorizeHook = c.hook
			}
			id := e.start(e.a, c.p, c.sp)
			if c.setup != nil {
				c.setup(e)
			}
			ch := c.chunk
			ch.Body = strings.NewReader("abc")
			got, err := e.s.WriteUpload(ctx, e.a, "a", id, nil, ch)
			if !errors.Is(err, c.want) || got.Status != UploadFinal || got.Err != c.want {
				t.Fatalf("WriteUpload = %+v, %v; want final %v", got, err, c.want)
			}
			for _, f := range []func() (UploadState, error){
				func() (UploadState, error) {
					return e.s.WriteUpload(ctx, e.a, "a", id, nil, Chunk{Offset: 0, Finalize: true, Length: 0, Total: -1})
				},
				func() (UploadState, error) { return e.s.QueryUpload(ctx, e.a, "a", id) },
			} {
				if got, err := f(); err != c.want || got.Status != UploadFinal || got.Err != c.want {
					t.Errorf("a repeat: %+v, %v", got, err)
				}
			}
			if e.count(`SELECT count(*) FROM storage.blob_gc`) != 1 || e.count(`SELECT count(*) FROM storage.objects`) != 0 {
				t.Error("the session's staging file is not queued, or an object was made")
			}
			e.checkUsage(projectA)
		})
	}
}

// 503 is never final: an overloaded rules engine and an unreachable Auth leave the session
// active, and the next finalize succeeds.
func TestUploadTransientRefusals(t *testing.T) {
	e := newEnv(t)
	id := e.start(e.a, user("u1"), spec("a", SurfaceFirebase))
	if _, err := e.chunk(e.a, "a", id, 0, "abc", false); err != nil {
		t.Fatal(err)
	}
	e.s.authorizeHook = func(context.Context, Bucket, Principal, ruleOp) error { return ErrOverloaded }
	got, err := e.chunk(e.a, "a", id, 3, "", true)
	if !errors.Is(err, ErrOverloaded) || got.Status != UploadActive || got.Received != 3 {
		t.Fatalf("overloaded: %+v, %v", got, err)
	}
	e.s.authorizeHook = nil
	e.s.authorizeHook = func(context.Context, Bucket, Principal, ruleOp) error { return nil }
	e.users.err = errors.New("connection refused")
	if got, err := e.chunk(e.a, "a", id, 3, "", true); !errors.Is(err, ErrUnavailable) || got.Status != UploadActive {
		t.Fatalf("Auth unreachable: %+v, %v", got, err)
	}
	e.users.err = nil
	if got, err := e.chunk(e.a, "a", id, 3, "", true); err != nil || got.Status != UploadFinal {
		t.Fatalf("after the outage: %+v, %v", got, err)
	}
}

// The identity is the start's: a chunk's credential counts for nothing, and the rules see the
// principal the session bound, at finalize.
func TestUploadIdentityIsTheStarts(t *testing.T) {
	e := newEnv(t)
	var seen []Principal
	var op ruleOp
	e.s.authorizeHook = func(_ context.Context, _ Bucket, p Principal, o ruleOp) error {
		seen, op = append(seen, p), o
		return nil
	}
	start := user("alice")
	start.Token["role"] = "editor"
	id := e.start(e.a, start, UploadSpec{Name: "a", Surface: SurfaceFirebase, Declared: -1,
		Attrs: Patch{Fields: map[string]*string{FieldContentType: strPtr("text/plain")}}})
	if len(seen) != 0 {
		t.Fatal("the rules ran at start")
	}
	mallory := user("mallory")
	got, err := e.s.WriteUpload(context.Background(), e.a, "a", id, &mallory,
		Chunk{Body: strings.NewReader("abc"), Length: 3, Finalize: true, Total: -1})
	if err != nil || got.Status != UploadFinal {
		t.Fatal(got, err)
	}
	if len(seen) != 1 || seen[0].UID != "alice" || seen[0].Token["role"] != "editor" || seen[0].AuthTime.Unix() != start.AuthTime.Unix() ||
		op.Method != rules.MethodCreate || op.Incoming.Size != 3 || op.Incoming.ContentType != "text/plain" {
		t.Errorf("the rules saw %+v, %+v", seen, op)
	}
	// Admins bypass the rules; others answer to them, through the hook until S2-E.
	e.s.authorizeHook = func(context.Context, Bucket, Principal, ruleOp) error { return ErrPermissionDenied }
	id = e.start(e.a, admin(), spec("b", SurfaceGCS))
	if got, err := e.chunk(e.a, "b", id, 0, "x", true); err != nil || got.Status != UploadFinal {
		t.Errorf("an admin: %+v, %v", got, err)
	}
	id = e.start(e.a, Principal{Kind: PrincipalAnonymous}, spec("c", SurfaceFirebase))
	if _, err := e.chunk(e.a, "c", id, 0, "x", true); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("an anonymous client the rules refuse: %v", err)
	}
}

// A dashboard session (ADR-022 §15): an hour's life, a declared size, its account checked again
// at finalize, and the upload audited.
func TestUploadConsoleSession(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	console := Principal{Kind: PrincipalAdmin, ConsoleAccount: ownerA}
	if _, err := e.s.StartUpload(ctx, e.a, console, spec("a", SurfaceConsole)); err == nil {
		t.Error("a dashboard session without a declared size")
	}
	if _, err := e.s.StartUpload(ctx, e.a, user("u1"), UploadSpec{Name: "a", Surface: SurfaceConsole, Declared: 3}); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("a dashboard session for a user: %v", err)
	}
	id := e.start(e.a, console, UploadSpec{Name: "a", Surface: SurfaceConsole, Declared: 3})
	if _, err := e.chunk(e.a, "a", id, 0, "ab", false); err != nil {
		t.Fatal(err)
	}
	var expires, created time.Time
	if err := e.pool.QueryRow(ctx, `SELECT expires_at, created_at FROM storage.upload_sessions`).Scan(&expires, &created); err != nil ||
		expires.Sub(created) != time.Hour {
		t.Errorf("a dashboard session lives %v", expires.Sub(created))
	}
	got, err := e.chunk(e.a, "a", id, 2, "c", true)
	if err != nil || got.Object == nil || len(got.Object.DownloadTokens) != 1 ||
		!strings.HasPrefix(got.Object.ContentDisposition, "inline;") || len(e.projects.audits) != 1 {
		t.Fatalf("finalize: %+v, %v, audits %q", got, err, e.projects.audits)
	}
	// The account no longer owns the project: 403 and final.
	id = e.start(e.a, console, UploadSpec{Name: "b", Surface: SurfaceConsole, Declared: 1})
	e.projects.setOwner("00000000-0000-7000-8000-000000000002")
	if got, err := e.chunk(e.a, "b", id, 0, "x", true); !errors.Is(err, ErrPermissionDenied) || got.Status != UploadFinal {
		t.Errorf("an account removed from the project: %+v, %v", got, err)
	}
}

// The upload id is 128 random bits, only its HMAC is stored, and it answers only on the project
// and the name it was started for.
func TestUploadIDIsACapability(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.start(e.a, Principal{Kind: PrincipalAnonymous}, spec("a.txt", SurfaceFirebase))
	if raw, err := base64.RawURLEncoding.DecodeString(id); err != nil || len(raw) != 16 {
		t.Fatalf("the id %q is not 128 bits: %v", id, err)
	}
	if n := e.count(`SELECT count(*) FROM storage.upload_sessions WHERE id_hash = $1`, e.s.keys.HashToken(id)); n != 1 {
		t.Error("the session is not keyed by the id's HMAC")
	}
	if n := e.count(`SELECT count(*) FROM storage.upload_sessions s WHERE position($1 in s::text) > 0`, id); n != 0 {
		t.Error("the id is stored")
	}
	for _, c := range []struct {
		b    Bucket
		name string
	}{{e.b, "a.txt"}, {e.a, "b.txt"}, {e.a, "a.txt/"}} {
		if _, err := e.chunk(c.b, c.name, id, 0, "x", true); !errors.Is(err, ErrUploadNotFound) {
			t.Errorf("on %s %s: %v", c.b.Name, c.name, err)
		}
		if _, err := e.s.QueryUpload(ctx, c.b, c.name, id); !errors.Is(err, ErrUploadNotFound) {
			t.Errorf("query on %s %s: %v", c.b.Name, c.name, err)
		}
		if _, err := e.s.CancelUpload(ctx, c.b, c.name, id); !errors.Is(err, ErrUploadNotFound) {
			t.Errorf("cancel on %s %s: %v", c.b.Name, c.name, err)
		}
	}
	if _, err := e.chunk(e.a, "a.txt", "nope", 0, "x", false); !errors.Is(err, ErrUploadNotFound) {
		t.Errorf("an unknown id: %v", err)
	}
	if got, err := e.s.QueryUpload(ctx, e.a, "a.txt", id); err != nil || got.Status != UploadActive || got.Received != 0 {
		t.Errorf("the session after the foreign requests: %+v, %v", got, err)
	}
}

func TestUploadCancel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.start(e.a, user("u1"), spec("a", SurfaceFirebase))
	if _, err := e.chunk(e.a, "a", id, 0, "abc", false); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got, err := e.s.CancelUpload(ctx, e.a, "a", id); err != nil || got.Status != UploadCancelled {
			t.Fatalf("cancel: %+v, %v", got, err)
		}
	}
	if got, err := e.chunk(e.a, "a", id, 3, "d", true); !errors.Is(err, ErrUploadCancelled) || got.Status != UploadCancelled {
		t.Errorf("a chunk after the cancel: %+v, %v", got, err)
	}
	if got, err := e.s.QueryUpload(ctx, e.a, "a", id); !errors.Is(err, ErrUploadCancelled) || got.Status != UploadCancelled {
		t.Errorf("a query after the cancel: %+v, %v", got, err)
	}
	if e.count(`SELECT count(*) FROM storage.blob_gc`) != 1 {
		t.Error("the staging file is not queued")
	}
	if _, err := e.s.CancelUpload(ctx, e.a, "a", "nope"); !errors.Is(err, ErrUploadNotFound) {
		t.Errorf("an unknown session: %v", err)
	}
	e.checkUsage(projectA)
}

// Two chunks at once on one session: the lock serialises them, the second finds the bytes the
// first wrote and ignores them, and nothing is written twice.
func TestUploadConcurrentChunks(t *testing.T) {
	e := newEnv(t)
	id := e.start(e.a, user("u1"), spec("a", SurfaceFirebase))
	data := strings.Repeat("0123456789abcdef", 64<<10) // 1 MiB
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			got, err := e.chunk(e.a, "a", id, 0, data, false)
			if err != nil || got.Received != int64(len(data)) {
				t.Errorf("a chunk: %+v, %v", got, err)
			}
		})
	}
	wg.Wait()
	got, err := e.chunk(e.a, "a", id, int64(len(data)), "", true)
	if err != nil || got.Object.Size != int64(len(data)) || e.read(e.a, "a") != data {
		t.Fatalf("finalize: %+v, %v", got, err)
	}
	// And a chunk racing the finalize: it either lands before (and is part of the object) or
	// finds the session final.
	id = e.start(e.a, user("u1"), spec("b", SurfaceFirebase))
	if _, err := e.chunk(e.a, "b", id, 0, "ab", false); err != nil {
		t.Fatal(err)
	}
	wg.Go(func() { _, _ = e.chunk(e.a, "b", id, 2, "", true) })
	wg.Go(func() { _, _ = e.chunk(e.a, "b", id, 2, "cd", false) })
	wg.Wait()
	if got, err := e.s.QueryUpload(context.Background(), e.a, "b", id); err != nil || got.Status != UploadFinal ||
		got.Object.Size != int64(len(e.read(e.a, "b"))) {
		t.Errorf("after the race: %+v, %v", got, err)
	}
	e.checkUsage(projectA)
}

// A body of unknown length reserves 8 MiB at a time and gives back what it did not use; a request
// cut in the middle keeps, and announces, what reached the disk.
func TestUploadUnknownLength(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.start(e.a, user("u1"), spec("a", SurfaceGCS))
	data := bytes.Repeat([]byte("x"), 20<<20)
	got, err := e.s.WriteUpload(ctx, e.a, "a", id, nil, Chunk{Body: bytes.NewReader(data), Length: -1, Total: -1})
	if err != nil || got.Received != int64(len(data)) {
		t.Fatalf("WriteUpload = %+v, %v", got, err)
	}
	if _, reserved, _, _ := e.usage(projectA); reserved != int64(len(data)) {
		t.Errorf("%d reserved for %d bytes received", reserved, len(data))
	}
	cut := errors.New("connection reset")
	body := io.MultiReader(bytes.NewReader(data[:3<<20]), errReader{cut})
	got, err = e.s.WriteUpload(ctx, e.a, "a", id, nil, Chunk{Offset: int64(len(data)), Body: body, Length: -1, Total: -1})
	if !errors.Is(err, cut) || got.Received != 23<<20 {
		t.Fatalf("a cut request: %+v, %v", got, err)
	}
	if q, err := e.s.QueryUpload(ctx, e.a, "a", id); err != nil || q.Received != 23<<20 {
		t.Errorf("query after the cut: %+v, %v", q, err)
	}
	// Node's single request: the rest of the object in one body of unknown length, then final.
	got, err = e.s.WriteUpload(ctx, e.a, "a", id, nil, Chunk{Offset: 23 << 20, Body: strings.NewReader("end"), Length: -1, Total: -1, Finalize: true})
	if err != nil || got.Object.Size != 23<<20+3 {
		t.Fatalf("finalize: %+v, %v", got, err)
	}
	e.checkUsage(projectA)
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// A store that writes other than it read announces nothing: the next request cuts the file back.
func TestUploadStoreMismatch(t *testing.T) {
	e := newEnv(t)
	id := e.start(e.a, user("u1"), spec("a", SurfaceFirebase))
	if _, err := e.chunk(e.a, "a", id, 0, "abc", false); err != nil {
		t.Fatal(err)
	}
	e.store.appendErr = errors.New("no space left on device")
	if got, err := e.chunk(e.a, "a", id, 3, "def", false); err == nil || got.Received != 3 {
		t.Fatalf("a failed append: %+v, %v", got, err)
	}
	e.store.appendErr = nil
	if got, err := e.chunk(e.a, "a", id, 3, "DEF", true); err != nil || got.Object.Size != 6 || e.read(e.a, "a") != "abcDEF" {
		t.Fatalf("after: %+v, %v (%q)", got, err, e.read(e.a, "a"))
	}
	e.checkUsage(projectA)
}

// An empty session expires at start + empty_session_ttl; its first durable byte moves expires_at
// to start + upload_session_ttl.
func TestUploadExpiresAt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.start(e.a, user("u1"), spec("a", SurfaceFirebase))
	life := func() time.Duration {
		var expires, created time.Time
		if err := e.pool.QueryRow(ctx, `SELECT expires_at, created_at FROM storage.upload_sessions`).Scan(&expires, &created); err != nil {
			t.Fatal(err)
		}
		return expires.Sub(created)
	}
	if l := life(); l != 10*time.Minute {
		t.Errorf("an empty session lives %v", l)
	}
	e.clock.add(5 * time.Minute)
	if _, err := e.chunk(e.a, "a", id, 0, "x", false); err != nil {
		t.Fatal(err)
	}
	if l := life(); l != 7*24*time.Hour {
		t.Errorf("a session with a byte lives %v", l)
	}
	e.clock.add(7 * 24 * time.Hour)
	if _, err := e.chunk(e.a, "a", id, 1, "y", false); !errors.Is(err, ErrUploadNotFound) {
		t.Errorf("a chunk after the session's life: %v", err)
	}
	if e.count(`SELECT count(*) FROM storage.upload_sessions`) != 0 || e.count(`SELECT count(*) FROM storage.blob_gc`) != 1 {
		t.Error("the expired session stays, or its file is not queued")
	}
	e.checkUsage(projectA)
}

func TestStartUploadChecks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	long := strings.Repeat("x", 2<<10)
	for _, c := range []struct {
		name string
		sp   UploadSpec
		want error
	}{
		{"a bad name", spec("", SurfaceFirebase), nil},
		{"no surface", spec("a", 0), ErrInvalidArgument},
		{"over object_bytes", UploadSpec{Name: "a", Surface: SurfaceGCS, Declared: 6 << 30}, ErrTooLarge},
		{"a header field too long", UploadSpec{Name: "a", Surface: SurfaceGCS, Declared: -1, Attrs: Patch{Fields: map[string]*string{FieldContentType: &long}}}, nil},
		{"a header field with a line feed", UploadSpec{Name: "a", Surface: SurfaceGCS, Declared: -1, Attrs: Patch{Fields: map[string]*string{FieldCacheControl: strPtr("a\nb")}}}, nil},
		{"an unknown field", UploadSpec{Name: "a", Surface: SurfaceGCS, Declared: -1, Attrs: Patch{Fields: map[string]*string{"size": strPtr("1")}}}, nil},
		{"custom metadata too large", UploadSpec{Name: "a", Surface: SurfaceGCS, Declared: -1, Attrs: Patch{Metadata: map[string]*string{"k": strPtr(strings.Repeat("v", 9<<10))}}}, nil},
		{"the token key in custom metadata", UploadSpec{Name: "a", Surface: SurfaceGCS, Declared: -1, Attrs: Patch{Metadata: map[string]*string{TokensKey: strPtr("t")}}}, nil},
	} {
		_, err := e.s.StartUpload(ctx, e.a, admin(), c.sp)
		var inv *InvalidError
		if (c.want == nil && !errors.As(err, &inv)) || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if e.count(`SELECT count(*) FROM storage.upload_sessions`) != 0 || len(e.store.keys("")) != 0 {
		t.Error("a refused start left a session or a file")
	}
	// An admin's tokens on GCS go with the session, sealed, to the object.
	tokens := []string{"t1", "t2"}
	id := e.start(e.a, admin(), UploadSpec{Name: "a", Surface: SurfaceGCS, Declared: -1, Attrs: Patch{Tokens: &tokens}})
	got, err := e.chunk(e.a, "a", id, 0, "x", true)
	if err != nil || strings.Join(got.Object.DownloadTokens, ",") != "t1,t2" {
		t.Errorf("an admin's tokens: %+v, %v", got, err)
	}
	if e.count(`SELECT count(*) FROM storage.upload_sessions s WHERE position('t2' in s::text) > 0`) != 0 {
		t.Error("an admin's tokens are kept in the clear")
	}
}

func TestLimitAddr(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.7": "192.0.2.7", "::ffff:192.0.2.7": "192.0.2.7", "2001:db8:1:2:3:4:5:6": "2001:db8:1:2::",
	} {
		if got := limitAddr(netip.MustParseAddr(in)); got.String() != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	if limitAddr(netip.Addr{}) != nil {
		t.Error("the zero Addr counts")
	}
}
