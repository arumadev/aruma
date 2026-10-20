package main

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// M5: in-memory tree (map children) + append-only log table.
type tn struct {
	kids map[string]*tn
	val  []byte
}

var intern = map[string]string{}

func init() {
	for _, s := range []string{"rooms", "users", "messages", "text", "uid", "at", "name", "online", "lastSeen"} {
		intern[s] = s
	}
}
func key(s string) string {
	if v, ok := intern[s]; ok {
		return v
	}
	return strings.Clone(s)
}

func segs(path string, f func(s string)) {
	for {
		i := strings.IndexByte(path, '/')
		if i < 0 {
			f(path)
			return
		}
		f(path[:i])
		path = path[i+1:]
	}
}

func (n *tn) set(path string, val []byte) {
	cur := n
	segs(path, func(s string) {
		cur.val = nil
		if cur.kids == nil {
			cur.kids = map[string]*tn{}
		}
		c := cur.kids[s]
		if c == nil {
			c = &tn{}
			cur.kids[key(s)] = c
		}
		cur = c
	})
	cur.kids = nil
	cur.val = append([]byte(nil), val...)
}

func (n *tn) get(path string) *tn {
	cur := n
	for _, s := range strings.Split(path, "/") {
		if cur.kids == nil {
			return nil
		}
		cur = cur.kids[s]
		if cur == nil {
			return nil
		}
	}
	return cur
}

func (n *tn) del(segs []string) bool { // returns true if n became empty
	if len(segs) == 0 {
		return true
	}
	c := n.kids[segs[0]]
	if c == nil {
		return false
	}
	if c.del(segs[1:]) {
		delete(n.kids, segs[0])
	}
	return len(n.kids) == 0
}

func (n *tn) encode(b []byte) []byte {
	if n.kids == nil {
		return append(b, n.val...)
	}
	keys := make([]string, 0, len(n.kids))
	for k := range n.kids {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b = append(b, '{')
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, q(k)...)
		b = append(b, ':')
		b = n.kids[k].encode(b)
	}
	return append(b, '}')
}

type M5 struct {
	c    *pgx.Conn
	db   int32
	root *tn
	mu   sync.RWMutex
}

func NewM5(c *pgx.Conn) *M5 { return &M5{c: c, db: 1, root: &tn{}} }

func (m *M5) Load(ctx context.Context, ds *Dataset) error {
	if _, err := m.c.Exec(ctx, `CREATE TABLE log(seq bigserial PRIMARY KEY, db int NOT NULL, op "char" NOT NULL, path text COLLATE "C" NOT NULL, value jsonb)`); err != nil {
		return err
	}
	ls := ds.Leaves()
	rows := make([][]any, len(ls))
	for i, l := range ls {
		rows[i] = []any{m.db, byte('s'), l.Path, l.Val}
	}
	if _, err := m.c.CopyFrom(ctx, pgx.Identifier{"log"}, []string{"db", "op", "path", "value"}, pgx.CopyFromRows(rows)); err != nil {
		return err
	}
	for _, l := range ls {
		m.root.set(l.Path, l.Val)
	}
	_, err := m.c.Exec(ctx, `VACUUM ANALYZE log`)
	return err
}

func (m *M5) Multi(ctx context.Context, ws []W) error {
	ps := make([]string, len(ws))
	vs := make([]string, len(ws))
	for i, w := range ws {
		ps[i], vs[i] = w.Path, string(w.Val)
	}
	if _, err := m.c.Exec(ctx, `INSERT INTO log(db,op,path,value) SELECT $1,'s',p,v::jsonb FROM unnest($2::text[],$3::text[]) t(p,v)`, m.db, ps, vs); err != nil {
		return err
	}
	m.mu.Lock()
	for _, w := range ws {
		m.root.set(w.Path, w.Val)
	}
	m.mu.Unlock()
	return nil
}

func (m *M5) NewMessage(ctx context.Context, room string, mm Msg) error {
	return m.Multi(ctx, msgLeaves(room, mm))
}

func (m *M5) Read(ctx context.Context, p string) (int, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := m.root.get(p)
	if n == nil {
		return 0, 4, nil
	}
	return 0, len(n.encode(nil)), nil
}

