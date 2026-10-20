package main

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// M3: jsonb document per key at `depth` (1 = per top-level child, 2 = per room/user).
type M3 struct {
	c     *pgx.Conn
	db    int32
	depth int
}

func msgObj(mm Msg) []byte {
	var b []byte
	b = append(b, `{"text":`...)
	b = append(b, q(mm.Text)...)
	b = append(b, `,"uid":`...)
	b = append(b, q(mm.UID)...)
	b = append(b, `,"at":`...)
	b = strconv.AppendInt(b, mm.At, 10)
	return append(b, '}')
}
func roomObj(msgs []Msg) []byte {
	b := []byte(`{"messages":{`)
	for i, mm := range msgs {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, q(mm.ID)...)
		b = append(b, ':')
		b = append(b, msgObj(mm)...)
	}
	return append(b, '}', '}')
}
func userObj(u User) []byte {
	return []byte(fmt.Sprintf(`{"name":%s,"online":%v,"lastSeen":%d}`, q(u.Name), u.Online, u.LastSeen))
}

func (m *M3) Load(ctx context.Context, ds *Dataset) error {
	if _, err := m.c.Exec(ctx, `CREATE TABLE docs(db int NOT NULL, key text COLLATE "C" NOT NULL, doc jsonb NOT NULL, PRIMARY KEY(db,key))`); err != nil {
		return err
	}
	var rows [][]any
	if m.depth == 1 {
		rb := []byte("{")
		for i, r := range ds.Rooms {
			if i > 0 {
				rb = append(rb, ',')
			}
			rb = append(rb, q(r.Name)...)
			rb = append(rb, ':')
			rb = append(rb, roomObj(r.Msgs)...)
		}
		rb = append(rb, '}')
		ub := []byte("{")
		for i, u := range ds.Users {
			if i > 0 {
				ub = append(ub, ',')
			}
			ub = append(ub, q(u.UID)...)
			ub = append(ub, ':')
			ub = append(ub, userObj(u)...)
		}
		ub = append(ub, '}')
		rows = [][]any{{m.db, "rooms", rb}, {m.db, "users", ub}}
	} else {
		for _, r := range ds.Rooms {
			rows = append(rows, []any{m.db, "rooms/" + r.Name, roomObj(r.Msgs)})
		}
		for _, u := range ds.Users {
			rows = append(rows, []any{m.db, "users/" + u.UID, userObj(u)})
		}
	}
	if _, err := m.c.CopyFrom(ctx, pgx.Identifier{"docs"}, []string{"db", "key", "doc"}, pgx.CopyFromRows(rows)); err != nil {
		return err
	}
	_, err := m.c.Exec(ctx, `VACUUM ANALYZE docs`)
	return err
}

func (m *M3) locate(path string) (string, []string) {
	s := strings.Split(path, "/")
	return strings.Join(s[:m.depth], "/"), s[m.depth:]
}

func (m *M3) Multi(ctx context.Context, ws []W) error {
	type upd struct {
		sql  strings.Builder
		args []any
		n    int
	}
	groups := map[string]*upd{}
	var order []string
	for _, w := range ws {
		key, rest := m.locate(w.Path)
		u := groups[key]
		if u == nil {
			u = &upd{}
			u.args = []any{m.db, key}
			groups[key] = u
			order = append(order, key)
		}
		u.args = append(u.args, rest, string(w.Val))
		u.n++
	}
	b := &pgx.Batch{}
	for _, k := range order {
		u := groups[k]
		expr := "doc"
		for i := 0; i < u.n; i++ {
			expr = fmt.Sprintf("jsonb_set(%s,$%d::text[],$%d::jsonb,true)", expr, 3+2*i, 4+2*i)
		}
		b.Queue("UPDATE docs SET doc="+expr+" WHERE db=$1 AND key=$2", u.args...)
	}
	return m.c.SendBatch(ctx, b).Close()
}

func (m *M3) NewMessage(ctx context.Context, room string, mm Msg) error {
	key, rest := m.locate("rooms/" + room + "/messages/" + mm.ID)
	_, err := m.c.Exec(ctx, `UPDATE docs SET doc=jsonb_set(doc,$3::text[],$4::jsonb,true) WHERE db=$1 AND key=$2`, m.db, key, rest, string(msgObj(mm)))
	return err
}

func (m *M3) Read(ctx context.Context, p string) (int, int, error) {
	segs := strings.Split(p, "/")
	if len(segs) < m.depth {
		rows, err := m.c.Query(ctx, `SELECT key, doc FROM docs WHERE db=$1 AND key >= $2||'/' AND key < $2||'0'`, m.db, p)
		if err != nil {
			return 0, 0, err
		}
		defer rows.Close()
		var buf bytes.Buffer
		buf.WriteByte('{')
		n := 0
		for rows.Next() {
			var k string
			var d []byte
			if err := rows.Scan(&k, &d); err != nil {
				return 0, 0, err
			}
			if n > 0 {
				buf.WriteByte(',')
			}
			buf.Write(q(k[len(p)+1:]))
			buf.WriteByte(':')
			buf.Write(d)
			n++
		}
		buf.WriteByte('}')
		return n, buf.Len(), rows.Err()
	}
	key, rest := m.locate(p)
	var d []byte
	var err error
	if len(rest) == 0 {
		err = m.c.QueryRow(ctx, `SELECT doc FROM docs WHERE db=$1 AND key=$2`, m.db, key).Scan(&d)
	} else {
		err = m.c.QueryRow(ctx, `SELECT doc #> $3::text[] FROM docs WHERE db=$1 AND key=$2`, m.db, key, rest).Scan(&d)
	}
	return 1, len(d), err
}

func (m *M3) Delete(ctx context.Context, p string) error {
	key, rest := m.locate(p)
	if len(rest) == 0 {
		_, err := m.c.Exec(ctx, `DELETE FROM docs WHERE db=$1 AND key=$2`, m.db, key)
		return err
	}
	_, err := m.c.Exec(ctx, `UPDATE docs SET doc = doc #- $3::text[] WHERE db=$1 AND key=$2`, m.db, key, rest)
	return err
}

func (m *M3) SeedRoom(ctx context.Context, r Room) error {
	obj := string(roomObj(r.Msgs))
	if m.depth == 2 {
		_, err := m.c.Exec(ctx, `INSERT INTO docs VALUES($1,$2,$3::jsonb)`, m.db, "rooms/"+r.Name, obj)
		return err
	}
	_, err := m.c.Exec(ctx, `UPDATE docs SET doc=jsonb_set(doc,$2::text[],$3::jsonb,true) WHERE db=$1 AND key='rooms'`, m.db, []string{r.Name}, obj)
	return err
}

func (m *M3) SetSync(ctx context.Context, on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	_, err := m.c.Exec(ctx, "SET synchronous_commit="+v)
	return err
}

func (m *M3) Size(ctx context.Context) (map[string]int64, error) {
	return sizes(ctx, m.c, "docs")
}
