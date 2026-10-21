package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/tools/internal/rtdbkit"
)

// The Realtime Database scenarios (-rtdb) measure the real binary in a process of its own,
// started on a throwaway database for each scenario, so the server's memory and file
// descriptors are its own and the load generator's CPU is not in its profile. The clients
// speak the wire protocol over WebSockets (and long polling, and REST) as the SDKs do, with
// the open rules of development, which skip the rules engine (Phase 1-2's scenarios measured
// the HTTP stack; rules cost is not measured here).

// RTDBScenarios are the -rtdb scenarios, in the order they run.
var RTDBScenarios = []struct {
	Name, What string
	Run        func(b *rtdbBench, ctx context.Context) ([]RTDBResult, error)
}{
	{"conns", "concurrent WebSocket connections one server holds, idle then with a listen each; memory and descriptors per connection", (*rtdbBench).conns},
	{"fanout", "one writer, K listeners on one path: latency from the write to each delivery", (*rtdbBench).fanout},
	{"writes", "sets per second into one database from concurrent writers, and into 10 databases at once", (*rtdbBench).writes},
	{"query", "limitToLast(50) listens on nodes of 10k and 100k children, by key and by child with and without .indexOn; writes under such a listen", (*rtdbBench).query},
	{"lp", "writes over long polling against the same over a WebSocket", (*rtdbBench).longPoll},
}

// rtdbOptions are the -rtdb-* flags.
type rtdbOptions struct {
	bin          string
	port         int
	metricsPort  int
	conns        int
	connStep     int
	fanout       []int
	fanoutRate   float64
	fanoutWrites int
	writers      []int
	querySizes   []int
}

// RTDBReport is the JSON output of -rtdb.
type RTDBReport struct {
	Meta     Meta              `json:"meta"`
	Postgres map[string]string `json:"postgres"`
	Limits   map[string]string `json:"limits"`
	Results  []RTDBResult      `json:"results"`
}

// RTDBResult is one measurement. Latencies are milliseconds.
type RTDBResult struct {
	Scenario string             `json:"scenario"`
	Case     string             `json:"case"`
	Params   map[string]any     `json:"params,omitempty"`
	OK       int64              `json:"ok,omitempty"`
	Failed   int64              `json:"failed,omitempty"`
	OKPerSec float64            `json:"okPerSecond,omitempty"`
	Latency  *Latency           `json:"latencyMs,omitempty"`
	Values   map[string]float64 `json:"values,omitempty"`
	Notes    []string           `json:"notes,omitempty"`
}

type rtdbBench struct {
	o        rtdbOptions
	duration time.Duration
	warmup   time.Duration
	hc       *http.Client
	logs     []string
}

