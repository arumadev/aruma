package core

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// TestMain makes a missing Postgres fail the integration tests rather than skip them; -short
// still leaves them out, as everywhere in the repository.
func TestMain(m *testing.M) {
	if os.Getenv("ARUMA_TEST_REQUIRE_DB") == "" {
		os.Setenv("ARUMA_TEST_REQUIRE_DB", "1")
	}
	os.Exit(m.Run())
}

func newStore(t *testing.T, o Options) (*PGStore, *pgxpool.Pool) {
	t.Helper()
	pool := storetest.NewPool(t)
	return NewPGStore(pool, o), pool
}

func doc(kv ...any) value.Value { return fieldsOf(kv...) }

func set(p string, fields value.Value) Write {
	return Write{Kind: WriteUpdate, Path: ParsePath(p), Fields: fields}
}

func mustCommit(t *testing.T, s Store, writes ...Write) CommitResult {
	t.Helper()
	res, err := s.Commit(t.Context(), testDB, writes)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return res
}

func wantError(t *testing.T, err error, code Code, msg string) {
	t.Helper()
	e, ok := errors.AsType[*Error](err)
	if !ok || e.Code != code || e.Message != msg {
		t.Fatalf("got %v, want %d %q", err, code, msg)
	}
}

func paths(docs []Document) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.Path.String()
		if !d.Exists() {
			out[i] += " (missing)"
		}
	}
	return out
}

