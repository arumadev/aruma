// Command soak runs a steady mixed workload against an Aruma server for hours or days and
// tells whether the server held up: every acknowledged write is still there, errors stayed
// rare, and memory, goroutines, open files, Postgres connections and table sizes stayed flat.
//
// It starts the aruma binary you give it on a throwaway database of your Postgres (named by
// ARUMA_TEST_DATABASE_URL) and a throwaway data directory, so it never touches your projects,
// and removes both at the end (-keep keeps them). The workload uses a bounded set of keys, so a
// healthy server's tables and data directory stop growing once autovacuum and the Storage GC
// keep up: what keeps growing is reported.
//
//	ARUMA_TEST_DATABASE_URL=postgres://postgres@127.0.0.1:5432/postgres \
//	  go run ./tools/soak -bin /usr/local/bin/aruma -d 72h -out soak.csv
//
// Every minute it samples the server's RSS, heap, goroutines and open files (from its metrics
// address), its Postgres connections, the database's size and dead tuples, and the data
// directory's size, and appends them to -out. At the end it fits a line to each series over the
// part after the warm-up and prints its slope per hour. A series is a leak when it rises
// steadily (correlation above 0.8 with time) and the line would add more than -leak-growth of
// its median over 72 hours. The exit status is 0 for PASS and 1 for FAIL.
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"connectrpc.com/connect"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
	"github.com/arumadev/aruma/tools/internal/rtdbkit"
)

type config struct {
	bin        string
	dur        time.Duration
	interval   time.Duration
	warmup     time.Duration
	out        string
	port       int
	mport      int
	rate       float64
	listeners  int
	keys       int
	leakGrowth float64
	maxErrors  float64
	keep       bool
}

