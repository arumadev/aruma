package core

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/tenancy/queries"
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

// The release names the services read (ADR-021 §7): firebase.storage/<bucket> as the API names
// the Storage release, and cloud.firestore for the default database.
const (
	storageReleasePrefix = "firebase.storage/"
	firestoreRelease     = "cloud.firestore"
)

// Ruleset is a saved rules source, as the firebaserules API shows it.
type Ruleset struct {
	Name       string // projects/{project}/rulesets/{uuid}
	Files      []rules.File
	CreateTime time.Time
	// Service is the service the ruleset was compiled for: the console's rules history lists
	// one service's alone. The firebaserules API does not show it.
	Service rules.Service
}

// Release points a name (cloud.firestore, firebase.storage/<bucket>) at a ruleset.
type Release struct {
	Name        string // projects/{project}/releases/{release}, the Storage one under the current bucket's name
	RulesetName string
	CreateTime  time.Time
	UpdateTime  time.Time
}

// storedFile is one file of control.rulesets.files.
type storedFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// CreateRuleset compiles and saves a ruleset for a project the caller owns. A source that does
// not compile is refused with its issues, and nothing is saved.
func (s *Service) CreateRuleset(ctx context.Context, p *authn.Principal, project string, svc rules.Service, files []rules.File) (Ruleset, rules.Issues, error) {
	proj, err := s.resolveProject(ctx, s.q, p, project, false)
	if err != nil {
		return Ruleset{}, nil, err
	}
	if len(files) != 1 {
		// The Rules API's own words (ADR-021 §2.1).
		return Ruleset{}, nil, apierr.New(apierr.InvalidArgument, "Currently only one file source can be passed to firebase rules.")
	}
	f := files[0]
	// jsonb holds no NUL; no rules file needs one.
	if strings.ContainsRune(f.Name, 0) || strings.ContainsRune(f.Content, 0) {
		return Ruleset{}, nil, apierr.New(apierr.InvalidArgument, "A rules source may not contain the NUL character.")
	}
	rs, issues, err := s.CompileRules(ctx, svc, f)
	if err != nil {
		return Ruleset{}, nil, err
	}
	if rs == nil {
		return Ruleset{}, issues, apierr.New(apierr.InvalidArgument, "The rules source does not compile.")
	}
	raw, err := json.Marshal([]storedFile{{Name: f.Name, Content: f.Content}})
	if err != nil {
		return Ruleset{}, nil, internal(err)
	}
	var row queries.ControlRuleset
	err = s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		state, err := q.LockProjectRulesets(ctx, proj.ProjectNumber)
		if err != nil {
			return err
		}
		if state != "ACTIVE" {
			return errNotFound()
		}
		n, err := q.CountRulesets(ctx, proj.ProjectNumber)
		if err != nil {
			return err
		}
		if n >= int64(s.opts.RulesLimits.RulesetsPerProject) {
			return apierr.New(apierr.ResourceExhausted, "The project has %d rulesets, the most it may keep. Delete unused ones first.", n)
		}
		if row, err = q.InsertRuleset(ctx, queries.InsertRulesetParams{ProjectNumber: proj.ProjectNumber, Service: string(svc), Files: raw}); err != nil {
			return err
		}
		return s.audit(ctx, q, p, proj.ProjectNumber, "rules.ruleset.create", rulesetName(proj.ProjectID, row.RulesetID), map[string]any{"service": string(svc)})
	})
	if err != nil {
		return Ruleset{}, nil, internal(err)
	}
	out, err := toRuleset(proj.ProjectID, row)
	if err != nil {
		return Ruleset{}, nil, internal(err)
	}
	return out, issues, nil
}

