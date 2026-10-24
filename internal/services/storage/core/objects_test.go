package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/services/storage/queries"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// S2-C's tests of Stat, Open, Update and Delete on Postgres, with the Admin principal and,
// for the others, authorizeHook in place of the rules (S2-E).

// blobsInMemory is a blob.Store of the bytes the tests put, for Open.
type blobsInMemory struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (m *blobsInMemory) put(key string, b []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[key] = b
}

type memReader struct {
	*bytes.Reader
	size int64
}

func (r memReader) Close() error { return nil }
func (r memReader) Size() int64  { return r.size }

func (m *blobsInMemory) Open(key string) (blob.Reader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return memReader{bytes.NewReader(b), int64(len(b))}, nil
}
func (m *blobsInMemory) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, key)
	return nil
}
func (m *blobsInMemory) Create(string) (blob.Writer, error)         { panic("not in S2-C's tests") }
func (m *blobsInMemory) Promote(string, string) error               { panic("not in S2-C's tests") }
func (m *blobsInMemory) CreateStaging(string) (blob.Staging, error) { panic("not in S2-C's tests") }
func (m *blobsInMemory) OpenStaging(string) (blob.Staging, error)   { panic("not in S2-C's tests") }
func (m *blobsInMemory) DeletePrefix(string) error                  { panic("not in S2-C's tests") }
func (m *blobsInMemory) FreeBytes() (int64, error)                  { panic("not in S2-C's tests") }
func (m *blobsInMemory) Walk(string, func(string, time.Time) error) error {
	panic("not in S2-C's tests")
}

// objectsFixture is a service on a fresh database with the project chat-app (testProject), and
// the bucket of it.
type objectsFixture struct {
	s     *Service
	pool  *pgxpool.Pool
	blobs *blobsInMemory
	b     Bucket
	// ops records what the hook was asked, for principals other than admins.
	mu  sync.Mutex
	ops []ruleOp
}

var (
	admin = Principal{Kind: PrincipalAdmin, ServiceAccount: "backend@chat-app.iam.gserviceaccount.com"}
	alice = Principal{Kind: PrincipalUser, UID: "alice", Token: map[string]any{"sub": "alice"}}
)

func newObjectsFixture(t *testing.T) *objectsFixture {
	t.Helper()
	s, pool := dbService(t)
	f := &objectsFixture{s: s, pool: pool, blobs: &blobsInMemory{}, b: Bucket{Project: testProject, Name: "chat-app.aruma.test"}}
	s.store = f.blobs
	s.limits = DefaultLimits()
	s.now = time.Now
	s.projects = fakeProjects{project: tcore.Project{Number: testProject, State: "ACTIVE", StorageBucket: f.b.Name,
		CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}}
	// Alice may do anything, unless a test says otherwise; every decision is recorded.
	f.allow(func(ruleOp) error { return nil })
	return f
}

// allow makes decide the rules for principals other than admins.
func (f *objectsFixture) allow(decide func(ruleOp) error) {
	f.s.authorizeHook = func(_ context.Context, _ Bucket, _ Principal, op ruleOp) error {
		f.mu.Lock()
		f.ops = append(f.ops, op)
		f.mu.Unlock()
		return decide(op)
	}
}

func (f *objectsFixture) lastOp(t *testing.T) ruleOp {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ops) == 0 {
		t.Fatal("the rules were not asked")
	}
	return f.ops[len(f.ops)-1]
}

// seed is an object as a test stores it.
type seed struct {
	body     string
	metadata map[string]string
	tokens   []string
	fields   map[string]string
}