func main() {
	var c config
	flag.StringVar(&c.bin, "bin", "", "the aruma binary (default: build ./cmd/aruma)")
	flag.DurationVar(&c.dur, "d", 72*time.Hour, "how long the workload runs")
	flag.DurationVar(&c.interval, "every", time.Minute, "how often to sample")
	flag.DurationVar(&c.warmup, "warmup", 0, "samples before this are left out of the slopes (default: a third of -d, at most 6h)")
	flag.StringVar(&c.out, "out", "soak.csv", "the samples, as CSV")
	flag.IntVar(&c.port, "port", 8797, "loopback port of the server")
	flag.IntVar(&c.mport, "metrics-port", 8798, "loopback port of its metrics address")
	flag.Float64Var(&c.rate, "rate", 20, "operations per second for each service")
	flag.IntVar(&c.listeners, "listeners", 50, "Realtime Database and Document Database listeners each")
	flag.IntVar(&c.keys, "keys", 2000, "keys each service writes, over and over")
	flag.Float64Var(&c.leakGrowth, "leak-growth", 0.5, "a series that would grow by more than this fraction of its median in 72 h is a leak")
	flag.Float64Var(&c.maxErrors, "max-errors", 0.001, "the largest fraction of failed operations that passes")
	flag.BoolVar(&c.keep, "keep", false, "keep the database and data directory")
	flag.Parse()
	if c.warmup == 0 {
		c.warmup = min(c.dur/3, 6*time.Hour)
	}
	ok, err := run(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, "soak:", err)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

type soak struct {
	cfg              config
	srv              *rtdbkit.Server
	base, project    string
	bucket, ns, db   string
	dataDir          string
	fs               firestorepbconnect.FirestoreClient
	hc               *http.Client
	ops, errs        map[string]*atomic.Int64
	firstErr         sync.Map
	ackMu            sync.Mutex
	acked            map[string]string // the last acknowledged value of each key, by service/key
	deliveries       atomic.Int64
	listenerFailures atomic.Int64
}

func run(c config) (bool, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir, err := os.MkdirTemp("", "aruma-soak-")
	if err != nil {
		return false, err
	}
	if c.bin == "" {
		if c.bin, err = rtdbkit.Build(ctx, dir); err != nil {
			return false, err
		}
	}
	s := &soak{cfg: c, dataDir: filepath.Join(dir, "data"), acked: map[string]string{},
		ops: map[string]*atomic.Int64{}, errs: map[string]*atomic.Int64{}}
	for _, k := range []string{"auth", "rtdb", "docs", "docs-query", "storage-up", "storage-down"} {
		s.ops[k], s.errs[k] = &atomic.Int64{}, &atomic.Int64{}
	}
	logf, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		return false, err
	}
	s.srv, err = rtdbkit.NewServer(ctx, rtdbkit.Options{Bin: c.bin, Port: c.port, MetricsPort: c.mport, Name: "soak", Log: logf, Env: map[string]string{
		"ARUMA_DOCUMENTS_ENABLED": "true", "ARUMA_DOCUMENTS_DEV_OPEN_RULES": "true",
		"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": s.dataDir,
		"ARUMA_AUTH_SIGN_UPS_PER_MINUTE": "100000", "ARUMA_AUTH_SIGN_UP_BURST": "100000",
		"ARUMA_AUTH_REQUESTS_PER_MINUTE": "1000000", "ARUMA_AUTH_REQUEST_BURST": "100000",
		"ARUMA_STORAGE_UPLOAD_REQUESTS_PER_IP": "100000", "ARUMA_STORAGE_UPLOAD_STARTS_PER_MINUTE_PER_IP": "1000000",
	}})
	if err != nil {
		return false, err
	}
	defer func() {
		if c.keep {
			_ = s.srv.Term()
			fmt.Printf("kept database %s and directory %s\n", s.srv.DBName, dir)
			return
		}
		s.srv.Drop()
		os.RemoveAll(dir)
	}()
	if err := s.srv.Start(ctx); err != nil {
		return false, err
	}
	if err := s.setup(ctx); err != nil {
		return false, err
	}
	f, err := os.Create(c.out)
	if err != nil {
		return false, err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	cols := []string{"minute", "rss_mb", "heap_mb", "goroutines", "open_fds", "pg_connections", "db_mb", "dead_tuples", "data_dir_mb", "ops", "errors", "load1"}
	_ = w.Write(cols)
	w.Flush()

	wctx, stopLoad := context.WithCancel(ctx)
	var wg sync.WaitGroup
	s.startLoad(wctx, &wg)
	start := time.Now()
	var samples [][]float64
	tick := time.NewTicker(c.interval)
	fmt.Printf("soak: %s for %v, sampling every %v into %s\n", s.base, c.dur, c.interval, c.out)
	for done := false; !done; {
		select {
		case <-ctx.Done():
			done = true
		case <-tick.C:
			row := s.sample(ctx, time.Since(start))
			samples = append(samples, row)
			rec := make([]string, len(row))
			for i, v := range row {
				rec[i] = strconv.FormatFloat(v, 'f', -1, 64)
			}
			_ = w.Write(rec)
			w.Flush()
			if time.Since(start) >= c.dur {
				done = true
			}
		}
	}
	stopLoad()
	wg.Wait()
	missing := s.verify(context.Background())
	return s.summary(cols, samples, missing), nil
}

func (s *soak) setup(ctx context.Context) error {
	s.project = "soak-" + rtdbkit.RandomHex(3)
	var p struct {
		StorageBucket string `json:"storageBucket"`
	}
	if err := s.srv.ConsoleJSON(ctx, "POST", "/console/v1/projects", map[string]string{"projectId": s.project, "displayName": s.project}, &p); err != nil {
		return err
	}
	open := "rules_version = '2';\nservice firebase.storage { match /b/{bucket}/o { match /{allPaths=**} { allow read, write: if true; } } }"
	if err := s.srv.Console(ctx, "PUT", "/console/v1/projects/"+s.project+"/storage/rules", map[string]string{"source": open}); err != nil {
		return err
	}
	if err := s.srv.Console(ctx, "POST", "/console/v1/projects/"+s.project+"/apps", map[string]string{"platform": "WEB", "displayName": "soak"}); err != nil {
		return err
	}
	if err := s.srv.Console(ctx, "PATCH", "/console/v1/projects/"+s.project+"/auth/config", map[string]any{"passwordEnabled": true, "clientSignupEnabled": true}); err != nil {
		return err
	}
	s.base, s.bucket, s.ns = s.srv.URL, p.StorageBucket, s.project+"-default-rtdb"
	s.db = "projects/" + s.project + "/databases/(default)"
	var cp http.Protocols
	cp.SetUnencryptedHTTP2(true)
	s.fs = firestorepbconnect.NewFirestoreClient(&http.Client{Transport: &http.Transport{Protocols: &cp}}, s.base, connect.WithGRPC())
	s.hc = &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 64}}
	return nil
}

