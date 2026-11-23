// Command docs-chaos checks that the Document Database keeps every write it acknowledged when
// its server dies, as tools/rtdb-chaos does for the Realtime Database.
//
// It starts a real Aruma binary with the Document Database enabled on a throwaway Postgres
// database, drives it with concurrent clients over gRPC and REST (commits, batched writes,
// read-write transactions, BatchWrite) and Listen streams, and at random points kills the server
// with SIGKILL and starts it again. A write whose reply was lost is read back after the restart
// and so settled: applied, or not. At the end it reads everything back and checks:
//
//   - every acknowledged write is there, and no refused one;
//
//   - a commit is whole or absent: its documents and the counters it incremented change
//     together, so each counter equals the sum of the documents that exist (no increment lost
//     or applied twice);
//
//   - a counter incremented by read-modify-write transactions, alone or contended, equals the
//     number of its markers, and the markers hold 1..n exactly;
//
//   - the changelog's seqs are contiguous, the sequencer row holds the last, and commit_time
//     rises strictly with seq; a commit acknowledged before another was sent has an earlier
//     commit_time, across restarts too;
//
//   - a Listen stream resumed with its token gets exactly what it missed: at its first
//     consistent snapshot after the resume its view is the query's result at that read time,
//     and nothing it held is sent again (unless the server said RESET).
//
//     go run ./tools/docs-chaos                         # seed 1: 10 kill cycles, then SIGTERM
//     go run ./tools/docs-chaos -seed 7 -runs 3 -cycles 10
//
// The seed fixes each client's operations and the kill schedule; where exactly a kill lands
// still depends on timing.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/tools/internal/chaoskit"
	"github.com/arumadev/aruma/tools/internal/rtdbkit"
)

type config struct {
	seed      int64
	runs      int
	clients   int
	listeners int
	sched     chaoskit.Schedule
	port      int
	bin       string
	keep      bool
	jsonOut   string
	verbose   bool
}

func main() {
	var c config
	flag.Int64Var(&c.seed, "seed", 1, "seed of the first run; a failure prints the seed that replays it")
	flag.IntVar(&c.runs, "runs", 1, "runs, with seeds seed, seed+1, …")
	flag.IntVar(&c.clients, "clients", 8, "concurrent writers")
	flag.IntVar(&c.listeners, "listeners", 2, "Listen streams that resume across restarts")
	flag.IntVar(&c.sched.Cycles, "cycles", 10, "SIGKILL and restart cycles per run, before the final SIGTERM")
	flag.DurationVar(&c.sched.MinUp, "min-up", 500*time.Millisecond, "shortest time the server runs before it is killed")
	flag.DurationVar(&c.sched.MaxUp, "max-up", 3*time.Second, "longest time the server runs before it is killed")
	flag.DurationVar(&c.sched.MaxDown, "max-down", 500*time.Millisecond, "longest time the server stays down")
	flag.IntVar(&c.port, "port", 8790, "loopback port of the server")
	flag.StringVar(&c.bin, "bin", "", "the aruma binary (default: build ./cmd/aruma)")
	flag.BoolVar(&c.keep, "keep", false, "keep the database of a failed run, for inspection")
	flag.StringVar(&c.jsonOut, "json", "", "also write the runs' reports as JSON to this file")
	flag.BoolVar(&c.verbose, "v", false, "print each kill and restart")
	flag.Parse()
	if err := runAll(c); err != nil {
		fmt.Fprintln(os.Stderr, "docs-chaos:", err)
		os.Exit(1)
	}
}

