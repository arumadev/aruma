// Throwaway RTDB storage benchmark (not Aruma code). See ../rtdb/G-storage-bench.md
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const adminURL = "postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable"

func dbURL(name string) string {
	return "postgres://postgres@127.0.0.1:5432/" + name + "?sslmode=disable"
}

type Msg struct {
	ID, Text, UID string
	At            int64
}
type Room struct {
	Name string
	Msgs []Msg
}
type User struct {
	UID, Name string
	Online    bool
	LastSeen  int64
}
type Dataset struct {
	Rooms []Room
	Users []User
}
type W struct {
	Path string
	Val  []byte
}

var words = strings.Fields("lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt ut labore et dolore magna aliqua hello world chat message from the team today meeting lunch ok thanks great see you")

func genDataset() *Dataset {
	rng := rand.New(rand.NewSource(42))
	ds := &Dataset{}
	for r := 0; r < 500; r++ {
		n := 297
		if r == 1 {
			n = 2000
		}
		room := Room{Name: "r" + strconv.Itoa(r)}
		for i := 0; i < n; i++ {
			var sb strings.Builder
			for sb.Len() < 40+rng.Intn(40) {
				sb.WriteString(words[rng.Intn(len(words))])
				sb.WriteByte(' ')
			}
			room.Msgs = append(room.Msgs, Msg{
				ID:   fmt.Sprintf("-N%06d%012x", i, rng.Uint64()&0xffffffffffff),
				Text: strings.TrimSpace(sb.String()),
				UID:  fmt.Sprintf("u%027d", rng.Intn(50000)),
				At:   1700000000000 + int64(i)*1000 + int64(rng.Intn(900)),
			})
		}
		ds.Rooms = append(ds.Rooms, room)
	}
	for u := 0; u < 50000; u++ {
		ds.Users = append(ds.Users, User{UID: fmt.Sprintf("u%027d", u), Name: fmt.Sprintf("User Number %d", u), Online: u%7 == 0, LastSeen: 1700000000000 + int64(u)*997})
	}
	return ds
}

func q(s string) []byte { return strconv.AppendQuote(nil, s) }

func msgLeaves(room string, m Msg) []W {
	p := "rooms/" + room + "/messages/" + m.ID + "/"
	return []W{{p + "text", q(m.Text)}, {p + "uid", q(m.UID)}, {p + "at", []byte(strconv.FormatInt(m.At, 10))}}
}

func (ds *Dataset) Leaves() []W {
	var ls []W
	for _, r := range ds.Rooms {
		for _, m := range r.Msgs {
			ls = append(ls, msgLeaves(r.Name, m)...)
		}
	}
	for _, u := range ds.Users {
		p := "users/" + u.UID + "/"
		ls = append(ls, W{p + "name", q(u.Name)}, W{p + "online", []byte(strconv.FormatBool(u.Online))}, W{p + "lastSeen", []byte(strconv.FormatInt(u.LastSeen, 10))})
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].Path < ls[j].Path })
	return ls
}

// reconstruction tree (used by M1 and M5 reads)
type rn struct {
	kids map[string]*rn
	val  []byte
}

func (n *rn) insert(segs []string, val []byte) {
	for _, s := range segs {
		if n.kids == nil {
			n.kids = map[string]*rn{}
		}
		c := n.kids[s]
		if c == nil {
			c = &rn{}
			n.kids[s] = c
		}
		n = c
	}
	n.val = val
}
func (n *rn) encode(b []byte) []byte {
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
		b = strconv.AppendQuote(b, k)
		b = append(b, ':')
		b = n.kids[k].encode(b)
	}
	return append(b, '}')
}

type Model interface {
	Load(ctx context.Context, ds *Dataset) error
	Multi(ctx context.Context, ws []W) error
	NewMessage(ctx context.Context, room string, m Msg) error
	Read(ctx context.Context, path string) (rows, bytes int, err error)
	Delete(ctx context.Context, path string) error
	SeedRoom(ctx context.Context, r Room) error
	SetSync(ctx context.Context, on bool) error
	Size(ctx context.Context) (map[string]int64, error)
}

type Result struct {
	Model, Op       string
	N               int
	MedianMs, P95Ms float64
	Rows, Bytes     int
	Extra           string `json:",omitempty"`
}

var resFile *os.File

func record(r Result) {
	fmt.Printf("%-8s %-34s n=%-4d median=%9.3f ms p95=%9.3f ms rows=%-7d bytes=%-9d %s\n", r.Model, r.Op, r.N, r.MedianMs, r.P95Ms, r.Rows, r.Bytes, r.Extra)
	b, _ := json.Marshal(r)
	resFile.Write(append(b, '\n'))
}

