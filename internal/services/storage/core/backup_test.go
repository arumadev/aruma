package core

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/storage/blob"
)

// readArchive returns the regular files of a backup archive by name.
func readArchive(t *testing.T, file string) map[string]string {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			b, _ := io.ReadAll(tr)
			out[h.Name] = string(b)
		}
	}
}

func keyFile(t *testing.T, key string) string {
	t.Helper()
	name, err := blob.KeyFile(key)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

// A backup taken while the server works holds the files its dump names: the gc deletes nothing
// while it runs, a staging file its upload moved meanwhile is copied from where it went, and
// the archive holds the files of the dump's snapshot and nothing written after it.
func TestBackupHoldsTheFilesItsDumpNames(t *testing.T) {
	e, dir := onDisk(t)
	ctx := context.Background()
	e.insert(e.a, "a", "one")
	_, v1, _ := e.s.storedObject(ctx, e.a, "a")
	e.insert(e.a, "a", "two") // v1's file now waits in blob_gc
	_, v2, _ := e.s.storedObject(ctx, e.a, "a")
	id := e.start(e.a, asUser("u1"), uploadSpec("up.txt", SurfaceFirebase))
	if _, err := e.chunk(e.a, "up.txt", id, 0, "half ", false); err != nil {
		t.Fatal(err)
	}
	var staging string
	if err := e.pool.QueryRow(ctx, `SELECT staging_key FROM storage.upload_sessions WHERE state = 'active'`).Scan(&staging); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "storage.tar.gz")
	var v3 string
	rep, err := Backup(ctx, e.pool, dir, archive, func(ctx context.Context, snapshot string) error {
		if snapshot == "" {
			t.Error("no snapshot for the dump")
		}
		// While the backup runs: the object is replaced and deleted, gc_grace passes, and the
		// upload finishes, which moves its staging file.
		e.insert(e.a, "a", "three")
		_, row, _ := e.s.storedObject(ctx, e.a, "a")
		v3 = row.BlobKey
		if err := e.deleteObject("a"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.chunk(e.a, "up.txt", id, 5, "done", true); err != nil {
			t.Fatal(err)
		}
		e.clock.add(time.Hour)
		if n, err := e.s.collectGarbage(ctx); err != nil || n != 0 {
			t.Errorf("the gc during a backup: %d deleted, %v", n, err)
		}
		if err := e.s.PurgeProject(ctx, projectA); !errors.Is(err, ErrBackupRunning) {
			t.Errorf("a purge during a backup: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	files := readArchive(t, archive)
	if files["INSTANCE"] == "" || !strings.Contains(files["INSTANCE"], e.s.instance) {
		t.Errorf("INSTANCE: %q", files["INSTANCE"])
	}
	if got := files[keyFile(t, v2.BlobKey)]; got != "two" {
		t.Errorf("the object of the snapshot: %q", got)
	}
	if got := files[keyFile(t, staging)]; got != "half done" || len(rep.Missing) != 0 {
		// The moved file is copied whole; the restored session cuts it back to its 5 bytes.
		t.Errorf("the staging file moved during the backup: %q, missing %v", got, rep.Missing)
	}
	for _, k := range []string{v1.BlobKey} {
		if _, ok := files[keyFile(t, k)]; ok {
			t.Errorf("the archive holds %s, which the snapshot does not name", k)
		}
	}
	if _, ok := files[keyFile(t, v3)]; ok {
		t.Error("the archive holds a file written after the snapshot")
	}
	if rep.Files != 2 {
		t.Errorf("%d files reported", rep.Files)
	}
	// Once it ends, the gc catches up.
	if n, err := e.s.collectGarbage(ctx); err != nil || n == 0 {
		t.Errorf("the gc after the backup: %d, %v", n, err)
	}
}
