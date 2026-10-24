package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// The contract's handler (S2-0): every path is 501 in /v0's error shape until S3 routes them.
func TestHandlerBeforeS3(t *testing.T) {
	h := New(nil, Options{}, nil).Handler()
	for _, path := range []string{"/v0/b/chat-app.aruma.test/o", "/firebasestorage.googleapis.com/v0/b/x/o/a%2Fb?alt=media"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		var body wire.FirebaseError
		if rec.Code != http.StatusNotImplemented || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Error.Code != 501 {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}