// GetRuleset returns one of a project's rulesets. name is as PublishRelease takes it: the full
// name under the project's id or number, or the uuid alone.
func (s *Service) GetRuleset(ctx context.Context, p *authn.Principal, project, name string) (Ruleset, error) {
	proj, err := s.resolveProject(ctx, s.q, p, project, false)
	if err != nil {
		return Ruleset{}, err
	}
	id, err := rulesetID(s.toProject(proj), name)
	if err != nil {
		return Ruleset{}, err
	}
	row, err := s.q.GetRuleset(ctx, queries.GetRulesetParams{ProjectNumber: proj.ProjectNumber, RulesetID: id})
	switch {
	case notFound(err):
		return Ruleset{}, errNotFound()
	case err != nil:
		return Ruleset{}, internal(err)
	}
	out, err := toRuleset(proj.ProjectID, row)
	if err != nil {
		return Ruleset{}, internal(err)
	}
	return out, nil
}

// ListRulesets lists a project's rulesets, newest first. The listing holds names and times, not
// files, as the Rules API's does: GetRuleset reads a source.
func (s *Service) ListRulesets(ctx context.Context, p *authn.Principal, project string, opt ListOptions) (Page[Ruleset], error) {
	proj, err := s.resolveProject(ctx, s.q, p, project, false)
	if err != nil {
		return Page[Ruleset]{}, err
	}
	size := pageSize(opt.PageSize)
	params := queries.ListRulesetsParams{ProjectNumber: proj.ProjectNumber, PageSize: size + 1}
	if opt.PageToken != "" {
		at, id, err := decodeRulesetToken(opt.PageToken)
		if err != nil {
			return Page[Ruleset]{}, err
		}
		params.AfterTime, params.AfterID = &at, &id
	}
	rows, err := s.q.ListRulesets(ctx, params)
	if err != nil {
		return Page[Ruleset]{}, internal(err)
	}
	var page Page[Ruleset]
	if len(rows) > int(size) {
		rows = rows[:size]
		last := rows[len(rows)-1]
		page.NextPageToken = encodeToken(strconv.FormatInt(last.CreateTime.UnixMicro(), 10) + "/" + last.RulesetID.String())
	}
	page.Items = make([]Ruleset, 0, len(rows))
	for _, r := range rows {
		page.Items = append(page.Items, Ruleset{Name: rulesetName(proj.ProjectID, r.RulesetID), CreateTime: r.CreateTime,
			Service: rules.Service(r.Service)})
	}
	return page, nil
}

// decodeRulesetToken reads a ruleset page token: the create time of the previous page's last
// ruleset, in microseconds as Postgres keeps it, and its id.
func decodeRulesetToken(tok string) (time.Time, uuid.UUID, error) {
	raw, err := decodeToken(tok)
	if err != nil {
		return time.Time{}, uuid.UUID{}, err
	}
	micros, rawID, ok := strings.Cut(raw, "/")
	us, err := strconv.ParseInt(micros, 10, 64)
	if !ok || err != nil {
		return time.Time{}, uuid.UUID{}, errBadPageToken
	}
	id, err := uuid.Parse(rawID)
	if err != nil {
		return time.Time{}, uuid.UUID{}, errBadPageToken
	}
	return time.UnixMicro(us), id, nil
}

// DeleteRuleset deletes a ruleset no release points to. One a release points to, even a release
// published while this runs, is FAILED_PRECONDITION and stays.
func (s *Service) DeleteRuleset(ctx context.Context, p *authn.Principal, project, name string) error {
	return internal(s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, err := s.resolveProject(ctx, q, p, project, false)
		if err != nil {
			return err
		}
		id, err := rulesetID(s.toProject(proj), name)
		if err != nil {
			return err
		}
		full := rulesetName(proj.ProjectID, id)
		inUse := apierr.New(apierr.FailedPrecondition, "The ruleset %s is referenced by a release and cannot be deleted.", full)
		n, err := q.DeleteUnreleasedRuleset(ctx, queries.DeleteUnreleasedRulesetParams{ProjectNumber: proj.ProjectNumber, RulesetID: id})
		if referenced(err) {
			return inUse
		}
		if err != nil {
			return err
		}
		if n == 0 {
			// It does not exist, or a release points to it.
			_, err := q.GetRuleset(ctx, queries.GetRulesetParams{ProjectNumber: proj.ProjectNumber, RulesetID: id})
			switch {
			case notFound(err):
				return errNotFound()
			case err != nil:
				return err
			}
			return inUse
		}
		return s.audit(ctx, q, p, proj.ProjectNumber, "rules.ruleset.delete", full, nil)
	}))
}

