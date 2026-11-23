// Command dsbench measures the Document Database and Storage of a running Aruma server through
// their public APIs: commits, batched writes, reads, queries with and without a composite
// index, Listen fan-out, and Storage uploads and downloads of a few sizes at a few
// concurrencies. Each scenario is a closed loop: a fixed number of workers each send the next
// request when the last one returned, for a warm-up and then a measured window.
//
// It needs a server on a loopback address with documents.dev_open_rules and
// documents.dev_owner_token on (so it needs no credentials and may create an index), a
// project, and Storage rules that let anyone read and write that project's bucket.
// tools/rtdbkit's server, as tools/bench starts it, is one; so is
//
//	go run ./tools/dsbench -start -scenarios all
//
// which starts one on a throwaway database and data directory and removes both at the end.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
	"github.com/arumadev/aruma/tools/internal/rtdbkit"
)

type bench struct {
	base, project, bucket, db string
	warm, window              time.Duration
	fs                        firestorepbconnect.FirestoreClient
	hc                        *http.Client
	out                       []result
	fsFan                     firestorepbconnect.FirestoreClient
}

type result struct {
	Scenario string  `json:"scenario"`
	Workers  int     `json:"workers"`
	Ops      int     `json:"ops"`
	Errors   int     `json:"errors"`
	PerSec   float64 `json:"perSecond"`
	MBps     float64 `json:"mbPerSecond,omitempty"`
	P50      string  `json:"p50"`
	P95      string  `json:"p95"`
	P99      string  `json:"p99"`
	Max      string  `json:"max"`
	Load     string  `json:"load1"`
	Note     string  `json:"note,omitempty"`
}

func main() {
	var b bench
	start := flag.Bool("start", false, "start a server on a throwaway database (needs ARUMA_TEST_DATABASE_URL), with -bin")
	bin := flag.String("bin", "", "the aruma binary for -start (default: build ./cmd/aruma)")
	port := flag.Int("port", 8796, "loopback port for -start")
	flag.StringVar(&b.base, "url", "", "the server, when not -start")
	flag.StringVar(&b.project, "project", "", "the project, when not -start")
	flag.StringVar(&b.bucket, "bucket", "", "its bucket, when not -start")
	flag.DurationVar(&b.warm, "warm", 2*time.Second, "warm-up of each scenario")
	flag.DurationVar(&b.window, "d", 10*time.Second, "measured window of each scenario")
	scen := flag.String("scenarios", "all", "comma-separated: docs-commit, docs-batch, docs-get, docs-query, docs-fanout, storage, or all")
	sizes := flag.String("sizes", "1,20,200", "Storage object sizes in MB")
	par := flag.String("parallel", "1,8,32", "Storage parallel clients")
	maxBytes := flag.Int64("max-bytes", 2<<30, "skip Storage cases whose objects in flight would exceed this")
	jsonOut := flag.String("json", "", "write the results as JSON to this file")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *start {
		cleanup, err := b.startServer(ctx, *bin, *port)
		if err != nil {
			fmt.Fprintln(os.Stderr, "dsbench:", err)
			os.Exit(1)
		}
		defer cleanup()
	}
	if b.base == "" || b.project == "" {
		fmt.Fprintln(os.Stderr, "dsbench: -url and -project, or -start")
		os.Exit(2)
	}
	b.db = "projects/" + b.project + "/databases/(default)"
	var cp http.Protocols
	cp.SetUnencryptedHTTP2(true)
	b.fs = firestorepbconnect.NewFirestoreClient(&http.Client{Transport: &http.Transport{Protocols: &cp}}, b.base, connect.WithGRPC())
	b.hc = &http.Client{Timeout: 10 * time.Minute, Transport: &http.Transport{MaxIdleConnsPerHost: 256}}
	want := func(s string) bool { return *scen == "all" || slices.Contains(strings.Split(*scen, ","), s) }
	if want("docs-commit") {
		for _, c := range []int{1, 8, 32} {
			b.docsCommit(ctx, c, 1)
		}
	}
	if want("docs-batch") {
		for _, c := range []int{1, 4} {
			b.docsCommit(ctx, c, 500)
		}
	}
	if want("docs-get") || want("docs-query") {
		b.seedQuery(ctx, 20000)
	}
	if want("docs-get") {
		b.docsGet(ctx, 32)
	}
	if want("docs-query") {
		b.docsQueries(ctx, 8)
	}
	if want("docs-fanout") {
		for _, n := range []int{100, 500} {
			b.fanout(ctx, n)
		}
	}
	if want("storage") {
		for _, mb := range ints(*sizes) {
			for _, p := range ints(*par) {
				if int64(mb)<<20*int64(p)*2 > *maxBytes {
					b.out = append(b.out, result{Scenario: fmt.Sprintf("storage %d MB", mb), Workers: p, Note: "skipped: over -max-bytes"})
					continue
				}
				b.storage(ctx, mb, p)
			}
		}
	}
	b.print()
	if *jsonOut != "" {
		j, _ := json.MarshalIndent(b.out, "", "  ")
		_ = os.WriteFile(*jsonOut, append(j, '\n'), 0o644)
	}
}

