package core

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/osama300/aurma-project/internal/store/storetest"
)

// memStore is a Store on an in-memory tree, built on Node.Set: a second, independent
// implementation of the storage semantics that the Postgres store must agree with.
type memStore struct {
	mu  sync.Mutex
	dbs map[string]*memDB
	// failCommits makes the next n commits fail, as a lost database connection would.
	failCommits int
	commits     int
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
		db = &memDB{info: DatabaseInfo{ID: int64(len(s.dbs) + 1), Project: project, Name: name}}
		s.dbs[name] = db
	}
	return db.info, nil
}

func (s *memStore) Begin(_ context.Context, info DatabaseInfo) (StoreTx, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db := s.dbs[info.Name]
	return &memTx{s: s, db: db, root: db.root, seq: db.info.Seq}, nil
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

func (t *memTx) Read(_ context.Context, p Path) (*Node, error) { return t.root.Get(p), nil }
func (t *memTx) Write(_ context.Context, p Path, v *Node) error {
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
}
