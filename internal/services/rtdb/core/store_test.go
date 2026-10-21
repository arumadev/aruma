package core

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/rtdb/rules"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// memStore is a Store on an in-memory tree, built on Node.Set: a second, independent
// implementation of the storage semantics that the Postgres store must agree with.
type memStore struct {
	mu  sync.Mutex
	dbs map[string]*memDB
	// failCommits makes the next n commits fail without effect, as a lost connection before
	// the commit reached the database would.
	failCommits int
	// lostAcks makes the next n commits take effect and still report an error, as a lost
	// connection after the commit would.
	lostAcks int
	// refuse makes writes of this string value fail as Postgres refuses data (class 22).
	refuse  string
	commits int
	lastID  int64
	// rules are the rules documents of databases yet to be opened, by name.
	rules map[string]string
	// slow delays every write, widening the window in which two writers would overlap.
	slow time.Duration
	// peekErr fails every read the rules make, as a database that is down.
	peekErr error
	// reads counts the subtree reads by path.
	reads map[string]int
}

type memDB struct {
	info DatabaseInfo
	root *Node
}

func newMemStore() *memStore { return &memStore{dbs: map[string]*memDB{}} }

func (s *memStore) OpenDatabase(_ context.Context, project int64, name string) (DatabaseInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db := s.dbs[name]
	if db == nil {
		s.lastID++
		rules := s.rules[name]
		if rules == "" {
			rules = LockedRules
		}
		db = &memDB{info: DatabaseInfo{ID: s.lastID, Project: project, Name: name, Rules: rules}}
		s.dbs[name] = db
	}
	return db.info, nil
}

func (s *memStore) Begin(_ context.Context, info DatabaseInfo) (StoreTx, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db := s.dbs[info.Name]
	if db == nil || db.info.ID != info.ID {
		return nil, errors.New("database gone")
	}
	return &memTx{s: s, db: db, root: db.root, seq: db.info.Seq}, nil
}

func (s *memStore) Seq(_ context.Context, info DatabaseInfo) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if db := s.dbs[info.Name]; db != nil {
		return db.info.Seq, nil
	}
	return 0, errors.New("database gone")
}

func (s *memStore) DeleteProject(_ context.Context, project int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, db := range s.dbs {
		if db.info.Project == project {
			delete(s.dbs, name)
		}
	}
	return nil
}

func (s *memStore) SetRules(_ context.Context, _ int64, name, doc string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if db := s.dbs[name]; db != nil {
		db.info.Rules = doc
		return nil
	}
	return errors.New("no database")
}

// set changes the store's failure settings under its lock.
func (s *memStore) set(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

func (s *memStore) value(name string) *Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	if db := s.dbs[name]; db != nil {
		return db.root
	}
	return nil
}

type memTx struct {
	s    *memStore
	db   *memDB
	root *Node
	seq  int64
}

func (t *memTx) Read(_ context.Context, p Path) (*Node, error) {
	t.s.mu.Lock()
	if t.s.reads == nil {
		t.s.reads = map[string]int{}
	}
	t.s.reads[p.String()]++
	t.s.mu.Unlock()
	return t.root.Get(p), nil
}
func (t *memTx) Peek(_ context.Context, p Path) (*Node, bool, error) {
	t.s.mu.Lock()
	err := t.s.peekErr
	t.s.mu.Unlock()
	if err != nil {
		return nil, false, err
	}
	n := t.root.Get(p)
	if n.IsLeaf() {
		return n.WithPriority(nil), false, nil
	}
	return nil, n != nil, nil
}
func (t *memTx) HasChildExcept(_ context.Context, p Path, keys []string) (bool, error) {
	for _, k := range t.root.Get(p).Keys() {
		if !slices.Contains(keys, k) {
			return true, nil
		}
	}
	return false, nil
}
func (t *memTx) Write(_ context.Context, p Path, v *Node) error {
	t.s.mu.Lock()
	refuse, slow := t.s.refuse, t.s.slow
	t.s.mu.Unlock()
	time.Sleep(slow)
	if refuse != "" && v.StringValue() == refuse {
		return &DataError{Err: errors.New("22P05: unsupported Unicode escape sequence")}
	}
	t.root = t.root.Set(p, v)
	return nil
}
func (t *memTx) AdvanceSeq(_ context.Context, n int64) (int64, error) { t.seq += n; return t.seq, nil }
func (t *memTx) Rollback(context.Context) error                       { return nil }
func (t *memTx) Commit(context.Context) error {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	if t.s.failCommits > 0 {
		t.s.failCommits--
		return errors.New("connection lost")
	}
	t.s.commits++
	t.db.root, t.db.info.Seq = t.root, t.seq
	if t.s.lostAcks > 0 {
		t.s.lostAcks--
		return errors.New("connection lost after commit")
	}
	return nil
}