func runAll(c config) error {
	if c.clients < 1 || c.sched.Cycles < 0 || c.runs < 1 || c.sched.MinUp <= 0 || c.sched.MaxUp < c.sched.MinUp {
		return errors.New("bad flags")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if c.bin == "" {
		dir, err := os.MkdirTemp("", "docs-chaos-")
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
		rep.print()
		if rep.Violations > 0 {
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

// run is one run: a server, its clients and listeners, and the schedule of kills.
type run struct {
	*chaoskit.Runner
	cfg  config
	bg   context.Context
	db   string
	grpc *grpcAPI
	rest *restAPI

	snapMu   sync.Mutex
	lastSnap time.Time
	lastTS   time.Time // the last commit's commit_time, read by check
}

func (r *run) noteSnapshot(t time.Time) {
	r.snapMu.Lock()
	if t.After(r.lastSnap) {
		r.lastSnap = t
	}
	r.snapMu.Unlock()
}

type report struct {
	Seed        int64          `json:"seed"`
	Pass        bool           `json:"pass"`
	Duration    string         `json:"duration"`
	Kills       int            `json:"kills"`
	Clients     int            `json:"clients"`
	Counts      map[string]int `json:"counts"`
	Docs        int            `json:"documents"`
	Seq         int64          `json:"seq"`
	Commits     int            `json:"ackedCommits"`
	Listen      map[string]int `json:"listen"`
	Violations  int            `json:"violations"`
	ViolationsL []string       `json:"violationList,omitempty"`
}

func (rep *report) print() {
	verdict := "PASS"
	if !rep.Pass {
		verdict = "FAIL"
	}
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(rep.Counts)) {
		if strings.Contains(k, "lost") || strings.Contains(k, "refused") {
			parts = append(parts, fmt.Sprintf("%s=%d", k, rep.Counts[k]))
		}
	}
	fmt.Printf("seed %d: %s in %s — %d clients, %d SIGKILL + 1 SIGTERM; %d commits acked, %d documents, seq %d; listen %v; %s\n",
		rep.Seed, verdict, rep.Duration, rep.Clients, rep.Kills, rep.Commits, rep.Docs, rep.Seq, rep.Listen, strings.Join(parts, " "))
	for _, v := range rep.ViolationsL {
		fmt.Println("  violation:", v)
	}
}

func runOne(ctx context.Context, c config, seed int64) (*report, error) {
	if !rtdbkit.FreePort(c.port) {
		return nil, fmt.Errorf("port %d is in use", c.port)
	}
	logf, err := os.CreateTemp("", "docs-chaos-server-*.log")
	if err != nil {
		return nil, err
	}
	defer os.Remove(logf.Name())
	defer logf.Close()
	srv, err := rtdbkit.NewServer(ctx, rtdbkit.Options{Bin: c.bin, Port: c.port, Name: "docchaos", Log: logf, Env: map[string]string{
		"ARUMA_DOCUMENTS_ENABLED":             "true",
		"ARUMA_DOCUMENTS_DEV_OPEN_RULES":      "true",
		"ARUMA_DOCUMENTS_DEV_OWNER_TOKEN":     "true",
		"ARUMA_DOCUMENTS_STREAMS_PER_IP":      "100000",
		"ARUMA_DOCUMENTS_STREAMS_PER_PROJECT": "100000",
		"ARUMA_RTDB_ENABLED":                  "false",
	}})
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed && c.keep {
			_ = srv.Kill()
			fmt.Fprintf(os.Stderr, "kept database %s\n", srv.DBName)
			return
		}
		srv.Drop()
	}()
	start := time.Now()
	r := &run{Runner: chaoskit.NewRunner(srv, seed), cfg: c, bg: ctx}
	if err := r.BringUp(ctx); err != nil {
		return nil, err
	}
	project := fmt.Sprintf("docchaos-%d", seed)
	if _, err := srv.Project(ctx, project); err != nil {
		return nil, err
	}
	r.db = "projects/" + project + "/databases/(default)"
	r.grpc = newGRPC(srv.URL, r.db, "")
	r.rest = newREST(srv.URL, r.db, "")

	wctx, stopClients := context.WithCancel(ctx)
	var wg sync.WaitGroup
	clients := make([]*client, c.clients)
	for i := range clients {
		auth := ""
		if i%2 == 0 {
			auth = "Bearer owner"
		}
		clients[i] = &client{r: r, i: i, rng: rand.New(rand.NewPCG(uint64(seed), uint64(i)+1)), admin: auth != "",
			apis: []api{newGRPC(srv.URL, r.db, auth), newREST(srv.URL, r.db, auth)}, stats: map[string]int{}}
		wg.Go(func() { clients[i].loop(wctx) })
	}
	lctx, stopListeners := context.WithCancel(ctx)
	var lwg sync.WaitGroup
	listeners := make([]*listener, c.listeners)
	for i := range listeners {
		listeners[i] = &listener{r: r, id: i}
		lwg.Go(func() { listeners[i].loop(lctx) })
	}
	rng := rand.New(rand.NewPCG(uint64(seed), 0))
	err = r.Run(ctx, c.sched, rng, func(how string, up, down time.Duration) {
		if c.verbose {
			fmt.Fprintf(os.Stderr, "%s after %v up, %v down\n", how, up.Round(time.Millisecond), down.Round(time.Millisecond))
		}
	})
	time.Sleep(c.sched.MinUp)
	stopClients()
	wg.Wait()
	if err != nil {
		stopListeners()
		lwg.Wait()
		return nil, err
	}
	rep := &report{Seed: seed, Kills: c.sched.Cycles, Clients: c.clients, Counts: map[string]int{}, Listen: map[string]int{}}
	r.check(ctx, clients, rep)
	// A last write to ops, after the check read everything: a commit elsewhere (a counter, a
	// transaction's marker) gives the listeners no new snapshot, so the final one is awaited
	// at this commit. Its k of 0 changes no sum.
	final := r.lastTS
	if resp, err := r.grpc.Commit(ctx, &pb.CommitRequest{Database: r.db, Writes: []*pb.Write{
		update(r.db+"/documents/ops/final", map[string]*pb.Value{"k": intV(0)})}}); err == nil {
		final = resp.GetCommitTime().AsTime()
	} else {
		r.Violation("the final write: %v", err)
	}
	for _, l := range listeners {
		l.finalCheck(ctx, final)
	}
	stopListeners()
	lwg.Wait()
	for _, l := range listeners {
		rep.Listen["streams"] += l.streams
		rep.Listen["resumes"] += l.resumes
		rep.Listen["resets"] += l.resets
		rep.Listen["redelivered"] += l.redeliver
		rep.Listen["checked"] += l.checked
		rep.Listen["skipped"] += l.skipped
		rep.Listen["snapshots"] += l.snapshots
		if l.redeliver > 0 {
			r.Violation("listener %d: %d documents sent again at the version it held, after a resume without RESET", l.id, l.redeliver)
		}
	}
	for _, cl := range clients {
		for k, v := range cl.stats {
			rep.Counts[k] += v
		}
	}
	viols, n := r.Violations()
	rep.Violations, rep.ViolationsL = n, viols
	rep.Pass = n == 0
	rep.Duration = time.Since(start).Round(time.Millisecond).String()
	failed = !rep.Pass
	return rep, nil
}