// referenced reports whether err is rules_releases_ruleset_fkey refusing a delete: a release
// committed after the delete's snapshot points to the ruleset. ON DELETE RESTRICT fails with
// restrict_violation (23001), a deferred check with foreign_key_violation (23503).
func referenced(err error) bool {
	pg, ok := errors.AsType[*pgconn.PgError](err)
	return ok && (pg.Code == "23001" || pg.Code == "23503")
}

// GetRelease returns one of a project's releases. NOT_FOUND when it does not exist.
func (s *Service) GetRelease(ctx context.Context, p *authn.Principal, project, release string) (Release, error) {
	proj, err := s.resolveProject(ctx, s.q, p, project, false)
	if err != nil {
		return Release{}, err
	}
	pr := s.toProject(proj)
	stored, err := storedRelease(pr, release)
	if err != nil {
		return Release{}, err
	}
	row, err := s.q.GetRelease(ctx, queries.GetReleaseParams{ProjectNumber: proj.ProjectNumber, Name: stored})
	switch {
	case notFound(err):
		return Release{}, errNotFound()
	case err != nil:
		return Release{}, internal(err)
	}
	return toRelease(pr, row), nil
}

// ListReleases lists a project's releases by name, the Storage release under the current
// bucket's name as everywhere else.
func (s *Service) ListReleases(ctx context.Context, p *authn.Principal, project string, opt ListOptions) (Page[Release], error) {
	proj, err := s.resolveProject(ctx, s.q, p, project, false)
	if err != nil {
		return Page[Release]{}, err
	}
	after, err := decodeToken(opt.PageToken)
	if err != nil {
		return Page[Release]{}, err
	}
	size := pageSize(opt.PageSize)
	rows, err := s.q.ListReleases(ctx, queries.ListReleasesParams{ProjectNumber: proj.ProjectNumber, After: after, PageSize: size + 1})
	if err != nil {
		return Page[Release]{}, internal(err)
	}
	var page Page[Release]
	if len(rows) > int(size) {
		rows = rows[:size]
		page.NextPageToken = encodeToken(rows[len(rows)-1].Name)
	}
	pr := s.toProject(proj)
	page.Items = make([]Release, 0, len(rows))
	for _, r := range rows {
		page.Items = append(page.Items, toRelease(pr, r))
	}
	return page, nil
}

// PublishRelease creates a release or points an existing one at another ruleset, and drops the
// release's compiled ruleset from the cache, so the next request sees it.
//
// ruleset is the ruleset's name, projects/{project}/rulesets/{uuid} with the project's id or
// number, or its uuid alone. A ruleset of another project is NOT_FOUND, as one that does not
// exist. The Storage release (firebase.storage/<the project's bucket>) and cloud.firestore take
// rulesets of their own service only.
func (s *Service) PublishRelease(ctx context.Context, p *authn.Principal, project, release, ruleset string) (Release, error) {
	var (
		pr     Project
		stored string
		row    queries.ControlRulesRelease
	)
	err := s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, err := s.resolveProject(ctx, q, p, project, false)
		if err != nil {
			return err
		}
		pr = s.toProject(proj)
		if stored, err = storedRelease(pr, release); err != nil {
			return err
		}
		id, err := rulesetID(pr, ruleset)
		if err != nil {
			return err
		}
		rs, err := q.GetRuleset(ctx, queries.GetRulesetParams{ProjectNumber: proj.ProjectNumber, RulesetID: id})
		switch {
		case notFound(err):
			return errNotFound()
		case err != nil:
			return err
		}
		if want, ok := releaseService(stored); ok && rules.Service(rs.Service) != want {
			return apierr.New(apierr.InvalidArgument, "The release %s takes a %s ruleset; %s is for %s.",
				release, want, rulesetName(pr.ID, id), rs.Service)
		}
		row, err = q.UpsertRelease(ctx, queries.UpsertReleaseParams{ProjectNumber: proj.ProjectNumber, Name: stored, RulesetID: id})
		if pg, ok := errors.AsType[*pgconn.PgError](err); ok && pg.Code == "23503" {
			// DeleteRuleset removed the ruleset after GetRuleset read it: gone, as if never read.
			return errNotFound()
		}
		if err != nil {
			return err
		}
		return s.audit(ctx, q, p, proj.ProjectNumber, "rules.release.publish", shownRelease(pr, stored),
			map[string]any{"ruleset": rulesetName(pr.ID, id)})
	})
	if err != nil {
		return Release{}, internal(err)
	}
	// After the commit: a load that read the old pointer before it cannot cache it now (the
	// cache's epoch moved), and every load from here reads the new one.
	s.compiledCache().forget(releaseKey{pr.Number, stored})
	return toRelease(pr, row), nil
}

