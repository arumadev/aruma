package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/osama300/aurma-project/internal/services/rtdb/core"
)

func TestTLSAsk(t *testing.T) {
	a, _, _ := newTestAPI(t, Options{Domain: "rtdb.aruma.test"}, core.Options{})
	ask := a.TLSAsk()
	for _, c := range []struct {
		domain string
		want   int
	}{
		{"chat-app-default-rtdb.rtdb.aruma.test", http.StatusOK},
		{"other-app-default-rtdb.rtdb.aruma.test", http.StatusOK},
		// Proxies may send the name in another case or with the root dot.
		{"Chat-App-Default-RTDB.rtdb.aruma.test.", http.StatusOK},
		{"missing-default-rtdb.rtdb.aruma.test", http.StatusNotFound},
		{"chat-app.rtdb.aruma.test", http.StatusNotFound},
		{"s-chat-app-default-rtdb.rtdb.aruma.test", http.StatusNotFound},
		// One label only: a wildcard certificate would not cover a deeper name either.
		{"x.chat-app-default-rtdb.rtdb.aruma.test", http.StatusNotFound},
		{"rtdb.aruma.test", http.StatusNotFound},
		{"aruma.test", http.StatusNotFound},
		{"chat-app-default-rtdb.evil.test", http.StatusNotFound},
		{"", http.StatusNotFound},
		// The control plane cannot answer: no certificate now, the proxy asks again later.
		{"outage-default-rtdb.rtdb.aruma.test", http.StatusServiceUnavailable},
	} {
		rec := httptest.NewRecorder()
		ask.ServeHTTP(rec, httptest.NewRequest("GET", "/rtdb/tls-ask?domain="+url.QueryEscape(c.domain), nil))
		if rec.Code != c.want {
			t.Errorf("ask %q: %d, want %d", c.domain, rec.Code, c.want)
		}
	}

	// Without rtdb.domain no host names a database, so no certificate is wanted.
	off, _, _ := newTestAPI(t, Options{}, core.Options{})
	rec := httptest.NewRecorder()
	off.TLSAsk().ServeHTTP(rec, httptest.NewRequest("GET", "/rtdb/tls-ask?domain=chat-app-default-rtdb.rtdb.aruma.test", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("without rtdb.domain: %d, want 404", rec.Code)
	}
}
