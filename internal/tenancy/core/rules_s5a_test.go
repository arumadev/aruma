package core

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// The listings and the deletion the firebaserules API needs (S5-A): GetRuleset, ListRulesets,
// DeleteRuleset and ListReleases, on Postgres.

func TestGetRulesetByEveryName(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	ctx := t.Context()
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	other, _ := rulesOwner(t, s, pool, "x@example.com", "other-app")
	files := storageFile("request.auth != null")
	rs, _, err := s.CreateRuleset(ctx, p, proj.ID, rules.Storage, files)
	if err != nil {
		t.Fatal(err)
	}
	id := rs.Name[strings.LastIndex(rs.Name, "/")+1:]
	for _, name := range []string{rs.Name, id, "projects/" + strconv.FormatInt(proj.Number, 10) + "/rulesets/" + id} {
		got, err := s.GetRuleset(ctx, p, proj.ID, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Name != rs.Name || !got.CreateTime.Equal(rs.CreateTime) || !slices.Equal(got.Files, files) {
			t.Errorf("%s: %+v", name, got)
		}
	}
	for _, c := range []struct {
		name string
		code apierr.Code
	}{
		{"projects/other-app/rulesets/" + id, apierr.NotFound},
		{"0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b", apierr.NotFound},
		{"not-a-uuid", apierr.InvalidArgument},
		{strings.ToUpper(id), apierr.InvalidArgument},
	} {
		_, err := s.GetRuleset(ctx, p, proj.ID, c.name)
		wantCode(t, c.name, err, c.code)
	}
	_, err = s.GetRuleset(ctx, other, proj.ID, rs.Name)
	wantCode(t, "another account", err, apierr.NotFound)
	_, err = s.GetRuleset(ctx, other, "other-app", id)
	wantCode(t, "through another project", err, apierr.NotFound)
}

func TestListRulesetsNewestFirstInPages(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	ctx := t.Context()
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	other, otherProj := rulesOwner(t, s, pool, "x@example.com", "other-app")
	if _, _, err := s.CreateRuleset(ctx, other, otherProj.ID, rules.Storage, storageFile("true")); err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := range 7 {
		rs, _, err := s.CreateRuleset(ctx, p, proj.ID, rules.Storage, storageFile("request.time.toMillis() > "+strconv.Itoa(i)))
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, rs.Name)
	}
	// Two rulesets saved in the same microsecond still page apart, by id.
	if _, err := pool.Exec(ctx, `UPDATE control.rulesets SET create_time = (SELECT create_time FROM control.rulesets
		WHERE ruleset_id = $1) WHERE ruleset_id = $2`, want[3][len(want[3])-36:], want[4][len(want[4])-36:]); err != nil {
		t.Fatal(err)
	}

	var got []string
	var times []string
	tok := ""
	for pages := 0; ; pages++ {
		page, err := s.ListRulesets(ctx, p, proj.ID, ListOptions{PageSize: 3, PageToken: tok})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 3 || pages > 3 {
			t.Fatalf("page %d: %d items", pages, len(page.Items))
		}
		for _, rs := range page.Items {
			if rs.Files != nil || rs.CreateTime.IsZero() {
				t.Errorf("listed %+v: a listing holds no files and a time", rs)
			}
			got = append(got, rs.Name)
			times = append(times, rs.CreateTime.UTC().Format("2006-01-02T15:04:05.000000Z"))
		}
		if tok = page.NextPageToken; tok == "" {
			break
		}
	}
	slices.Sort(got)
	sorted := slices.Clone(want)
	slices.Sort(sorted)
	if !slices.Equal(got, sorted) {
		t.Fatalf("listed %v, want the 7 of the project: %v", got, want)
	}
	if !slices.IsSortedFunc(times, func(a, b string) int { return strings.Compare(b, a) }) {
		t.Errorf("not newest first: %v", times)
	}
	_, err := s.ListRulesets(ctx, p, proj.ID, ListOptions{PageToken: "garbage!"})
	wantCode(t, "a bad token", err, apierr.InvalidArgument)
	_, err = s.ListRulesets(ctx, p, proj.ID, ListOptions{PageToken: encodeToken("12/nope")})
	wantCode(t, "a token without a uuid", err, apierr.InvalidArgument)
	_, err = s.ListRulesets(ctx, other, proj.ID, ListOptions{})
	wantCode(t, "another account", err, apierr.NotFound)
}