// maxCompiles is how many rules compilations of each kind run at once in the process, so that
// requests in parallel cannot multiply what one hostile source costs (the review's SR3-05).
// There are two kinds, each with its own turns:
//
//   - the management's (compiles): the firebaserules API's :test and createRuleset, and the
//     console's validate and publish, which the owner of any project may send at will;
//   - the loads of CompiledRelease (loads), which the services wait for, so that they never
//     queue behind the management's: a load waits at most for other loads, each for one
//     release, and fails its requests at loadTimeout (the review of S7-B1,
//     SR3-05-dataplane-starvation).
//
// One of the largest source takes a fraction of a second and its tree's memory, its literal
// patterns bounded by regex_bytes; the compilations past these wait their turn, or their
// request's end.
func maxCompiles() int { return max(2, runtime.GOMAXPROCS(0)/2) }

// slot waits for one of turns. The caller calls done when its compilation ends.
func slot(ctx context.Context, turns chan struct{}) (done func(), err error) {
	select {
	case turns <- struct{}{}:
		return func() { <-turns }, nil
	case <-ctx.Done():
		return nil, apierr.Wrap(ctx.Err(), apierr.Canceled, "The request was cancelled.")
	}
}

// compileSlot waits for a turn of the management's compilations.
func (s *Service) compileSlot(ctx context.Context) (done func(), err error) {
	return slot(ctx, s.compiles)
}

// compile compiles a rules file for svc under the configured limits, in one of turns.
func (s *Service) compile(ctx context.Context, turns chan struct{}, svc rules.Service, f rules.File) (*rules.Ruleset, rules.Issues, error) {
	done, err := slot(ctx, turns)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	rs, issues := rules.Compile(svc, f, s.opts.RulesLimits)
	return rs, issues, nil
}

// CompileRules compiles a rules file for svc under the configured limits, in a turn of the
// management's compilations (maxCompiles). The issues and the ruleset are rules.Compile's; the
// error is the request's end while it waited for its turn, CANCELLED.
func (s *Service) CompileRules(ctx context.Context, svc rules.Service, f rules.File) (*rules.Ruleset, rules.Issues, error) {
	return s.compile(ctx, s.compiles, svc, f)
}

