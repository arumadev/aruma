// Command storage-chaos checks that Storage keeps every object it acknowledged when its server
// dies, and never serves or keeps half of one.
//
// It starts a real Aruma binary with Storage enabled on a throwaway Postgres database and data
// directory, drives it with concurrent clients over the Firebase protocol (/v0: simple,
// multipart and resumable uploads of mixed sizes, deletes, metadata updates, downloads), and at
// random points kills the server with SIGKILL and starts it again. Each client owns a few
// objects, so it knows what each must hold: a request whose reply was lost is settled by reading
// the object back after the restart. Every object's bytes name their version and size, so a
// download that is cut, padded or of another version is caught. It checks:
//
//   - an acknowledged upload, delete or metadata update is there; a refused one is not; one
//     whose reply was lost left the object either as it was or as the request makes it;
//
//   - every object served is whole: its bytes, size and md5 are those of one version;
//
//   - a resumable upload cut by the kill resumes from the offset the session reports, which is
//     no less than what the server acknowledged; or its session is gone and the object is as
//     it was;
//
//   - at the end, after a restart: every object row has its file, the usage row matches the
//     objects, and the files no row points to (orphans of the kills) are deleted by the sweep at
//     boot, and the replaced ones by the GC after gc_grace.
//
//     go run ./tools/storage-chaos                       # seed 1: 10 kill cycles, then SIGTERM
//     go run ./tools/storage-chaos -seed 7 -runs 3
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/arumadev/aruma/tools/internal/chaoskit"
	"github.com/arumadev/aruma/tools/internal/rtdbkit"
)

type config struct {
	seed    int64
	runs    int
	clients int
	objects int
	sched   chaoskit.Schedule
	port    int
	bin     string
	keep    bool
	jsonOut string
	verbose bool
	settle  time.Duration
}

func main() {
	var c config
	flag.Int64Var(&c.seed, "seed", 1, "seed of the first run; a failure prints the seed that replays it")
	flag.IntVar(&c.runs, "runs", 1, "runs, with seeds seed, seed+1, …")
	flag.IntVar(&c.clients, "clients", 8, "concurrent clients")
	flag.IntVar(&c.objects, "objects", 6, "objects each client owns")
	flag.IntVar(&c.sched.Cycles, "cycles", 10, "SIGKILL and restart cycles per run, before the final SIGTERM")
	flag.DurationVar(&c.sched.MinUp, "min-up", 500*time.Millisecond, "shortest time the server runs before it is killed")
	flag.DurationVar(&c.sched.MaxUp, "max-up", 3*time.Second, "longest time the server runs before it is killed")
	flag.DurationVar(&c.sched.MaxDown, "max-down", 500*time.Millisecond, "longest time the server stays down")
	flag.DurationVar(&c.settle, "settle", 2*time.Minute, "how long the end waits for the GC and the session expiry to clean up")
	flag.IntVar(&c.port, "port", 8795, "loopback port of the server")
	flag.StringVar(&c.bin, "bin", "", "the aruma binary (default: build ./cmd/aruma)")
	flag.BoolVar(&c.keep, "keep", false, "keep the database and data directory of a failed run")
	flag.StringVar(&c.jsonOut, "json", "", "also write the runs' reports as JSON to this file")
	flag.BoolVar(&c.verbose, "v", false, "print each kill and restart")
	flag.Parse()
	if err := runAll(c); err != nil {
		fmt.Fprintln(os.Stderr, "storage-chaos:", err)
		os.Exit(1)
	}
}