func TestDeleteRulesetKeepsWhatAReleaseUses(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	ctx := t.Context()
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	other, _ := rulesOwner(t, s, pool, "x@example.com", "other-app")
	released, _ := publish(t, s, p, proj, "firebase.storage/"+proj.StorageBucket, storageFile("true"))
	spare, _, err := s.CreateRuleset(ctx, p, proj.ID, rules.Storage, storageFile("false"))
	if err != nil {
		t.Fatal(err)
	}

	wantCode(t, "a released ruleset", s.DeleteRuleset(ctx, p, proj.ID, released.Name), apierr.FailedPrecondition)
	wantCode(t, "another account", s.DeleteRuleset(ctx, other, proj.ID, spare.Name), apierr.NotFound)
	wantCode(t, "through another project", s.DeleteRuleset(ctx, other, "other-app", spare.Name), apierr.NotFound)
	if err := s.DeleteRuleset(ctx, p, proj.ID, spare.Name); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetRuleset(ctx, p, proj.ID, spare.Name)
	wantCode(t, "after the delete", err, apierr.NotFound)
	wantCode(t, "a second delete", s.DeleteRuleset(ctx, p, proj.ID, spare.Name), apierr.NotFound)
	if _, err := s.GetRuleset(ctx, p, proj.ID, released.Name); err != nil {
		t.Errorf("the released ruleset: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM control.audit_log WHERE action = 'rules.ruleset.delete' AND target = $1`, spare.Name).Scan(&n); err != nil || n != 1 {
		t.Errorf("audit rows for the delete: %d %v", n, err)
	}
}

// A delete and a publish of the same ruleset race: one of them wins, never both, and the delete
// never fails for another reason.
func TestDeleteRulesetRacesPublish(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	ctx := t.Context()
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	release := "firebase.storage/" + proj.StorageBucket
	publish(t, s, p, proj, release, storageFile("false"))
	deleted, published := 0, 0
	for i := range 20 {
		rs, _, err := s.CreateRuleset(ctx, p, proj.ID, rules.Storage, storageFile("request.time.toMillis() > "+strconv.Itoa(i)))
		if err != nil {
			t.Fatal(err)
		}
		var (
			wg             sync.WaitGroup
			delErr, pubErr error
		)
		wg.Go(func() { delErr = s.DeleteRuleset(ctx, p, proj.ID, rs.Name) })
		wg.Go(func() { _, pubErr = s.PublishRelease(ctx, p, proj.ID, release, rs.Name) })
		wg.Wait()
		switch {
		case delErr == nil && pubErr == nil:
			t.Fatalf("round %d: both the delete and the publish succeeded", i)
		case delErr == nil:
			deleted++
			wantCode(t, "the losing publish", pubErr, apierr.NotFound)
		case pubErr == nil:
			published++
			wantCode(t, "the losing delete", delErr, apierr.FailedPrecondition)
		default:
			t.Fatalf("round %d: delete %v, publish %v", i, delErr, pubErr)
		}
		rel, err := s.GetRelease(ctx, p, proj.ID, release)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetRuleset(ctx, p, proj.ID, rel.RulesetName); err != nil {
			t.Fatalf("round %d: the release points at %s: %v", i, rel.RulesetName, err)
		}
	}
	t.Logf("deleted %d, published %d", deleted, published)
}

// A release committed after the delete's snapshot, which NOT EXISTS cannot see: the foreign key
// refuses the delete, and the answer is still FAILED_PRECONDITION.
func TestDeleteRulesetWaitsForAPublishInFlight(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	ctx := t.Context()
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	rs, _, err := s.CreateRuleset(ctx, p, proj.ID, rules.Storage, storageFile("true"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO control.rules_releases (project_number, name, ruleset_id) VALUES ($1, $2, $3)`,
		proj.Number, StorageRelease, rs.Name[len(rs.Name)-36:]); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.DeleteRuleset(ctx, p, proj.ID, rs.Name) }()
	// The delete waits on the row the uncommitted release locks.
	waitFor(t, func() bool {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE '%DeleteUnreleasedRuleset%'`).Scan(&n)
		return n > 0
	})
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wantCode(t, "the delete", <-done, apierr.FailedPrecondition)
	if _, err := s.GetRuleset(ctx, p, proj.ID, rs.Name); err != nil {
		t.Errorf("the released ruleset: %v", err)
	}
}

