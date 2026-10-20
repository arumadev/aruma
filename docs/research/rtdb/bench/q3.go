package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

func explain(ctx context.Context, c *pgx.Conn, label, sql string, args ...any) {
	rows, err := c.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
	if err != nil {
		fmt.Println("explain err", err)
		return
	}
	defer rows.Close()
	f, _ := os.OpenFile("explain.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	defer f.Close()
	fmt.Fprintf(f, "=== %s\n%s\n", label, sql)
	for rows.Next() {
		var l string
		rows.Scan(&l)
		fmt.Fprintln(f, l)
	}
	fmt.Fprintln(f)
}

const topExpr = `WITH top AS (SELECT left(path, length(path)-3) AS mp FROM nodes
  WHERE db=1 AND path LIKE '%/at' AND regexp_replace(path,'/[^/]+/at$','') = $1
  ORDER BY (value #>> '{}')::numeric DESC LIMIT 50)
SELECT n.path, n.value FROM top t JOIN nodes n ON n.db=1 AND n.path >= t.mp||'/' AND n.path < t.mp||'0'`

const topIdx = `WITH top AS (SELECT child_key FROM idx WHERE db=1 AND parent_path=$1 AND index_key='at'
  ORDER BY sort_num DESC, child_key DESC LIMIT 50)
SELECT n.path, n.value FROM top t JOIN nodes n ON n.db=1 AND n.path >= $1||'/'||t.child_key||'/' AND n.path < $1||'/'||t.child_key||'0'`

func runQ3(ctx context.Context) {
	c := connect(ctx, "aruma_bench_rtdb_m1")
	defer c.Close(ctx)
	ds := genDataset()
	m := &M1{c: c, db: 1}
	rng := newRng()
	const parent = "rooms/r1/messages"
	fetchQ := func(sql string) func(int) (int, int, error) {
		return func(i int) (int, int, error) {
			rows, err := c.Query(ctx, sql, parent)
			if err != nil {
				return 0, 0, err
			}
			var ps []string
			var vs [][]byte
			for rows.Next() {
				var p string
				var v []byte
				rows.Scan(&p, &v)
				ps = append(ps, p)
				vs = append(vs, v)
			}
			rows.Close()
			out := buildJSON(parent, ps, vs)
			return len(ps), len(out), rows.Err()
		}
	}
	// baseline: unindexed query = fetch all children, sort by at in client, keep 50
	measure("q3", "unindexed: send 2000 children + client sort/limit 50", 3, 30, func(i int) (int, int, error) {
		ps, vs, err := m.fetch(ctx, parent)
		if err != nil {
			return 0, 0, err
		}
		out := buildJSON(parent, ps, vs)
		var tree map[string]map[string]json.RawMessage
		if err := json.Unmarshal(out, &tree); err != nil {
			return 0, 0, err
		}
		type kv struct {
			k  string
			at float64
		}
		var l []kv
		for k, v := range tree {
			var a float64
			json.Unmarshal(v["at"], &a)
			l = append(l, kv{k, a})
		}
		sort.Slice(l, func(i, j int) bool { return l[i].at > l[j].at })
		return len(ps), len(out), nil
	})
	newMsg := 0
	nm := func(mm Model) func(int) (int, int, error) {
		return func(i int) (int, int, error) {
			newMsg++
			return 3, 0, mm.NewMessage(ctx, "r1", Msg{ID: fmt.Sprintf("-Y%019d", newMsg), Text: "a brand new message text here", UID: "u000000000000000000000000001", At: 1900000000000 + int64(newMsg)})
		}
	}
	measure("q3", "baseline write new message (no index)", 5, 300, nm(m))

	// A: expression + partial index
	t := time.Now()
	if _, err := c.Exec(ctx, `CREATE INDEX nodes_at_expr ON nodes (db, (regexp_replace(path,'/[^/]+/at$','')), ((value #>> '{}')::numeric)) WHERE path LIKE '%/at'`); err != nil {
		panic(err)
	}
	c.Exec(ctx, "ANALYZE nodes")
	record(Result{Model: "q3", Op: "expr-index build", MedianMs: float64(time.Since(t).Milliseconds()), Extra: "CREATE INDEX nodes_at_expr (partial, regexp_replace + numeric cast)"})
	sz, _ := sizes(ctx, c, "nodes_at_expr")
	b, _ := json.Marshal(sz)
	record(Result{Model: "q3", Op: "expr-index size", Extra: string(b)})
	explain(ctx, c, "expr index: top 50 by at", topExpr, parent)
	measure("q3", "expr-index: orderByChild(at) limitToLast(50)", 5, 100, fetchQ(topExpr))
	measure("q3", "expr-index: write new message (index maintained)", 5, 300, nm(m))
	_ = rng
	c.Exec(ctx, "DROP INDEX nodes_at_expr")

	// B: separate idx table
	t = time.Now()
	c.Exec(ctx, `CREATE TABLE idx(db int NOT NULL, parent_path text COLLATE "C" NOT NULL, index_key text COLLATE "C" NOT NULL, sort_num double precision NOT NULL, child_key text COLLATE "C" NOT NULL, PRIMARY KEY(db,parent_path,index_key,sort_num,child_key))`)
	if _, err := c.Exec(ctx, `INSERT INTO idx SELECT db, regexp_replace(path,'/[^/]+/at$',''), 'at', (value #>> '{}')::double precision, substring(path from '([^/]+)/at$') FROM nodes WHERE db=1 AND path LIKE 'rooms/%/at' ORDER BY 1,2,3,4,5`); err != nil {
		panic(err)
	}
	c.Exec(ctx, "VACUUM ANALYZE idx")
	record(Result{Model: "q3", Op: "idx-table build", MedianMs: float64(time.Since(t).Milliseconds())})
	sz, _ = sizes(ctx, c, "idx")
	b, _ = json.Marshal(sz)
	record(Result{Model: "q3", Op: "idx-table size", Extra: string(b)})
	explain(ctx, c, "idx table: top 50 by at", topIdx, parent)
	measure("q3", "idx-table: orderByChild(at) limitToLast(50)", 5, 100, fetchQ(topIdx))
	measure("q3", "idx-table: write new message (+idx row, 1 tx)", 5, 300, func(i int) (int, int, error) {
		newMsg++
		mm := Msg{ID: fmt.Sprintf("-X%019d", newMsg), Text: "a brand new message text here", UID: "u000000000000000000000000001", At: 1950000000000 + int64(newMsg)}
		ws := msgLeaves("r1", mm)
		ps := []string{ws[0].Path, ws[1].Path, ws[2].Path}
		vs := []string{string(ws[0].Val), string(ws[1].Val), string(ws[2].Val)}
		b := &pgx.Batch{}
		b.Queue(m1Upsert, int32(1), ps, vs)
		b.Queue(`INSERT INTO idx VALUES(1,'rooms/r1/messages','at',$1,$2)`, float64(mm.At), mm.ID)
		return 4, 0, c.SendBatch(ctx, b).Close()
	})
	_ = ds
}

func newRng() int { return 0 }
