package queries

import (
	"testing"

	"github.com/osama300/aurma-project/internal/store/scopecheck"
)

// TestQueriesAreProjectScoped applies the isolation guard to the auth schema: every table there
// belongs to one project.
func TestQueriesAreProjectScoped(t *testing.T) {
	scopecheck.Check(t, ".", []string{"auth."})
}
