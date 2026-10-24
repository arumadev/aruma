package acceptance

import (
	"io"
	"net/http"
	"strings"
	"testing"
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
