package acceptance

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ADR-018 §5: a TLS proxy issuing certificates on demand asks the private address whether a
// name is a database host. Only an active project's database is.
func TestRTDBTLSAsk(t *testing.T) {
	e := rtdbEnv(t, true, map[string]string{"ARUMA_RTDB_DOMAIN": "rtdb.aruma.test"})
	c := e.owner()
	c.project("chat-app")
	private := httptest.NewServer(e.App.Server.PrivateHandler())
	t.Cleanup(private.Close)
	ask := func(domain string) int {
		t.Helper()
		resp, err := http.Get(private.URL + "/rtdb/tls-ask?domain=" + domain)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	assertEqual(t, "ask for the project's database", ask("chat-app-default-rtdb.rtdb.aruma.test"), 200)
	assertEqual(t, "ask for a missing project", ask("other-app-default-rtdb.rtdb.aruma.test"), 404)
	assertEqual(t, "ask for a name outside rtdb.domain", ask("chat-app-default-rtdb.aruma.test"), 404)

	// A deleted project's host gets no new certificate, and gets one again once restored.
	c.mustOK(c.do("POST", "/console/v1/projects/chat-app:delete", map[string]any{"confirmProjectId": "chat-app"}))
	assertEqual(t, "ask for a deleted project", ask("chat-app-default-rtdb.rtdb.aruma.test"), 404)
	c.mustOK(c.do("POST", "/console/v1/projects/chat-app:restore", nil))
	assertEqual(t, "ask for a restored project", ask("chat-app-default-rtdb.rtdb.aruma.test"), 200)

	// The public entry point never answers it, on the main host or on a database host.
	resp, err := http.Get(e.URL + "/rtdb/tls-ask?domain=chat-app-default-rtdb.rtdb.aruma.test")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("the ask is served on the public entry point")
	}

	// The config files carry the database host (ADR-018).
	app := c.app("chat-app", "WEB", "")
	cfg := c.mustOK(c.do("GET", "/console/v1/projects/chat-app/apps/"+app.str("appId")+"/config", nil))
	assertEqual(t, "databaseURL", cfg.str("databaseURL"), "https://chat-app-default-rtdb.rtdb.aruma.test")
}