// storeCases are writes and the tree they must leave, for every Store.
var storeCases = []struct {
	name   string
	writes []struct{ path, value string }
	read   string
	want   string
}{
	{"leaf", []struct{ path, value string }{{"a", `1`}}, "", `{"a":1}`},
	// Firebase stores U+0000 in values (emulator 4.11.2); jsonb text cannot (review #6).
	{"nul", []struct{ path, value string }{{"s", `"a\u0000b"`}}, "s", `"a\u0000b"`},
	{"nul priority", []struct{ path, value string }{{"p", `{".value":"\u0000",".priority":"x\u0000y"}`}, {"q", `{".priority":"\u0000z","a":1}`}}, "", `{"p":{".value":"\u0000",".priority":"x\u0000y"},"q":{".priority":"\u0000z","a":1}}`},
	{"root leaf", []struct{ path, value string }{{"", `"x"`}}, "", `"x"`},
	{"root leaf replaced by a child", []struct{ path, value string }{{"", `"x"`}, {"a", `1`}}, "", `{"a":1}`},
	{"object", []struct{ path, value string }{{"a", `{"b":{"c":true},"d":"s"}`}}, "a/b", `{"c":true}`},
	{"overwrite", []struct{ path, value string }{{"a", `{"b":1,"c":2}`}, {"a", `{"d":3}`}}, "", `{"a":{"d":3}}`},
	{"set below a leaf", []struct{ path, value string }{{"a", `1`}, {"a/b", `2`}}, "", `{"a":{"b":2}}`},
	{"leaf priority kept on the object", []struct{ path, value string }{{"a", `{".value":1,".priority":5}`}, {"a/b", `2`}}, "", `{"a":{".priority":5,"b":2}}`},
	{"delete below a leaf", []struct{ path, value string }{{"a", `1`}, {"a/b", `null`}}, "", `{"a":1}`},
	{"delete", []struct{ path, value string }{{"a", `{"b":1,"c":2}`}, {"a/b", `null`}}, "", `{"a":{"c":2}}`},
	{"delete with {}", []struct{ path, value string }{{"a", `{"b":1}`}, {"a", `{}`}}, "", `null`},
	{"last child gone", []struct{ path, value string }{{"a", `{".priority":3,"b":{"c":1}}`}, {"a/b/c", `null`}}, "", `null`},
	{"sibling stays", []struct{ path, value string }{{"a", `{".priority":3,"b":{"c":1},"d":1}`}, {"a/b/c", `null`}}, "", `{"a":{".priority":3,"d":1}}`},
	{"priorities", []struct{ path, value string }{{"p", `{"j":{".priority":2,"a":1},"k":{".value":1,".priority":"abc"}}`}}, "p", `{"j":{".priority":2,"a":1},"k":{".value":1,".priority":"abc"}}`},
	{"set priority on a leaf", []struct{ path, value string }{{"a", `1`}, {"a/.priority", `9`}}, "a", `{".value":1,".priority":9}`},
	{"set priority on an object", []struct{ path, value string }{{"a", `{"b":1}`}, {"a/.priority", `"z"`}}, "a", `{".priority":"z","b":1}`},
	{"clear priority", []struct{ path, value string }{{"a", `{".value":1,".priority":9}`}, {"a/.priority", `null`}}, "a", `1`},
	{"priority on nothing", []struct{ path, value string }{{"a/.priority", `9`}}, "", `null`},
	{"root delete", []struct{ path, value string }{{"a", `1`}, {"b", `2`}, {"", `null`}}, "", `null`},
	{"similar names", []struct{ path, value string }{{"a", `1`}, {"a-b", `2`}, {"ab", `3`}, {"a/b", `4`}}, "a", `{"b":4}`},
	{"unicode", []struct{ path, value string }{{"مرحبا/😀", `"日本"`}}, "مرحبا", `{"😀":"日本"}`},
	{"numbers", []struct{ path, value string }{{"n", `{"a":1.5,"b":-2,"c":1e21,"d":5e-324,"e":12345678901234567890}`}}, "n", `{"a":1.5,"b":-2,"c":1e+21,"d":5e-324,"e":12345678901234567000}`},
	{"read missing", []struct{ path, value string }{{"a", `1`}}, "b", `null`},
	{"read through a leaf", []struct{ path, value string }{{"a", `1`}}, "a/b", `null`},
}