func measure(model, op string, warm, iters int, f func(i int) (int, int, error)) {
	var ds []float64
	var rows, bytes int
	for i := 0; i < warm+iters; i++ {
		t := time.Now()
		r, b, err := f(i)
		d := time.Since(t)
		if err != nil {
			fmt.Println("ERROR", model, op, err)
			return
		}
		if i >= warm {
			ds = append(ds, float64(d.Microseconds())/1000)
			rows, bytes = r, b
		}
	}
	sort.Float64s(ds)
	record(Result{Model: model, Op: op, N: iters, MedianMs: ds[len(ds)/2], P95Ms: ds[p95idx(len(ds))], Rows: rows, Bytes: bytes})
}

func connect(ctx context.Context, name string) *pgx.Conn {
	c, err := pgx.Connect(ctx, dbURL(name))
	if err != nil {
		panic(err)
	}
	return c
}

func recreateDB(ctx context.Context, name string) {
	a, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		panic(err)
	}
	defer a.Close(ctx)
	if !strings.HasPrefix(name, "aruma_bench_rtdb_") {
		panic("refusing: " + name)
	}
	a.Exec(ctx, "DROP DATABASE IF EXISTS "+name)
	if _, err := a.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		panic(err)
	}
}

func main() {
	model := flag.String("model", "", "m1|m3d1|m3d2|m5|mem|drop")
	q3 := flag.Bool("q3", false, "run Q3 experiments on the m1 db")
	flag.Parse()
	ctx := context.Background()
	var err error
	resFile, err = os.OpenFile("results.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer resFile.Close()
	if *q3 {
		runQ3(ctx)
		return
	}
	switch *model {
	case "drop":
		a, _ := pgx.Connect(ctx, adminURL)
		for _, n := range []string{"m1", "m3d1", "m3d2", "m5"} {
			a.Exec(ctx, "DROP DATABASE IF EXISTS aruma_bench_rtdb_"+n)
		}
		a.Close(ctx)
		return
	case "mem":
		memBench("A")
		return
	case "memb":
		memBench("B")
		return
	}
	runModel(ctx, *model)
}

func pick[T any](r *rand.Rand, s []T) T { return s[r.Intn(len(s))] }

func runModel(ctx context.Context, name string) {
	ds := genDataset()
	dbn := "aruma_bench_rtdb_" + name
	recreateDB(ctx, dbn)
	c := connect(ctx, dbn)
	defer c.Close(ctx)
	var m Model
	heavy := false
	switch name {
	case "m1":
		m = &M1{c: c, db: 1}
	case "m3d1":
		m = &M3{c: c, db: 1, depth: 1}
		heavy = true
	case "m3d2":
		m = &M3{c: c, db: 1, depth: 2}
	case "m5":
		m = NewM5(c)
	}
	t := time.Now()
	if err := m.Load(ctx, ds); err != nil {
		panic(err)
	}
	fmt.Println("load time", time.Since(t))
	sz, _ := m.Size(ctx)
	b, _ := json.Marshal(sz)
	record(Result{Model: name, Op: "(i) size at load", Extra: string(b)})
	for _, on := range []bool{true} {
		m.SetSync(ctx, on)
	}
	it := func(n, heavyN int) int {
		if heavy {
			return heavyN
		}
		return n
	}
	// candidate leaf paths: message 'at' leaves
	var atPaths []string
	for _, r := range ds.Rooms {
		for _, mm := range r.Msgs {
			atPaths = append(atPaths, "rooms/"+r.Name+"/messages/"+mm.ID+"/at")
		}
	}
	rng := rand.New(rand.NewSource(7))
	val := func() []byte { return []byte(strconv.FormatInt(1800000000000+rng.Int63n(1e9), 10)) }
	run := func(label string) {
		measure(name, label+"(a) write 1 leaf", it(600, 5), it(300, 15), func(i int) (int, int, error) {
			return 1, 0, m.Multi(ctx, []W{{pick(rng, atPaths), val()}})
		})
		measure(name, label+"(b) multi-path 10 leaves 1 tx", it(300, 5), it(200, 15), func(i int) (int, int, error) {
			seen := map[string]bool{}
			var ws []W
			for len(ws) < 10 {
				p := pick(rng, atPaths)
				if !seen[p] {
					seen[p] = true
					ws = append(ws, W{p, val()})
				}
			}
			return 10, 0, m.Multi(ctx, ws)
		})
	}
	run("")
	r1 := ds.Rooms[1]
	measure(name, "(d) read one message", 10, it(300, 15), func(i int) (int, int, error) {
		mm := r1.Msgs[rng.Intn(len(r1.Msgs))]
		return m.Read(ctx, "rooms/r1/messages/"+mm.ID)
	})
	measure(name, "(e) read room r1 (2000 msgs)", 3, it(30, 8), func(i int) (int, int, error) {
		return m.Read(ctx, "rooms/r1")
	})
	measure(name, "(f) read all users (50k)", 1, it(7, 4), func(i int) (int, int, error) {
		return m.Read(ctx, "users")
	})
	measure(name, "(q3) read rooms/r1/messages (unindexed query)", 3, it(30, 8), func(i int) (int, int, error) {
		return m.Read(ctx, "rooms/r1/messages")
	})
	nm := 0
	measure(name, "(c) write new message (3 leaves)", 5, it(300, 15), func(i int) (int, int, error) {
		nm++
		return 3, 0, m.NewMessage(ctx, "r1", Msg{ID: fmt.Sprintf("-Z%019d", nm), Text: "a brand new message text here", UID: "u000000000000000000000000001", At: 1800000000000 + int64(nm)})
	})
	// (g) delete subtree
	nd := it(8, 5)
	for i := 0; i < nd+1; i++ {
		rm := Room{Name: fmt.Sprintf("del%d", i), Msgs: r1.Msgs}
		if err := m.SeedRoom(ctx, rm); err != nil {
			panic(err)
		}
	}
	measure(name, "(g) delete room subtree (2000 msgs)", 1, nd, func(i int) (int, int, error) {
		return 6000, 0, m.Delete(ctx, fmt.Sprintf("rooms/del%d", i))
	})
	// (h) group commit throughput
	ns := []int{1, 16, 128}
	if name == "m3d1" {
		ns = []int{1}
	}
	secs := 6 * time.Second
	if heavy {
		secs = 5 * time.Second
	}
	for _, sync := range []bool{true, false} {
		m.SetSync(ctx, sync)
		for _, n := range ns {
			var lat []float64
			writes := 0
			start := time.Now()
			for time.Since(start) < secs {
				seen := map[string]bool{}
				var ws []W
				for len(ws) < n {
					p := pick(rng, atPaths)
					if !seen[p] {
						seen[p] = true
						ws = append(ws, W{p, val()})
					}
				}
				t0 := time.Now()
				if err := m.Multi(ctx, ws); err != nil {
					fmt.Println("ERR", err)
					break
				}
				lat = append(lat, float64(time.Since(t0).Microseconds())/1000)
				writes += n
			}
			el := time.Since(start).Seconds()
			sort.Float64s(lat)
			record(Result{Model: name, Op: fmt.Sprintf("(h) single-writer N=%d sync=%v", n, sync), N: len(lat), MedianMs: lat[len(lat)/2], P95Ms: lat[int(float64(len(lat))*0.95)], Rows: writes,
				Extra: fmt.Sprintf("writes/s=%.0f txn/s=%.0f", float64(writes)/el, float64(len(lat))/el)})
		}
	}
	m.SetSync(ctx, true)
	if name == "m1" {
		m1 := m.(*M1)
		m1.full = true
		run("full-semantics ")
		for _, n := range []int{1, 16, 128} {
			for _, sync := range []bool{true, false} {
				m.SetSync(ctx, sync)
				var lat []float64
				writes := 0
				start := time.Now()
				for time.Since(start) < secs {
					seen := map[string]bool{}
					var ws []W
					for len(ws) < n {
						p := pick(rng, atPaths)
						if !seen[p] {
							seen[p] = true
							ws = append(ws, W{p, val()})
						}
					}
					t0 := time.Now()
					if err := m.Multi(ctx, ws); err != nil {
						fmt.Println("ERR", err)
						break
					}
					lat = append(lat, float64(time.Since(t0).Microseconds())/1000)
					writes += n
				}
				el := time.Since(start).Seconds()
				sort.Float64s(lat)
				record(Result{Model: name, Op: fmt.Sprintf("(h) FULL-semantics N=%d sync=%v", n, sync), N: len(lat), MedianMs: lat[len(lat)/2], P95Ms: lat[int(float64(len(lat))*0.95)], Rows: writes,
					Extra: fmt.Sprintf("writes/s=%.0f txn/s=%.0f", float64(writes)/el, float64(len(lat))/el)})
			}
		}
		m1.full = false
		m.SetSync(ctx, true)
	}
	if m5, ok := m.(*M5); ok {
		d, n, err := m5.Replay(ctx)
		record(Result{Model: name, Op: "cold-start replay of log", MedianMs: float64(d.Milliseconds()), Rows: n, Extra: fmt.Sprint(err)})
	}
	c.Exec(ctx, "VACUUM ANALYZE")
	sz, _ = m.Size(ctx)
	b, _ = json.Marshal(sz)
	record(Result{Model: name, Op: "(i) size after benchmark (bloat incl.)", Extra: string(b)})
}

func p95idx(n int) int {
	i := int(float64(n) * 0.95)
	if i >= n {
		i = n - 1
	}
	return i
}
