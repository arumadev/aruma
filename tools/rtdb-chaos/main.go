// Command rtdb-chaos checks that the Realtime Database keeps every write it acknowledged
// when its server dies.
//
// It starts a real Aruma binary with the Realtime Database enabled on a throwaway Postgres
// database, drives it with concurrent clients over the wire protocol and REST, and at random
// points kills the server with SIGKILL and starts it again. The clients record every write
// the server acknowledged with ok. At the end the tool reads everything back and checks it
// against a ground truth built from those acknowledgements alone:
//
//   - every acknowledged write is there;
//   - nothing that was not acknowledged is there, unless it was in flight when the server
//     died (allowed, and counted);
//   - no increment ran twice, and no transaction was lost or doubled;
//   - the database's write count (seq) is exactly the number of writes that ran;
//   - onDisconnect writes run when a client goes away and when the server stops with
//     SIGTERM; after SIGKILL they are lost with the process, and none of them may appear.
//
// Each client owns a subtree, /c/<client>, so its expected value follows from that client's
// writes in the order it sent them. The shared paths take only changes that commute:
// increments, and keys added by transactions that contend with every other client. A write
// whose reply was lost is settled after the restart: the server must have applied a prefix
// of the writes in flight on that connection, and the subtree read back says which.
//
//	go run ./tools/rtdb-chaos                          # seed 1: 5 kill cycles, then SIGTERM
//	go run ./tools/rtdb-chaos -seed 7 -runs 4 -cycles 6
//	go run ./tools/rtdb-chaos -seed 42 -clients 16 -journal /tmp/chaos   # replay seed 42
//
// The seed fixes each client's operations and the kill schedule; the interleaving of the
// clients, and so where exactly a kill lands, still depends on timing. A failure prints its
// seed, and -journal keeps each client's log of what it sent and what came back.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/arumadev/aruma/tools/internal/rtdbkit"
)

type config struct {
	seed           int64
	runs           int
	clients        int
	cycles         int
	minUp, maxUp   time.Duration
	maxDown        time.Duration
	port           int
	bin            string
	journal        string
	keep           bool
	jsonOut        string
	verbose        bool
	skipGracefulUp bool
}

func main() {
	var c config
	flag.Int64Var(&c.seed, "seed", 1, "seed of the first run; a failure prints the seed that replays it")
	flag.IntVar(&c.runs, "runs", 1, "runs, with seeds seed, seed+1, …")
	flag.IntVar(&c.clients, "clients", 8, "concurrent clients")
	flag.IntVar(&c.cycles, "cycles", 5, "SIGKILL and restart cycles per run, before the final SIGTERM")
	flag.DurationVar(&c.minUp, "min-up", 300*time.Millisecond, "shortest time the server runs before it is killed")
	flag.DurationVar(&c.maxUp, "max-up", 3*time.Second, "longest time the server runs before it is killed")
	flag.DurationVar(&c.maxDown, "max-down", 500*time.Millisecond, "longest time the server stays down")
	flag.IntVar(&c.port, "port", 8780, "loopback port of the server")
	flag.StringVar(&c.bin, "bin", "", "the aruma binary (default: build ./cmd/aruma)")
	flag.StringVar(&c.journal, "journal", "", "directory to write each client's journal to")
	flag.BoolVar(&c.keep, "keep", false, "keep the database of a failed run, for inspection")
	flag.StringVar(&c.jsonOut, "json", "", "also write the runs' reports as JSON to this file")
	flag.BoolVar(&c.verbose, "v", false, "print each kill and restart")
	flag.Parse()
	if err := runAll(c); err != nil {
		fmt.Fprintln(os.Stderr, "rtdb-chaos:", err)
		os.Exit(1)
	}
}