func runStoreCases(t *testing.T, store Store) {
	ctx := context.Background()
	for i, c := range storeCases {
		t.Run(c.name, func(t *testing.T) {
			db, err := store.OpenDatabase(ctx, 482915736204, "case"+string(rune('a'+i))+"-default-rtdb")
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range c.writes {
				p, err := ParseWritePath(w.path)
				if err != nil {
					t.Fatal(err)
				}
				tx, err := store.Begin(ctx, db)
				if err != nil {
					t.Fatal(err)
				}
				v, err := parseAt(p, []byte(w.value), 1<<20)
				if err != nil {
					t.Fatal(err)
				}
				if err := tx.Write(ctx, p, v); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := store.Begin(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			p, _ := ParsePath(c.read)
			got, err := tx.Read(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			if s := string(got.ExportJSON()); s != c.want {
				t.Errorf("read %q: %s, want %s", c.read, s, c.want)
			}
			// What rules read through Peek and HasChildExcept agrees with Read, at the path
			// read and at the root.
			for _, at := range []Path{p, nil} {
				n, err := tx.Read(ctx, at)
				if err != nil {
					t.Fatal(err)
				}
				leaf, has, err := tx.Peek(ctx, at)
				if err != nil {
					t.Fatal(err)
				}
				var wantLeaf *Node
				if n.IsLeaf() {
					wantLeaf = n.WithPriority(nil)
				}
				if has != (n != nil && !n.IsLeaf()) || !Equal(leaf.WithPriority(nil), wantLeaf) {
					t.Errorf("peek %q: %v %v, read %s", at, leaf, has, n.ExportJSON())
				}
				keys := n.Keys()
				for i := range len(keys) + 1 {
					other, err := tx.HasChildExcept(ctx, at, keys[:i])
					if err != nil {
						t.Fatal(err)
					}
					if want := i < len(keys); other != want {
						t.Errorf("HasChildExcept(%q, %q) = %v, want %v", at, keys[:i], other, want)
					}
				}
			}
		})
	}
}

func TestMemStore(t *testing.T) { runStoreCases(t, newMemStore()) }

func TestPGStore(t *testing.T) {
	pool := storetest.NewPool(t)
	store := NewPGStore(pool)
	runStoreCases(t, store)

	// Seq grows in the transaction; a rolled-back transaction leaves no trace.
	ctx := context.Background()
	db, err := store.OpenDatabase(ctx, 482915736204, "seq-default-rtdb")
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := store.Begin(ctx, db)
	tx.Write(ctx, Path{"x"}, Number(1))
	if seq, err := tx.AdvanceSeq(ctx, 3); err != nil || seq != 3 {
		t.Fatalf("seq %d %v", seq, err)
	}
	tx.Rollback(ctx)
	again, err := store.OpenDatabase(ctx, 482915736204, "seq-default-rtdb")
	if err != nil || again.ID != db.ID || again.Seq != 0 {
		t.Fatalf("after rollback: %+v %v", again, err)
	}
	tx, _ = store.Begin(ctx, db)
	if n, _ := tx.Read(ctx, Path{}); n != nil {
		t.Error("rolled-back write is visible")
	}
	tx.Rollback(ctx)

	// The schema refuses rows that break the storage model.
	for _, bad := range []string{
		`INSERT INTO rtdb.nodes (database_id, path, value, priority) VALUES ($1, 'a', NULL, NULL)`,
		`INSERT INTO rtdb.nodes (database_id, path, value, priority) VALUES ($1, 'b', '{"x":1}', NULL)`,
		`INSERT INTO rtdb.nodes (database_id, path, value, priority) VALUES ($1, 'c', '1', 'true')`,
	} {
		if _, err := pool.Exec(ctx, bad, db.ID); err == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO rtdb.databases (project_number, name) VALUES (1, 'Bad_Name')`); err == nil {
		t.Error("accepted an invalid database name")
	}

	// A value Postgres refuses (SQLSTATE class 22) is a DataError, failing one write.
	_, err = pool.Exec(ctx, `SELECT '"\u0000"'::jsonb`)
	var de *DataError
	if err == nil || !errors.As(dataError(err), &de) {
		t.Errorf("class 22 error not marked: %v", err)
	}
	var unrelated *DataError
	if errors.As(dataError(errors.New("connection reset")), &unrelated) {
		t.Error("an unrelated error marked as data")
	}

	// Seq reads the committed count; a purged project's databases and nodes go.
	tx, _ = store.Begin(ctx, db)
	tx.Write(ctx, Path{"x"}, Number(1))
	tx.AdvanceSeq(ctx, 2)
	tx.Commit(ctx)
	if seq, err := store.Seq(ctx, db); err != nil || seq != 2 {
		t.Errorf("seq %d %v", seq, err)
	}
	other, _ := store.OpenDatabase(ctx, 582915736204, "keep-default-rtdb")
	tx, _ = store.Begin(ctx, other)
	tx.Write(ctx, Path{"y"}, Number(1))
	tx.Commit(ctx)
	if err := store.DeleteProject(ctx, 482915736204); err != nil {
		t.Fatal(err)
	}
	var left int
	pool.QueryRow(ctx, `SELECT count(*) FROM rtdb.nodes n JOIN rtdb.databases d ON d.id = n.database_id WHERE d.project_number = 482915736204`).Scan(&left)
	var kept int
	pool.QueryRow(ctx, `SELECT count(*) FROM rtdb.nodes WHERE database_id = $1`, other.ID).Scan(&kept)
	if left != 0 || kept != 1 {
		t.Errorf("after purge: %d rows left, %d of another project kept", left, kept)
	}
	db, _ = store.OpenDatabase(ctx, 482915736204, "seq-default-rtdb")

	// A new database starts locked (ADR-020), in a document the rules engine reads; admins
	// pass anyway.
	var doc string
	if err := pool.QueryRow(ctx, `SELECT rules FROM rtdb.databases WHERE id = $1`, db.ID).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	rs, err := rules.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("default rules %s: %v", doc, err)
	}
	src, _ := rules.NewMemorySource(map[string]any{"a": 1})
	for _, c := range []struct {
		auth rules.Auth
		want bool
	}{{rules.Unauthenticated(), false}, {rules.Admin(), true}} {
		env := rules.Env{Auth: c.auth, Data: src}
		read, err := rs.CanRead(ctx, env, []string{"a"}, rules.PlainListen)
		if err != nil || read != c.want {
			t.Errorf("default rules, admin %v: read %v %v", c.auth.IsAdmin(), read, err)
		}
	}
}
