package acceptance

import (
	"net/http"
	"testing"
)

// A server with Storage enabled boots it as Serve does (startStorage) and mounts its APIs. Until
// S2-D writes Boot the harness skips here, naming it; from then on, this is the smoke test that
// a Storage server comes up, and S3-A and S4-A replace the 501s. /v0/b/ names no operation, so
// a mounted /v0 answers it 400 (S3-A), never the entry point's 404.
func TestStorageBoots(t *testing.T) {
	e := newEnv(t, map[string]string{"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": t.TempDir()})
	for _, path := range []string{"/v0/b/", "/storage/v1/b/x"} {
		resp, err := http.Get(e.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			t.Errorf("%s: 404, Storage is not mounted", path)
		}
	}
}
