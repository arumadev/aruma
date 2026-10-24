package gcs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// The contract's handler (S2-0): every path is 501 in GCS's error shape until S4 routes them.
func TestHandlerBeforeS4(t *testing.T) {
	h := New(nil, Options{}, nil).Handler()
	for _, path := range []string{"/storage/v1/b/x", "/upload/storage/v1/b/x/o?uploadType=media", "/download/storage/v1/b/x/o/a?alt=media"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		var body wire.GCSError
		if rec.Code != http.StatusNotImplemented || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error.Code != 501 ||
			len(body.Error.Errors) != 1 {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}
