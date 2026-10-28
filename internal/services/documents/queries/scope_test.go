package queries

import (
	"testing"

	"github.com/arumadev/aruma/internal/store/scopecheck"
)

// TestQueriesAreProjectScoped applies the isolation guard to the documents schema (ADR-017,
// ADR-024 §1.1): every table is a project's.
func TestQueriesAreProjectScoped(t *testing.T) {
	scopecheck.Check(t, ".", []string{
		"documents.databases", "documents.sequencer", "documents.documents", "documents.versions",
		"documents.changelog", "documents.indexes", "documents.field_overrides", "documents.index_entries",
		"documents.usage", "documents.usage_hours", "documents.operations",
	})
}