func runAll(c config) error {
	if c.clients < 1 || c.objects < 1 || c.sched.Cycles < 0 || c.runs < 1 || c.sched.MinUp <= 0 || c.sched.MaxUp < c.sched.MinUp {
		return errors.New("bad flags")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if c.bin == "" {
		dir, err := os.MkdirTemp("", "storage-chaos-")
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

type run struct {
	*chaoskit.Runner
	cfg     config
	bg      context.Context
	hc      *http.Client
	base    string // http://host:port/v0/b/<bucket>/o
	dataDir string
	project string

	downMu      sync.Mutex
	orphansSeen int
	heldSeen    int
}

type report struct {
	Seed        int64          `json:"seed"`
	Pass        bool           `json:"pass"`
	Duration    string         `json:"duration"`
	Kills       int            `json:"kills"`
	Clients     int            `json:"clients"`
	Counts      map[string]int `json:"counts"`
	Objects     int            `json:"objects"`
	Cleanup     map[string]any `json:"cleanup"`
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
		parts = append(parts, fmt.Sprintf("%s=%d", k, rep.Counts[k]))
	}
	fmt.Printf("seed %d: %s in %s — %d clients, %d SIGKILL + 1 SIGTERM; %d objects at the end; cleanup %v\n  %s\n",
		rep.Seed, verdict, rep.Duration, rep.Clients, rep.Kills, rep.Objects, rep.Cleanup, strings.Join(parts, " "))
	for _, v := range rep.ViolationsL {
		fmt.Println("  violation:", v)
	}
}

func runOne(ctx context.Context, c config, seed int64) (*report, error) {
	if !rtdbkit.FreePort(c.port) {
		return nil, fmt.Errorf("port %d is in use", c.port)
	}
	logf, err := os.CreateTemp("", "storage-chaos-server-*.log")
	if err != nil {
		return nil, err
	}
	defer os.Remove(logf.Name())
	defer logf.Close()
	dataDir, err := os.MkdirTemp("", "storage-chaos-data-")
	if err != nil {
		return nil, err
	}
	srv, err := rtdbkit.NewServer(ctx, rtdbkit.Options{Bin: c.bin, Port: c.port, Name: "storchaos", Log: logf, Env: map[string]string{
		"ARUMA_STORAGE_ENABLED":                         "true",
		"ARUMA_STORAGE_DATA_DIR":                        dataDir,
		"ARUMA_STORAGE_GC_GRACE":                        "2s",
		"ARUMA_STORAGE_EMPTY_SESSION_TTL":               "20s",
		"ARUMA_STORAGE_UPLOAD_IDLE_TTL":                 "30s",
		"ARUMA_STORAGE_UPLOAD_SESSION_TTL":              "45s",
		"ARUMA_STORAGE_CONSOLE_SESSION_TTL":             "45s",
		"ARUMA_STORAGE_UPLOAD_SESSIONS_PER_IP":          "100000",
		"ARUMA_STORAGE_UPLOAD_SESSIONS_PER_PROJECT":     "100000",
		"ARUMA_STORAGE_UPLOAD_SESSIONS_PER_USER":        "100000",
		"ARUMA_STORAGE_UPLOAD_STARTS_PER_MINUTE_PER_IP": "1000000",
		"ARUMA_STORAGE_UPLOAD_STARTS_PER_SECOND":        "100000",
		"ARUMA_STORAGE_UPLOAD_REQUESTS_PER_IP":          "100000",
		"ARUMA_RTDB_ENABLED":                            "false",
		// The run writes a few hundred MB at most; a disk that is nearly full otherwise refuses
		// every upload, which would hide what the run checks.
		"ARUMA_STORAGE_MIN_FREE_BYTES": "268435456",
	}})
	if err != nil {
		os.RemoveAll(dataDir)
		return nil, err
	}
	failed := true
	defer func() {
		if failed && c.keep {
			_ = srv.Kill()
			fmt.Fprintf(os.Stderr, "kept database %s and data directory %s\n", srv.DBName, dataDir)
			return
		}
		srv.Drop()
		os.RemoveAll(dataDir)
	}()
	start := time.Now()
	r := &run{Runner: chaoskit.NewRunner(srv, seed), cfg: c, bg: ctx, dataDir: dataDir,
		hc: &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 32}}}
	if err := r.BringUp(ctx); err != nil {
		return nil, err
	}
	r.project = fmt.Sprintf("storchaos-%d", seed)
	var p struct {
		StorageBucket string `json:"storageBucket"`
	}
	if err := srv.ConsoleJSON(ctx, "POST", "/console/v1/projects", map[string]string{"projectId": r.project, "displayName": r.project}, &p); err != nil {
		return nil, err
	}
	if p.StorageBucket == "" {
		return nil, errors.New("the project has no default bucket")
	}
	open := "rules_version = '2';\nservice firebase.storage { match /b/{bucket}/o { match /{allPaths=**} { allow read, write: if true; } } }"
	if err := srv.Console(ctx, "PUT", "/console/v1/projects/"+r.project+"/storage/rules", map[string]string{"source": open}); err != nil {
		return nil, err
	}
	r.base = srv.URL + "/v0/b/" + p.StorageBucket + "/o"

	wctx, stopClients := context.WithCancel(ctx)
	var wg sync.WaitGroup
	clients := make([]*client, c.clients)
	for i := range clients {
		cl := &client{r: r, i: i, rng: rand.New(rand.NewPCG(uint64(seed), uint64(i)+1)), want: map[string]state{}, stats: map[string]int{}}
		for j := range c.objects {
			cl.names = append(cl.names, fmt.Sprintf("o/c%d/%d", i, j))
		}
		clients[i] = cl
		wg.Go(func() { cl.loop(wctx) })
	}
	rng := rand.New(rand.NewPCG(uint64(seed), 0))
	err = r.Run(ctx, c.sched, rng, func(how string, up, down time.Duration) {
		r.whileDown(ctx, how)
		if c.verbose {
			fmt.Fprintf(os.Stderr, "%s after %v up, %v down\n", how, up.Round(time.Millisecond), down.Round(time.Millisecond))
		}
	})
	time.Sleep(c.sched.MinUp)
	stopClients()
	wg.Wait()
	if err != nil {
		return nil, err
	}
	rep := &report{Seed: seed, Kills: c.sched.Cycles, Clients: c.clients, Counts: map[string]int{}, Cleanup: map[string]any{}}
	r.check(ctx, clients, rep)
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