// '(default)' is created on first use; other database ids answer production's NOT_FOUND
// (f0-missing-database.json).
func TestDatabases(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	d, err := s.Database(ctx, testDB)
	if err != nil {
		t.Fatal(err)
	}
	if d.Type != "FIRESTORE_NATIVE" || d.Edition != "STANDARD" || d.ConcurrencyMode != "PESSIMISTIC" ||
		d.VersionRetention != time.Hour || d.LocationID != DefaultLocation || d.UID == "" || d.Etag == "" {
		t.Errorf("database %+v", d)
	}
	if err := s.EnsureDatabase(ctx, testDB.Project); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Database(ctx, testDB); again.UID != d.UID {
		t.Error("EnsureDatabase replaced the database")
	}
	other := DB{Project: testDB.Project, ProjectID: "chatapp-8ae21", ID: "aruma-ref-nonexistent"}
	for name, call := range map[string]func() error{
		"database": func() error { _, err := s.Database(ctx, other); return err },
		"get":      func() error { _, _, err := s.Get(ctx, other, ParsePath("c/d"), ReadOptions{}); return err },
		"list": func() error {
			_, err := s.ListDocuments(ctx, other, ListRequest{CollectionID: "c"})
			return err
		},
		"commit": func() error { _, err := s.Commit(ctx, other, []Write{set("c/d", doc())}); return err },
	} {
		err := call()
		nf, ok := errors.AsType[*DatabaseNotFoundError](err)
		if !ok || nf.RESTMessage() != "The database aruma-ref-nonexistent does not exist for project chatapp-8ae21 Please visit https://console.cloud.google.com/datastore/setup?project=chatapp-8ae21 to add a Cloud Datastore or Cloud Firestore database. " {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A commit on a project's first use creates its database too.
	fresh := DB{Project: 582915736204, ProjectID: "other-app", ID: DefaultDatabase}
	if _, err := s.Commit(ctx, fresh, []Write{set("c/d", doc())}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateUpdateDeleteAndPreconditions(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	p := ParsePath("aruma-ref-f1-write/c1")
	name := testDB.DocumentName(p)

	created := mustCommit(t, s, Write{Kind: WriteUpdate, Path: p, Fields: doc("a", value.Int(1)), Precondition: Precondition{Exists: new(false)}})
	if created.Seq != 1 || !created.Results[0].UpdateTime.Equal(created.CommitTime) {
		t.Fatalf("create: %+v", created)
	}
	_, err := s.Commit(ctx, testDB, []Write{{Kind: WriteUpdate, Path: p, Fields: doc(), Precondition: Precondition{Exists: new(false)}}})
	wantError(t, err, AlreadyExists, "Document already exists: "+name)

	got, readTime, err := s.Get(ctx, testDB, p, ReadOptions{})
	if err != nil || !got.CreateTime.Equal(created.CommitTime) || !got.UpdateTime.Equal(created.CommitTime) || !readTime.Equal(created.CommitTime) {
		t.Fatalf("get: %+v %v %v", got, readTime, err)
	}

	// update_time preconditions, by the microsecond.
	_, err = s.Commit(ctx, testDB, []Write{{Kind: WriteUpdate, Path: p, Fields: doc(), Precondition: Precondition{UpdateTime: created.CommitTime.Add(-time.Microsecond)}}})
	wantError(t, err, FailedPrecondition, "the stored version ("+itoa(created.CommitTime.UnixMicro())+") does not match the required base version ("+itoa(created.CommitTime.UnixMicro()-1)+")")
	masked := mustCommit(t, s, Write{Kind: WriteUpdate, Path: p, Masked: true, Mask: []value.FieldPath{mustPath(t, "b")},
		Fields: doc("b", value.String("x"), "a", value.Int(9)), Precondition: Precondition{Exists: new(true), UpdateTime: created.CommitTime}})
	got, _, _ = s.Get(ctx, testDB, p, ReadOptions{})
	if !value.Identical(got.Fields, doc("a", value.Int(1), "b", value.String("x"))) || !got.CreateTime.Equal(created.CommitTime) ||
		!got.UpdateTime.Equal(masked.CommitTime) || !masked.CommitTime.After(created.CommitTime) {
		t.Fatalf("masked update: %+v", got)
	}

	// The same fields again: no change, no seq, the previous update_time (f1-write-results.json).
	same := mustCommit(t, s, set(p.String(), got.Fields))
	if same.Seq != 0 || !same.Results[0].UpdateTime.Equal(masked.CommitTime) || same.CommitTime.Before(masked.CommitTime) {
		t.Fatalf("unchanged update: %+v", same)
	}
	// A failed precondition fails every write of the commit.
	_, err = s.Commit(ctx, testDB, []Write{set("aruma-ref-f1-write/other", doc()),
		{Kind: WriteDelete, Path: ParsePath("aruma-ref-f1-write/d-missing"), Precondition: Precondition{Exists: new(true)}}})
	wantError(t, err, NotFound, "No document to update: "+testDB.DocumentName(ParsePath("aruma-ref-f1-write/d-missing")))
	if docs, _, _ := s.BatchGet(ctx, testDB, []Path{ParsePath("aruma-ref-f1-write/other")}, ReadOptions{}); docs[0].Exists() {
		t.Fatal("a write of a failed commit applied")
	}

	del := mustCommit(t, s, Write{Kind: WriteDelete, Path: p, Precondition: Precondition{Exists: new(true)}})
	if del.Seq != 3 || !del.Results[0].UpdateTime.IsZero() {
		t.Fatalf("delete: %+v", del)
	}
	_, _, err = s.Get(ctx, testDB, p, ReadOptions{})
	wantError(t, err, NotFound, `Document "`+name+`" not found.`)
	empty := mustCommit(t, s)
	if empty.Seq != 0 || empty.CommitTime.Before(del.CommitTime) {
		t.Fatalf("empty commit: %+v", empty)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A read at a read_time sees the documents as they were, from the versions table; outside the
// window it answers production's errors (f1-get.json).
func TestReadTime(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	p := ParsePath("c/d")
	r1 := mustCommit(t, s, set("c/d", doc("v", value.Int(1))), set("c/e", doc("v", value.Int(10))))
	r2 := mustCommit(t, s, set("c/d", doc("v", value.Int(2))))
	r3 := mustCommit(t, s, Write{Kind: WriteDelete, Path: p})
	r4 := mustCommit(t, s, set("c/d", doc("v", value.Int(4))))

	at := func(ts time.Time) ReadOptions { return ReadOptions{ReadTime: ts} }
	for _, c := range []struct {
		t    time.Time
		want int64 // 0: absent
	}{
		{r1.CommitTime.Add(-time.Microsecond), 0},
		{r1.CommitTime, 1},
		{r2.CommitTime.Add(-time.Microsecond), 1},
		{r2.CommitTime, 2},
		{r3.CommitTime, 0},
		{r4.CommitTime, 4},
	} {
		docs, readTime, err := s.BatchGet(ctx, testDB, []Path{p}, at(c.t))
		if err != nil {
			t.Fatal(err)
		}
		if !readTime.Equal(c.t) {
			t.Errorf("read_time %v, want %v", readTime, c.t)
		}
		got := docs[0]
		switch {
		case c.want == 0 && got.Exists():
			t.Errorf("at %v: %v, want absent", c.t, got.Fields)
		case c.want != 0 && (!got.Exists() || got.Fields.Fields()[0].Value.IntValue() != c.want):
			t.Errorf("at %v: %+v, want v=%d", c.t, got, c.want)
		}
		if c.want == 2 && (!got.CreateTime.Equal(r1.CommitTime) || !got.UpdateTime.Equal(r2.CommitTime)) {
			t.Errorf("version times %v %v", got.CreateTime, got.UpdateTime)
		}
		if c.want == 4 && !got.CreateTime.Equal(r4.CommitTime) {
			t.Errorf("a recreated document's create_time %v", got.CreateTime)
		}
	}
	// Listing at a read_time reads the versions too.
	list, err := s.ListDocuments(ctx, testDB, ListRequest{CollectionID: "c", ReadOptions: at(r2.CommitTime)})
	if err != nil || !slices.Equal(paths(list.Documents), []string{"c/d", "c/e"}) || list.Documents[0].Fields.Fields()[0].Value.IntValue() != 2 {
		t.Fatalf("list at r2: %v %v", paths(list.Documents), err)
	}
	list, _ = s.ListDocuments(ctx, testDB, ListRequest{CollectionID: "c", ReadOptions: at(r3.CommitTime)})
	if !slices.Equal(paths(list.Documents), []string{"c/e"}) {
		t.Fatalf("list at r3: %v", paths(list.Documents))
	}

	_, _, err = s.BatchGet(ctx, testDB, []Path{p}, at(time.Now().Add(-2*time.Hour)))
	wantError(t, err, FailedPrecondition, "The requested 'read_time' is too old.")
	_, _, err = s.BatchGet(ctx, testDB, []Path{p}, at(time.Now().Add(time.Hour)))
	wantError(t, err, InvalidArgument, "The requested 'read_time' cannot be in the future.")
	// A read_time after the last commit and before now waits for no one and sees it.
	time.Sleep(2 * time.Millisecond)
	now := time.Now().Add(-time.Millisecond).Truncate(time.Microsecond)
	docs, readTime, err := s.BatchGet(ctx, testDB, []Path{p}, at(now))
	if err != nil || !readTime.Equal(now) || !docs[0].Exists() {
		t.Fatalf("read at now: %v %v", readTime, err)
	}
}

// show_missing as production answers it (f0-list-show-missing.json): a document that does not
// exist but has descendants is listed by name alone, in name order with the others.
func TestShowMissing(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	var writes []Write
	for _, p := range []string{"tree/a", "tree/a/other/z", "tree/a/sub/x", "tree/a/sub/x/deep/y", "tree/b", "tree/m/kids/k1", "tree2/q"} {
		writes = append(writes, set(p, doc("v", value.Int(1))))
	}
	mustCommit(t, s, writes...)

	list := func(r ListRequest) ListResult {
		t.Helper()
		res, err := s.ListDocuments(ctx, testDB, r)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if got := paths(list(ListRequest{CollectionID: "tree", ShowMissing: true}).Documents); !slices.Equal(got, []string{"tree/a", "tree/b", "tree/m (missing)"}) {
		t.Errorf("show_missing: %v", got)
	}
	page := list(ListRequest{CollectionID: "tree", ShowMissing: true, PageSize: 2})
	if got := paths(page.Documents); !slices.Equal(got, []string{"tree/a", "tree/b"}) || page.Next.String() != "tree/b" {
		t.Errorf("first page: %v next %v", got, page.Next)
	}
	page = list(ListRequest{CollectionID: "tree", ShowMissing: true, PageSize: 2, After: page.Next})
	if got := paths(page.Documents); !slices.Equal(got, []string{"tree/m (missing)"}) || page.Next != nil {
		t.Errorf("second page: %v next %v", got, page.Next)
	}
	if got := paths(list(ListRequest{CollectionID: "tree"}).Documents); !slices.Equal(got, []string{"tree/a", "tree/b"}) {
		t.Errorf("without show_missing: %v", got)
	}
	if got := paths(list(ListRequest{Parent: ParsePath("tree/a")}).Documents); !slices.Equal(got, []string{"tree/a/other/z", "tree/a/sub/x"}) {
		t.Errorf("every collection: %v", got)
	}
	if got := paths(list(ListRequest{Parent: ParsePath("tree/a"), PageSize: 1, After: ParsePath("tree/a/other/z")}).Documents); !slices.Equal(got, []string{"tree/a/sub/x"}) {
		t.Errorf("every collection, second page: %v", got)
	}

	ids, err := s.ListCollectionIDs(ctx, testDB, ListCollectionIDsRequest{Parent: ParsePath("tree/m")})
	if err != nil || !slices.Equal(ids.IDs, []string{"kids"}) || ids.More {
		t.Errorf("collection ids of a missing document: %+v %v", ids, err)
	}
	ids, _ = s.ListCollectionIDs(ctx, testDB, ListCollectionIDsRequest{})
	if !slices.Equal(ids.IDs, []string{"tree", "tree2"}) {
		t.Errorf("root collection ids: %v", ids.IDs)
	}
	ids, _ = s.ListCollectionIDs(ctx, testDB, ListCollectionIDsRequest{PageSize: 1, After: "tree"})
	if !slices.Equal(ids.IDs, []string{"tree2"}) || ids.More {
		t.Errorf("root collection ids after tree: %+v", ids)
	}
	_, _, err = s.Get(ctx, testDB, ParsePath("tree/m"), ReadOptions{})
	wantError(t, err, NotFound, `Document "projects/chatapp-8ae21/databases/(default)/documents/tree/m" not found.`)

	// At a read_time before the subtree was deleted, the missing document is still listed.
	before, _ := s.ListDocuments(ctx, testDB, ListRequest{CollectionID: "tree", ShowMissing: true})
	del := mustCommit(t, s, Write{Kind: WriteDelete, Path: ParsePath("tree/m/kids/k1")})
	if got := paths(list(ListRequest{CollectionID: "tree", ShowMissing: true}).Documents); !slices.Equal(got, []string{"tree/a", "tree/b"}) {
		t.Errorf("after the delete: %v", got)
	}
	old := list(ListRequest{CollectionID: "tree", ShowMissing: true, ReadOptions: ReadOptions{ReadTime: before.ReadTime}})
	if got := paths(old.Documents); !slices.Equal(got, []string{"tree/a", "tree/b", "tree/m (missing)"}) || !del.CommitTime.After(before.ReadTime) {
		t.Errorf("at the earlier read_time: %v", got)
	}
}

// Names longer than a btree entry holds (ADR-024 §1.4): 1,500-byte ids make names of over 3,000
// bytes, and documents whose keys share the first 2,048 bytes are ordered by their whole names,
// through pages, show_missing and collection ids alike.
func TestLongNames(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	long := strings.Repeat("x", 1499)
	parent := "c/" + long + "p"
	var want []string
	var writes []Write
	for _, suffix := range []string{"e", "a", "d", "b", "c"} {
		p := parent + "/" + "s/" + long + suffix
		writes = append(writes, set(p, doc("v", value.String(suffix))))
		want = append(want, p)
	}
	missing := parent + "/s/" + long + "m"
	writes = append(writes, set(missing+"/k/1", doc()))
	mustCommit(t, s, writes...)
	slices.Sort(want)
	if n := len(nameKey(ParsePath(want[0]))); n <= 2704 {
		t.Fatalf("name key of %d bytes is not past the btree bound", n)
	}

	for _, p := range want {
		if d, _, err := s.Get(ctx, testDB, ParsePath(p), ReadOptions{}); err != nil || d.Path.String() != p {
			t.Fatalf("get %d-byte name: %v", len(p), err)
		}
	}
	var got []string
	var after Path
	for {
		page, err := s.ListDocuments(ctx, testDB, ListRequest{Parent: ParsePath(parent), CollectionID: "s", PageSize: 2, After: after})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, paths(page.Documents)...)
		if after = page.Next; after == nil {
			break
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("pages:\n%v\nwant\n%v", short(got), short(want))
	}
	withMissing := slices.Clone(want)
	withMissing = append(withMissing, missing+" (missing)")
	slices.Sort(withMissing)
	got = nil
	after = nil
	for {
		page, err := s.ListDocuments(ctx, testDB, ListRequest{Parent: ParsePath(parent), CollectionID: "s", PageSize: 2, After: after, ShowMissing: true})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, paths(page.Documents)...)
		if after = page.Next; after == nil {
			break
		}
	}
	if !slices.Equal(got, withMissing) {
		t.Fatalf("show_missing pages:\n%v\nwant\n%v", short(got), short(withMissing))
	}
	// Collection ids past the cut: three collections under one long document.
	var cw []Write
	for _, id := range []string{"z", "x", "y"} {
		cw = append(cw, set(want[0]+"/"+strings.Repeat("k", 1400)+id+"/d", doc()))
	}
	mustCommit(t, s, cw...)
	ids, err := s.ListCollectionIDs(ctx, testDB, ListCollectionIDsRequest{Parent: ParsePath(want[0]), PageSize: 2})
	if err != nil || len(ids.IDs) != 2 || !ids.More || !strings.HasSuffix(ids.IDs[0], "x") || !strings.HasSuffix(ids.IDs[1], "y") {
		t.Fatalf("collection ids: %v %v %v", short(ids.IDs), ids.More, err)
	}
	ids, _ = s.ListCollectionIDs(ctx, testDB, ListCollectionIDsRequest{Parent: ParsePath(want[0]), After: ids.IDs[1]})
	if len(ids.IDs) != 1 || !strings.HasSuffix(ids.IDs[0], "z") {
		t.Fatalf("collection ids after: %v", short(ids.IDs))
	}
	// Deleting and reading one at a read_time goes through the versions table's cut keys too.
	del := mustCommit(t, s, Write{Kind: WriteDelete, Path: ParsePath(want[2])})
	docs, _, err := s.BatchGet(ctx, testDB, []Path{ParsePath(want[2])}, ReadOptions{ReadTime: del.CommitTime.Add(-time.Microsecond)})
	if err != nil || !docs[0].Exists() {
		t.Fatalf("long name at a read_time: %v", err)
	}
}

// short abbreviates long ids for a failure message.
func short(ps []string) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = strings.ReplaceAll(strings.ReplaceAll(p, strings.Repeat("x", 1499), "x…"), strings.Repeat("k", 1400), "k…")
	}
	return out
}

// Commits run concurrently on many connections: seq is contiguous, commit_time strictly
// increases in seq order, and each commit's result is the changelog's (ADR-024 §2.1).
func TestConcurrentCommitsAreTotallyOrdered(t *testing.T) {
	notes := &recordingNotifier{}
	s, pool := newStore(t, Options{Notifier: notes})
	ctx := t.Context()
	const workers, each = 16, 25
	var mu sync.Mutex
	results := map[int64]time.Time{}
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range each {
				p := "c/" + string(rune('a'+(w+i)%8))
				res, err := s.Commit(ctx, testDB, []Write{set(p, doc("w", value.Int(int64(w)), "i", value.Int(int64(i)))), set("w/"+string(rune('a'+w)), doc("i", value.Int(int64(i))))})
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				results[res.Seq] = res.CommitTime
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	rows, err := pool.Query(ctx, `SELECT seq, min(commit_time), max(commit_time) FROM documents.changelog
		WHERE project_number = $1 AND database_id = $2 GROUP BY seq ORDER BY seq`, testDB.Project, testDB.ID)
	if err != nil {
		t.Fatal(err)
	}
	var prev time.Time
	n := int64(0)
	for rows.Next() {
		var seq int64
		var lo, hi time.Time
		if err := rows.Scan(&seq, &lo, &hi); err != nil {
			t.Fatal(err)
		}
		n++
		if seq != n || !lo.Equal(hi) || !lo.After(prev) || !results[seq].Equal(lo) {
			t.Fatalf("seq %d (want %d): commit_time %v after %v, result %v", seq, n, lo, prev, results[seq])
		}
		prev = lo
	}
	if rows.Err() != nil || n != workers*each || len(results) != workers*each {
		t.Fatalf("%d changelog seqs, %d results: %v", n, len(results), rows.Err())
	}
	var head int64
	if err := pool.QueryRow(ctx, `SELECT seq FROM documents.sequencer WHERE project_number = $1`, testDB.Project).Scan(&head); err != nil || head != n {
		t.Fatalf("sequencer at %d: %v", head, err)
	}
	if notes.count() != workers*each {
		t.Fatalf("notified %d commits", notes.count())
	}
}

type recordingNotifier struct {
	mu   sync.Mutex
	seqs []int64
}

func (r *recordingNotifier) Committed(_ DB, seq int64, _ time.Time, changes []Change) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seqs = append(r.seqs, seq)
}
func (r *recordingNotifier) Unknown(DB, int64) {}
func (r *recordingNotifier) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seqs)
}

// Concurrent creates of one document with exists: false: one wins, the others see it and fail
// with ALREADY_EXISTS, even without the lock manager (F2): the stamp asserts what it read
// (conflict_unless), and a conflict computes the commit again. Read-modify-write with an
// update_time precondition loses no increment.
func TestConcurrentConflicts(t *testing.T) {
	s, _ := newStore(t, Options{CommitConcurrency: 8})
	ctx := t.Context()
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, exists := 0, 0
	for range 12 {
		wg.Go(func() {
			_, err := s.Commit(ctx, testDB, []Write{{Kind: WriteUpdate, Path: ParsePath("race/one"), Fields: doc(), Precondition: Precondition{Exists: new(false)}}})
			mu.Lock()
			defer mu.Unlock()
			if e, ok := errors.AsType[*Error](err); ok && e.Code == AlreadyExists {
				exists++
			} else if err == nil {
				wins++
			} else {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if wins != 1 || exists != 11 {
		t.Fatalf("%d creates won, %d saw the document", wins, exists)
	}

	counter := ParsePath("race/counter")
	mustCommit(t, s, set(counter.String(), doc("n", value.Int(0))))
	const workers, each = 6, 10
	for range workers {
		wg.Go(func() {
			for done := 0; done < each; {
				d, _, err := s.Get(ctx, testDB, counter, ReadOptions{})
				if err != nil {
					t.Error(err)
					return
				}
				n, _ := d.Fields.Get("n")
				_, err = s.Commit(ctx, testDB, []Write{{Kind: WriteUpdate, Path: counter, Fields: doc("n", value.Int(n.IntValue()+1)),
					Precondition: Precondition{UpdateTime: d.UpdateTime}}})
				if e, ok := errors.AsType[*Error](err); ok && e.Code == FailedPrecondition {
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				done++
			}
		})
	}
	wg.Wait()
	d, _, _ := s.Get(ctx, testDB, counter, ReadOptions{})
	if n, _ := d.Fields.Get("n"); n.IntValue() != workers*each {
		t.Fatalf("counter at %d, want %d", n.IntValue(), workers*each)
	}
}

// A store whose process does not hold the instance lock serves nothing.
func TestNotServingWithoutTheInstanceLock(t *testing.T) {
	s, _ := newStore(t, Options{Instance: &Instance{}})
	_, err := s.Commit(t.Context(), testDB, nil)
	wantError(t, err, Unavailable, ErrNotServing.Message)
	_, _, err = s.Get(context.Background(), testDB, ParsePath("c/d"), ReadOptions{})
	wantError(t, err, Unavailable, ErrNotServing.Message)
}

// A ListRequest.After that is not a document of the listing, as a forged page token makes it,
// is refused rather than indexed (review M2).
func TestListDocumentsRefusesAForeignAfter(t *testing.T) {
	s, _ := newStore(t, Options{})
	for _, r := range []ListRequest{
		{After: Path{"a"}},
		{After: Path{}},
		{CollectionID: "c", After: ParsePath("other/d")},
		{Parent: ParsePath("c/d"), After: ParsePath("c/e/s/f")},
		{Parent: ParsePath("c/d"), CollectionID: "s", ShowMissing: true, After: ParsePath("s/f")},
	} {
		_, err := s.ListDocuments(t.Context(), testDB, r)
		wantError(t, err, InvalidArgument, ErrInvalidAfter.Message)
	}
	if _, err := s.ListDocuments(t.Context(), testDB, ListRequest{Parent: ParsePath("c/d"), After: ParsePath("c/d/s/f")}); err != nil {
		t.Fatal(err)
	}
}