func ints(s string) []int {
	var out []int
	for _, f := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func load1() string {
	b, _ := os.ReadFile("/proc/loadavg")
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return "?"
	}
	return f[0]
}

// startServer runs aruma serve on a throwaway database and data directory.
func (b *bench) startServer(ctx context.Context, bin string, port int) (func(), error) {
	dir, err := os.MkdirTemp("", "dsbench-")
	if err != nil {
		return nil, err
	}
	if bin == "" {
		if bin, err = rtdbkit.Build(ctx, dir); err != nil {
			return nil, err
		}
	}
	var logw io.Writer
	if p := os.Getenv("DSBENCH_SERVER_LOG"); p != "" {
		if f, err := os.Create(p); err == nil {
			logw = f
		}
	}
	srv, err := rtdbkit.NewServer(ctx, rtdbkit.Options{Bin: bin, Port: port, Name: "dsbench", Log: logw, Env: map[string]string{
		"ARUMA_LOG_LEVEL": "warn", "ARUMA_RTDB_ENABLED": "false",
		"ARUMA_DOCUMENTS_ENABLED": "true", "ARUMA_DOCUMENTS_DEV_OPEN_RULES": "true", "ARUMA_DOCUMENTS_DEV_OWNER_TOKEN": "true",
		"ARUMA_DOCUMENTS_INDEX_ENFORCEMENT": "off", "ARUMA_DOCUMENTS_STREAMS_PER_IP": "100000",
		"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": dir + "/data", "ARUMA_STORAGE_GC_GRACE": "1s",
		"ARUMA_STORAGE_OBJECT_BYTES": strconv.Itoa(1 << 30), "ARUMA_STORAGE_MIN_FREE_BYTES": strconv.Itoa(256 << 20),
		"ARUMA_STORAGE_UPLOAD_REQUESTS_PER_IP": "100000", "ARUMA_STORAGE_UPLOAD_STARTS_PER_MINUTE_PER_IP": "1000000",
		"ARUMA_STORAGE_UPLOAD_STARTS_PER_SECOND": "100000",
	}})
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	cleanup := func() { srv.Drop(); os.RemoveAll(dir) }
	if err := srv.Start(ctx); err != nil {
		cleanup()
		return nil, err
	}
	b.project = "dsbench-" + rtdbkit.RandomHex(3)
	var p struct {
		StorageBucket string `json:"storageBucket"`
	}
	if err := srv.ConsoleJSON(ctx, "POST", "/console/v1/projects", map[string]string{"projectId": b.project, "displayName": b.project}, &p); err != nil {
		cleanup()
		return nil, err
	}
	open := "rules_version = '2';\nservice firebase.storage { match /b/{bucket}/o { match /{allPaths=**} { allow read, write: if true; } } }"
	if err := srv.Console(ctx, "PUT", "/console/v1/projects/"+b.project+"/storage/rules", map[string]string{"source": open}); err != nil {
		cleanup()
		return nil, err
	}
	b.base, b.bucket = srv.URL, p.StorageBucket
	return cleanup, nil
}

func req[T any](b *bench, m *T) *connect.Request[T] {
	r := connect.NewRequest(m)
	r.Header().Set("Google-Cloud-Resource-Prefix", b.db)
	r.Header().Set("Authorization", "Bearer owner")
	return r
}

