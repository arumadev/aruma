package acceptance

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	storage "github.com/arumadev/aruma/internal/services/storage/core"
)

// S2-C's Storage isolation cases (ADR-022 §3); the file is S2-C's alone. The service is built on
// the server's control plane and database, as in TestIsolationStorageBuckets, with the objects'
// files in memory until S2-A's disk and S2-D's uploads: the rows are written as a commit leaves
// them, and the download tokens minted by the service itself.

// s2cBlobs holds the objects' bytes in memory, for Open.
type s2cBlobs struct {
	blob.Store // the rest is not reached here
	mu         sync.Mutex
	files      map[string][]byte
}

// s2cReader is a file's bytes; bytes.Reader has the Size a blob.Reader needs.
type s2cReader struct{ *bytes.Reader }

func (r s2cReader) Close() error { return nil }

func (m *s2cBlobs) Open(key string) (blob.Reader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return s2cReader{bytes.NewReader(b)}, nil
}

// s2cEnv is the two projects' buckets on one storage service.
type s2cEnv struct {
	*isolationEnv
	st     *storage.Service
	blobs  *s2cBlobs
	ba, bb storage.Bucket
}

func newS2CEnv(t *testing.T) *s2cEnv {
	t.Helper()
	ie := newIsolation(t)
	master, err := base64.StdEncoding.DecodeString(testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := crypto.NewKeys(master)
	if err != nil {
		t.Fatal(err)
	}
	blobs := &s2cBlobs{files: map[string][]byte{}}
	st, err := storage.New(context.Background(), ie.Pool, blobs, storage.Options{
		Limits: storage.DefaultLimits(), Keys: keys, Projects: ie.App.Service,
		Inflight: rules.NewInflight(1 << 20), DataDir: t.TempDir(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := &s2cEnv{isolationEnv: ie, st: st, blobs: blobs}
	domain := ie.App.Config.ProjectDomain
	if e.ba, err = st.Bucket(context.Background(), "alpha-app."+domain); err != nil {
		t.Fatal(err)
	}
	if e.bb, err = st.Bucket(context.Background(), "bravo-app."+domain); err != nil {
		t.Fatal(err)
	}
	return e
}

// put stores an object as a commit leaves it, without tokens.
func (e *s2cEnv) put(t *testing.T, b storage.Bucket, name, body string) {
	t.Helper()
	key := blob.ObjectKey(b.Project)
	e.blobs.mu.Lock()
	e.blobs.files[key] = []byte(body)
	e.blobs.mu.Unlock()
	now := time.Now()
	if _, err := e.Pool.Exec(context.Background(), `INSERT INTO storage.objects (project_number, name, generation, blob_key, size, md5, crc32c, time_created, updated)
		VALUES ($1, $2, $3, $4, $5, decode(repeat('00', 16), 'hex'), 0, $6, $6)`, b.Project, name, now.UnixMicro(), key, len(body), now); err != nil {
		t.Fatal(err)
	}
}

// An object's download token opens that object alone: not the object of the same name in
// another project, even with the other project's sealed list copied into its row.
func TestIsolationStorageDownloadToken(t *testing.T) {
	e := newS2CEnv(t)
	ctx := context.Background()
	admin := storage.Principal{Kind: storage.PrincipalAdmin}
	const name = "shared/photo.png"
	e.put(t, e.ba, name, "alpha's bytes")
	e.put(t, e.bb, name, "bravo's bytes")
	tokens := map[int64]string{}
	for _, b := range []storage.Bucket{e.ba, e.bb} {
		o, err := e.st.Stat(ctx, b, name, admin, storage.SurfaceFirebase, 0)
		if err != nil || len(o.DownloadTokens) != 1 {
			t.Fatalf("the /v0 read in %d: %+v, %v", b.Project, o, err)
		}
		tokens[b.Project] = o.DownloadTokens[0]
	}
	read := func(b storage.Bucket, token string) (string, error) {
		_, r, err := e.st.Open(ctx, b, name, storage.Principal{}, token, 0)
		if err != nil {
			return "", err
		}
		defer r.Close()
		body, err := io.ReadAll(r)
		return string(body), err
	}
	if body, err := read(e.ba, tokens[e.ba.Project]); err != nil || body != "alpha's bytes" {
		t.Fatalf("A's token on A: %q, %v", body, err)
	}
	if body, err := read(e.bb, tokens[e.bb.Project]); err != nil || body != "bravo's bytes" {
		t.Fatalf("B's token on B: %q, %v", body, err)
	}
	if _, err := read(e.bb, tokens[e.ba.Project]); !errors.Is(err, storage.ErrPermissionDenied) {
		t.Errorf("A's token on B's object of the same name: %v, want 403", err)
	}
	if _, err := read(e.ba, tokens[e.bb.Project]); !errors.Is(err, storage.ErrPermissionDenied) {
		t.Errorf("B's token on A's object: %v, want 403", err)
	}
	// The seal is bound to its project: A's sealed list copied into B's row opens nothing.
	if _, err := e.Pool.Exec(ctx, `UPDATE storage.objects SET download_tokens = (SELECT download_tokens FROM storage.objects
		WHERE project_number = $1 AND name = $3) WHERE project_number = $2 AND name = $3`, e.ba.Project, e.bb.Project, name); err != nil {
		t.Fatal(err)
	}
	if _, err := read(e.bb, tokens[e.ba.Project]); !errors.Is(err, storage.ErrPermissionDenied) {
		t.Errorf("A's token on B's row holding A's seal: %v, want 403", err)
	}
}

// A list's page token is for its own bucket: on another project's bucket it is 400, never a
// first page of that bucket.
func TestIsolationStoragePageToken(t *testing.T) {
	e := newS2CEnv(t)
	ctx := context.Background()
	admin := storage.Principal{Kind: storage.PrincipalAdmin}
	for _, b := range []storage.Bucket{e.ba, e.bb} {
		for _, name := range []string{"docs/1", "docs/2", "docs/3"} {
			e.put(t, b, name, "x")
		}
	}
	for _, sf := range []storage.Surface{storage.SurfaceFirebase, storage.SurfaceGCS} {
		o := storage.ListOptions{Prefix: "docs/", Delimiter: "/", MaxResults: 1, Surface: sf}
		page, err := e.st.List(ctx, e.ba, admin, o)
		if err != nil || page.NextPageToken == "" {
			t.Fatalf("%v: A's first page: %+v, %v", sf, page, err)
		}
		o.PageToken = page.NextPageToken
		if next, err := e.st.List(ctx, e.ba, admin, o); err != nil || len(next.Objects) != 1 || next.Objects[0].Name != "docs/2" {
			t.Errorf("%v: the token on A: %+v, %v", sf, next, err)
		}
		if got, err := e.st.List(ctx, e.bb, admin, o); !errors.Is(err, storage.ErrInvalidPageToken) {
			t.Errorf("%v: A's token on B: %+v, %v; want ErrInvalidPageToken", sf, got, err)
		}
	}
}
