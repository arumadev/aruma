package core

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// Rulesets and releases on Postgres (ADR-021 §7, ADR-022 §2): the logical Storage release, the
// locked default, the cache, isolation between projects, and the purge.

const rulesDomain = "aruma.test"

func rulesService(t *testing.T, pool *pgxpool.Pool, domain string, logs *bytes.Buffer) *Service {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	if logs != nil {
		log = slog.New(slog.NewTextHandler(logs, nil))
	}
	return New(pool, nil, nil, Options{ProjectDomain: domain, PublicURL: "http://127.0.0.1", RulesLimits: rules.DefaultLimits()}, log)
}

// rulesOwner makes an account and a project it owns.
func rulesOwner(t *testing.T, s *Service, pool *pgxpool.Pool, email, projectID string) (*authn.Principal, Project) {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO control.accounts (id, email, password_hash) VALUES ($1, $2, 'x')`, id, email); err != nil {
		t.Fatal(err)
	}
	p := &authn.Principal{Kind: authn.KindAccount, AccountID: id}
	proj, err := s.CreateProject(context.Background(), p, NewProject{ID: projectID, DisplayName: projectID})
	if err != nil {
		t.Fatal(err)
	}
	return p, proj
}

func storageFile(cond string) []rules.File {
	return []rules.File{{Name: "storage.rules", Content: "rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n    match /{all=**} {\n      allow read: if " + cond + ";\n    }\n  }\n}\n"}}
}

// allows evaluates a get of a.png on the project's bucket under rs.
func allows(t *testing.T, rs *rules.Ruleset, bucket string) bool {
	t.Helper()
	if rs == nil {
		t.Fatal("no ruleset")
	}
	d, err := rs.Evaluate(context.Background(), &rules.Request{
		Method: rules.MethodGet, Path: rules.Literals("b", bucket, "o", "a.png"),
		Variables: map[string]rules.Value{"request": rules.Map(nil), "resource": rules.Null()},
		Documents: rules.NoDocuments{}, Budget: rules.NewBudget(), Inflight: rules.NewInflight(1 << 20),
	})
	if err != nil {
		t.Fatal(err)
	}
	return d.Allow
}

func publish(t *testing.T, s *Service, p *authn.Principal, proj Project, release string, files []rules.File) (Ruleset, Release) {
	t.Helper()
	ctx := context.Background()
	rs, issues, err := s.CreateRuleset(ctx, p, proj.ID, rules.Storage, files)
	if err != nil {
		t.Fatalf("CreateRuleset: %v %v", err, issues)
	}
	rel, err := s.PublishRelease(ctx, p, proj.ID, release, rs.Name)
	if err != nil {
		t.Fatalf("PublishRelease: %v", err)
	}
	return rs, rel
}

func wantCode(t *testing.T, what string, err error, code apierr.Code) {
	t.Helper()
	var e *apierr.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Errorf("%s: %v, want %v", what, err, code)
	}
}

func TestStorageReleaseIsStoredUnderItsLogicalName(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	ctx := context.Background()
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	bucket := proj.StorageBucket

	// A project without a release has the locked default, which denies.
	locked, err := s.CompiledRelease(ctx, proj.Number, StorageRelease)
	if err != nil || allows(t, locked, bucket) {
		t.Fatalf("before any release: %v, allows %v", err, allows(t, locked, bucket))
	}
	if _, err := s.GetRelease(ctx, p, proj.ID, "firebase.storage/"+bucket); err == nil {
		t.Error("GetRelease finds a release never published")
	}

	rs, rel := publish(t, s, p, proj, "firebase.storage/"+bucket, storageFile("true"))
	if !strings.HasPrefix(rs.Name, "projects/chat-app/rulesets/") || len(rs.Files) != 1 || rs.CreateTime.IsZero() {
		t.Errorf("ruleset %+v", rs)
	}
	if rel.Name != "projects/chat-app/releases/firebase.storage/"+bucket || rel.RulesetName != rs.Name {
		t.Errorf("release %+v", rel)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT name FROM control.rules_releases WHERE project_number = $1`, proj.Number).Scan(&stored); err != nil || stored != StorageRelease {
		t.Errorf("stored as %q, %v; want %s", stored, err, StorageRelease)
	}
	got, err := s.GetRelease(ctx, p, proj.ID, "firebase.storage/"+bucket)
	if err != nil || got != rel {
		t.Errorf("GetRelease = %+v, %v; want %+v", got, err, rel)
	}
	// By number too.
	if _, err := s.GetRelease(ctx, p, formatNumber(proj.Number), "firebase.storage/"+bucket); err != nil {
		t.Errorf("GetRelease by number: %v", err)
	}
	// The locked default was cached; publishing drops it, so the next request sees the release.
	if rs, err := s.CompiledRelease(ctx, proj.Number, StorageRelease); err != nil || !allows(t, rs, bucket) {
		t.Errorf("after publishing: %v", err)
	}
	// Publishing again moves the pointer; update_time moves, create_time stays.
	_, rel2 := publish(t, s, p, proj, "firebase.storage/"+bucket, storageFile("false"))
	if rel2.CreateTime != rel.CreateTime || !rel2.UpdateTime.After(rel.UpdateTime) || rel2.RulesetName == rel.RulesetName {
		t.Errorf("republished: %+v after %+v", rel2, rel)
	}
	if rs, _ := s.CompiledRelease(ctx, proj.Number, StorageRelease); allows(t, rs, bucket) {
		t.Error("the cache serves the old ruleset after a publish")
	}
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM control.audit_log WHERE project_number = $1 AND action IN ('rules.ruleset.create', 'rules.release.publish')`, proj.Number).Scan(&audits); err != nil || audits != 4 {
		t.Errorf("%d audit entries, %v; want 4", audits, err)
	}
}

func TestReleaseNames(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	ctx := context.Background()
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	storage, _, err := s.CreateRuleset(ctx, p, proj.ID, rules.Storage, storageFile("true"))
	if err != nil {
		t.Fatal(err)
	}
	firestore, _, err := s.CreateRuleset(ctx, p, proj.ID, rules.Firestore, []rules.File{{Name: "firestore.rules",
		Content: "rules_version = '2';\nservice cloud.firestore {\n  match /databases/{d}/documents {\n    match /{x=**} { allow read: if true; }\n  }\n}\n"}})
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(storage.Name, "projects/chat-app/rulesets/")
	for _, c := range []struct {
		release, ruleset string
		code             apierr.Code // OK when it publishes
	}{
		{"firebase.storage/" + proj.StorageBucket, id, apierr.OK}, // the uuid alone
		{"firebase.storage/" + proj.StorageBucket, "projects/" + formatNumber(proj.Number) + "/rulesets/" + id, apierr.OK},
		{"firebase.storage/default", storage.Name, apierr.NotFound},                  // the logical name is not the API's
		{"firebase.storage/other-app." + rulesDomain, storage.Name, apierr.NotFound}, // another bucket
		{"firebase.storage/chat-app.appspot.com", storage.Name, apierr.NotFound},
		{"firebase.storage/" + proj.StorageBucket, firestore.Name, apierr.InvalidArgument}, // another service's ruleset
		{"cloud.firestore", storage.Name, apierr.InvalidArgument},
		{"cloud.firestore", firestore.Name, apierr.OK},
		{"custom-release", storage.Name, apierr.OK},
		{"firebase.storage/" + proj.StorageBucket, "projects/other-app/rulesets/" + id, apierr.NotFound}, // under another project
		{"firebase.storage/" + proj.StorageBucket, uuid.NewString(), apierr.NotFound},
		{"firebase.storage/" + proj.StorageBucket, "projects/chat-app/rulesets/" + strings.ToUpper(id), apierr.InvalidArgument},
		{"firebase.storage/" + proj.StorageBucket, "rulesets/" + id, apierr.InvalidArgument},
		{"", storage.Name, apierr.InvalidArgument},
		{"a b", storage.Name, apierr.InvalidArgument},
		{"x/", storage.Name, apierr.InvalidArgument},
		{"x//y", storage.Name, apierr.InvalidArgument},
		{strings.Repeat("x", 257), storage.Name, apierr.InvalidArgument},
	} {
		_, err := s.PublishRelease(ctx, p, proj.ID, c.release, c.ruleset)
		if c.code == apierr.OK {
			if err != nil {
				t.Errorf("PublishRelease(%q, %q): %v", c.release, c.ruleset, err)
			}
			continue
		}
		wantCode(t, fmt.Sprintf("PublishRelease(%q, %q)", c.release, c.ruleset), err, c.code)
	}
	if _, err := s.GetRelease(ctx, p, proj.ID, "firebase.storage/default"); err == nil {
		t.Error("GetRelease answers the logical name")
	}
	if rel, err := s.GetRelease(ctx, p, proj.ID, "cloud.firestore"); err != nil || rel.Name != "projects/chat-app/releases/cloud.firestore" {
		t.Errorf("cloud.firestore: %+v, %v", rel, err)
	}
	// A project without its Firestore release has Firestore's locked default; a name no service
	// reads has none.
	var n int
	if err := pool.QueryRow(ctx, `DELETE FROM control.rules_releases WHERE name = 'cloud.firestore' RETURNING 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	s.compiledCache().forget(releaseKey{proj.Number, firestoreRelease})
	if rs, err := s.CompiledRelease(ctx, proj.Number, firestoreRelease); err != nil || rs.Service() != rules.Firestore {
		t.Errorf("Firestore without a release: %v, %v", rs, err)
	}
	if _, err := s.CompiledRelease(ctx, proj.Number, "nobody.reads"); err == nil {
		t.Error("a missing release no service reads has a ruleset")
	}
}