// loop runs op on workers workers for the warm-up and the window, and records the window.
func (b *bench) loop(ctx context.Context, name string, workers int, op func(ctx context.Context, w, n int) (int64, error)) result {
	var (
		mu    sync.Mutex
		lat   []time.Duration
		errs  int
		bytes int64
		first error
	)
	measuring := atomic.Bool{}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for n := 0; ctx.Err() == nil; n++ {
				t := time.Now()
				nb, err := op(ctx, w, n)
				d := time.Since(t)
				if !measuring.Load() || ctx.Err() != nil {
					continue
				}
				mu.Lock()
				if err != nil {
					errs++
					if first == nil {
						first = err
					}
				} else {
					lat = append(lat, d)
					bytes += nb
				}
				mu.Unlock()
			}
		})
	}
	time.Sleep(b.warm)
	ld := load1()
	measuring.Store(true)
	t0 := time.Now()
	time.Sleep(b.window)
	mu.Lock()
	el := time.Since(t0)
	r := result{Scenario: name, Workers: workers, Ops: len(lat), Errors: errs, Load: ld}
	r.PerSec = float64(len(lat)) / el.Seconds()
	if bytes > 0 {
		r.MBps = float64(bytes) / (1 << 20) / el.Seconds()
	}
	pct(&r, lat)
	if first != nil {
		r.Note = "first error: " + first.Error()
	}
	mu.Unlock()
	cancel()
	wg.Wait()
	fmt.Fprintf(os.Stderr, "%s c=%d: %.0f/s p50 %s p99 %s errors %d load %s\n", name, workers, r.PerSec, r.P50, r.P99, errs, ld)
	b.out = append(b.out, r)
	return r
}

func pct(r *result, lat []time.Duration) {
	if len(lat) == 0 {
		return
	}
	slices.Sort(lat)
	f := func(q float64) string {
		return lat[min(len(lat)-1, int(q*float64(len(lat))))].Round(10 * time.Microsecond).String()
	}
	r.P50, r.P95, r.P99, r.Max = f(0.50), f(0.95), f(0.99), lat[len(lat)-1].Round(10*time.Microsecond).String()
}

func intV(n int64) *pb.Value  { return &pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: n}} }
func strV(s string) *pb.Value { return &pb.Value{ValueType: &pb.Value_StringValue{StringValue: s}} }

func (b *bench) doc(coll, id string, f map[string]*pb.Value) *pb.Write {
	return &pb.Write{Operation: &pb.Write_Update{Update: &pb.Document{Name: b.db + "/documents/" + coll + "/" + id, Fields: f}}}
}

func (b *bench) docsCommit(ctx context.Context, c, writes int) {
	name := "docs commit, 1 write"
	if writes > 1 {
		name = fmt.Sprintf("docs commit, %d writes", writes)
	}
	b.loop(ctx, name, c, func(ctx context.Context, w, n int) (int64, error) {
		ws := make([]*pb.Write, writes)
		for i := range ws {
			ws[i] = b.doc("w", fmt.Sprintf("w%d-%d-%d", w, n%1000, i), map[string]*pb.Value{
				"a": intV(int64(n)), "b": strV("some text of a typical length"), "c": intV(int64(i))})
		}
		_, err := b.fs.Commit(ctx, req(b, &pb.CommitRequest{Database: b.db, Writes: ws}))
		return 0, err
	})
}

// seedQuery writes n documents to q: a in 0..99, b random.
func (b *bench) seedQuery(ctx context.Context, n int) {
	for i := 0; i < n; i += 500 {
		ws := make([]*pb.Write, 0, 500)
		for j := i; j < min(n, i+500); j++ {
			ws = append(ws, b.doc("q", fmt.Sprintf("d%06d", j), map[string]*pb.Value{
				"a": intV(int64(j % 100)), "b": intV(rand.Int64N(1 << 30)), "t": strV("payload " + strconv.Itoa(j))}))
		}
		if _, err := b.fs.Commit(ctx, req(b, &pb.CommitRequest{Database: b.db, Writes: ws})); err != nil {
			fmt.Fprintln(os.Stderr, "seed:", err)
			return
		}
	}
}

func (b *bench) docsGet(ctx context.Context, c int) {
	b.loop(ctx, "docs get (20,000 documents)", c, func(ctx context.Context, w, n int) (int64, error) {
		_, err := b.fs.GetDocument(ctx, req(b, &pb.GetDocumentRequest{Name: fmt.Sprintf("%s/documents/q/d%06d", b.db, rand.IntN(20000))}))
		return 0, err
	})
}

func (b *bench) query(ctx context.Context, q *pb.StructuredQuery) error {
	s, err := b.fs.RunQuery(ctx, req(b, &pb.RunQueryRequest{Parent: b.db + "/documents", QueryType: &pb.RunQueryRequest_StructuredQuery{StructuredQuery: q}}))
	if err != nil {
		return err
	}
	defer s.Close()
	n := 0
	for s.Receive() {
		if s.Msg().GetDocument() != nil {
			n++
		}
	}
	if s.Err() == nil && n == 0 {
		return errors.New("no documents")
	}
	return s.Err()
}

