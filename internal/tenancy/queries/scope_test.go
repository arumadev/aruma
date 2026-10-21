package queries

import (
	"testing"

	"github.com/arumadev/aruma/internal/store/scopecheck"
)

// TestQueriesAreProjectScoped is the Phase 1 isolation guard for the control plane.
func TestQueriesAreProjectScoped(t *testing.T) {
	scopecheck.Check(t, ".", []string{
		"control.projects", "control.apps", "control.app_sha_certificates", "control.api_keys",
		"control.service_accounts", "control.service_account_keys", "control.oauth_access_tokens",
		"events.changes",
	})
}