// CompiledRelease returns the compiled ruleset a project's release points to, for the services
// that evaluate it. A project without its Storage release gets the locked default; a release
// whose ruleset no longer compiles denies everything (ADR-021 §7).
//
// release is the stored name: StorageRelease for Storage, whatever the bucket is called. An error
// is an outage of the control plane, never a denial; the services answer UNAVAILABLE.
func (s *Service) CompiledRelease(ctx context.Context, project int64, release string) (*rules.Ruleset, error) {
	c := s.compiledCache()
	return c.load(ctx, releaseKey{project, release}, func(ctx context.Context) (*rules.Ruleset, int64, error) {
		row, err := s.q.GetReleaseSource(ctx, queries.GetReleaseSourceParams{ProjectNumber: project, Name: release})
		switch {
		case notFound(err):
			rs, err := s.lockedDefault(release)
			return rs, markerWeight, err
		case err != nil:
			return nil, 0, internal(err)
		}
		rs, err := s.compileStored(ctx, project, release, row)
		if err != nil {
			return nil, 0, err
		}
		if rs == nil {
			locked, err := s.lockedDefault(release)
			return locked, markerWeight, err
		}
		return rs, rs.Weight(), nil
	})
}

// compileStored compiles a release's stored ruleset again. A ruleset that no longer compiles
// (the limits or the language changed between two versions of Aruma), or whose service is not
// the release's, is nil, which denies everything, with a warning in the log (ADR-021 §7).
func (s *Service) compileStored(ctx context.Context, project int64, release string, row queries.GetReleaseSourceRow) (*rules.Ruleset, error) {
	var files []storedFile
	if err := json.Unmarshal(row.Files, &files); err != nil {
		return nil, internal(fmt.Errorf("rules: ruleset %s: %w", row.RulesetID, err))
	}
	deny := func(why string, args ...any) {
		s.log.Warn("rules: a release denies everything", append([]any{"project_number", project, "release", release,
			"ruleset", row.RulesetID.String(), "reason", why}, args...)...)
	}
	svc := rules.Service(row.Service)
	if want, ok := releaseService(release); ok && svc != want {
		deny("the ruleset is for another service", "service", row.Service)
		return nil, nil
	}
	if len(files) != 1 {
		deny("the ruleset does not hold one file", "files", len(files))
		return nil, nil
	}
	rs, issues, err := s.compile(ctx, s.loads, svc, rules.File{Name: files[0].Name, Content: files[0].Content})
	if err != nil {
		return nil, internal(err)
	}
	if rs == nil {
		deny("the ruleset no longer compiles", "error", issues.Err())
		return nil, nil
	}
	return rs, nil
}

// The locked defaults: firebase init storage's template word for word (C §2.1), and its
// Firestore counterpart, as a new project in Firebase's locked mode has (ADR-010, ADR-022 §8).
const (
	lockedStorageSource = `rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    match /{allPaths=**} {
      allow read, write: if false;
    }
  }
}
`
	lockedFirestoreSource = `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /{document=**} {
      allow read, write: if false;
    }
  }
}
`
)

// lockedDefault returns the locked ruleset for a release that has none or that denies: the
// release's service's template, or NOT_FOUND for a release no service reads.
func (s *Service) lockedDefault(release string) (*rules.Ruleset, error) {
	svc, ok := releaseService(release)
	if !ok {
		return nil, errNotFound()
	}
	c := s.compiledCache()
	c.lockedOnce.Do(func() {
		for svc, src := range map[rules.Service]string{rules.Storage: lockedStorageSource, rules.Firestore: lockedFirestoreSource} {
			rs, issues := rules.Compile(svc, rules.File{Name: "locked.rules", Content: src}, s.opts.RulesLimits)
			if rs == nil {
				c.lockedErr = fmt.Errorf("rules: the locked default for %s does not compile: %w", svc, issues.Err())
				return
			}
			c.locked[svc] = rs
		}
	})
	if c.lockedErr != nil {
		return nil, internal(c.lockedErr)
	}
	return c.locked[svc], nil
}

// releaseService is the service a stored release name is read by, if any service reads it.
func releaseService(stored string) (rules.Service, bool) {
	switch {
	case stored == StorageRelease:
		return rules.Storage, true
	case stored == firestoreRelease || strings.HasPrefix(stored, firestoreRelease+"/"):
		return rules.Firestore, true
	}
	return "", false
}

