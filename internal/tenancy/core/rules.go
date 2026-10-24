package core

import (
	"context"
	"time"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/rules"
)

// Rulesets and releases (rules-layout.md:115-118, ADR-021 §7, ADR-022 §2): saving a ruleset,
// publishing a release, the locked default for Storage, and CompiledRelease with its cache
// bounded by Options.RulesLimits.CompiledRulesetsBytes. The store, the publication and
// CompiledRelease are S2-E's, with the control migration, the logical name of the Storage
// release and the purge of both tables in PurgeExpired; the listings and deletion the
// firebaserules API needs are S5-A's. The console (S5-B) and the API (S5-A) call these, and
// nothing else, so neither touches this file's signatures.

// StorageRelease is the logical name the Storage release is stored under, whatever the bucket
// is called today (ADR-022 §2).
const StorageRelease = "firebase.storage/default"

// Ruleset is a saved rules source, as the firebaserules API shows it.
type Ruleset struct {
	Name       string // projects/{project}/rulesets/{uuid}
	Files      []rules.File
	CreateTime time.Time
}

// Release points a name (cloud.firestore, firebase.storage/<bucket>) at a ruleset.
type Release struct {
	Name        string // projects/{project}/releases/{release}, the Storage one under the current bucket's name
	RulesetName string
	CreateTime  time.Time
	UpdateTime  time.Time
}

// CreateRuleset compiles and saves a ruleset for a project the caller owns. A source that does
// not compile is refused with its issues, and nothing is saved.
func (s *Service) CreateRuleset(ctx context.Context, p *authn.Principal, project string, svc rules.Service, files []rules.File) (Ruleset, rules.Issues, error) {
	panic("S2-E: CreateRuleset")
}

// GetRuleset returns one of a project's rulesets.
func (s *Service) GetRuleset(ctx context.Context, p *authn.Principal, project, name string) (Ruleset, error) {
	panic("S5-A: GetRuleset")
}

// ListRulesets lists a project's rulesets, newest first.
func (s *Service) ListRulesets(ctx context.Context, p *authn.Principal, project string, opt ListOptions) (Page[Ruleset], error) {
	panic("S5-A: ListRulesets")
}

// DeleteRuleset deletes a ruleset no release points to.
func (s *Service) DeleteRuleset(ctx context.Context, p *authn.Principal, project, name string) error {
	panic("S5-A: DeleteRuleset")
}

// GetRelease returns one of a project's releases. NOT_FOUND when it does not exist.
func (s *Service) GetRelease(ctx context.Context, p *authn.Principal, project, release string) (Release, error) {
	panic("S2-E: GetRelease")
}

// ListReleases lists a project's releases.
func (s *Service) ListReleases(ctx context.Context, p *authn.Principal, project string, opt ListOptions) (Page[Release], error) {
	panic("S5-A: ListReleases")
}

// PublishRelease creates a release or points an existing one at another ruleset, and drops the
// release's compiled ruleset from the cache, so the next request sees it.
func (s *Service) PublishRelease(ctx context.Context, p *authn.Principal, project, release, ruleset string) (Release, error) {
	panic("S2-E: PublishRelease")
}

// CompiledRelease returns the compiled ruleset a project's release points to, for the services
// that evaluate it. A project without its Storage release gets the locked default; a release
// whose ruleset no longer compiles denies everything (ADR-021 §7).
func (s *Service) CompiledRelease(ctx context.Context, project int64, release string) (*rules.Ruleset, error) {
	panic("S2-E: CompiledRelease")
}