func runRTDB(ctx context.Context, o options) error {
	selected := RTDBScenarios
	if o.rtdb != "all" {
		selected = selected[:0:0]
		for _, n := range strings.Split(o.rtdb, ",") {
			found := false
			for _, s := range RTDBScenarios {
				if s.Name == strings.TrimSpace(n) {
					selected, found = append(selected, s), true
				}
			}
			if !found {
				return fmt.Errorf("unknown -rtdb scenario %q", n)
			}
		}
	}
	b := &rtdbBench{o: o.rtdbOpts, duration: o.duration, warmup: o.warmup,
		hc: &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 512, MaxIdleConns: 512}}}
	if b.o.bin == "" {
		dir, err := os.MkdirTemp("", "bench-rtdb-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, "building aruma…")
		if b.o.bin, err = rtdbkit.Build(ctx, dir); err != nil {
			return err
		}
	}
	report := RTDBReport{Meta: collectMeta(o, "subprocess"), Postgres: postgresInfo(ctx), Limits: fdLimits()}
	for _, s := range selected {
		if ctx.Err() != nil {
			break
		}
		fmt.Fprintf(os.Stderr, "rtdb %s…\n", s.Name)
		rs, err := s.Run(b, ctx)
		report.Results = append(report.Results, rs...)
		if err != nil {
			report.Results = append(report.Results, RTDBResult{Scenario: s.Name, Case: "error", Notes: []string{err.Error()}})
			fmt.Fprintf(os.Stderr, "rtdb %s: %v\n", s.Name, err)
		}
	}
	report.Meta.MaxRSSMiB = maxRSSMiB()
	printRTDB(os.Stdout, report)
	if o.jsonOut != "" {
		bs, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(o.jsonOut, append(bs, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", o.jsonOut)
	}
	return ctx.Err()
}

// server starts a fresh server on its own database, with n projects and a database pool of
// poolMax connections (0: the default). Its errors, if any, are printed when it stops.
func (b *rtdbBench) server(ctx context.Context, name string, projects, poolMax int) (*rtdbkit.Server, []string, func(), error) {
	logf, err := os.CreateTemp("", "bench-rtdb-*.log")
	if err != nil {
		return nil, nil, nil, err
	}
	srv, err := rtdbkit.NewServer(ctx, rtdbkit.Options{Bin: b.o.bin, Port: b.o.port, MetricsPort: b.o.metricsPort,
		Name: "bench_" + name, Log: logf, PoolMax: poolMax, Env: map[string]string{"ARUMA_LOG_LEVEL": "error"}})
	if err != nil {
		logf.Close()
		os.Remove(logf.Name())
		return nil, nil, nil, err
	}
	stop := func() {
		srv.Drop()
		logf.Close()
		if st, err := os.Stat(logf.Name()); err == nil && st.Size() > 0 {
			bs, _ := os.ReadFile(logf.Name())
			if len(bs) > 4000 {
				bs = bs[len(bs)-4000:]
			}
			fmt.Fprintf(os.Stderr, "server log (%s, tail):\n%s\n", name, bs)
		}
		os.Remove(logf.Name())
	}
	if err := srv.Start(ctx); err != nil {
		stop()
		return nil, nil, nil, err
	}
	var nss []string
	for i := range projects {
		ns, err := srv.Project(ctx, fmt.Sprintf("bench-%s-%d", strings.ReplaceAll(name, "_", "-"), i))
		if err != nil {
			stop()
			return nil, nil, nil, err
		}
		nss = append(nss, ns)
	}
	return srv, nss, stop, nil
}

// --- connections

type memSample struct {
	conns            int
	rss, heap, stack float64
	goroutines, fds  float64
	clientHeap       float64
}

func (b *rtdbBench) sample(ctx context.Context, srv *rtdbkit.Server, n int) memSample {
	time.Sleep(time.Second) // let the last connections settle
	s := memSample{conns: n}
	if m, err := srv.Metrics(ctx); err == nil {
		s.heap, s.stack, s.goroutines = m["go_memstats_heap_inuse_bytes"], m["go_memstats_stack_inuse_bytes"], m["go_goroutines"]
		s.fds = m["process_open_fds"]
	}
	if rss, err := srv.RSSBytes(); err == nil {
		s.rss = float64(rss)
	}
	if s.fds == 0 {
		if n, err := srv.OpenFiles(); err == nil {
			s.fds = float64(n)
		}
	}
	return s
}

func (b *rtdbBench) conns(ctx context.Context) ([]RTDBResult, error) {
	srv, nss, stop, err := b.server(ctx, "conns", 1, 0)
	if err != nil {
		return nil, err
	}
	defer stop()
	ns := nss[0]
	base := b.sample(ctx, srv, 0)
	var (
		mu       sync.Mutex
		conns    []*rtdbkit.Conn
		firstErr error
		samples  = []memSample{base}
	)
	defer func() {
		for _, c := range conns {
			c.CloseNow()
		}
	}()
	// A keepalive, as the SDKs send after 45 s of silence, so the server's 120 s idle limit
	// does not end connections opened early in a long ramp.
	kctx, kcancel := context.WithCancel(ctx)
	defer kcancel()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				mu.Lock()
				cs := append([]*rtdbkit.Conn(nil), conns...)
				mu.Unlock()
				for _, c := range cs {
					_ = c.Ping()
				}
			case <-kctx.Done():
				return
			}
		}
	}()
	tr := &http.Transport{MaxIdleConnsPerHost: -1}
	hc := &http.Client{Transport: tr}
	rampStart := time.Now()
	for len(conns) < b.o.conns && firstErr == nil && ctx.Err() == nil {
		want := min(b.o.conns-len(conns), b.o.connStep)
		var wg sync.WaitGroup
		sem := make(chan struct{}, 32)
		for range want {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				c, err := rtdbkit.Dial(dctx, srv.URL, ns, rtdbkit.DialOptions{HTTPClient: hc})
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					return
				}
				conns = append(conns, c)
			}()
		}
		wg.Wait()
		samples = append(samples, b.sample(ctx, srv, len(conns)))
		fmt.Fprintf(os.Stderr, "  %d connections, server RSS %.0f MiB\n", len(conns), samples[len(samples)-1].rss/(1<<20))
	}
	rampSec := time.Since(rampStart).Seconds()
	idle := samples[len(samples)-1]
	var out []RTDBResult
	notes := []string{}
	if firstErr != nil {
		notes = append(notes, "ramp stopped at the first failed dial: "+firstErr.Error())
	}
	out = append(out, connResult("idle", base, idle, samples, rampSec, notes))

	// Each connection listens at a path of its own, as an app showing a user's own node.
	n := len(conns)
	lat := &Histogram{}
	var failed atomic.Int64
	var lmu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 64)
	for i, c := range conns {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			t0 := time.Now()
			r, err := c.Call(ctx, "q", rtdbkit.Listen(fmt.Sprintf("/conns/c%d", i)))
			if err != nil || !r.OK() {
				failed.Add(1)
				return
			}
			lmu.Lock()
			lat.Record(time.Since(t0))
			lmu.Unlock()
		}()
	}
	wg.Wait()
	listened := b.sample(ctx, srv, n)
	res := connResult("with a listen each", base, listened, nil, 0, nil)
	l := summarize(lat)
	res.Latency, res.Failed = &l, failed.Load()
	res.Values["listensPerConnection"] = 1
	out = append(out, res)
	return out, nil
}

