package acceptance

import (
	"context"
	"errors"
	"strings"
	"testing"

	storage "github.com/arumadev/aruma/internal/services/storage/core"
)

// S2-D's Storage isolation cases (ADR-022 §3); the file is S2-D's alone.

// An upload session URL of one project does not finish an upload in another, nor on another
// name: the id answers only on the bucket and the name it was started for, and every request
// elsewhere is ErrUploadNotFound, leaving the session as it was.
func TestIsolationStorageUploadURL(t *testing.T) {
	ie := newIsolationWith(t, map[string]string{"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": t.TempDir()})
	st := ie.App.Storage
	ctx := context.Background()
	domain := ie.App.Config.ProjectDomain
	a, err := st.Bucket(ctx, "alpha-app."+domain)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Bucket(ctx, "bravo-app."+domain)
	if err != nil {
		t.Fatal(err)
	}
	admin := storage.Principal{Kind: storage.PrincipalAdmin, ServiceAccount: "backend"}
	id, err := st.StartUpload(ctx, a, admin, storage.UploadSpec{Name: "doc.txt", Surface: storage.SurfaceGCS, Declared: -1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteUpload(ctx, a, "doc.txt", id, nil, storage.Chunk{Body: strings.NewReader("abc"), Length: 3, Total: -1}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		bucket storage.Bucket
		name   string
	}{{b, "doc.txt"}, {a, "other.txt"}, {b, "other.txt"}} {
		chunk := storage.Chunk{Offset: 3, Body: strings.NewReader("def"), Length: 3, Total: -1, Finalize: true}
		if _, err := st.WriteUpload(ctx, c.bucket, c.name, id, nil, chunk); !errors.Is(err, storage.ErrUploadNotFound) {
			t.Errorf("finalize on %s %s: %v", c.bucket.Name, c.name, err)
		}
		if _, err := st.QueryUpload(ctx, c.bucket, c.name, id); !errors.Is(err, storage.ErrUploadNotFound) {
			t.Errorf("query on %s %s: %v", c.bucket.Name, c.name, err)
		}
		if _, err := st.CancelUpload(ctx, c.bucket, c.name, id); !errors.Is(err, storage.ErrUploadNotFound) {
			t.Errorf("cancel on %s %s: %v", c.bucket.Name, c.name, err)
		}
	}
	if u, err := st.Usage(ctx, b); err != nil || u.Objects != 0 || u.ReservedBytes != 0 {
		t.Errorf("B after the foreign requests: %+v, %v", u, err)
	}
	// The session itself is untouched, and finishes where it was started.
	got, err := st.WriteUpload(ctx, a, "doc.txt", id, nil, storage.Chunk{Offset: 3, Body: strings.NewReader("def"), Length: 3, Total: -1, Finalize: true})
	if err != nil || got.Object == nil || got.Object.Size != 6 || got.Object.Bucket != a.Name {
		t.Fatalf("finishing in A: %+v, %v", got, err)
	}
	if u, err := st.Usage(ctx, b); err != nil || u.Objects != 0 {
		t.Errorf("B after A's upload: %+v, %v", u, err)
	}
}