func runAll(c config) error {
	if c.clients < 1 || c.cycles < 0 || c.runs < 1 || c.minUp <= 0 || c.maxUp < c.minUp {
		return errors.New("bad flags")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if c.bin == "" {
		dir, err := os.MkdirTemp("", "rtdb-chaos-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		fmt.Fprintln(os.Stderr, "building aruma…")
		if c.bin, err = rtdbkit.Build(ctx, dir); err != nil {
			return err
		}
	}
	var reports []*report
	failed := 0
	for i := range c.runs {
		seed := c.seed + int64(i)
		rep, err := runOne(ctx, c, seed)
		if err != nil {
			return fmt.Errorf("seed %d: %w", seed, err)
		}
		reports = append(reports, rep)
		rep.print(os.Stdout)
		if len(rep.Violations) > 0 {
			failed++
		}
	}
	if c.jsonOut != "" {
		b, _ := json.MarshalIndent(reports, "", "  ")
		if err := os.WriteFile(c.jsonOut, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d runs found violations", failed, c.runs)
	}
	return nil
}

// runner is one run: a server, its clients, and the schedule of kills.
type runner struct {
	cfg    config
	seed   int64
	srv    *rtdbkit.Server
	ns     string
	rest   rtdbkit.REST
	wsHTTP *http.Client

	mu     sync.Mutex
	ep     int64
	up     chan struct{}    // closed while the server of epoch ep runs
	stops  map[int64]string // how each epoch ended: "kill" or "term"
	viols  []string
	log    *os.File
	report *report

	stopping atomic.Bool
}

func (r *runner) epoch() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ep
}

// waitUp waits until a server runs and returns its epoch.
func (r *runner) waitUp(ctx context.Context) (int64, error) {
	for {
		r.mu.Lock()
		up, ep := r.up, r.ep
		r.mu.Unlock()
		select {
		case <-up:
			return ep, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// stopOf is how epoch ep ended, "" while it runs.
func (r *runner) stopOf(ep int64) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stops[ep]
}

func (r *runner) violation(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.viols) < 200 {
		r.viols = append(r.viols, msg)
	}
	fmt.Fprintf(os.Stderr, "VIOLATION (seed %d): %s\n", r.seed, msg)
}

// restGet reads a path over REST; nil for null.
func (r *runner) restGet(ctx context.Context, path string) (any, error) {
	status, b, err := r.rest.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %d %s", path, status, b)
	}
	return decode(b)
}

// bringUp starts the server of the next epoch.
func (r *runner) bringUp(ctx context.Context) error {
	if err := r.srv.Start(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	r.ep++
	close(r.up)
	r.mu.Unlock()
	return nil
}

// bringDown stops the server with sig ("kill" or "term"); clients wait for the next epoch.
func (r *runner) bringDown(how string) error {
	r.mu.Lock()
	r.stops[r.ep] = how
	r.up = make(chan struct{})
	r.mu.Unlock()
	if how == "kill" {
		return r.srv.Kill()
	}
	return r.srv.Term()
}

func runOne(ctx context.Context, c config, seed int64) (*report, error) {
	if !rtdbkit.FreePort(c.port) {
		return nil, fmt.Errorf("port %d is in use", c.port)
	}
	logf, err := os.CreateTemp("", "rtdb-chaos-server-*.log")
	if err != nil {
		return nil, err
	}
	defer os.Remove(logf.Name())
	defer logf.Close()
	srv, err := rtdbkit.NewServer(ctx, rtdbkit.Options{Bin: c.bin, Port: c.port, Name: "chaos", Log: logf})
	if err != nil {
		return nil, err
	}
	r := &runner{
		cfg: c, seed: seed, srv: srv, up: make(chan struct{}), stops: map[int64]string{}, log: logf,
		wsHTTP: &http.Client{Transport: &http.Transport{}},
		report: &report{Seed: seed, Clients: c.clients, Database: srv.DBName},
	}
	// One connection per request: Go's client retries an idempotent request (PUT, DELETE)
	// on a fresh connection when a reused one fails, which would send a write twice.
	r.rest = rtdbkit.REST{HC: &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}, Base: srv.URL}
	keep := false
	defer func() {
		if keep {
			_ = srv.Kill()
			fmt.Fprintf(os.Stderr, "kept database %s\n", srv.DBName)
			return
		}
		srv.Drop()
	}()

	start := time.Now()
	if err := r.bringUp(ctx); err != nil {
		return nil, err
	}
	if r.ns, err = srv.Project(ctx, fmt.Sprintf("chaos-%d", seed)); err != nil {
		return nil, err
	}
	r.rest.NS = r.ns

	rng := rand.New(rand.NewPCG(uint64(seed), 0))
	workers := make([]*worker, c.clients)
	var wg sync.WaitGroup
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := range workers {
		workers[i] = newWorker(r, i)
		wg.Go(func() { workers[i].run(wctx) })
	}
	upFor := func() time.Duration { return c.minUp + time.Duration(rng.Int64N(int64(c.maxUp-c.minUp)+1)) }
	var lastSeq int64
	cycle := func(how string) error {
		if !sleepCtx(ctx, upFor()) {
			return ctx.Err()
		}
		if c.verbose {
			fmt.Fprintf(os.Stderr, "seed %d: %s at epoch %d\n", seed, how, r.epoch())
		}
		if err := r.bringDown(how); err != nil {
			r.violation("server stop (%s): %v", how, err)
		}
		// While it is down, its seq can be read straight from Postgres: it never goes back.
		seq, err := srv.Seq(ctx, r.ns)
		if err != nil && !strings.Contains(err.Error(), "no rows") {
			return err
		}
		if seq < lastSeq {
			r.violation("seq went back from %d to %d across a %s", lastSeq, seq, how)
		}
		lastSeq = seq
		if !sleepCtx(ctx, time.Duration(rng.Int64N(int64(c.maxDown)+1))) {
			return ctx.Err()
		}
		return r.bringUp(ctx)
	}
	for range c.cycles {
		if err := cycle("kill"); err != nil {
			return nil, err
		}
		r.report.Kills++
	}
	if err := cycle("term"); err != nil {
		return nil, err
	}
	r.report.Terms++
	// Let the clients settle what the shutdown left in flight, then stop them.
	sleepCtx(ctx, c.minUp)
	r.stopping.Store(true)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		cancel()
		return nil, errors.New("the clients did not finish within 2 minutes of the stop")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	r.check(ctx, workers)
	r.report.Elapsed = time.Since(start).Round(time.Millisecond).String()
	r.report.Violations = r.viols
	if len(r.viols) > 0 {
		keep = c.keep
		if c.journal != "" {
			if err := writeJournals(c.journal, seed, workers, logf.Name()); err != nil {
				fmt.Fprintln(os.Stderr, "journal:", err)
			}
		}
	}
	return r.report, nil
}

func writeJournals(dir string, seed int64, workers []*worker, serverLog string) error {
	d := filepath.Join(dir, fmt.Sprintf("seed-%d", seed))
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	for _, w := range workers {
		if err := os.WriteFile(filepath.Join(d, w.key+".log"), []byte(strings.Join(w.journal, "\n")+"\n"), 0o644); err != nil {
			return err
		}
	}
	if b, err := os.ReadFile(serverLog); err == nil {
		_ = os.WriteFile(filepath.Join(d, "server.log"), b, 0o644)
	}
	fmt.Fprintf(os.Stderr, "journals in %s\n", d)
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
