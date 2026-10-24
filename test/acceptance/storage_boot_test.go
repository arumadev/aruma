package acceptance

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// A server with Storage enabled boots it as Serve does (startStorage) and mounts its APIs. Until
// S2-D writes Boot the harness skips here, naming it; from then on, this is the smoke test that
// a Storage server comes up, and S3-A and S4-A replace the 501s. The GCS surface answers an
// unknown bucket with production's 404 (S4-A), which the server's own 404 is not.
func TestStorageBoots(t *testing.T) {
	e := newEnv(t, map[string]string{"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": t.TempDir()})
	for _, path := range []string{"/v0/b/x/o", "/storage/v1/b/x"} {
		resp, err := http.Get(e.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		gcsNotFound := strings.HasPrefix(path, "/storage/") && strings.Contains(string(body), `"The specified bucket does not exist."`)
		if resp.StatusCode == http.StatusNotFound && !gcsNotFound {
			t.Errorf("%s: 404, Storage is not mounted: %s", path, body)
		}
	}
}