func connResult(name string, base, s memSample, ramp []memSample, rampSec float64, notes []string) RTDBResult {
	n := float64(max(s.conns, 1))
	r := RTDBResult{Scenario: "conns", Case: name, OK: int64(s.conns), Notes: notes, Values: map[string]float64{
		"connections":             float64(s.conns),
		"serverRssMiB":            s.rss / (1 << 20),
		"serverHeapInuseMiB":      s.heap / (1 << 20),
		"serverStackInuseMiB":     s.stack / (1 << 20),
		"serverGoroutines":        s.goroutines,
		"serverOpenFds":           s.fds,
		"rssKiBPerConnection":     (s.rss - base.rss) / n / 1024,
		"heapKiBPerConnection":    (s.heap - base.heap) / n / 1024,
		"stackKiBPerConnection":   (s.stack - base.stack) / n / 1024,
		"goroutinesPerConnection": (s.goroutines - base.goroutines) / n,
	}}
	if rampSec > 0 {
		r.Values["rampSeconds"] = rampSec
		r.OKPerSec = float64(s.conns) / rampSec
	}
	for _, x := range ramp {
		r.Notes = append(r.Notes, fmt.Sprintf("at %d: RSS %.0f MiB, heap %.0f MiB, stacks %.0f MiB, %0.f goroutines, %0.f fds",
			x.conns, x.rss/(1<<20), x.heap/(1<<20), x.stack/(1<<20), x.goroutines, x.fds))
	}
	return r
}

// --- fan-out

