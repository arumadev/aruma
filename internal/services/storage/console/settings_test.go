package console

import (
	"net/http"
	"strings"
	"testing"
)

func TestCORS(t *testing.T) {
	e := newEnv(t, true)
	got := e.do(amalSession, "GET", path("/cors"), nil)
	if got.code != http.StatusOK || string(got.body) != "{\"cors\":[]}\n" {
		t.Fatalf("no rules: %d %s", got.code, got.body)
	}
	rule := map[string]any{"origin": []string{"https://app.example"}, "method": []string{"GET"},
		"responseHeader": []string{"Content-Type"}, "maxAgeSeconds": 3600}
	got = e.do(amalSession, "PUT", path("/cors"), map[string]any{"cors": []any{rule}})
	if got.code != http.StatusOK || !strings.Contains(string(got.body), `"origin":["https://app.example"]`) {
		t.Fatalf("set: %d %s", got.code, got.body)
	}
	got = e.do(amalCLI, "GET", path("/cors"), nil)
	if !strings.Contains(string(got.body), `"maxAgeSeconds":3600`) {
		t.Errorf("read back: %s", got.body)
	}
	cors, _ := e.svc.CORS(t.Context(), e.bucket)
	if allow, _ := e.svc.MatchCORS(cors, "https://app.example", "GET"); !allow {
		t.Error("the rule set from the dashboard does not apply")
	}
	if a, ok := e.lastAudit("storage.bucket.cors.update"); !ok || a.actor != amal || a.details["rules"] != float64(1) {
		t.Errorf("audit: %+v", a)
	}
	if got := e.do(amalSession, "PUT", path("/cors"), map[string]any{"cors": []any{map[string]any{"origin": []string{"x"}, "methods": []string{"GET"}}}}); got.code != http.StatusBadRequest {
		t.Errorf("an unknown field: %d %s", got.code, got.body)
	}
	if got := e.do(amalSession, "PUT", path("/cors"), map[string]any{"cors": []any{}}); got.code != http.StatusOK || string(got.body) != "{\"cors\":[]}\n" {
		t.Errorf("clear: %d %s", got.code, got.body)
	}
}

func TestUsage(t *testing.T) {
	e := newEnv(t, true)
	e.insert("a.txt", "", "hello")
	e.insert("b.txt", "", "hi")
	got := e.do(amalSession, "GET", path("/usage"), nil)
	if got.code != http.StatusOK || got.json["usedBytes"] != float64(7) || got.json["objects"] != float64(2) ||
		got.json["quotaBytes"] != float64(e.svc.Limits().ProjectBytes) {
		t.Errorf("usage: %d %s", got.code, got.body)
	}
	// A session's reservation shows as reserved once its bytes arrive; the issue alone reserves
	// nothing.
	e.issue(map[string]any{"name": "c.bin", "size": 1 << 20})
	got = e.do(amalSession, "GET", path("/usage"), nil)
	if got.json["reservedBytes"] != float64(0) {
		t.Errorf("after an issue: %s", got.body)
	}
}
