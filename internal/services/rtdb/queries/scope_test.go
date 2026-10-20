package queries

import (
	"testing"

	"github.com/osama300/aurma-project/internal/store/scopecheck"
)

// TestQueriesAreProjectScoped applies the isolation guard to the rtdb schema. Node rows are
// keyed by database_id, which only the project-scoped EnsureDatabase hands out, so their
// queries carry a scope annotation saying so.
func TestQueriesAreProjectScoped(t *testing.T) {
	scopecheck.Check(t, ".", []string{"rtdb."})
}