func (s *soak) apiKey(ctx context.Context) (string, error) {
	var keys struct {
		Keys []struct {
			KeyString string `json:"keyString"`
		} `json:"keys"`
	}
	if err := s.srv.ConsoleJSON(ctx, "GET", "/console/v1/projects/"+s.project+"/apiKeys", nil, &keys); err != nil || len(keys.Keys) == 0 {
		return "", fmt.Errorf("api key: %v", err)
	}
	return keys.Keys[0].KeyString, nil
}

func (s *soak) result(kind string, err error) {
	s.ops[kind].Add(1)
	if err != nil {
		s.errs[kind].Add(1)
		s.firstErr.LoadOrStore(kind, err.Error())
	}
}

func (s *soak) ack(key, v string) {
	s.ackMu.Lock()
	s.acked[key] = v
	s.ackMu.Unlock()
}

// every runs f rate times a second until ctx ends.
func every(ctx context.Context, wg *sync.WaitGroup, rate float64, f func(n int)) {
	wg.Go(func() {
		t := time.NewTicker(time.Duration(float64(time.Second) / rate))
		defer t.Stop()
		sem := make(chan struct{}, 64)
		for n := 0; ; n++ {
			select {
			case <-ctx.Done():
				for range cap(sem) {
					sem <- struct{}{}
				}
				return
			case <-t.C:
			}
			select {
			case sem <- struct{}{}:
				go func() { defer func() { <-sem }(); f(n) }()
			default: // the server is behind; the operation is skipped, not queued
			}
		}
	})
}

func (s *soak) do(ctx context.Context, method, u string, hdr map[string]string, body []byte) (int, []byte, error) {
	r, _ := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := s.hc.Do(r)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}

func status(code int, b []byte, err error) error {
	if err != nil {
		return err
	}
	if code >= 300 {
		return fmt.Errorf("%d %.120s", code, b)
	}
	return nil
}

