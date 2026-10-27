package core

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/queries"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
)

func tsValue(t time.Time) value.Value { return value.Timestamp(t.Unix(), int32(t.Nanosecond())) }

// expireOf returns a document's stored expire_time, nil for none.
func expireOf(t *testing.T, s *PGStore, path string) *time.Time {
	t.Helper()
	var e *time.Time
	if err := s.pool.QueryRow(t.Context(), `SELECT expire_time FROM documents.documents WHERE name_key = $1`,
		keysOf(ParsePath(path)).stored).Scan(&e); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return e
}

func wantExpire(t *testing.T, s *PGStore, path string, want *time.Time) {
	t.Helper()
	got := expireOf(t, s, path)
	switch {
	case want == nil && got != nil:
		t.Errorf("%s: expire_time %v, want none", path, *got)
	case want != nil && (got == nil || !got.Equal(want.Truncate(time.Microsecond))):
		t.Errorf("%s: expire_time %v, want %v", path, got, *want)
	}
}

func setTTL(t *testing.T, s *PGStore, group, path string, on bool) {
	t.Helper()
	f := &adminpb.Field{Name: testDB.FieldName(group, path)}
	if on {
		f.TtlConfig = &adminpb.Field_TtlConfig{}
	}
	if _, err := s.UpdateField(t.Context(), testDB, group, f, []string{"ttl_config"}); err != nil {
		t.Fatal(err)
	}
}

func ttlState(t *testing.T, s *PGStore, group, path string) adminpb.Field_TtlConfig_State {
	t.Helper()
	f, err := s.GetField(t.Context(), testDB, group, path)
	if err != nil {
		t.Fatal(err)
	}
	return f.GetTtlConfig().GetState()
}

// A policy is CREATING until the group's documents are filled, and every commit from its
// creation stores expire_time: a Timestamp alone, the earliest policy's, never another group's.
// Removing it clears the group's.
func TestTTLCommitAndPolicy(t *testing.T) {
	s, _ := newStore(t, Options{})
	at := time.Date(2030, 1, 2, 3, 4, 5, 678901234, time.UTC)
	mustCommit(t, s,
		set("s/ts", doc("exp", tsValue(at))),
		set("s/str", doc("exp", value.String("2030-01-01"))),
		set("s/none", doc("x", value.Int(1))),
		set("s/arr", doc("exp", value.Array(tsValue(at)))),
		set("other/ts", doc("exp", tsValue(at))),
	)
	wantExpire(t, s, "s/ts", nil)

	setTTL(t, s, "s", "exp", true)
	// The queued update applies, CREATING; the build has not run yet.
	o := smallBuilds
	o.maxBatches = 1
	if err := s.BuildIndexes(t.Context(), o); !errors.Is(err, errBuildStopped) {
		t.Fatalf("stopped build: %v", err)
	}
	if st := ttlState(t, s, "s", "exp"); st != adminpb.Field_TtlConfig_CREATING {
		t.Fatalf("state %v, want CREATING", st)
	}
	// A commit while CREATING writes its expire_time.
	later := at.Add(time.Hour)
	mustCommit(t, s, set("s/new", doc("exp", tsValue(later))))
	wantExpire(t, s, "s/new", &later)

	mustBuild(t, s)
	if st := ttlState(t, s, "s", "exp"); st != adminpb.Field_TtlConfig_ACTIVE {
		t.Fatalf("state %v, want ACTIVE", st)
	}
	wantExpire(t, s, "s/ts", &at)
	wantExpire(t, s, "s/str", nil)
	wantExpire(t, s, "s/none", nil)
	wantExpire(t, s, "s/arr", nil) // Standard edition: a Timestamp only
	wantExpire(t, s, "other/ts", nil)

	// Updates follow the field.
	mustCommit(t, s, set("s/ts", doc("exp", tsValue(later))), set("s/str", doc("exp", tsValue(at))),
		set("s/new", doc("exp", value.Null())))
	wantExpire(t, s, "s/ts", &later)
	wantExpire(t, s, "s/str", &at)
	wantExpire(t, s, "s/new", nil)
	// A masked update of another field keeps it.
	mustCommit(t, s, Write{Kind: WriteUpdate, Path: ParsePath("s/ts"), Fields: doc("y", value.Int(2)), Masked: true,
		Mask: []value.FieldPath{{"y"}}})
	wantExpire(t, s, "s/ts", &later)

	// A second policy on the group: the earliest.
	mustCommit(t, s, set("s/two", doc("exp", tsValue(later), "end", tsValue(at))))
	setTTL(t, s, "s", "end", true)
	mustBuild(t, s)
	wantExpire(t, s, "s/two", &at)
	setTTL(t, s, "s", "end", false)
	mustBuild(t, s)
	wantExpire(t, s, "s/two", &later)

	setTTL(t, s, "s", "exp", false)
	mustBuild(t, s)
	if st := ttlState(t, s, "s", "exp"); st != adminpb.Field_TtlConfig_STATE_UNSPECIFIED {
		t.Fatalf("state %v after removal", st)
	}
	for _, p := range []string{"s/ts", "s/str", "s/two"} {
		wantExpire(t, s, p, nil)
	}
	mustCommit(t, s, set("s/after", doc("exp", tsValue(at))))
	wantExpire(t, s, "s/after", nil)
	mustCheck(t, s)
}