func TestCreateRulesetRefusals(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	ctx := context.Background()
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	count := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM control.rulesets`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	_, issues, err := s.CreateRuleset(ctx, p, proj.ID, rules.Storage, storageFile("true &&"))
	wantCode(t, "a syntax error", err, apierr.InvalidArgument)
	if issues.Err() == nil || issues[0].Position == nil {
		t.Errorf("issues %v", issues)
	}
	_, _, err = s.CreateRuleset(ctx, p, proj.ID, rules.Storage, append(storageFile("true"), storageFile("true")...))
	wantCode(t, "two files", err, apierr.InvalidArgument)
	_, _, err = s.CreateRuleset(ctx, p, proj.ID, rules.Storage, nil)
	wantCode(t, "no file", err, apierr.InvalidArgument)
	_, _, err = s.CreateRuleset(ctx, p, proj.ID, rules.Storage, storageFile("true // \x00"))
	wantCode(t, "NUL", err, apierr.InvalidArgument)
	_, _, err = s.CreateRuleset(ctx, p, proj.ID, rules.Firestore, storageFile("true"))
	wantCode(t, "a Storage file for Firestore", err, apierr.InvalidArgument)
	if n := count(); n != 0 {
		t.Errorf("%d rulesets saved", n)
	}
	// A warning does not refuse, and comes back.
	_, issues, err = s.CreateRuleset(ctx, p, proj.ID, rules.Storage, []rules.File{{Name: "storage.rules",
		Content: "service firebase.storage {\n  match /b/{bucket}/o {\n    match /{x} { allow read: if true; }\n  }\n}\n"}})
	if err != nil || len(issues) == 0 || issues.Err() != nil {
		t.Errorf("rules_version 1: %v, %v", err, issues)
	}
	// 2,500 per project.
	if _, err := pool.Exec(ctx, `INSERT INTO control.rulesets (project_number, service, files)
		SELECT $1, 'firebase.storage', '[]' FROM generate_series(1, $2)`, proj.Number, s.opts.RulesLimits.RulesetsPerProject-1-count()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateRuleset(ctx, p, proj.ID, rules.Storage, storageFile("true")); err != nil {
		t.Errorf("the 2,500th: %v", err)
	}
	_, _, err = s.CreateRuleset(ctx, p, proj.ID, rules.Storage, storageFile("true"))
	wantCode(t, "the 2,501st", err, apierr.ResourceExhausted)
}

// A ruleset saved by one version of Aruma that the next no longer compiles denies everything,
// with a warning, as RTDB's stored rules do (ADR-021 §7).
func TestStoredRulesetThatNoLongerCompilesDenies(t *testing.T) {
	pool := storetest.NewPool(t)
	var logs bytes.Buffer
	s := rulesService(t, pool, rulesDomain, &logs)
	ctx := context.Background()
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	_, _ = publish(t, s, p, proj, "firebase.storage/"+proj.StorageBucket, storageFile("true"))
	for _, files := range []string{
		`[{"name": "storage.rules", "content": "service firebase.storage { match /b/{bucket}/o { allow read: if ; } }"}]`,
		`[]`,
		`[{"name": "firestore.rules", "content": "service cloud.firestore { match /databases/{d}/documents { match /{x=**} { allow read: if true; } } }"}]`,
	} {
		if _, err := pool.Exec(ctx, `UPDATE control.rulesets SET files = $2 WHERE project_number = $1`, proj.Number, files); err != nil {
			t.Fatal(err)
		}
		s.compiledCache().forget(releaseKey{proj.Number, StorageRelease})
		rs, err := s.CompiledRelease(ctx, proj.Number, StorageRelease)
		if err != nil || allows(t, rs, proj.StorageBucket) {
			t.Errorf("%s: %v, allows", files, err)
		}
	}
	if !strings.Contains(logs.String(), "a release denies everything") {
		t.Errorf("no warning: %s", logs.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE control.rulesets SET service = 'cloud.firestore' WHERE project_number = $1`, proj.Number); err != nil {
		t.Fatal(err)
	}
	s.compiledCache().forget(releaseKey{proj.Number, StorageRelease})
	if rs, err := s.CompiledRelease(ctx, proj.Number, StorageRelease); err != nil || rs.Service() != rules.Storage || allows(t, rs, proj.StorageBucket) {
		t.Errorf("a Firestore ruleset under the Storage release: %v", err)
	}
}