func (b *bench) docsQueries(ctx context.Context, c int) {
	eq := func(a int64) *pb.StructuredQuery_Filter {
		return &pb.StructuredQuery_Filter{FilterType: &pb.StructuredQuery_Filter_FieldFilter{FieldFilter: &pb.StructuredQuery_FieldFilter{
			Field: &pb.StructuredQuery_FieldReference{FieldPath: "a"}, Op: pb.StructuredQuery_FieldFilter_EQUAL, Value: intV(a)}}}
	}
	from := []*pb.StructuredQuery_CollectionSelector{{CollectionId: "q"}}
	limit := func(n int32) *wrapperspb.Int32Value { return wrapperspb.Int32(n) }
	b.loop(ctx, "docs query a == x, limit 50 (single-field index)", c, func(ctx context.Context, w, n int) (int64, error) {
		return 0, b.query(ctx, &pb.StructuredQuery{From: from, Where: eq(rand.Int64N(100)), Limit: limit(50)})
	})
	ordered := func() *pb.StructuredQuery {
		return &pb.StructuredQuery{From: from, Where: eq(rand.Int64N(100)), Limit: limit(50),
			OrderBy: []*pb.StructuredQuery_Order{{Field: &pb.StructuredQuery_FieldReference{FieldPath: "b"}, Direction: pb.StructuredQuery_DESCENDING}}}
	}
	b.loop(ctx, "docs query a == x order by b desc, limit 50, no composite index", c, func(ctx context.Context, w, n int) (int64, error) {
		return 0, b.query(ctx, ordered())
	})
	if err := b.createIndex(ctx); err != nil {
		b.out = append(b.out, result{Scenario: "docs query with composite index", Note: "index: " + err.Error()})
		return
	}
	b.loop(ctx, "docs query a == x order by b desc, limit 50, composite index (a, b desc)", c, func(ctx context.Context, w, n int) (int64, error) {
		return 0, b.query(ctx, ordered())
	})
}