// A policy's backfill resumes from its cursor after a restart.
func TestTTLBackfillResumes(t *testing.T) {
	s, pool := newStore(t, Options{})
	ctx := t.Context()
	at := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)
	var ws []Write
	for i := range 20 {
		ws = append(ws, set(fmt.Sprintf("s/%02d", i), doc("exp", tsValue(at.Add(time.Duration(i)*time.Second)))))
	}
	mustCommit(t, s, ws...)
	setTTL(t, s, "s", "exp", true)
	o := smallBuilds
	o.maxBatches = 3
	if err := s.BuildIndexes(ctx, o); !errors.Is(err, errBuildStopped) {
		t.Fatalf("stopped build: %v", err)
	}
	var cursor []byte
	if err := pool.QueryRow(ctx, `SELECT build_cursor FROM documents.field_overrides WHERE building`).Scan(&cursor); err != nil || cursor == nil {
		t.Fatalf("no cursor after 3 batches: %v", err)
	}
	var filled int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM documents.documents WHERE expire_time IS NOT NULL`).Scan(&filled)
	if filled == 0 || filled == 20 {
		t.Fatalf("%d documents filled after 2 batches", filled)
	}
	restarted := NewPGStore(pool, Options{})
	if st := ttlState(t, restarted, "s", "exp"); st != adminpb.Field_TtlConfig_CREATING {
		t.Fatalf("state %v, want CREATING", st)
	}
	mustBuild(t, restarted)
	if st := ttlState(t, restarted, "s", "exp"); st != adminpb.Field_TtlConfig_ACTIVE {
		t.Fatalf("state %v, want ACTIVE", st)
	}
	for i := range 20 {
		want := at.Add(time.Duration(i) * time.Second)
		wantExpire(t, restarted, fmt.Sprintf("s/%02d", i), &want)
	}
}

func exists(t *testing.T, s *PGStore, path string) bool {
	t.Helper()
	d, _, err := s.Get(t.Context(), testDB, ParsePath(path), ReadOptions{})
	if e, ok := errors.AsType[*Error](err); ok && e.Code == NotFound {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return d.Exists()
}

type changeNotifier struct {
	mu      sync.Mutex
	changes []Change
}

func (r *changeNotifier) Committed(_ DB, _ int64, _ time.Time, changes []Change) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, changes...)
}
func (r *changeNotifier) Unknown(DB, int64) {}

// deleted returns the paths the commits deleted.
func (r *changeNotifier) deleted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.changes {
		if c.Before.Exists() && !c.After.Exists() {
			out = append(out, c.Path.String())
		}
	}
	return out
}

// The sweeper deletes the documents due under an ACTIVE policy, through the commit path (the
// Notifier and the changelog see each delete), and nothing else: not a document due later, nor
// one whose field is not a Timestamp, nor one of a CREATING policy's group, nor a subcollection's
// document, nor a document rewritten after it was read.
func TestTTLSweep(t *testing.T) {
	notes := &changeNotifier{}
	s, pool := newStore(t, Options{Notifier: notes})
	ctx := t.Context()
	now := time.Now().Truncate(time.Microsecond)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	mustCommit(t, s,
		set("s/due", doc("exp", tsValue(past))),
		set("s/due2", doc("exp", tsValue(past.Add(time.Second)))),
		set("s/later", doc("exp", tsValue(future))),
		set("s/str", doc("exp", value.String("past"))),
		set("s/none", doc()),
		set("s/due/sub/child", doc("exp", tsValue(past))),
		set("s/raced", doc("exp", tsValue(past), "v", value.Int(1))),
		set("c/due", doc("exp", tsValue(past))),
	)
	setTTL(t, s, "s", "exp", true)
	mustBuild(t, s)
	// c's policy stays CREATING: its build stops before its last batch.
	setTTL(t, s, "c", "exp", true)
	stop := smallBuilds
	stop.maxBatches = 1
	if err := s.BuildIndexes(ctx, stop); !errors.Is(err, errBuildStopped) {
		t.Fatalf("stopped build: %v", err)
	}
	wantExpire(t, s, "c/due", &past)

	// A rewrite between the sweeper's read and its delete keeps the document.
	cat, err := s.cats.get(ctx, testDB)
	if err != nil {
		t.Fatal(err)
	}
	var raced queries.DueDocumentsRow
	raced.NameKey, raced.Name = keysOf(ParsePath("s/raced")).stored, keysOf(ParsePath("s/raced")).name
	if err := pool.QueryRow(ctx, `SELECT doc, update_time FROM documents.documents WHERE name_key = $1`, raced.NameKey).
		Scan(&raced.Doc, &raced.UpdateTime); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, s, set("s/raced", doc("exp", tsValue(past), "v", value.Int(2))))
	if deleted, err := s.expire(ctx, testDB, cat, raced, now); err != nil || deleted {
		t.Fatalf("rewritten document: deleted %v, %v", deleted, err)
	}

	before := len(notes.deleted())
	n, err := s.SweepExpired(ctx, TTLOptions{Batch: 2, now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	// s/due, s/due2, and s/raced, still due once read again; c/due skipped.
	if n.Deleted != 3 || n.Skipped != 1 {
		t.Errorf("swept %+v", n)
	}
	got := notes.deleted()[before:]
	want := map[string]bool{"s/due": true, "s/due2": true, "s/raced": true}
	if len(got) != len(want) {
		t.Errorf("notified deletes %v", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("notified delete of %s", p)
		}
	}
	for _, p := range []string{"s/later", "s/str", "s/none", "c/due", "s/due/sub/child"} {
		if !exists(t, s, p) {
			t.Errorf("%s was deleted", p)
		}
	}
	var logged int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM documents.changelog WHERE after IS NULL AND name_key = ANY($1)`,
		[][]byte{keysOf(ParsePath("s/due")).stored, keysOf(ParsePath("s/due2")).stored}).Scan(&logged); err != nil || logged != 2 {
		t.Errorf("changelog deletes: %d %v", logged, err)
	}
	if st := s.TTLStats(); st.Passes != 1 || st.Deleted != 3 {
		t.Errorf("stats %+v", st)
	}

	// Once c's policy is ACTIVE its document goes too; a removed policy deletes nothing.
	mustBuild(t, s)
	setTTL(t, s, "s", "exp", false)
	mustBuild(t, s)
	mustCommit(t, s, set("s/again", doc("exp", tsValue(past))))
	if n, err := s.SweepExpired(ctx, TTLOptions{now: func() time.Time { return now }}); err != nil || n.Deleted != 1 {
		t.Errorf("second sweep: %+v %v", n, err)
	}
	if exists(t, s, "c/due") || !exists(t, s, "s/again") {
		t.Errorf("c/due %v, s/again %v", exists(t, s, "c/due"), exists(t, s, "s/again"))
	}
}