// put stores an object directly through the contract's InsertObject, with its file in memory
// and its bytes counted in the usage row, as a commit leaves it (S2-D's).
func (f *objectsFixture) put(t *testing.T, name string, sd seed) Object {
	t.Helper()
	ctx := context.Background()
	key := blob.ObjectKey(f.b.Project)
	f.blobs.put(key, []byte(sd.body))
	sealed, err := f.s.sealTokens(f.b.Project, name, sd.tokens)
	if err != nil {
		t.Fatal(err)
	}
	md, err := encodeMetadata(sd.metadata)
	if err != nil {
		t.Fatal(err)
	}
	str := func(k string) *string { return nullable(sd.fields[k]) }
	var row queries.StorageObject
	now := time.Now()
	err = f.s.inTx(ctx, func(q *queries.Queries) error {
		row, err = q.InsertObject(ctx, queries.InsertObjectParams{
			ProjectNumber: f.b.Project, Name: name, Generation: now.UnixMicro(), BlobKey: key, Size: int64(len(sd.body)),
			Md5: make([]byte, 16), Metadata: md, DownloadTokens: sealed, Now: now,
			ContentType: str(FieldContentType), ContentEncoding: str(FieldContentEncoding),
			ContentDisposition: str(FieldContentDisposition), ContentLanguage: str(FieldContentLanguage),
			CacheControl: str(FieldCacheControl),
		})
		if err != nil {
			return err
		}
		_, err = f.pool.Exec(ctx, `INSERT INTO storage.usage (project_number, used_bytes, object_count) VALUES ($1, $2, 1)
			ON CONFLICT (project_number) DO UPDATE SET used_bytes = storage.usage.used_bytes + $2,
			object_count = storage.usage.object_count + 1`, f.b.Project, len(sd.body))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	o, err := f.s.objectFromRow(f.b, row)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func (f *objectsFixture) row(t *testing.T, name string) queries.StorageObject {
	t.Helper()
	r, err := f.s.reads.GetObject(context.Background(), queries.GetObjectParams{ProjectNumber: f.b.Project, Name: name})
	if err != nil {
		t.Fatalf("row %q: %v", name, err)
	}
	return r
}

func ptr[T any](v T) *T { return &v }

func TestStat(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	put := f.put(t, "photos/a.png", seed{body: "png", metadata: map[string]string{"k": "v"}, fields: map[string]string{FieldContentType: "image/png"}})

	// GCS and the dashboard read without minting.
	for _, sf := range []Surface{SurfaceGCS, SurfaceConsole} {
		o, err := f.s.Stat(ctx, f.b, "photos/a.png", admin, sf, 0)
		if err != nil || o.Metageneration != 1 || len(o.DownloadTokens) != 0 || o.ContentType != "image/png" || o.Metadata["k"] != "v" {
			t.Fatalf("%v: %+v, %v", sf, o, err)
		}
	}
	// A pinned generation other than the current one is not found.
	if _, err := f.s.Stat(ctx, f.b, "photos/a.png", admin, SurfaceGCS, put.Generation+1); err != ErrNotFound {
		t.Errorf("another generation: %v", err)
	}
	if o, err := f.s.Stat(ctx, f.b, "photos/a.png", admin, SurfaceGCS, put.Generation); err != nil || o.Generation != put.Generation {
		t.Errorf("the current generation pinned: %+v, %v", o, err)
	}

	// The rules decide before existence: a refusal is 403 whether the object exists or not,
	// and refused reads mint nothing.
	f.allow(func(ruleOp) error { return ErrPermissionDenied })
	for _, name := range []string{"photos/a.png", "photos/none.png"} {
		if _, err := f.s.Stat(ctx, f.b, name, alice, SurfaceFirebase, 0); err != ErrPermissionDenied {
			t.Errorf("refused %q: %v", name, err)
		}
	}
	if r := f.row(t, "photos/a.png"); r.Metageneration != 1 || r.DownloadTokens != nil {
		t.Fatalf("a refused read changed the row: metageneration %d, tokens %v", r.Metageneration, r.DownloadTokens)
	}
	f.allow(func(ruleOp) error { return nil })
	if _, err := f.s.Stat(ctx, f.b, "photos/none.png", alice, SurfaceFirebase, 0); err != ErrNotFound {
		t.Errorf("allowed, missing: %v", err)
	}
	if op := f.lastOp(t); op.Method != rules.MethodGet || op.Resource != nil || op.Incoming != nil || op.Name != "photos/none.png" {
		t.Errorf("the rules saw %+v for a missing object", op)
	}

	// An authorized /v0 read of an object without a token mints one, with a new metageneration.
	o, err := f.s.Stat(ctx, f.b, "photos/a.png", alice, SurfaceFirebase, 0)
	if err != nil || len(o.DownloadTokens) != 1 || o.Metageneration != 2 || o.Generation != put.Generation {
		t.Fatalf("the minting read: %+v, %v", o, err)
	}
	if op := f.lastOp(t); op.Resource == nil || op.Resource.Metageneration != 1 || op.Surface != SurfaceFirebase {
		t.Errorf("the rules saw %+v", op)
	}
	again, err := f.s.Stat(ctx, f.b, "photos/a.png", alice, SurfaceFirebase, 0)
	if err != nil || !slices.Equal(again.DownloadTokens, o.DownloadTokens) || again.Metageneration != 2 {
		t.Errorf("a second read: %+v, %v; want the same token", again, err)
	}
	if !o.Updated.After(put.Updated) && !o.Updated.Equal(put.Updated) {
		t.Errorf("updated went back: %v before %v", o.Updated, put.Updated)
	}

	for _, bad := range []string{"", ".", "a\nb"} {
		var ie *InvalidError
		if _, err := f.s.Stat(ctx, f.b, bad, admin, SurfaceGCS, 0); !errors.As(err, &ie) {
			t.Errorf("Stat(%q) = %v, want 400", bad, err)
		}
	}
}

// Readers racing on an object without a token mint exactly one, however many they are.
func TestStatMintsOneTokenUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	f.put(t, "a.png", seed{body: "x"})
	const readers = 32
	start := make(chan struct{})
	tokens := make([][]string, readers)
	errs := make([]error, readers)
	var wg sync.WaitGroup
	for i := range readers {
		wg.Go(func() {
			<-start
			p := alice
			if i%2 == 0 {
				p = admin
			}
			o, err := f.s.Stat(ctx, f.b, "a.png", p, SurfaceFirebase, 0)
			tokens[i], errs[i] = o.DownloadTokens, err
		})
	}
	close(start)
	wg.Wait()
	for i := range readers {
		if errs[i] != nil || len(tokens[i]) != 1 || tokens[i][0] != tokens[0][0] {
			t.Fatalf("reader %d: %v, %v; reader 0 has %v", i, tokens[i], errs[i], tokens[0])
		}
	}
	r := f.row(t, "a.png")
	stored, err := f.s.openTokens(testProject, "a.png", r.DownloadTokens)
	if err != nil || !slices.Equal(stored, tokens[0]) || r.Metageneration != 2 {
		t.Errorf("the row holds %v (metageneration %d), %v; want one token, minted once", stored, r.Metageneration, err)
	}
}

func TestOpen(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	tok := newDownloadToken()
	put := f.put(t, "docs/a.txt", seed{body: "hello", tokens: []string{newDownloadToken(), tok}})
	f.put(t, "docs/b.txt", seed{body: "no token"})
	read := func(r blob.Reader) string {
		t.Helper()
		defer r.Close()
		b, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// A matching token opens the object whatever the rules say, and for anyone.
	f.allow(func(ruleOp) error { return ErrPermissionDenied })
	f.ops = nil
	o, r, err := f.s.Open(ctx, f.b, "docs/a.txt", Principal{}, tok, 0)
	if err != nil || read(r) != "hello" || o.Generation != put.Generation {
		t.Fatalf("with its token: %+v, %v", o, err)
	}
	if len(f.ops) != 0 {
		t.Errorf("a token asked the rules: %+v", f.ops)
	}
	// A token present that matches none is 403 without the rules: a wrong one, one of another
	// object, an object without tokens, and one that does not exist.
	f.allow(func(ruleOp) error { return nil })
	for _, c := range []struct{ name, token string }{
		{"docs/a.txt", newDownloadToken()}, {"docs/a.txt", tok[:35]}, {"docs/a.txt", tok + "x"}, {"docs/a.txt", strings.ToUpper(tok)},
		{"docs/b.txt", tok}, {"docs/none.txt", tok},
	} {
		if _, _, err := f.s.Open(ctx, f.b, c.name, alice, c.token, 0); err != ErrPermissionDenied {
			t.Errorf("Open(%q, %q) = %v, want 403", c.name, c.token, err)
		}
	}
	if len(f.ops) != 0 {
		t.Errorf("a wrong token asked the rules: %+v", f.ops)
	}
	// Without a token the rules decide a get, before existence.
	if _, r, err := f.s.Open(ctx, f.b, "docs/b.txt", alice, "", 0); err != nil || read(r) != "no token" {
		t.Errorf("allowed: %v", err)
	}
	if op := f.lastOp(t); op.Method != rules.MethodGet || op.Resource == nil || op.Resource.Name != "docs/b.txt" {
		t.Errorf("the rules saw %+v", op)
	}
	if _, _, err := f.s.Open(ctx, f.b, "docs/none.txt", alice, "", 0); err != ErrNotFound {
		t.Errorf("allowed, missing: %v", err)
	}
	f.allow(func(ruleOp) error { return ErrPermissionDenied })
	for _, name := range []string{"docs/b.txt", "docs/none.txt"} {
		if _, _, err := f.s.Open(ctx, f.b, name, alice, "", 0); err != ErrPermissionDenied {
			t.Errorf("refused %q: %v", name, err)
		}
	}
	// Opening mints nothing.
	if r := f.row(t, "docs/b.txt"); r.DownloadTokens != nil || r.Metageneration != 1 {
		t.Error("Open minted a token")
	}
	// A pinned generation.
	if _, _, err := f.s.Open(ctx, f.b, "docs/a.txt", admin, "", put.Generation-1); err != ErrNotFound {
		t.Errorf("an old generation: %v", err)
	}
	if _, r, err := f.s.Open(ctx, f.b, "docs/a.txt", admin, "", put.Generation); err != nil {
		t.Errorf("the current generation: %v", err)
	} else {
		r.Close()
	}
	// A row without its file is the server's fault, and one of another size too.
	f.blobs.Delete(f.row(t, "docs/b.txt").BlobKey)
	if _, _, err := f.s.Open(ctx, f.b, "docs/b.txt", admin, "", 0); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a missing file: %v", err)
	}
	f.blobs.put(f.row(t, "docs/a.txt").BlobKey, []byte("hello, longer"))
	if _, _, err := f.s.Open(ctx, f.b, "docs/a.txt", admin, "", 0); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a file of another size: %v", err)
	}
}

// A row whose sealed tokens were copied from another object opens with no token, and its reads
// fail loudly rather than show tokens of another object.
func TestOpenCopiedSeal(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	tok := newDownloadToken()
	f.put(t, "a.txt", seed{body: "a", tokens: []string{tok}})
	f.put(t, "b.txt", seed{body: "b"})
	if _, err := f.pool.Exec(ctx, `UPDATE storage.objects SET download_tokens = (SELECT download_tokens FROM storage.objects
		WHERE name = 'a.txt') WHERE name = 'b.txt'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.Open(ctx, f.b, "b.txt", Principal{}, tok, 0); err != ErrPermissionDenied {
		t.Errorf("a copied seal opens: %v", err)
	}
	if _, err := f.s.Stat(ctx, f.b, "b.txt", admin, SurfaceGCS, 0); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Stat of a copied seal: %v", err)
	}
}

func TestUpdate(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	tok := newDownloadToken()
	put := f.put(t, "a.txt", seed{body: "a", tokens: []string{tok}, metadata: map[string]string{"keep": "1", "drop": "2"},
		fields: map[string]string{FieldContentType: "text/plain", FieldCacheControl: "no-cache", FieldContentLanguage: "en"}})

	// A PATCH merges: a field set, a field deleted by null, a metadata key set, one deleted by
	// null, the others kept. The client's token key goes before anything (ADR-022 §9).
	planted := "planted"
	u := Patch{
		Fields:   map[string]*string{FieldContentType: ptr("text/markdown"), FieldCacheControl: nil},
		Metadata: map[string]*string{"new": ptr("3"), "drop": nil, TokensKey: &planted},
	}
	o, err := f.s.Update(ctx, f.b, "a.txt", alice, SurfaceFirebase, u, Preconditions{})
	if err != nil {
		t.Fatal(err)
	}
	if o.ContentType != "text/markdown" || o.CacheControl != "" || o.ContentLanguage != "en" ||
		len(o.Metadata) != 2 || o.Metadata["keep"] != "1" || o.Metadata["new"] != "3" ||
		!slices.Equal(o.DownloadTokens, []string{tok}) || o.Metageneration != 2 || o.Generation != put.Generation {
		t.Fatalf("after the PATCH: %+v", o)
	}
	if u.Metadata[TokensKey] == nil {
		t.Error("Update changed its caller's patch")
	}
	op := f.lastOp(t)
	if op.Method != rules.MethodUpdate || op.Resource == nil || op.Incoming == nil || op.Patch == nil {
		t.Fatalf("the rules saw %+v", op)
	}
	if _, ok := op.Patch.Metadata[TokensKey]; ok || op.Incoming.Metadata[TokensKey] != "" ||
		op.Incoming.ContentType != "text/markdown" || op.Incoming.Metageneration != 1 || op.Resource.ContentType != "text/plain" {
		t.Errorf("the rules saw the incoming %+v and the existing %+v", op.Incoming, op.Resource)
	}
	if r := f.row(t, "a.txt"); r.CacheControl != nil || string(r.Metadata) != `{"new": "3", "keep": "1"}` {
		t.Errorf("the row: cacheControl %v, metadata %s", r.CacheControl, r.Metadata)
	}

	// "metadata": null clears the custom metadata, then the keys sent merge (ADR-023 §3-17);
	// on /v0 it leaves the tokens.
	o, err = f.s.Update(ctx, f.b, "a.txt", alice, SurfaceFirebase, Patch{ClearMetadata: true, Metadata: map[string]*string{"only": ptr("x")}}, Preconditions{})
	if err != nil || len(o.Metadata) != 1 || o.Metadata["only"] != "x" || !slices.Equal(o.DownloadTokens, []string{tok}) || o.Metageneration != 3 {
		t.Fatalf("metadata null: %+v, %v", o, err)
	}
	o, err = f.s.Update(ctx, f.b, "a.txt", alice, SurfaceFirebase, Patch{ClearMetadata: true}, Preconditions{})
	if err != nil || len(o.Metadata) != 0 || len(o.DownloadTokens) != 1 {
		t.Fatalf("metadata null alone: %+v, %v", o, err)
	}
	if r := f.row(t, "a.txt"); string(r.Metadata) != "{}" {
		t.Errorf("the row's metadata: %s", r.Metadata)
	}
	// An empty patch still advances the metageneration, as the emulator does.
	if o, err = f.s.Update(ctx, f.b, "a.txt", alice, SurfaceFirebase, Patch{}, Preconditions{}); err != nil || o.Metageneration != 5 {
		t.Errorf("an empty patch: %+v, %v", o, err)
	}

	// Refused by the rules: 403 whether the object exists or not, and nothing changes.
	f.allow(func(ruleOp) error { return ErrPermissionDenied })
	for _, name := range []string{"a.txt", "none.txt"} {
		if _, err := f.s.Update(ctx, f.b, name, alice, SurfaceFirebase, Patch{Metadata: map[string]*string{"x": ptr("y")}}, Preconditions{}); err != ErrPermissionDenied {
			t.Errorf("refused %q: %v", name, err)
		}
	}
	if r := f.row(t, "a.txt"); r.Metageneration != 5 {
		t.Errorf("a refused update wrote: metageneration %d", r.Metageneration)
	}
	f.allow(func(ruleOp) error { return nil })
	if _, err := f.s.Update(ctx, f.b, "none.txt", alice, SurfaceFirebase, Patch{}, Preconditions{}); err != ErrNotFound {
		t.Errorf("allowed, missing: %v", err)
	}
	if op := f.lastOp(t); op.Resource != nil || op.Incoming != nil {
		t.Errorf("the rules saw %+v for a missing object", op)
	}
	// Tokens are GCS's alone.
	if _, err := f.s.Update(ctx, f.b, "a.txt", admin, SurfaceFirebase, Patch{Tokens: &[]string{"x"}}, Preconditions{}); err != ErrInvalidArgument {
		t.Errorf("tokens on /v0: %v", err)
	}
}

func TestUpdateLimits(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	f.put(t, "a.txt", seed{body: "a", metadata: map[string]string{"big": strings.Repeat("x", 8<<10-3-1)}})
	for _, c := range []struct {
		name string
		u    Patch
	}{
		{"an unknown field", Patch{Fields: map[string]*string{"size": ptr("1")}}},
		{"a field over 1 KiB", Patch{Fields: map[string]*string{FieldCacheControl: ptr(strings.Repeat("a", 1025))}}},
		{"CR in a field", Patch{Fields: map[string]*string{FieldContentType: ptr("text/html\r\nX-Evil: 1")}}},
		{"a control character", Patch{Fields: map[string]*string{FieldContentDisposition: ptr("inline\x01")}}},
		{"DEL", Patch{Fields: map[string]*string{FieldContentLanguage: ptr("en\x7f")}}},
		{"an empty key", Patch{Metadata: map[string]*string{"": ptr("x")}}},
		{"LF in a key", Patch{Metadata: map[string]*string{"a\nb": ptr("x")}}},
		{"NUL in a value", Patch{Metadata: map[string]*string{"k": ptr("a\x00b")}}},
		{"metadata over 8 KiB", Patch{Metadata: map[string]*string{"ab": ptr("cd")}}},
	} {
		var ie *InvalidError
		if _, err := f.s.Update(ctx, f.b, "a.txt", admin, SurfaceGCS, c.u, Preconditions{}); !errors.As(err, &ie) {
			t.Errorf("%s: %v, want 400", c.name, err)
		}
	}
	if r := f.row(t, "a.txt"); r.Metageneration != 1 {
		t.Errorf("a refused update wrote: %d", r.Metageneration)
	}
	// At the limit exactly, and a value of 1 KiB exactly.
	if _, err := f.s.Update(ctx, f.b, "a.txt", admin, SurfaceGCS, Patch{Metadata: map[string]*string{"k": ptr("")},
		Fields: map[string]*string{FieldCacheControl: ptr(strings.Repeat("a", 1024))}}, Preconditions{}); err != nil {
		t.Errorf("at the limits: %v", err)
	}
}

// On GCS the tokens are custom metadata (B §7): setting the key replaces them, which revokes the
// old link, deleting it removes them, and "metadata": null clears them with the rest.
func TestUpdateTokensOnGCS(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	old := newDownloadToken()
	f.put(t, "a.txt", seed{body: "a", tokens: []string{old}, metadata: map[string]string{"note": "n"}})
	o, err := f.s.Update(ctx, f.b, "a.txt", admin, SurfaceGCS, Patch{Metadata: map[string]*string{TokensKey: ptr(" mine-1 ,mine-2,,")}}, Preconditions{})
	if err != nil || !slices.Equal(o.DownloadTokens, []string{"mine-1", "mine-2"}) || o.Metadata["note"] != "n" || len(o.Metadata) != 1 {
		t.Fatalf("set by an admin: %+v, %v", o, err)
	}
	if _, _, err := f.s.Open(ctx, f.b, "a.txt", Principal{}, old, 0); err != ErrPermissionDenied {
		t.Errorf("the replaced token still opens: %v", err)
	}
	if _, r, err := f.s.Open(ctx, f.b, "a.txt", Principal{}, "mine-2", 0); err != nil {
		t.Errorf("the new token: %v", err)
	} else {
		r.Close()
	}
	// A null for another key keeps the tokens (Pyric nullRemovesKey).
	o, err = f.s.Update(ctx, f.b, "a.txt", admin, SurfaceGCS, Patch{Metadata: map[string]*string{"note": nil}}, Preconditions{})
	if err != nil || len(o.DownloadTokens) != 2 || len(o.Metadata) != 0 {
		t.Fatalf("note: null: %+v, %v", o, err)
	}
	o, err = f.s.Update(ctx, f.b, "a.txt", admin, SurfaceGCS, Patch{Metadata: map[string]*string{TokensKey: nil}}, Preconditions{})
	if err != nil || len(o.DownloadTokens) != 0 || f.row(t, "a.txt").DownloadTokens != nil {
		t.Fatalf("the key deleted: %+v, %v", o, err)
	}
	// Removing the last token on GCS mints none; the next authorized /v0 read does.
	if o, err := f.s.Stat(ctx, f.b, "a.txt", alice, SurfaceFirebase, 0); err != nil || len(o.DownloadTokens) != 1 {
		t.Fatalf("the /v0 read after: %+v, %v", o, err)
	}
	o, err = f.s.Update(ctx, f.b, "a.txt", admin, SurfaceGCS, Patch{ClearMetadata: true}, Preconditions{})
	if err != nil || len(o.DownloadTokens) != 0 {
		t.Errorf("metadata null on GCS: %+v, %v", o, err)
	}
	// Patch.Tokens wins over the key.
	o, err = f.s.Update(ctx, f.b, "a.txt", admin, SurfaceGCS, Patch{Tokens: &[]string{"t"}, Metadata: map[string]*string{TokensKey: ptr("u")}}, Preconditions{})
	if err != nil || !slices.Equal(o.DownloadTokens, []string{"t"}) || len(o.Metadata) != 0 {
		t.Errorf("both: %+v, %v", o, err)
	}
	// Tokens count against metadata_bytes, as GCS holds them in the custom metadata.
	many := strings.Repeat("t,", 4<<10)
	var ie *InvalidError
	if _, err := f.s.Update(ctx, f.b, "a.txt", admin, SurfaceGCS, Patch{Metadata: map[string]*string{TokensKey: &many}}, Preconditions{}); !errors.As(err, &ie) {
		t.Errorf("tokens over the limit: %v", err)
	}
}

func TestUpdatePreconditions(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	put := f.put(t, "a.txt", seed{body: "a"})
	g, m := put.Generation, put.Metageneration
	for _, c := range []struct {
		pre Preconditions
		ok  bool
	}{
		{Preconditions{IfGenerationMatch: &g}, true},
		{Preconditions{IfGenerationMatch: ptr(g + 1)}, false},
		{Preconditions{IfGenerationMatch: ptr(int64(0))}, false},
		{Preconditions{IfGenerationNotMatch: &g}, false},
		{Preconditions{IfGenerationNotMatch: ptr(g + 1)}, true},
		{Preconditions{IfMetagenerationMatch: ptr(int64(1))}, false}, // the first case moved it to 2
		{Preconditions{IfMetagenerationNotMatch: ptr(int64(1))}, true},
	} {
		_, err := f.s.Update(ctx, f.b, "a.txt", admin, SurfaceGCS, Patch{}, c.pre)
		if c.ok != (err == nil) || (!c.ok && err != ErrPreconditionFailed) {
			t.Errorf("%+v: %v", c.pre, err)
		}
		m = f.row(t, "a.txt").Metageneration
	}
	if _, err := f.s.Update(ctx, f.b, "a.txt", admin, SurfaceGCS, Patch{}, Preconditions{IfMetagenerationMatch: &m}); err != nil {
		t.Errorf("the current metageneration: %v", err)
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	f.put(t, "a.txt", seed{body: "12345"})
	f.put(t, "b.txt", seed{body: "123"})
	key := f.row(t, "a.txt").BlobKey

	f.allow(func(ruleOp) error { return ErrPermissionDenied })
	for _, name := range []string{"a.txt", "none.txt"} {
		if err := f.s.Delete(ctx, f.b, name, alice, SurfaceFirebase, Preconditions{}); err != ErrPermissionDenied {
			t.Errorf("refused %q: %v", name, err)
		}
	}
	f.allow(func(ruleOp) error { return nil })
	if err := f.s.Delete(ctx, f.b, "none.txt", alice, SurfaceFirebase, Preconditions{}); err != ErrNotFound {
		t.Errorf("allowed, missing: %v", err)
	}
	if err := f.s.Delete(ctx, f.b, "a.txt", alice, SurfaceFirebase, Preconditions{}); err != nil {
		t.Fatal(err)
	}
	if op := f.lastOp(t); op.Method != rules.MethodDelete || op.Resource == nil || op.Resource.Size != 5 || op.Incoming != nil {
		t.Errorf("the rules saw %+v", op)
	}
	if _, err := f.s.Stat(ctx, f.b, "a.txt", admin, SurfaceGCS, 0); err != ErrNotFound {
		t.Errorf("after the delete: %v", err)
	}
	// The quota and blob_gc stay right (releaseObject).
	u, err := f.s.reads.GetUsage(ctx, testProject)
	if err != nil || u.UsedBytes != 3 || u.ObjectCount != 1 {
		t.Errorf("usage %+v, %v", u, err)
	}
	var queued string
	if err := f.pool.QueryRow(ctx, "SELECT blob_key FROM storage.blob_gc").Scan(&queued); err != nil || queued != key {
		t.Errorf("blob_gc holds %q, %v; want %q", queued, err, key)
	}
	if err := f.s.Delete(ctx, f.b, "a.txt", admin, SurfaceGCS, Preconditions{}); err != ErrNotFound {
		t.Errorf("a second delete: %v", err)
	}
	// GCS preconditions, on the object they name.
	b := f.row(t, "b.txt")
	if err := f.s.Delete(ctx, f.b, "b.txt", admin, SurfaceGCS, Preconditions{IfGenerationMatch: ptr(b.Generation + 1)}); err != ErrPreconditionFailed {
		t.Errorf("a precondition that fails: %v", err)
	}
	if err := f.s.Delete(ctx, f.b, "b.txt", admin, SurfaceGCS, Preconditions{IfGenerationMatch: &b.Generation, IfMetagenerationMatch: &b.Metageneration}); err != nil {
		t.Errorf("preconditions that hold: %v", err)
	}
	if u, _ := f.s.reads.GetUsage(ctx, testProject); u.UsedBytes != 0 || u.ObjectCount != 0 {
		t.Errorf("usage after both: %+v", u)
	}
}

// A delete and a metadata change racing on one object: the delete either wins, and the change
// is 404, or reads the changed row again; the file is queued once and the quota released once.
func TestDeleteRacesUpdate(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	for i := range 20 {
		name := "r" + string(rune('a'+i))
		f.put(t, name, seed{body: "xx"})
		var wg sync.WaitGroup
		var derr, uerr error
		wg.Go(func() { derr = f.s.Delete(ctx, f.b, name, admin, SurfaceGCS, Preconditions{}) })
		wg.Go(func() {
			_, uerr = f.s.Update(ctx, f.b, name, admin, SurfaceGCS, Patch{Metadata: map[string]*string{"k": ptr("v")}}, Preconditions{})
		})
		wg.Wait()
		if derr != nil || (uerr != nil && uerr != ErrNotFound) {
			t.Fatalf("%s: delete %v, update %v", name, derr, uerr)
		}
	}
	var queued int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM storage.blob_gc").Scan(&queued); err != nil || queued != 20 {
		t.Errorf("%d files queued, %v", queued, err)
	}
	if u, _ := f.s.reads.GetUsage(ctx, testProject); u.UsedBytes != 0 || u.ObjectCount != 0 {
		t.Errorf("usage %+v", u)
	}
}

// The tokens are sealed in the database: no token in the clear in any column, nor in a dump.
func TestTokensSealedInTheDatabase(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	f.put(t, "a.txt", seed{body: "a"})
	o, err := f.s.Stat(ctx, f.b, "a.txt", admin, SurfaceFirebase, 0)
	if err != nil {
		t.Fatal(err)
	}
	o, err = f.s.CreateToken(ctx, f.b, "a.txt", admin)
	if err != nil || len(o.DownloadTokens) != 2 {
		t.Fatal(o, err)
	}
	var row string
	if err := f.pool.QueryRow(ctx, "SELECT o::text FROM storage.objects o").Scan(&row); err != nil {
		t.Fatal(err)
	}
	for _, tok := range o.DownloadTokens {
		if strings.Contains(row, tok) || strings.Contains(row, strings.ReplaceAll(tok, "-", "")) {
			t.Errorf("the row holds the token %s in the clear: %s", tok, row)
		}
	}
	dump := pgDump(t, f.pool)
	if dump == nil {
		return
	}
	if !bytes.Contains(dump, []byte("a.txt")) {
		t.Fatalf("the dump does not hold the object: %.300s", dump)
	}
	for _, tok := range o.DownloadTokens {
		if bytes.Contains(dump, []byte(tok)) {
			t.Errorf("pg_dump holds the token %s in the clear", tok)
		}
	}
}

// pgDump returns pg_dump's output for the storage schema of the pool's database, or nil (the
// test then relies on the row's text) when pg_dump is not installed.
func pgDump(t *testing.T, pool *pgxpool.Pool) []byte {
	t.Helper()
	bin := "pg_dump"
	if dir := os.Getenv("ARUMA_PG_BIN"); dir != "" {
		bin = dir + "/pg_dump"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		t.Logf("no pg_dump (%v): the row's text alone is checked", err)
		return nil
	}
	cfg := pool.Config().ConnConfig
	out, err := exec.Command(path, "--schema=storage", "--data-only", "-h", cfg.Host, "-p", strconv.Itoa(int(cfg.Port)),
		"-U", cfg.User, cfg.Database).CombinedOutput()
	if err != nil {
		t.Fatalf("pg_dump: %v: %s", err, out)
	}
	return out
}

func TestObjectFromRow(t *testing.T) {
	s := &Service{keys: testKeys(t)}
	sealed, _ := s.sealTokens(testProject, "a", []string{"t1"})
	now := time.Now()
	o, err := s.objectFromRow(Bucket{testProject, "b"}, queries.StorageObject{
		ProjectNumber: testProject, Name: "a", Generation: 7, Metageneration: 2, Size: 3, Md5: bytes.Repeat([]byte{1}, 16),
		Crc32c: 0xFFFFFFFF, ContentType: ptr("text/plain"), Metadata: json.RawMessage(`{"k":"v"}`), DownloadTokens: sealed,
		TimeCreated: now, Updated: now,
	})
	if err != nil || o.Bucket != "b" || o.CRC32C != 0xFFFFFFFF || o.MD5[15] != 1 || o.ContentType != "text/plain" ||
		o.ContentEncoding != "" || o.Metadata["k"] != "v" || !slices.Equal(o.DownloadTokens, []string{"t1"}) {
		t.Errorf("%+v, %v", o, err)
	}
}