func (b *rtdbBench) fanout(ctx context.Context) ([]RTDBResult, error) {
	var out []RTDBResult
	for _, k := range b.o.fanout {
		r, err := b.fanoutOne(ctx, k, false)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	r, err := b.fanoutOne(ctx, 1, true)
	if err != nil {
		return out, err
	}
	return append(out, r), nil
}

// fanoutOne writes at a fixed rate to one path that k connections listen at, and measures
// the time from sending each write to each listener reading its push. Writes are paced (open
// loop), well below saturation, so the latency is the path's, not a queue's.
func (b *rtdbBench) fanoutOne(ctx context.Context, k int, lp bool) (RTDBResult, error) {
	srv, nss, stop, err := b.server(ctx, fmt.Sprintf("fanout_%d", k), 1, 0)
	if err != nil {
		return RTDBResult{}, err
	}
	defer stop()
	ns := nss[0]
	const path = "/fan/room"
	writes := b.o.fanoutWrites
	warm := writes / 10
	sent := make([]atomic.Int64, writes) // unix nanos of each send
	hists := make([]*Histogram, k)
	var got, late atomic.Int64
	onPush := func(i int) func(rtdbkit.Push) {
		h := &Histogram{}
		hists[i] = h
		return func(p rtdbkit.Push) {
			var v struct {
				N *int `json:"n"`
			}
			if p.Action != "d" || json.Unmarshal(p.Data, &v) != nil || v.N == nil || *v.N < 0 || *v.N >= writes {
				return
			}
			t0 := sent[*v.N].Load()
			if t0 == 0 {
				late.Add(1)
				return
			}
			got.Add(1)
			if *v.N >= warm {
				h.Record(p.At.Sub(time.Unix(0, t0)))
			}
		}
	}
	type caller interface {
		Call(ctx context.Context, action string, body any) (rtdbkit.Reply, error)
	}
	var closers []func()
	defer func() {
		for _, c := range closers {
			c()
		}
	}()
	tr := &http.Transport{MaxIdleConnsPerHost: -1}
	hc := &http.Client{Transport: tr}
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var dialErr error
	for i := range k {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			var c caller
			if lp {
				l, err := rtdbkit.DialLP(ctx, b.hc, srv.URL, ns, onPush(i))
				if err != nil {
					mu.Lock()
					dialErr = err
					mu.Unlock()
					return
				}
				mu.Lock()
				closers = append(closers, l.Close)
				mu.Unlock()
				c = l
			} else {
				w, err := rtdbkit.Dial(ctx, srv.URL, ns, rtdbkit.DialOptions{HTTPClient: hc, OnPush: onPush(i)})
				if err != nil {
					mu.Lock()
					dialErr = err
					mu.Unlock()
					return
				}
				mu.Lock()
				closers = append(closers, w.CloseNow)
				mu.Unlock()
				c = w
			}
			if r, err := c.Call(ctx, "q", rtdbkit.Listen(path)); err != nil || !r.OK() {
				mu.Lock()
				dialErr = fmt.Errorf("listen: %v %v", r, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if dialErr != nil {
		return RTDBResult{}, dialErr
	}
	w, err := rtdbkit.Dial(ctx, srv.URL, ns, rtdbkit.DialOptions{})
	if err != nil {
		return RTDBResult{}, err
	}
	defer w.CloseNow()
	ack := &Histogram{}
	var ackMu sync.Mutex
	interval := time.Duration(float64(time.Second) / b.o.fanoutRate)
	start := time.Now()
	var awg sync.WaitGroup
	pad := strings.Repeat("x", 64)
	for n := range writes {
		if d := time.Until(start.Add(time.Duration(n) * interval)); d > 0 {
			time.Sleep(d)
		}
		t0 := time.Now()
		sent[n].Store(t0.UnixNano())
		ch, err := w.Send("p", rtdbkit.Put(path, map[string]any{"n": n, "pad": pad}, nil))
		if err != nil {
			return RTDBResult{}, err
		}
		awg.Add(1)
		go func() {
			defer awg.Done()
			if r, err := rtdbkit.Await(ctx, ch); err == nil && r.OK() && n >= warm {
				ackMu.Lock()
				ack.Record(time.Since(t0))
				ackMu.Unlock()
			}
		}()
	}
	awg.Wait()
	// Let the last deliveries arrive.
	want := int64(k * writes)
	deadline := time.Now().Add(30 * time.Second)
	for got.Load() < want && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	all := &Histogram{}
	for _, h := range hists {
		all.Merge(h)
	}
	l, al := summarize(all), summarize(ack)
	transport := "WebSocket"
	if lp {
		transport = "long poll"
	}
	return RTDBResult{
		Scenario: "fanout", Case: fmt.Sprintf("K=%d (%s)", k, transport),
		Params: map[string]any{"listeners": k, "writes": writes, "warmupWrites": warm, "writesPerSecond": b.o.fanoutRate, "transport": transport},
		OK:     got.Load(), Failed: want - got.Load(), Latency: &l,
		OKPerSec: float64(k) * b.o.fanoutRate,
		Values:   map[string]float64{"writerAckP50Ms": al.P50, "writerAckP99Ms": al.P99, "deliveriesPerSecond": float64(k) * b.o.fanoutRate},
	}, nil
}

// --- writes

func (b *rtdbBench) writes(ctx context.Context) ([]RTDBResult, error) {
	srv, nss, stop, err := b.server(ctx, "writes", 10, 0)
	if err != nil {
		return nil, err
	}
	var out []RTDBResult
	for _, c := range b.o.writers {
		r, err := b.drive(ctx, srv, nss[:1], c, "ws", fmt.Sprintf("1 database, %d writers", c))
		if err != nil {
			stop()
			return out, err
		}
		out = append(out, r)
	}
	r, err := b.drive(ctx, srv, nss[:1], 32, "rest", "1 database, 32 REST writers")
	if err != nil {
		stop()
		return out, err
	}
	out = append(out, r)
	// Ten databases at once. Each one's writer holds a pooled connection for a whole batch,
	// so the default pool (max(4, NumCPU)) is measured, then one larger than the databases.
	const per = 16
	r, err = b.drive(ctx, srv, nss, per*len(nss), "ws", fmt.Sprintf("10 databases, %d writers each, default pool", per))
	stop()
	if err != nil {
		return out, err
	}
	out = append(out, r)
	srv, nss, stop, err = b.server(ctx, "writes_pool", 10, 32)
	if err != nil {
		return out, err
	}
	defer stop()
	r, err = b.drive(ctx, srv, nss, per*len(nss), "ws", fmt.Sprintf("10 databases, %d writers each, pool_max_conns=32", per))
	if err != nil {
		return out, err
	}
	return append(out, r), nil
}

// writer is a connection a write driver sends on.
type writer interface {
	Call(ctx context.Context, action string, body any) (rtdbkit.Reply, error)
}

// drive runs closed-loop sets: each of c workers sets a small value at a path of its own and
// waits for the ack. Workers spread over the databases nss round robin.
func (b *rtdbBench) drive(ctx context.Context, srv *rtdbkit.Server, nss []string, c int, transport, name string) (RTDBResult, error) {
	ws := make([]writer, c)
	var closers []func()
	defer func() {
		for _, f := range closers {
			f()
		}
	}()
	for i := range c {
		ns := nss[i%len(nss)]
		switch transport {
		case "ws":
			conn, err := rtdbkit.Dial(ctx, srv.URL, ns, rtdbkit.DialOptions{})
			if err != nil {
				return RTDBResult{}, err
			}
			closers = append(closers, conn.CloseNow)
			ws[i] = conn
		case "lp":
			conn, err := rtdbkit.DialLP(ctx, b.hc, srv.URL, ns, nil)
			if err != nil {
				return RTDBResult{}, err
			}
			closers = append(closers, conn.Close)
			ws[i] = conn
		case "rest":
			ws[i] = restWriter{rtdbkit.REST{HC: b.hc, Base: srv.URL, NS: ns}}
		}
	}
	var seq atomic.Int64
	m := Drive(ctx, Plan{Concurrency: c, Warmup: b.warmup, Duration: b.duration}, func(ctx context.Context, w int) Outcome {
		n := seq.Add(1)
		r, err := ws[w].Call(ctx, "p", rtdbkit.Put(fmt.Sprintf("/w/w%d/k%d", w, n%64), map[string]any{"n": n, "v": "abcdefgh"}, nil))
		if err != nil {
			return Outcome{Err: err}
		}
		if !r.OK() {
			return Outcome{Status: 500}
		}
		return Outcome{Status: 200}
	})
	l := summarize(&m.Latency)
	r := RTDBResult{Scenario: "writes", Case: name, Params: map[string]any{"writers": c, "databases": len(nss), "transport": transport},
		OK: m.OK(), Failed: m.Total() - m.OK(), Latency: &l, OKPerSec: float64(m.OK()) / m.Elapsed.Seconds()}
	if m.FirstError != "" {
		r.Notes = append(r.Notes, m.FirstError)
	}
	if transport == "lp" {
		r.Scenario = "lp"
	}
	fmt.Fprintf(os.Stderr, "  %s (%s): %.0f/s, p50 %.2f ms\n", name, transport, r.OKPerSec, l.P50)
	return r, nil
}

// restWriter sends a p as a REST PUT with print=silent, as the Admin SDK for Go writes.
type restWriter struct{ r rtdbkit.REST }

func (w restWriter) Call(ctx context.Context, _ string, body any) (rtdbkit.Reply, error) {
	m := body.(map[string]any)
	status, b, err := w.r.Do(ctx, "PUT", m["p"].(string), map[string][]string{"print": {"silent"}}, m["d"])
	if err != nil {
		return rtdbkit.Reply{}, err
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return rtdbkit.Reply{Status: strconv.Itoa(status), Data: b}, nil
	}
	return rtdbkit.Reply{Status: "ok"}, nil
}

// --- queries

func (b *rtdbBench) query(ctx context.Context) ([]RTDBResult, error) {
	srv, nss, stop, err := b.server(ctx, "query", 1, 0)
	if err != nil {
		return nil, err
	}
	defer stop()
	ns := nss[0]
	project := strings.TrimSuffix(ns, "-default-rtdb")
	rest := rtdbkit.REST{HC: b.hc, Base: srv.URL, NS: ns}
	rng := rand.New(rand.NewPCG(1, 2))
	for _, size := range b.o.querySizes {
		start := time.Now()
		const chunk = 5000
		for i := 0; i < size; i += chunk {
			d := map[string]any{}
			for j := i; j < min(i+chunk, size); j++ {
				d[fmt.Sprintf("n%d/c%07d", size, j)] = map[string]any{"score": rng.Float64() * 1000, "name": fmt.Sprintf("item %d", j)}
			}
			status, body, err := rest.Do(ctx, "PATCH", "/q", map[string][]string{"print": {"silent"}}, d)
			if err != nil || status >= 300 {
				return nil, fmt.Errorf("seed /q/n%d: %d %s %v", size, status, body, err)
			}
		}
		fmt.Fprintf(os.Stderr, "  seeded %d children in %s\n", size, time.Since(start).Round(time.Millisecond))
	}
	c, err := rtdbkit.Dial(ctx, srv.URL, ns, rtdbkit.DialOptions{})
	if err != nil {
		return nil, err
	}
	defer c.CloseNow()
	var pushBytes atomic.Int64
	l, err := rtdbkit.Dial(ctx, srv.URL, ns, rtdbkit.DialOptions{OnPush: func(p rtdbkit.Push) { pushBytes.Add(int64(len(p.Data))) }})
	if err != nil {
		return nil, err
	}
	defer l.CloseNow()

	type variant struct {
		name    string
		q       map[string]any
		indexed bool
	}
	variants := []variant{
		{"orderByKey().limitToLast(50)", map[string]any{"i": ".key", "l": 50, "vf": "r"}, false},
		{"orderByChild(score).limitToLast(50), no .indexOn", map[string]any{"i": "score", "l": 50, "vf": "r"}, false},
		{"orderByChild(score).limitToLast(50), .indexOn", map[string]any{"i": "score", "l": 50, "vf": "r"}, true},
	}
	var out []RTDBResult
	tag := int64(0)
	for _, size := range b.o.querySizes {
		path := fmt.Sprintf("/q/n%d", size)
		for _, v := range variants {
			rules := `{"rules": {".read": false, ".write": false}}`
			if v.indexed {
				rules = `{"rules": {".read": false, ".write": false, "q": {"$n": {".indexOn": ["score"]}}}}`
			}
			if err := srv.SetRules(ctx, project, rules); err != nil {
				return out, err
			}
			// Listen latency: the time from q to its ok, which follows the data.
			h := &Histogram{}
			var warnings string
			pushBytes.Store(0)
			n := 0
			deadline := time.Now().Add(b.duration)
			for (n < 5 || time.Now().Before(deadline)) && n < 200 && ctx.Err() == nil {
				tag++
				t0 := time.Now()
				r, err := l.Call(ctx, "q", rtdbkit.ListenQuery(path, v.q, tag))
				if err != nil {
					return out, err
				}
				if !r.OK() {
					return out, fmt.Errorf("listen %s %s: %s %s", path, v.name, r.Status, r.Data)
				}
				h.Record(time.Since(t0))
				warnings = string(r.Data)
				if _, err := l.Call(ctx, "n", rtdbkit.ListenQuery(path, v.q, tag)); err != nil {
					return out, err
				}
				n++
			}
			lat := summarize(h)
			res := RTDBResult{Scenario: "query", Case: fmt.Sprintf("listen n=%d %s", size, v.name), OK: int64(n), Latency: &lat,
				Params: map[string]any{"children": size, "query": v.q, "indexOn": v.indexed},
				Values: map[string]float64{"pushBytesPerListen": float64(pushBytes.Load()) / float64(n)}}
			if warnings != "{}" {
				res.Notes = append(res.Notes, "reply data: "+warnings)
			}
			out = append(out, res)
			fmt.Fprintf(os.Stderr, "  %s: p50 %.1f ms, %.0f bytes pushed per listen\n", res.Case, lat.P50, res.Values["pushBytesPerListen"])

			// The cost of a write under that listen: one child's score changes; the server
			// works out the listen's new window.
			tag++
			if r, err := l.Call(ctx, "q", rtdbkit.ListenQuery(path, v.q, tag)); err != nil || !r.OK() {
				return out, fmt.Errorf("listen: %v %v", r, err)
			}
			wres := b.writeUnder(ctx, c, path, size, rng)
			wres.Case = fmt.Sprintf("write under the listen n=%d %s", size, v.name)
			wres.Params = res.Params
			out = append(out, wres)
			if _, err := l.Call(ctx, "n", rtdbkit.ListenQuery(path, v.q, tag)); err != nil {
				return out, err
			}
		}
		wres := b.writeUnder(ctx, c, path, size, rng)
		wres.Case = fmt.Sprintf("write, no listen n=%d", size)
		wres.Params = map[string]any{"children": size}
		out = append(out, wres)
	}
	return out, nil
}

// writeUnder sets random children's scores in a closed loop, one at a time, for up to the
// scenario duration.
func (b *rtdbBench) writeUnder(ctx context.Context, c *rtdbkit.Conn, path string, size int, rng *rand.Rand) RTDBResult {
	h := &Histogram{}
	n := 0
	start := time.Now()
	deadline := start.Add(b.duration)
	for (n < 5 || time.Now().Before(deadline)) && n < 2000 && ctx.Err() == nil {
		t0 := time.Now()
		r, err := c.Call(ctx, "p", rtdbkit.Put(fmt.Sprintf("%s/c%07d/score", path, rng.IntN(size)), rng.Float64()*1000, nil))
		if err != nil || !r.OK() {
			break
		}
		h.Record(time.Since(t0))
		n++
	}
	lat := summarize(h)
	fmt.Fprintf(os.Stderr, "  writes under %s: p50 %.2f ms\n", path, lat.P50)
	return RTDBResult{Scenario: "query", OK: int64(n), Latency: &lat, OKPerSec: float64(n) / time.Since(start).Seconds()}
}

// --- long polling

func (b *rtdbBench) longPoll(ctx context.Context) ([]RTDBResult, error) {
	srv, nss, stop, err := b.server(ctx, "lp", 1, 0)
	if err != nil {
		return nil, err
	}
	defer stop()
	var out []RTDBResult
	for _, c := range []int{1, 8, 32} {
		for _, t := range []string{"ws", "lp"} {
			r, err := b.drive(ctx, srv, nss, c, t, fmt.Sprintf("%d writers, %s", c, map[string]string{"ws": "WebSocket", "lp": "long poll"}[t]))
			if err != nil {
				return out, err
			}
			r.Scenario = "lp"
			out = append(out, r)
		}
	}
	return out, nil
}

// --- report

func postgresInfo(ctx context.Context) map[string]string {
	out := map[string]string{}
	c, err := pgx.Connect(ctx, rtdbkit.AdminURL())
	if err != nil {
		return out
	}
	defer c.Close(ctx)
	var v string
	if c.QueryRow(ctx, "SELECT version()").Scan(&v) == nil {
		out["version"] = v
	}
	for _, s := range []string{"shared_buffers", "synchronous_commit", "fsync", "max_connections", "wal_sync_method", "work_mem"} {
		if c.QueryRow(ctx, "SHOW "+s).Scan(&v) == nil {
			out[s] = v
		}
	}
	return out
}

func fdLimits() map[string]string {
	out := map[string]string{}
	for _, k := range []string{"kern.maxfiles", "kern.maxfilesperproc", "kern.num_files", "kern.ipc.somaxconn", "net.inet.ip.portrange.first", "net.inet.ip.portrange.last", "hw.memsize"} {
		if b, err := exec.Command("sysctl", "-n", k).Output(); err == nil {
			out[k] = strings.TrimSpace(string(b))
		}
	}
	if b, err := exec.Command("sh", "-c", "ulimit -n").Output(); err == nil {
		out["ulimit -n"] = strings.TrimSpace(string(b))
	}
	return out
}

func printRTDB(w io.Writer, r RTDBReport) {
	fmt.Fprintf(w, "\nAruma RTDB bench — %s/%s %d CPUs, %s, %s\n\n", r.Meta.OS, r.Meta.Arch, r.Meta.NumCPU, r.Meta.GoVersion, r.Postgres["version"])
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "scenario\tcase\tok\tfailed\tok/s\tp50 ms\tp90 ms\tp99 ms\tmax ms\t")
	for _, x := range r.Results {
		p50, p90, p99, mx := "-", "-", "-", "-"
		if x.Latency != nil {
			p50, p90, p99, mx = f(x.Latency.P50), f(x.Latency.P90), f(x.Latency.P99), f(x.Latency.Max)
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%.0f\t%s\t%s\t%s\t%s\t\n", x.Scenario, x.Case, x.OK, x.Failed, x.OKPerSec, p50, p90, p99, mx)
	}
	tw.Flush()
	for _, x := range r.Results {
		if len(x.Values) == 0 && len(x.Notes) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s / %s:\n", x.Scenario, x.Case)
		keys := make([]string, 0, len(x.Values))
		for k := range x.Values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  %s = %.2f\n", k, x.Values[k])
		}
		for _, n := range x.Notes {
			fmt.Fprintf(w, "  %s\n", n)
		}
	}
	fmt.Fprintln(w)
}

// parseInts reads a comma-separated list of positive integers.
func parseInts(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 1 {
			return nil, errors.New("not a list of positive integers: " + s)
		}
		out = append(out, n)
	}
	return out, nil
}