// A publish that read the ruleset before a delete committed: the foreign key refuses the
// release, and the answer is NOT_FOUND, as for a ruleset that never existed.
func TestPublishReleaseLosesToADeleteInFlight(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	ctx := t.Context()
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	rs, _, err := s.CreateRuleset(ctx, p, proj.ID, rules.Storage, storageFile("true"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM control.rulesets WHERE ruleset_id = $1`, rs.Name[len(rs.Name)-36:]); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.PublishRelease(ctx, p, proj.ID, "firebase.storage/"+proj.StorageBucket, rs.Name)
		done <- err
	}()
	// The publish read the ruleset, and its foreign key check waits on the deleted row.
	waitFor(t, func() bool {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE '%UpsertRelease%'`).Scan(&n)
		return n > 0
	})
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wantCode(t, "the publish", <-done, apierr.NotFound)
	if _, err := s.GetRelease(ctx, p, proj.ID, "firebase.storage/"+proj.StorageBucket); err == nil {
		t.Error("a release points at the deleted ruleset")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestListReleasesShowsTheCurrentBucket(t *testing.T) {
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	ctx := t.Context()
	p, proj := rulesOwner(t, s, pool, "o@example.com", "chat-app")
	other, otherProj := rulesOwner(t, s, pool, "x@example.com", "other-app")
	publish(t, s, other, otherProj, "firebase.storage/"+otherProj.StorageBucket, storageFile("true"))

	page, err := s.ListReleases(ctx, p, proj.ID, ListOptions{})
	if err != nil || len(page.Items) != 0 || page.NextPageToken != "" {
		t.Fatalf("before any release: %+v %v", page, err)
	}
	storage, _ := publish(t, s, p, proj, "firebase.storage/"+proj.StorageBucket, storageFile("true"))
	fs, _, err := s.CreateRuleset(ctx, p, proj.ID, rules.Firestore, []rules.File{{Name: "firestore.rules",
		Content: "rules_version = '2';\nservice cloud.firestore {\n  match /databases/{database}/documents {\n    match /{d=**} {\n      allow read: if false;\n    }\n  }\n}\n"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cloud.firestore", "cloud.firestore/named", "custom"} {
		if _, err := s.PublishRelease(ctx, p, proj.ID, name, fs.Name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	var names []string
	tok := ""
	for range 5 {
		page, err := s.ListReleases(ctx, p, proj.ID, ListOptions{PageSize: 2, PageToken: tok})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Items {
			names = append(names, strings.TrimPrefix(r.Name, "projects/chat-app/releases/"))
			if r.CreateTime.IsZero() || r.UpdateTime.IsZero() || !strings.HasPrefix(r.RulesetName, "projects/chat-app/rulesets/") {
				t.Errorf("release %+v", r)
			}
			if strings.HasPrefix(r.Name, "projects/chat-app/releases/firebase.storage/") && r.RulesetName != storage.Name {
				t.Errorf("the Storage release points at %s", r.RulesetName)
			}
		}
		if tok = page.NextPageToken; tok == "" {
			break
		}
	}
	want := []string{"cloud.firestore", "cloud.firestore/named", "custom", "firebase.storage/" + proj.StorageBucket}
	if !slices.Equal(names, want) {
		t.Errorf("releases %v, want %v", names, want)
	}

	// Another project domain renames the bucket, and the listing with it (ADR-022 §2).
	renamed := rulesService(t, pool, "other.test", nil)
	page, err = renamed.ListReleases(ctx, p, proj.ID, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if last := page.Items[len(page.Items)-1].Name; last != "projects/chat-app/releases/firebase.storage/chat-app.other.test" {
		t.Errorf("after the domain change: %s", last)
	}
	_, err = s.ListReleases(ctx, other, proj.ID, ListOptions{})
	wantCode(t, "another account", err, apierr.NotFound)
	_, err = s.ListReleases(ctx, p, proj.ID, ListOptions{PageToken: "%%"})
	wantCode(t, "a bad token", err, apierr.InvalidArgument)
}
