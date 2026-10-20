package main

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// M1: one row per leaf.
type M1 struct {
	c    *pgx.Conn
	db   int32
	full bool // full set-semantics: delete ancestors that are leaves and any descendants
}

func (m *M1) Load(ctx context.Context, ds *Dataset) error {
	if _, err := m.c.Exec(ctx, `CREATE TABLE nodes(db int NOT NULL, path text COLLATE "C" NOT NULL, value jsonb NOT NULL)`); err != nil {
		return err
	}
	ls := ds.Leaves()
	rows := make([][]any, len(ls))
	for i, l := range ls {
		rows[i] = []any{m.db, l.Path, l.Val}
	}
	if _, err := m.c.CopyFrom(ctx, pgx.Identifier{"nodes"}, []string{"db", "path", "value"}, pgx.CopyFromRows(rows)); err != nil {
		return err
	}
	if _, err := m.c.Exec(ctx, `ALTER TABLE nodes ADD PRIMARY KEY(db, path)`); err != nil {
		return err
	}
	_, err := m.c.Exec(ctx, `VACUUM ANALYZE nodes`)
	return err
}

const m1Upsert = `INSERT INTO nodes(db,path,value) SELECT $1,p,v::jsonb FROM unnest($2::text[],$3::text[]) t(p,v) ON CONFLICT (db,path) DO UPDATE SET value=EXCLUDED.value`
const m1Full = `WITH d1 AS (DELETE FROM nodes WHERE db=$1 AND path = ANY($3::text[])),
d2 AS (DELETE FROM nodes WHERE db=$1 AND path >= $2||'/' AND path < $2||'0')
INSERT INTO nodes(db,path,value) VALUES($1,$2,$4::jsonb) ON CONFLICT (db,path) DO UPDATE SET value=EXCLUDED.value`

func ancestors(p string) []string {
	var a []string
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			a = append(a, p[:i])
		}
	}
	return a
}

func (m *M1) Multi(ctx context.Context, ws []W) error {
	if !m.full {
		ps := make([]string, len(ws))
		vs := make([]string, len(ws))
		for i, w := range ws {
			ps[i], vs[i] = w.Path, string(w.Val)
		}
		_, err := m.c.Exec(ctx, m1Upsert, m.db, ps, vs)
		return err
	}
	b := &pgx.Batch{}
	for _, w := range ws {
		b.Queue(m1Full, m.db, w.Path, ancestors(w.Path), string(w.Val))
	}
	return m.c.SendBatch(ctx, b).Close()
}

func (m *M1) NewMessage(ctx context.Context, room string, mm Msg) error {
	return m.Multi(ctx, msgLeaves(room, mm))
}

func (m *M1) fetch(ctx context.Context, p string) ([]string, [][]byte, error) {
	rows, err := m.c.Query(ctx, `SELECT path, value FROM nodes WHERE db=$1 AND path=$2
UNION ALL SELECT path, value FROM nodes WHERE db=$1 AND path >= $2||'/' AND path < $2||'0'`, m.db, p)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ps []string
	var vs [][]byte
	for rows.Next() {
		var path string
		var v []byte
		if err := rows.Scan(&path, &v); err != nil {
			return nil, nil, err
		}
		ps = append(ps, path)
		vs = append(vs, v)
	}
	return ps, vs, rows.Err()
}

func buildJSON(p string, ps []string, vs [][]byte) []byte {
	root := &rn{}
	for i, path := range ps {
		if path == p {
			root.val = vs[i]
			continue
		}
		root.insert(strings.Split(path[len(p)+1:], "/"), vs[i])
	}
	if root.kids == nil && root.val == nil {
		return []byte("null")
	}
	return root.encode(nil)
}

func (m *M1) Read(ctx context.Context, p string) (int, int, error) {
	ps, vs, err := m.fetch(ctx, p)
	if err != nil {
		return 0, 0, err
	}
	out := buildJSON(p, ps, vs)
	return len(ps), len(out), nil
}

func (m *M1) Delete(ctx context.Context, p string) error {
	_, err := m.c.Exec(ctx, `DELETE FROM nodes WHERE db=$1 AND (path=$2 OR (path >= $2||'/' AND path < $2||'0'))`, m.db, p)
	return err
}

func (m *M1) SeedRoom(ctx context.Context, r Room) error {
	var ws []W
	for _, mm := range r.Msgs {
		ws = append(ws, msgLeaves(r.Name, mm)...)
	}
	old := m.full
	m.full = false
	defer func() { m.full = old }()
	return m.Multi(ctx, ws)
}

func (m *M1) SetSync(ctx context.Context, on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	_, err := m.c.Exec(ctx, "SET synchronous_commit="+v)
	return err
}

func sizes(ctx context.Context, c *pgx.Conn, tables ...string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, t := range tables {
		var tot, heap int64
		if err := c.QueryRow(ctx, `SELECT pg_total_relation_size($1::regclass), pg_relation_size($1::regclass)`, t).Scan(&tot, &heap); err != nil {
			return nil, err
		}
		out[t+".total"], out[t+".heap"] = tot, heap
	}
	return out, nil
}

func (m *M1) Size(ctx context.Context) (map[string]int64, error) {
	return sizes(ctx, m.c, "nodes")
}
