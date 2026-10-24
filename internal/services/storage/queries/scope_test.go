package queries

import (
	"testing"

	"github.com/arumadev/aruma/internal/store/scopecheck"
)

// TestQueriesAreProjectScoped applies the isolation guard to the storage schema (ADR-017,
// ADR-022 §3): the five tables of a project's data, and storage.instance, which belongs to no
// project, so every query on it carries a global scope annotation.
func TestQueriesAreProjectScoped(t *testing.T) {
	scopecheck.Check(t, ".", []string{
		"storage.objects", "storage.upload_sessions", "storage.blob_gc", "storage.usage", "storage.buckets",
		"storage.instance",
	})
}