// ADR-022 §2: changing project_domain renames the bucket, not the rules.
func TestProjectDomainChangeKeepsTheRules(t *testing.T) {
	pool := storetest.NewPool(t)
	before := rulesService(t, pool, "old.example.com", nil)
	ctx := context.Background()
	p, proj := rulesOwner(t, before, pool, "o@example.com", "chat-app")
	_, _ = publish(t, before, p, proj, "firebase.storage/chat-app.old.example.com", storageFile("true"))

	after := rulesService(t, pool, "new.example.com", nil)
	rs, err := after.CompiledRelease(ctx, proj.Number, StorageRelease)
	if err != nil || !allows(t, rs, "chat-app.new.example.com") {
		t.Errorf("the rules after the change: %v", err)
	}
	rel, err := after.GetRelease(ctx, p, proj.ID, "firebase.storage/chat-app.new.example.com")
	if err != nil || rel.Name != "projects/chat-app/releases/firebase.storage/chat-app.new.example.com" {
		t.Errorf("under the new bucket: %+v, %v", rel, err)
	}
	if _, err := after.GetRelease(ctx, p, proj.ID, "firebase.storage/chat-app.old.example.com"); err == nil {
		t.Error("the old bucket's name still answers")
	}
}

// One project's owner reaches neither another's rulesets nor its releases, and a release never
// points at another project's ruleset, not even written in SQL (ADR-017, ADR-021 §7).
func TestRulesIsolation(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	ctx := context.Background()
	pa, a := rulesOwner(t, s, pool, "a@example.com", "alpha-app")
	pb, b := rulesOwner(t, s, pool, "b@example.com", "bravo-app")
	ra, _ := publish(t, s, pa, a, "firebase.storage/"+a.StorageBucket, storageFile("true"))
	rb, _, err := s.CreateRuleset(ctx, pb, b.ID, rules.Storage, storageFile("false"))
	if err != nil {
		t.Fatal(err)
	}
	idA := strings.TrimPrefix(ra.Name, "projects/alpha-app/rulesets/")
	for name, err := range map[string]error{
		"A creates on B":           third(s.CreateRuleset(ctx, pa, b.ID, rules.Storage, storageFile("true"))),
		"A reads B's release":      second(s.GetRelease(ctx, pa, b.ID, "firebase.storage/"+b.StorageBucket)),
		"A publishes on B":         second(s.PublishRelease(ctx, pa, b.ID, "firebase.storage/"+b.StorageBucket, ra.Name)),
		"A publishes B's ruleset":  second(s.PublishRelease(ctx, pa, a.ID, "firebase.storage/"+a.StorageBucket, rb.Name)),
		"B points at A's by uuid":  second(s.PublishRelease(ctx, pb, b.ID, "firebase.storage/"+b.StorageBucket, idA)),
		"B points at A's by name":  second(s.PublishRelease(ctx, pb, b.ID, "firebase.storage/"+b.StorageBucket, ra.Name)),
		"B publishes A's bucket":   second(s.PublishRelease(ctx, pb, b.ID, "firebase.storage/"+a.StorageBucket, rb.Name)),
		"B reads A's by B's ref":   second(s.GetRelease(ctx, pb, b.ID, "firebase.storage/"+a.StorageBucket)),
		"by A's number on B's ref": second(s.GetRelease(ctx, pb, formatNumber(a.Number), "firebase.storage/"+a.StorageBucket)),
	} {
		wantCode(t, name, err, apierr.NotFound)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO control.rules_releases (project_number, name, ruleset_id) VALUES ($1, 'x', $2)`, b.Number, idA); err == nil {
		t.Error("the schema lets a release point at another project's ruleset")
	}
	// B's bucket keeps the locked default; A's rules are A's alone.
	if rs, err := s.CompiledRelease(ctx, b.Number, StorageRelease); err != nil || allows(t, rs, b.StorageBucket) {
		t.Errorf("B's bucket under A's rules: %v", err)
	}
	if rs, err := s.CompiledRelease(ctx, a.Number, StorageRelease); err != nil || !allows(t, rs, a.StorageBucket) {
		t.Errorf("A's rules: %v", err)
	}
}

func second[T any](_ T, err error) error { return err }

func third[T, U any](_ T, _ U, err error) error { return err }

// PurgeExpired removes a project's releases and rulesets with its apps and keys, a project with
// rulesets alone included, and drops their compiled entries (ADR-022 §2).
func TestPurgeExpiredRemovesTheRules(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	ctx := context.Background()
	pa, a := rulesOwner(t, s, pool, "a@example.com", "alpha-app")
	pb, b := rulesOwner(t, s, pool, "b@example.com", "bravo-app")
	for _, x := range []struct {
		p    *authn.Principal
		proj Project
	}{{pa, a}, {pb, b}} {
		_, _ = publish(t, s, x.p, x.proj, "firebase.storage/"+x.proj.StorageBucket, storageFile("true"))
		if _, _, err := s.CreateRuleset(ctx, x.p, x.proj.ID, rules.Storage, storageFile("false")); err != nil {
			t.Fatal(err)
		}
		if rs, _ := s.CompiledRelease(ctx, x.proj.Number, StorageRelease); !allows(t, rs, x.proj.StorageBucket) {
			t.Fatal("not published")
		}
	}
	if _, err := s.DeleteProject(ctx, pa, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE control.projects SET expire_time = now() - interval '1 second' WHERE project_number = $1`, a.Number); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PurgeExpired(ctx); err != nil || n != 1 {
		t.Fatalf("PurgeExpired = %d, %v", n, err)
	}
	rows := func(project int64) (n int) {
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM control.rulesets WHERE project_number = $1)
			+ (SELECT count(*) FROM control.rules_releases WHERE project_number = $1)`, project).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := rows(a.Number); n != 0 {
		t.Errorf("%d rules rows left for the purged project", n)
	}
	if n := rows(b.Number); n != 3 {
		t.Errorf("the other project has %d rules rows, want 3", n)
	}
	if rs, err := s.CompiledRelease(ctx, a.Number, StorageRelease); err != nil || allows(t, rs, a.StorageBucket) {
		t.Errorf("the purged project's cached rules: %v", err)
	}
	if rs, _ := s.CompiledRelease(ctx, b.Number, StorageRelease); !allows(t, rs, b.StorageBucket) {
		t.Error("the other project lost its rules")
	}
	// Nothing left to purge: the project is not listed again.
	if n, err := s.PurgeExpired(ctx); err != nil || n != 0 {
		t.Errorf("a second PurgeExpired = %d, %v", n, err)
	}
}

// A release published while requests run: once PublishRelease returns, no request is served
// the ruleset it replaced, from the database or from the cache.
func TestPublishWhileRequestsRun(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	ctx := context.Background()
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	deny, _, err := s.CreateRuleset(ctx, p, proj.ID, rules.Storage, storageFile("false"))
	if err != nil {
		t.Fatal(err)
	}
	allow, _, err := s.CreateRuleset(ctx, p, proj.ID, rules.Storage, storageFile("true"))
	if err != nil {
		t.Fatal(err)
	}
	// seq is odd while a publish of the denying ruleset may be under way, and even once the
	// allowing one is published: a request that saw one even value before and after it must
	// have the allowing ruleset.
	var seq atomic.Int64
	var stale, checked atomic.Int32
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				before := seq.Load()
				rs, err := s.CompiledRelease(ctx, proj.Number, StorageRelease)
				if err != nil {
					t.Error(err)
					return
				}
				if before > 0 && before%2 == 0 && seq.Load() == before {
					checked.Add(1)
					if !allows(t, rs, proj.StorageBucket) {
						stale.Add(1)
					}
				}
			}
		}()
	}
	for range 20 {
		seq.Add(1)
		if _, err := s.PublishRelease(ctx, p, proj.ID, "firebase.storage/"+proj.StorageBucket, deny.Name); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PublishRelease(ctx, p, proj.ID, "firebase.storage/"+proj.StorageBucket, allow.Name); err != nil {
			t.Fatal(err)
		}
		seq.Add(1)
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	if n := stale.Load(); n > 0 || checked.Load() == 0 {
		t.Errorf("%d of %d requests saw the replaced ruleset after the publish returned", n, checked.Load())
	}
}

// The cache is an LRU by weight under compiled_rulesets_bytes (ADR-021 §4, §7).
func TestReleaseCacheIsBoundedByWeight(t *testing.T) {
	ctx := context.Background()
	c := &releaseCache{max: 100, order: list.New(), entries: map[releaseKey]*list.Element{}, loading: map[releaseKey]*releaseLoad{}}
	rs := &rules.Ruleset{}
	var loads atomic.Int32
	get := func(project int64, weight int64) {
		t.Helper()
		if _, err := c.load(ctx, releaseKey{project, StorageRelease}, func(context.Context) (*rules.Ruleset, int64, error) {
			loads.Add(1)
			return rs, weight, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	get(1, 40)
	get(2, 40)
	get(1, 40) // a hit: 1 is now the most recent
	if loads.Load() != 2 || c.used != 80 {
		t.Fatalf("loads %d, used %d", loads.Load(), c.used)
	}
	get(3, 40) // evicts 2, the least recently used
	if _, ok := c.entries[releaseKey{2, StorageRelease}]; ok || c.used != 80 {
		t.Errorf("after an eviction: used %d, entries %v", c.used, c.entries)
	}
	if _, ok := c.entries[releaseKey{1, StorageRelease}]; !ok {
		t.Error("the most recent entry was evicted")
	}
	get(4, 101) // heavier than the whole cache: served, not kept
	if _, ok := c.entries[releaseKey{4, StorageRelease}]; ok || c.used != 80 {
		t.Errorf("a ruleset over the cap is kept: used %d", c.used)
	}
	// A load that began before a forget does not cache what it read.
	_, err := c.load(ctx, releaseKey{5, StorageRelease}, func(context.Context) (*rules.Ruleset, int64, error) {
		c.forget(releaseKey{5, StorageRelease})
		return rs, 1, nil
	})
	if _, ok := c.entries[releaseKey{5, StorageRelease}]; err != nil || ok {
		t.Error("a load overtaken by a forget is cached")
	}
	c.forgetProject(1)
	if _, ok := c.entries[releaseKey{1, StorageRelease}]; ok || c.used != 40 {
		t.Errorf("forgetProject: used %d", c.used)
	}
}

// Requests for one release while it loads wait for that load instead of each reading and
// compiling it.
func TestReleaseCacheLoadsOnce(t *testing.T) {
	ctx := context.Background()
	c := &releaseCache{max: 1 << 20, order: list.New(), entries: map[releaseKey]*list.Element{}, loading: map[releaseKey]*releaseLoad{}}
	rs := &rules.Ruleset{}
	release := make(chan struct{})
	var loads atomic.Int32
	key := releaseKey{1, StorageRelease}
	load := func() {
		got, err := c.load(ctx, key, func(context.Context) (*rules.Ruleset, int64, error) {
			loads.Add(1)
			<-release
			return rs, 10, nil
		})
		if err != nil || got != rs {
			t.Errorf("load = %v, %v", got, err)
		}
	}
	var wg sync.WaitGroup
	wg.Go(load)
	for inFlight := false; !inFlight; {
		c.mu.Lock()
		_, inFlight = c.loading[key]
		c.mu.Unlock()
		runtime.Gosched()
	}
	for range 15 {
		wg.Go(load)
	}
	close(release)
	wg.Wait()
	if n := loads.Load(); n != 1 {
		t.Errorf("%d loads, want 1", n)
	}
	// A service's cache follows its limits, and is one.
	s := &Service{opts: Options{RulesLimits: rules.DefaultLimits()}}
	if got := s.compiledCache().max; got != rules.DefaultLimits().CompiledRulesetsBytes {
		t.Errorf("the cache's cap is %d", got)
	}
	if s.compiledCache() != s.compiledCache() {
		t.Error("a service has two caches")
	}
}

// The shared load is every waiter's: the first request's short deadline ends its own wait, not
// the load the others joined, and the result is cached.
func TestReleaseCacheLoadOutlivesItsFirstRequest(t *testing.T) {
	c := &releaseCache{max: 1 << 20, order: list.New(), entries: map[releaseKey]*list.Element{}, loading: map[releaseKey]*releaseLoad{}}
	rs := &rules.Ruleset{}
	key := releaseKey{1, StorageRelease}
	release := make(chan struct{})
	var stopped atomic.Bool
	fn := func(ctx context.Context) (*rules.Ruleset, int64, error) {
		select {
		case <-release:
		case <-ctx.Done():
			stopped.Store(true)
			return nil, 0, ctx.Err()
		}
		return rs, 10, nil
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	first := make(chan error, 1)
	go func() {
		_, err := c.load(short, key, fn)
		first <- err
	}()
	for inFlight := false; !inFlight; {
		c.mu.Lock()
		_, inFlight = c.loading[key]
		c.mu.Unlock()
		runtime.Gosched()
	}
	second := make(chan *rules.Ruleset, 1)
	go func() {
		got, err := c.load(context.Background(), key, func(context.Context) (*rules.Ruleset, int64, error) {
			t.Error("a second load started")
			return nil, 0, nil
		})
		if err != nil {
			t.Errorf("the second request: %v", err)
		}
		second <- got
	}()
	if err := <-first; !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the first request's own deadline: %v", err)
	}
	close(release)
	if got := <-second; got != rs {
		t.Errorf("the second request got %v", got)
	}
	if stopped.Load() {
		t.Error("the load stopped with the first request")
	}
	c.mu.Lock()
	_, cached := c.entries[key]
	c.mu.Unlock()
	if !cached {
		t.Error("the load's result is not cached")
	}
}
