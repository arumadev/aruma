package acceptance

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/app"
	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/log"
	"github.com/arumadev/aruma/internal/store"
)

// A server with Storage enabled boots it as Serve does (startStorage) and mounts its APIs. Until
// S2-D writes Boot the harness skips here, naming it; from then on, this is the smoke test that
// a Storage server comes up with both surfaces mounted. Each answers in its own production shape,
// which the entry point's 404 is not: /v0/b/ names no operation, so a mounted /v0 answers it 400
// (S3-A), and the GCS surface answers an unknown bucket with production's 404 (S4-A).
func TestStorageBoots(t *testing.T) {
	e := newEnv(t, map[string]string{"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": t.TempDir()})
	for _, c := range []struct {
		path   string
		status int
		body   string
	}{
		{"/v0/b/", http.StatusBadRequest, `"Invalid HTTP method/URL pair."`},
		{"/storage/v1/b/x", http.StatusNotFound, `"The specified bucket does not exist."`},
	} {
		resp, err := http.Get(e.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.status || !strings.Contains(string(body), c.body) {
			t.Errorf("%s: %d %s, want %d with %s: Storage is not mounted", c.path, resp.StatusCode, body, c.status, c.body)
		}
	}
}

// A data directory that is empty while the database holds objects is a disk that is not mounted
// (its mount point is an empty directory) or a data_dir that names another one. Booting on it
// served every object 503 and put every new upload on the disk that holds the mount point, where
// the real disk hides it again once mounted (security review v0.3, SR3-01). Boot refuses it,
// naming the way out, and writes nothing there.
func TestStorageBootRefusesAnUnmountedDisk(t *testing.T) {
	disk := t.TempDir()
	e := newEnv(t, map[string]string{"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": disk})
	owner := e.owner()
	bucket := owner.project("chat-app").str("storageBucket")
	req, _ := http.NewRequest(http.MethodPost, e.URL+"/v0/b/"+bucket+"/o?name=kept.txt", strings.NewReader("kept on the disk"))
	req.Header.Set("Authorization", "Bearer "+owner.pat())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d", resp.StatusCode)
	}

	// A restarted process whose disk is not mounted.
	mountPoint := t.TempDir()
	ctx := context.Background()
	pool, err := store.Open(ctx, e.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	vars := map[string]string{"ARUMA_DATABASE_URL": e.DB, "ARUMA_PUBLIC_URL": e.URL, "ARUMA_MASTER_KEY": testMasterKey,
		"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": mountPoint}
	cfg, err := config.Load("", func(k string) string { return vars[k] })
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(cfg, log.New(io.Discard, "error"), "test", pool, app.WithArgon2(fastArgon2))
	if err != nil {
		t.Fatal(err)
	}
	err = a.Storage.Boot(ctx)
	if err == nil {
		_ = a.Storage.Close()
		t.Fatal("Storage booted on an empty data directory while the database holds an object")
	}
	if !strings.Contains(err.Error(), "the disk is not mounted") || !strings.Contains(err.Error(), filepath.Join(mountPoint, "INSTANCE")) {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(mountPoint, "INSTANCE")); !os.IsNotExist(serr) {
		t.Errorf("the refused boot wrote an INSTANCE on the mount point: %v", serr)
	}
}