// createIndex creates the composite index (a asc, b desc) on q over REST and waits until it
// is READY.
func (b *bench) createIndex(ctx context.Context) error {
	u := b.base + "/v1/" + b.db + "/collectionGroups/q/indexes"
	body := `{"queryScope":"COLLECTION","fields":[{"fieldPath":"a","order":"ASCENDING"},{"fieldPath":"b","order":"DESCENDING"}]}`
	r, _ := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer owner")
	r.Header.Set("Content-Type", "application/json")
	resp, err := b.hc.Do(r)
	if err != nil {
		return err
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%d %s", resp.StatusCode, rb)
	}
	for range 600 {
		r, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		r.Header.Set("Authorization", "Bearer owner")
		resp, err := b.hc.Do(r)
		if err != nil {
			return err
		}
		var l struct {
			Indexes []struct {
				State string `json:"state"`
			} `json:"indexes"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&l)
		resp.Body.Close()
		for _, ix := range l.Indexes {
			if ix.State == "READY" {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("index not READY after 5 minutes")
}

// fanout holds n Listen streams on one document and updates it 10 times a second; latency is
// from the commit's acknowledgement's start to each delivery.
func (b *bench) fanout(ctx context.Context, n int) {
	id := fmt.Sprintf("d%d", n)
	name := b.db + "/documents/fan/" + id
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var lat []time.Duration
	var got atomic.Int64
	var cp http.Protocols
	cp.SetUnencryptedHTTP2(true)
	ready := make(chan struct{}, n)
	for i := range n {
		// 50 streams per connection, as several apps behind one address would hold.
		if i%50 == 0 {
			b.fsFan = firestorepbconnect.NewFirestoreClient(&http.Client{Transport: &http.Transport{Protocols: &cp}}, b.base, connect.WithGRPC())
		}
		cl := b.fsFan
		go func() {
			s := cl.Listen(ctx)
			s.RequestHeader().Set("Google-Cloud-Resource-Prefix", b.db)
			_ = s.Send(&pb.ListenRequest{Database: b.db, TargetChange: &pb.ListenRequest_AddTarget{AddTarget: &pb.Target{TargetId: 1,
				TargetType: &pb.Target_Documents{Documents: &pb.Target_DocumentsTarget{Documents: []string{name}}}}}})
			sent := false
			for {
				m, err := s.Receive()
				if err != nil {
					return
				}
				if tc := m.GetTargetChange(); tc != nil && tc.GetTargetChangeType() == pb.TargetChange_CURRENT && !sent {
					sent = true
					ready <- struct{}{}
				}
				if d := m.GetDocumentChange().GetDocument(); d != nil {
					t := d.GetFields()["t"].GetIntegerValue()
					if t > 0 {
						got.Add(1)
						mu.Lock()
						lat = append(lat, time.Since(time.Unix(0, t)))
						mu.Unlock()
					}
				}
			}
		}()
	}
	timeout := time.After(60 * time.Second)
	for range n {
		select {
		case <-ready:
		case <-timeout:
			b.out = append(b.out, result{Scenario: fmt.Sprintf("docs listen fan-out to %d", n), Note: "listeners not ready within 60 s"})
			return
		}
	}
	ld := load1()
	writes := 0
	end := time.Now().Add(b.window)
	for time.Now().Before(end) {
		_, err := b.fs.Commit(ctx, req(b, &pb.CommitRequest{Database: b.db, Writes: []*pb.Write{b.doc("fan", id, map[string]*pb.Value{"t": intV(time.Now().UnixNano())})}}))
		if err == nil {
			writes++
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(2 * time.Second)
	mu.Lock()
	r := result{Scenario: fmt.Sprintf("docs listen fan-out, 10 writes/s to %d listeners", n), Workers: n, Ops: int(got.Load()), Load: ld,
		Note: fmt.Sprintf("%d of %d deliveries", got.Load(), writes*n)}
	r.PerSec = float64(got.Load()) / b.window.Seconds()
	pct(&r, lat)
	mu.Unlock()
	fmt.Fprintf(os.Stderr, "%s: p50 %s p99 %s %s load %s\n", r.Scenario, r.P50, r.P99, r.Note, ld)
	b.out = append(b.out, r)
}

// body is a reader of n bytes that are not all the same.
type body struct{ n, off int64 }

func (r *body) Read(p []byte) (int, error) {
	if r.off >= r.n {
		return 0, io.EOF
	}
	k := min(int64(len(p)), r.n-r.off)
	for i := range k {
		p[i] = byte((r.off + i) * 2654435761 >> 13)
	}
	r.off += k
	return int(k), nil
}

func (b *bench) storage(ctx context.Context, mb, p int) {
	size := int64(mb) << 20
	obj := func(w int) string {
		return b.base + "/v0/b/" + b.bucket + "/o?name=" + url.QueryEscape(fmt.Sprintf("bench/%dmb/w%d", mb, w))
	}
	b.loop(ctx, fmt.Sprintf("storage upload %d MB", mb), p, func(ctx context.Context, w, n int) (int64, error) {
		r, _ := http.NewRequestWithContext(ctx, "POST", obj(w), &body{n: size})
		r.ContentLength = size
		r.Header.Set("Content-Type", "application/octet-stream")
		resp, err := b.hc.Do(r)
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return 0, fmt.Errorf("upload: %s", resp.Status)
		}
		return size, nil
	})
	b.loop(ctx, fmt.Sprintf("storage download %d MB", mb), p, func(ctx context.Context, w, n int) (int64, error) {
		u := b.base + "/v0/b/" + b.bucket + "/o/" + url.QueryEscape(fmt.Sprintf("bench/%dmb/w%d", mb, w)) + "?alt=media"
		r, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		resp, err := b.hc.Do(r)
		if err != nil {
			return 0, err
		}
		k, err := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || k != size {
			return 0, fmt.Errorf("download: %s, %d bytes, %v", resp.Status, k, err)
		}
		return k, nil
	})
	for w := range p {
		r, _ := http.NewRequestWithContext(ctx, "DELETE", b.base+"/v0/b/"+b.bucket+"/o/"+url.QueryEscape(fmt.Sprintf("bench/%dmb/w%d", mb, w)), nil)
		if resp, err := b.hc.Do(r); err == nil {
			resp.Body.Close()
		}
	}
	time.Sleep(3 * time.Second) // let the GC take the replaced files
}

func (b *bench) print() {
	fmt.Println("| Scenario | Workers | Per second | MB/s | p50 | p95 | p99 | max | Errors | load1 | Note |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|")
	for _, r := range b.out {
		mbs := ""
		if r.MBps > 0 {
			mbs = fmt.Sprintf("%.0f", r.MBps)
		}
		fmt.Printf("| %s | %d | %.0f | %s | %s | %s | %s | %s | %d | %s | %s |\n", r.Scenario, r.Workers, r.PerSec, mbs, r.P50, r.P95, r.P99, r.Max, r.Errors, r.Load, r.Note)
	}
	_ = bytes.MinRead
}