// releasePattern is what a release name may hold: the services' names, a bucket's, and the
// custom names the API allows, without spaces, controls or a path that climbs.
var releasePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~/-]{0,255}$`)

// storedRelease maps a release name as the API gives it to the name it is stored under. The
// Storage release of the project's current bucket is StorageRelease; firebase.storage/ with any
// other bucket, firebase.storage/default among them, is NOT_FOUND (ADR-022 §2).
func storedRelease(p Project, release string) (string, error) {
	if !releasePattern.MatchString(release) || strings.Contains(release, "//") || strings.HasSuffix(release, "/") {
		return "", apierr.New(apierr.InvalidArgument, "Invalid release name.")
	}
	if bucket, ok := strings.CutPrefix(release, storageReleasePrefix); ok {
		if bucket != p.StorageBucket {
			return "", errNotFound()
		}
		return StorageRelease, nil
	}
	return release, nil
}

// shownRelease is a stored release's name as the API shows it: the Storage release under the
// project's current bucket, so firebase-tools finds it by its prefix (ADR-022 §2).
func shownRelease(p Project, stored string) string {
	if stored == StorageRelease {
		return storageReleasePrefix + p.StorageBucket
	}
	return stored
}

func rulesetName(projectID string, id uuid.UUID) string {
	return "projects/" + projectID + "/rulesets/" + id.String()
}

// rulesetID reads a ruleset reference: its full name under the project (by id or number), or
// its uuid alone. A name under another project is NOT_FOUND.
func rulesetID(p Project, ref string) (uuid.UUID, error) {
	raw := ref
	if rest, ok := strings.CutPrefix(ref, "projects/"); ok {
		proj, id, ok := strings.Cut(rest, "/rulesets/")
		if !ok {
			return uuid.UUID{}, apierr.New(apierr.InvalidArgument, "Invalid ruleset name.")
		}
		if proj != p.ID && proj != formatNumber(p.Number) {
			return uuid.UUID{}, errNotFound()
		}
		raw = id
	}
	id, err := uuid.Parse(raw)
	if err != nil || id.String() != raw {
		return uuid.UUID{}, apierr.New(apierr.InvalidArgument, "Invalid ruleset name.")
	}
	return id, nil
}

func toRuleset(projectID string, r queries.ControlRuleset) (Ruleset, error) {
	var files []storedFile
	if err := json.Unmarshal(r.Files, &files); err != nil {
		return Ruleset{}, fmt.Errorf("rules: ruleset %s: %w", r.RulesetID, err)
	}
	out := Ruleset{Name: rulesetName(projectID, r.RulesetID), CreateTime: r.CreateTime, Service: rules.Service(r.Service)}
	for _, f := range files {
		out.Files = append(out.Files, rules.File{Name: f.Name, Content: f.Content})
	}
	return out, nil
}

func toRelease(p Project, r queries.ControlRulesRelease) Release {
	return Release{
		Name:        "projects/" + p.ID + "/releases/" + shownRelease(p, r.Name),
		RulesetName: rulesetName(p.ID, r.RulesetID),
		CreateTime:  r.CreateTime,
		UpdateTime:  r.UpdateTime,
	}
}

// The cache of compiled releases (ADR-021 §7): an LRU by weight under
// RulesLimits.CompiledRulesetsBytes, keyed by (project, stored release name). Publishing through
// this service drops its release's entry, so no notice crosses packages; one server, as in
// ADR-019. A release without a ruleset, or whose ruleset denies, is kept as a marker of
// markerWeight that stands for the locked default, which is compiled once and shared.

// markerWeight is what an entry for the locked default weighs: the ruleset is shared, so the
// entry holds its key alone.
const markerWeight = 64

// loadTimeout bounds one load of a release: its read and its compilation.
const loadTimeout = 10 * time.Second

type releaseKey struct {
	project int64
	name    string
}

type releaseCache struct {
	max int64

	mu      sync.Mutex
	used    int64
	epoch   uint64 // moves on every forget: a load begun before it does not cache what it read
	order   *list.List
	entries map[releaseKey]*list.Element
	loading map[releaseKey]*releaseLoad

	lockedOnce sync.Once
	locked     map[rules.Service]*rules.Ruleset
	lockedErr  error
}

type cacheEntry struct {
	key    releaseKey
	rs     *rules.Ruleset
	weight int64
}

// releaseLoad is one load in flight, which the requests for the same release wait for instead
// of each reading and compiling it.
type releaseLoad struct {
	done chan struct{}
	rs   *rules.Ruleset
	err  error
}

// compiledCache is the Service's cache of compiled releases, made on first use.
func (s *Service) compiledCache() *releaseCache {
	s.compiledOnce.Do(func() {
		s.compiled = &releaseCache{
			max: s.opts.RulesLimits.CompiledRulesetsBytes, order: list.New(),
			entries: map[releaseKey]*list.Element{}, loading: map[releaseKey]*releaseLoad{},
			locked: map[rules.Service]*rules.Ruleset{},
		}
	})
	return s.compiled
}

// load returns the cached ruleset for key, or the result of the load in flight for it, starting
// one with fn (which returns the ruleset and its weight) when there is none. The load is every
// waiter's: it runs on its own, under context.WithoutCancel and loadTimeout, so the first
// request's short deadline or cancellation fails no other; each waiter, the first included,
// stops waiting when its own ctx ends.
func (c *releaseCache) load(ctx context.Context, key releaseKey, fn func(context.Context) (*rules.Ruleset, int64, error)) (*rules.Ruleset, error) {
	c.mu.Lock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		rs := el.Value.(*cacheEntry).rs
		c.mu.Unlock()
		return rs, nil
	}
	l, ok := c.loading[key]
	if !ok {
		l = &releaseLoad{done: make(chan struct{})}
		c.loading[key] = l
		go c.run(context.WithoutCancel(ctx), key, l, c.epoch, fn)
	}
	c.mu.Unlock()
	select {
	case <-l.done:
		return l.rs, l.err
	case <-ctx.Done():
		return nil, internal(ctx.Err())
	}
}

// run performs one load and caches its result, unless a forget overtook it.
func (c *releaseCache) run(ctx context.Context, key releaseKey, l *releaseLoad, epoch uint64, fn func(context.Context) (*rules.Ruleset, int64, error)) {
	ctx, cancel := context.WithTimeout(ctx, loadTimeout)
	rs, weight, err := fn(ctx)
	cancel()
	l.rs, l.err = rs, err
	c.mu.Lock()
	if c.loading[key] == l {
		delete(c.loading, key)
	}
	if err == nil && epoch == c.epoch {
		c.put(key, rs, weight)
	}
	c.mu.Unlock()
	close(l.done)
}

// put adds an entry under c.mu, evicting the least recently used ones over the cap. A ruleset
// heavier than the whole cache is not kept.
func (c *releaseCache) put(key releaseKey, rs *rules.Ruleset, weight int64) {
	if weight > c.max {
		return
	}
	if el, ok := c.entries[key]; ok {
		c.remove(el)
	}
	for c.used+weight > c.max && c.order.Len() > 0 {
		c.remove(c.order.Back())
	}
	c.entries[key] = c.order.PushFront(&cacheEntry{key: key, rs: rs, weight: weight})
	c.used += weight
}

func (c *releaseCache) remove(el *list.Element) {
	e := el.Value.(*cacheEntry)
	c.order.Remove(el)
	delete(c.entries, e.key)
	c.used -= e.weight
}

// forget drops a release's entry and any load in flight for it, so the next request reads the
// database again.
func (c *releaseCache) forget(key releaseKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	delete(c.loading, key)
	if el, ok := c.entries[key]; ok {
		c.remove(el)
	}
}

// forgetProject drops every entry of a project, when it is purged.
func (c *releaseCache) forgetProject(project int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	for key := range c.loading {
		if key.project == project {
			delete(c.loading, key)
		}
	}
	for key, el := range c.entries {
		if key.project == project {
			c.remove(el)
		}
	}
}