func (s *soak) startLoad(ctx context.Context, wg *sync.WaitGroup) {
	c := s.cfg
	key, _ := s.apiKey(ctx)
	rest := rtdbkit.REST{HC: s.hc, Base: s.base, NS: s.ns}
	// Auth over a fixed pool of users, so that the tables stay bounded: each operation verifies
	// a user's ID token (accounts:lookup) and every tenth signs a user in with the password
	// (argon2id, costly by design).
	users := min(c.keys/10+1, 100)
	tokens := make([]string, users)
	auth := func(path string, body map[string]any) (map[string]any, error) {
		b, _ := json.Marshal(body)
		code, rb, err := s.do(ctx, "POST", s.base+"/identitytoolkit.googleapis.com/v1/"+path+"?key="+key, map[string]string{"Content-Type": "application/json"}, b)
		if err := status(code, rb, err); err != nil {
			return nil, err
		}
		var out map[string]any
		return out, json.Unmarshal(rb, &out)
	}
	for i := range users {
		out, err := auth("accounts:signUp", map[string]any{"email": fmt.Sprintf("u%d@soak.test", i), "password": "soak-password-1", "returnSecureToken": true})
		if err != nil {
			s.result("auth", err)
			continue
		}
		tokens[i], _ = out["idToken"].(string)
	}
	var tokMu sync.Mutex
	every(ctx, wg, max(c.rate/2, 0.5), func(n int) {
		i := rand.IntN(users)
		var err error
		if n%10 == 0 {
			var out map[string]any
			if out, err = auth("accounts:signInWithPassword", map[string]any{"email": fmt.Sprintf("u%d@soak.test", i), "password": "soak-password-1", "returnSecureToken": true}); err == nil {
				tokMu.Lock()
				tokens[i], _ = out["idToken"].(string)
				tokMu.Unlock()
			}
		} else {
			tokMu.Lock()
			t := tokens[i]
			tokMu.Unlock()
			_, err = auth("accounts:lookup", map[string]any{"idToken": t})
		}
		if ctx.Err() == nil {
			s.result("auth", err)
		}
	})
	// Realtime Database: sets over a bounded key space, with listeners on the whole tree.
	every(ctx, wg, c.rate, func(n int) {
		k := fmt.Sprintf("k%d", rand.IntN(c.keys))
		v := fmt.Sprintf("%d-%s", n, strings.Repeat("x", rand.IntN(512)))
		code, b, err := rest.Do(ctx, "PUT", "soak/"+k, nil, v)
		if ctx.Err() == nil {
			s.result("rtdb", status(code, b, err))
			if err == nil && code == 200 {
				s.ack("rtdb/"+k, v)
			}
		}
	})
	for range c.listeners {
		wg.Go(func() {
			for ctx.Err() == nil {
				conn, err := rtdbkit.Dial(ctx, s.base, s.ns, rtdbkit.DialOptions{OnPush: func(rtdbkit.Push) { s.deliveries.Add(1) }})
				if err != nil {
					s.listenerFailures.Add(1)
					time.Sleep(time.Second)
					continue
				}
				_, _ = conn.Call(ctx, "q", rtdbkit.Listen("/soak"))
				// The SDKs send a keepalive every 45 s; the server closes a connection silent
				// for 120 s.
				ping := time.NewTicker(45 * time.Second)
			wait:
				for {
					select {
					case <-ctx.Done():
						break wait
					case <-conn.Done():
						s.listenerFailures.Add(1)
						break wait
					case <-ping.C:
						_ = conn.Ping()
					}
				}
				ping.Stop()
				conn.CloseNow()
			}
		})
	}
	// Document Database: commits over a bounded key space, queries, and Listen streams.
	every(ctx, wg, c.rate, func(n int) {
		id := fmt.Sprintf("d%d", rand.IntN(c.keys))
		v := fmt.Sprintf("%d", n)
		r := connect.NewRequest(&pb.CommitRequest{Database: s.db, Writes: []*pb.Write{{Operation: &pb.Write_Update{Update: &pb.Document{
			Name: s.db + "/documents/soak/" + id, Fields: map[string]*pb.Value{
				"v": {ValueType: &pb.Value_StringValue{StringValue: v}}, "g": {ValueType: &pb.Value_IntegerValue{IntegerValue: int64(n % 10)}}}}}}}})
		r.Header().Set("Google-Cloud-Resource-Prefix", s.db)
		_, err := s.fs.Commit(ctx, r)
		if ctx.Err() == nil {
			s.result("docs", err)
			if err == nil {
				s.ack("docs/"+id, v)
			}
		}
	})
	every(ctx, wg, max(c.rate/4, 0.2), func(n int) {
		q := &pb.StructuredQuery{From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: "soak"}},
			Where: &pb.StructuredQuery_Filter{FilterType: &pb.StructuredQuery_Filter_FieldFilter{FieldFilter: &pb.StructuredQuery_FieldFilter{
				Field: &pb.StructuredQuery_FieldReference{FieldPath: "g"}, Op: pb.StructuredQuery_FieldFilter_EQUAL,
				Value: &pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: int64(n % 10)}}}}}}
		r := connect.NewRequest(&pb.RunQueryRequest{Parent: s.db + "/documents", QueryType: &pb.RunQueryRequest_StructuredQuery{StructuredQuery: q}})
		r.Header().Set("Google-Cloud-Resource-Prefix", s.db)
		st, err := s.fs.RunQuery(ctx, r)
		if err == nil {
			for st.Receive() {
			}
			err = st.Err()
			st.Close()
		}
		if ctx.Err() == nil {
			s.result("docs-query", err)
		}
	})
	for i := range c.listeners {
		wg.Go(func() {
			for ctx.Err() == nil {
				st := s.fs.Listen(ctx)
				st.RequestHeader().Set("Google-Cloud-Resource-Prefix", s.db)
				q := &pb.StructuredQuery{From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: "soak"}},
					Where: &pb.StructuredQuery_Filter{FilterType: &pb.StructuredQuery_Filter_FieldFilter{FieldFilter: &pb.StructuredQuery_FieldFilter{
						Field: &pb.StructuredQuery_FieldReference{FieldPath: "g"}, Op: pb.StructuredQuery_FieldFilter_EQUAL,
						Value: &pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: int64(i % 10)}}}}}}
				_ = st.Send(&pb.ListenRequest{Database: s.db, TargetChange: &pb.ListenRequest_AddTarget{AddTarget: &pb.Target{TargetId: 1,
					TargetType: &pb.Target_Query{Query: &pb.Target_QueryTarget{Parent: s.db + "/documents", QueryType: &pb.Target_QueryTarget_StructuredQuery{StructuredQuery: q}}}}}})
				// A cancelled context alone does not always end a Receive on an h2c stream.
				done := make(chan struct{})
				go func() {
					select {
					case <-ctx.Done():
						_ = st.CloseResponse()
					case <-done:
					}
				}()
				for {
					m, err := st.Receive()
					if err != nil {
						break
					}
					if m.GetDocumentChange() != nil {
						s.deliveries.Add(1)
					}
				}
				close(done)
				_ = st.CloseRequest()
				if ctx.Err() == nil {
					// Streams end after server_stream_max or idle; a new one is opened, as an SDK does.
					time.Sleep(time.Second)
				}
			}
		})
	}
	// Storage: uploads of 1 KB to 1 MB over a bounded set of names (each replaces the last, so
	// the GC works), and downloads.
	every(ctx, wg, c.rate/2, func(n int) {
		name := fmt.Sprintf("soak/o%d", rand.IntN(c.keys/4+1))
		size := 1024 + rand.IntN(1<<20)
		b := make([]byte, size)
		for i := range b {
			b[i] = byte(n + i)
		}
		code, rb, err := s.do(ctx, "POST", s.base+"/v0/b/"+s.bucket+"/o?name="+url.QueryEscape(name), map[string]string{"Content-Type": "application/octet-stream"}, b)
		if ctx.Err() == nil {
			s.result("storage-up", status(code, rb, err))
			if err == nil && code == 200 {
				s.ack("storage/"+name, strconv.Itoa(size))
			}
		}
	})
	every(ctx, wg, c.rate/2, func(n int) {
		name := fmt.Sprintf("soak/o%d", rand.IntN(c.keys/4+1))
		code, b, err := s.do(ctx, "GET", s.base+"/v0/b/"+s.bucket+"/o/"+url.QueryEscape(name)+"?alt=media", nil, nil)
		if code == 404 {
			err, code = nil, 200 // not uploaded yet
		}
		if ctx.Err() == nil {
			s.result("storage-down", status(code, b, err))
		}
	})
}

