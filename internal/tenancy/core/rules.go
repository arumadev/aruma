package core

import (
	"context"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// Rulesets and releases (rules-layout.md:115-118): saving a ruleset, publishing a release, the
// locked default for Storage, and CompiledRelease with its cache bounded by weight. S2-E writes
// them, with the control migration and the logical name of the Storage release.

// CompiledRelease returns the compiled ruleset a project's release points to. The Storage
// release is stored under its logical name, firebase.storage/default (ADR-022 §2). A project
// without the release gets the locked default; a release whose ruleset no longer compiles
// denies everything (ADR-021 §7).
func (s *Service) CompiledRelease(ctx context.Context, project int64, release string) (*rules.Ruleset, error) {
	panic("S2-E: CompiledRelease")
}