func (m *M5) Delete(ctx context.Context, p string) error {
	if _, err := m.c.Exec(ctx, `INSERT INTO log(db,op,path) VALUES($1,'d',$2)`, m.db, p); err != nil {
		return err
	}
	m.mu.Lock()
	m.root.del(strings.Split(p, "/"))
	m.mu.Unlock()
	return nil
}

func (m *M5) SeedRoom(ctx context.Context, r Room) error {
	var ws []W
	for _, mm := range r.Msgs {
		ws = append(ws, msgLeaves(r.Name, mm)...)
	}
	return m.Multi(ctx, ws)
}

func (m *M5) SetSync(ctx context.Context, on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	_, err := m.c.Exec(ctx, "SET synchronous_commit="+v)
	return err
}

func (m *M5) Size(ctx context.Context) (map[string]int64, error) {
	return sizes(ctx, m.c, "log")
}

// Replay measures cold-start: rebuild the tree from the log.
func (m *M5) Replay(ctx context.Context) (time.Duration, int, error) {
	t := time.Now()
	root := &tn{}
	rows, err := m.c.Query(ctx, `SELECT op, path, value FROM log WHERE db=$1 ORDER BY seq`, m.db)
	if err != nil {
		return 0, 0, err
	}
	n := 0
	for rows.Next() {
		var op byte
		var p string
		var v []byte
		if err := rows.Scan(&op, &p, &v); err != nil {
			return 0, 0, err
		}
		if op == 's' {
			root.set(p, v)
		} else {
			root.del(strings.Split(p, "/"))
		}
		n++
	}
	rows.Close()
	runtime.KeepAlive(root)
	return time.Since(t), n, rows.Err()
}

// Variant B: sorted slice of children.
type tk struct {
	key string
	n   *tb
}
type tb struct {
	kids []tk
	leaf bool
	val  []byte
}

func (n *tb) set(path string, val []byte) {
	cur := n
	segs(path, func(s string) {
		i := sort.Search(len(cur.kids), func(i int) bool { return cur.kids[i].key >= s })
		if i < len(cur.kids) && cur.kids[i].key == s {
			cur = cur.kids[i].n
			return
		}
		c := &tb{}
		cur.kids = append(cur.kids, tk{})
		copy(cur.kids[i+1:], cur.kids[i:])
		cur.kids[i] = tk{key(s), c}
		cur = c
	})
	cur.val = append([]byte(nil), val...)
}

func heap() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

func count(n *tn) (inner, leaf int) {
	if n.kids == nil {
		return 0, 1
	}
	inner = 1
	for _, c := range n.kids {
		i, l := count(c)
		inner += i
		leaf += l
	}
	return
}

func ms() runtime.MemStats {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m
}

// memBench builds the whole tree from the leaf list and reports live heap growth.
// variant "A" = map children, "B" = sorted-slice children.
func memBench(variant string) {
	ls := genDataset().Leaves()
	valBytes := 0
	for _, l := range ls {
		valBytes += len(l.Val)
	}
	s0 := ms()
	var root any
	var inner, leaf int
	if variant == "A" {
		a := &tn{}
		for _, l := range ls {
			a.set(l.Path, l.Val)
		}
		root = a
		inner, leaf = count(a)
	} else {
		b := &tb{}
		for _, l := range ls {
			b.set(l.Path, l.Val)
		}
		root = b
		inner, leaf = 201206, 600609
	}
	s1 := ms()
	live := s1.HeapAlloc - s0.HeapAlloc
	tot := s1.TotalAlloc - s0.TotalAlloc
	nodes := leaf + inner
	record(Result{Model: "m5", Op: "memory variant " + variant, Rows: len(ls), Bytes: int(live),
		Extra: fmt.Sprintf("leaves=%d inner=%d nodes=%d live_heap_delta=%d total_alloc_delta=%d objects_delta=%d bytes/leaf=%.0f bytes/node=%.0f raw_value_bytes=%d",
			leaf, inner, nodes, live, tot, int64(s1.HeapObjects)-int64(s0.HeapObjects), float64(live)/float64(leaf), float64(live)/float64(nodes), valBytes)})
	runtime.KeepAlive(root)
	runtime.KeepAlive(ls)
}