func (s *soak) metrics(ctx context.Context) map[string]float64 {
	out := map[string]float64{}
	r, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/metrics", s.cfg.mport), nil)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && !strings.HasPrefix(l, "#") {
			v, _ := strconv.ParseFloat(f[1], 64)
			out[f[0]] = v
		}
	}
	return out
}

func (s *soak) sample(ctx context.Context, at time.Duration) []float64 {
	m := s.metrics(ctx)
	var conns, dead int64
	var dbBytes int64
	if c, err := s.srv.DB(ctx); err == nil {
		_ = c.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()`).Scan(&conns)
		_ = c.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&dbBytes)
		_ = c.QueryRow(ctx, `SELECT coalesce(sum(n_dead_tup), 0)::bigint FROM pg_stat_user_tables`).Scan(&dead)
		c.Close(ctx)
	}
	var dirBytes int64
	_ = filepath.WalkDir(s.dataDir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				dirBytes += fi.Size()
			}
		}
		return nil
	})
	var ops, errs int64
	for k := range s.ops {
		ops += s.ops[k].Load()
		errs += s.errs[k].Load()
	}
	lb, _ := os.ReadFile("/proc/loadavg")
	load, _ := strconv.ParseFloat(strings.Fields(string(lb) + " 0")[0], 64)
	row := []float64{math.Round(at.Minutes()*100) / 100, m["process_resident_memory_bytes"] / 1e6, m["go_memstats_heap_inuse_bytes"] / 1e6,
		m["go_goroutines"], m["process_open_fds"], float64(conns), float64(dbBytes) / 1e6, float64(dead), float64(dirBytes) / 1e6,
		float64(ops), float64(errs), load}
	fmt.Printf("%7.1f min: rss %.0f MB heap %.0f MB goroutines %.0f fds %.0f pg %d db %.0f MB dir %.0f MB ops %d errors %d load %.2f\n",
		row[0], row[1], row[2], row[3], row[4], conns, row[6], row[8], ops, errs, load)
	return row
}

// verify reads back every key's last acknowledged value.
func (s *soak) verify(ctx context.Context) int {
	rest := rtdbkit.REST{HC: s.hc, Base: s.base, NS: s.ns}
	missing := 0
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	for key, v := range s.acked {
		svc, k, _ := strings.Cut(key, "/")
		var ok bool
		switch svc {
		case "rtdb":
			code, b, err := rest.Do(ctx, "GET", "soak/"+k, nil, nil)
			var got string
			_ = json.Unmarshal(b, &got)
			// A later write whose reply was lost at the end may have replaced it: compare the
			// counter part, which only grows.
			ok = err == nil && code == 200 && seqOf(got) >= seqOf(v)
		case "docs":
			r := connect.NewRequest(&pb.GetDocumentRequest{Name: s.db + "/documents/soak/" + k})
			r.Header().Set("Google-Cloud-Resource-Prefix", s.db)
			d, err := s.fs.GetDocument(ctx, r)
			ok = err == nil && seqOf(d.Msg.GetFields()["v"].GetStringValue()) >= seqOf(v)
		case "storage":
			code, _, err := s.do(ctx, "GET", s.base+"/v0/b/"+s.bucket+"/o/"+url.QueryEscape(k), nil, nil)
			ok = err == nil && code == 200
		}
		if !ok {
			missing++
			if missing <= 5 {
				fmt.Printf("MISSING: %s (last acknowledged %.40s)\n", key, v)
			}
		}
	}
	fmt.Printf("verified %d keys' last acknowledged writes: %d missing\n", len(s.acked), missing)
	return missing
}

func seqOf(v string) int {
	n, _ := strconv.Atoi(strings.SplitN(v, "-", 2)[0])
	return n
}

// fit returns the least-squares slope of y over x and the correlation.
func fit(x, y []float64) (slope, r float64) {
	n := float64(len(x))
	var sx, sy, sxx, syy, sxy float64
	for i := range x {
		sx += x[i]
		sy += y[i]
		sxx += x[i] * x[i]
		syy += y[i] * y[i]
		sxy += x[i] * y[i]
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, 0
	}
	slope = (n*sxy - sx*sy) / den
	dy := n*syy - sy*sy
	if dy <= 0 {
		return slope, 0
	}
	return slope, (n*sxy - sx*sy) / math.Sqrt(den*dy)
}

func (s *soak) summary(cols []string, samples [][]float64, missing int) bool {
	pass := true
	fail := func(f string, a ...any) {
		pass = false
		fmt.Printf("FAIL: "+f+"\n", a...)
	}
	fmt.Println("\noperations by service:")
	var ops, errs int64
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range s.ops {
			if !yield(k) {
				return
			}
		}
	}) {
		o, e := s.ops[k].Load(), s.errs[k].Load()
		ops += o
		errs += e
		first, _ := s.firstErr.Load(k)
		fmt.Printf("  %-13s %10d ops %6d errors  %v\n", k, o, e, first)
	}
	fmt.Printf("  listener deliveries %d, Realtime Database listeners the server closed %d\n", s.deliveries.Load(), s.listenerFailures.Load())
	if ops == 0 {
		fail("no operation ran")
	} else if rate := float64(errs) / float64(ops); rate > s.cfg.maxErrors {
		fail("%.4f%% of operations failed (limit %.4f%%)", rate*100, s.cfg.maxErrors*100)
	}
	if missing > 0 {
		fail("%d acknowledged writes are missing", missing)
	}
	var x []float64
	var rows [][]float64
	for _, r := range samples {
		if time.Duration(r[0]*float64(time.Minute)) >= s.cfg.warmup {
			x = append(x, r[0]/60)
			rows = append(rows, r)
		}
	}
	fmt.Printf("\nslopes after the %v warm-up, over %d samples (per hour; leak: r > 0.8 and +%.0f%% of the median in 72 h):\n", s.cfg.warmup, len(rows), s.cfg.leakGrowth*100)
	if len(rows) < 5 {
		fmt.Println("  too few samples after the warm-up to judge")
		return pass
	}
	for i, name := range cols {
		if i == 0 || name == "ops" || name == "errors" || name == "load1" || name == "dead_tuples" {
			continue
		}
		y := make([]float64, len(rows))
		for j, r := range rows {
			y[j] = r[i]
		}
		slope, r := fit(x, y)
		med := slices.Clone(y)
		slices.Sort(med)
		m := med[len(med)/2]
		growth := slope * 72
		leak := r > 0.8 && m > 0 && growth > s.cfg.leakGrowth*m
		verdict := "flat"
		if leak {
			verdict = "LEAK"
			fail("%s rises %.3g per hour (r %.2f), %.0f%% of its median %.4g in 72 h", name, slope, r, growth/m*100, m)
		} else if r > 0.8 && slope > 0 {
			verdict = "rising slowly"
		}
		fmt.Printf("  %-15s median %10.4g  first %10.4g  last %10.4g  slope %+10.3g/h  r %+.2f  %s\n", name, m, y[0], y[len(y)-1], slope, r, verdict)
	}
	if pass {
		fmt.Println("\nPASS")
	} else {
		fmt.Println("\nFAIL")
	}
	return pass
}

var _ = errors.New
