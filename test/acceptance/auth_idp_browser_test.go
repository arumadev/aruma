//go:build browser

package acceptance

import (
	"net/url"
	"os"
	"os/exec"
	"testing"
)

// TestAuthIdPBrowser runs Google and Apple popup and redirect sign-in, link and
// re-authentication with the official web SDK in headless Chromium, against fake providers
// (AC-A8, AC-A9 locally). It needs Node, network access to apis.google.com (the SDK's gapi,
// ADR-013) and `npm ci` in test/experiments/idp-browser:
//
//	go test -tags browser -run TestAuthIdPBrowser -v ./test/acceptance/
func TestAuthIdPBrowser(t *testing.T) {
	x := newIdPEnv(t)
	x.enableAnonymous()
	x.google.consentAs = func(q url.Values) map[string]any {
		hint := q.Get("login_hint")
		return googleClaims("g-"+hint, hint+"@gmail.com", true)
	}
	x.apple.consentAs = func(url.Values) map[string]any {
		return map[string]any{"sub": "apple-browser", "email": "relay@privaterelay.appleid.com", "email_verified": "true"}
	}
	// The app on Aruma's site, on another site, and on another site with Chrome's storage
	// partitioning, which breaks the redirect relay (docs/research/phase2-idp.md §7).
	for _, run := range []struct{ name, host, partitioned string }{
		{"same-site", "127.0.0.1", ""}, {"cross-site", "localhost", ""}, {"cross-site-partitioned", "localhost", "1"},
	} {
		t.Run(run.name, func(t *testing.T) {
			cmd := exec.Command("node", "run.mjs")
			cmd.Dir = "../experiments/idp-browser"
			cmd.Env = append(os.Environ(), "ARUMA_URL="+x.env.URL, "API_KEY="+x.key, "PROJECT_ID="+x.id, "APP_HOST="+run.host, "PARTITIONED="+run.partitioned, "RUN="+run.name)
			cmd.Stderr = os.Stderr // progress
			out, err := cmd.Output()
			t.Logf("%s:\n%s", run.name, out)
			if err != nil {
				t.Fatalf("browser run: %v", err)
			}
		})
	}
}
