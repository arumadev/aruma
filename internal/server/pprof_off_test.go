//go:build !pprof

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A build without the pprof tag serves no profiler, not even on the private address.
func TestNoProfilerWithoutTag(t *testing.T) {
	mux := http.NewServeMux()
	mountPprof(mux)
	for _, r := range []*http.Request{
		httptest.NewRequest("GET", "/debug/pprof/", nil),
		httptest.NewRequest("POST", "/debug/pprof/cpu/start", nil),
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404", r.Method, r.URL.Path, rec.Code)
		}
	}
}
