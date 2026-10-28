// Command bench measures Aruma's latency and throughput.
//
// By default it creates a throwaway database, starts Aruma in this process on a loopback port
// (wired as `aruma serve` wires it, with production argon2id cost), seeds projects, apps and
// Auth users through the public APIs, then drives each scenario in turn and drops the database.
//
//	go run ./tools/bench                                   # all scenarios, 10 s each
//	go run ./tools/bench -scenarios healthz,mgmt-web-config -c 16 -duration 20s
//	go run ./tools/bench -rate 500                         # open loop: 500 req/s per scenario
//	go run ./tools/bench -json run.json
//
// To keep the load generator out of the server's process, start a server with -serve in one
// terminal and point a second bench at it with the printed -url/-token/-email/-password. The
// same flags measure any running Aruma; seeding then creates projects with that token.
//
// Two driving modes:
//   - closed loop (default): -c workers each send a request as soon as their last one returns.
//     This finds throughput at that concurrency; latency is per-request service time.
//   - open loop (-rate R): requests are scheduled at R per second and latency is measured from
//     each request's scheduled time, so a stall shows as the queueing a real client would see
//     rather than being hidden by the load generator slowing down (coordinated omission).
//
// Latency percentiles count successful (2xx) responses only; failures are counted by status.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"slices"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

// buildCommit can be set with -ldflags "-X main.buildCommit=<hash>" when building from a tree
// without VCS information (such as a git archive); it takes precedence in the report.
var buildCommit string

type options struct {
	url, token, email, password string
	scenarios                   string
	concurrency                 int
	duration, warmup, pause     time.Duration
	rate                        float64
	projects                    int
	jsonOut                     string
	cpuProfile                  string
	serve                       bool
	note                        string
	poolMax                     int
	authUsers                   int
	rtdb                        string
	rtdbOpts                    rtdbOptions
}

func main() {
	var o options
	flag.StringVar(&o.url, "url", "", "measure a running server at this base URL instead of starting one")
	flag.StringVar(&o.token, "token", "", "with -url: a personal CLI token (aruma_pat_…) used to seed and call the Management API")
	flag.StringVar(&o.email, "email", "", "with -url: console account email, for the session and login scenarios")
	flag.StringVar(&o.password, "password", "", "with -url: console account password")
	flag.StringVar(&o.scenarios, "scenarios", "all", "comma-separated scenarios to run, or all: "+strings.Join(names(), ","))
	flag.IntVar(&o.concurrency, "c", 8, "concurrent workers per scenario (the argon2id-bound ones use 4 unless -login-c is set)")
	loginC := flag.Int("login-c", 0, "concurrent workers for console-login, auth-signup and auth-signin-password (default 4, the server's argon2id gate)")
	flag.DurationVar(&o.duration, "duration", 10*time.Second, "measured time per scenario (at most 60s)")
	flag.DurationVar(&o.warmup, "warmup", 2*time.Second, "unrecorded warm-up before each scenario's measured window")
	flag.DurationVar(&o.pause, "pause", time.Second, "idle time between scenarios")
	flag.Float64Var(&o.rate, "rate", 0, "open loop: requests per second per scenario (0 = closed loop)")
	flag.IntVar(&o.projects, "projects", 10, "projects to seed, each with a web, Android and iOS app")
	flag.StringVar(&o.jsonOut, "json", "", "also write the results as JSON to this file")
	flag.StringVar(&o.cpuProfile, "cpuprofile", "", "write a CPU profile of this process (server included when in-process)")
	flag.BoolVar(&o.serve, "serve", false, "start an in-process server, print how to target it, and wait for Ctrl-C")
	flag.IntVar(&o.poolMax, "db-pool-max", 0, "in-process: the server's database pool size (default: pgx's max(4, NumCPU))")
	flag.IntVar(&o.authUsers, "auth-users", 256, "email/password users to seed for the auth-* scenarios")
	flag.Uint64Var(&clientAddrs, "client-addrs", clientAddrs, "client addresses the rate-limited scenarios cycle through (0: a new one per request)")
	flag.StringVar(&o.note, "note", "", "free text stored in the JSON, e.g. the machine's state")
	flag.StringVar(&o.rtdb, "rtdb", "", "run the Realtime Database scenarios instead (all, or a list of: conns,fanout,writes,query,lp) against the aruma binary in a process of its own")
	flag.StringVar(&o.rtdbOpts.bin, "rtdb-bin", "", "with -rtdb: the aruma binary (default: build ./cmd/aruma)")
	flag.IntVar(&o.rtdbOpts.port, "rtdb-port", 8785, "with -rtdb: the server's loopback port")
	flag.IntVar(&o.rtdbOpts.metricsPort, "rtdb-metrics-port", 8786, "with -rtdb: the server's metrics port, read for its heap and goroutines")
	flag.IntVar(&o.rtdbOpts.conns, "rtdb-conns", 10_000, "with -rtdb conns: connections to ramp to")
	flag.IntVar(&o.rtdbOpts.connStep, "rtdb-conn-step", 1000, "with -rtdb conns: connections added between samples")
	fanout := flag.String("rtdb-fanout", "1,100,1000", "with -rtdb fanout: listener counts")
	flag.Float64Var(&o.rtdbOpts.fanoutRate, "rtdb-fanout-rate", 20, "with -rtdb fanout: writes per second")
	flag.IntVar(&o.rtdbOpts.fanoutWrites, "rtdb-fanout-writes", 200, "with -rtdb fanout: writes per listener count, the first tenth unrecorded")
	writers := flag.String("rtdb-writers", "1,8,32,128", "with -rtdb writes: concurrent writer counts")
	sizes := flag.String("rtdb-query-sizes", "10000,100000", "with -rtdb query: children of the queried nodes")
	flag.Parse()
	var err error
	if o.rtdbOpts.fanout, err = parseInts(*fanout); err == nil {
		if o.rtdbOpts.writers, err = parseInts(*writers); err == nil {
			o.rtdbOpts.querySizes, err = parseInts(*sizes)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(2)
	}
	if *loginC > 0 {
		for i := range Scenarios {
			if Scenarios[i].Argon2 {
				Scenarios[i].Concurrency = *loginC
			}
		}
	}
	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func names() []string {
	var out []string
	for _, s := range Scenarios {
		out = append(out, s.Name)
	}
	return out
}

func run(o options) error {
	if o.duration <= 0 || o.duration > time.Minute {
		return errors.New("-duration must be between 0 and 60s")
	}
	if o.concurrency < 1 || o.projects < 1 || o.authUsers < 1 {
		return errors.New("-c, -projects and -auth-users must be at least 1")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if o.rtdb != "" {
		return runRTDB(ctx, o)
	}
	selected, err := selectScenarios(o.scenarios)
	if err != nil {
		return err
	}

	maxConc := o.concurrency
	for _, s := range selected {
		maxConc = max(maxConc, s.Concurrency)
	}
	hc := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        4 * maxConc,
			MaxIdleConnsPerHost: 4 * maxConc,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
		},
	}

	var t *Target
	mode := "in-process"
	if o.url != "" {
		if o.serve {
			return errors.New("-serve and -url exclude each other")
		}
		if o.token == "" {
			return errors.New("-url needs -token")
		}
		mode = "remote"
		t = &Target{URL: strings.TrimSuffix(o.url, "/"), Token: o.token, Email: o.email, Password: o.password}
	} else {
		fmt.Fprintln(os.Stderr, "starting Aruma in-process on a throwaway database…")
		if t, err = StartInProcess(ctx, io.Discard, o.poolMax); err != nil {
			return err
		}
		defer t.Close()
	}

	if o.serve {
		fmt.Printf("server ready. In another terminal:\n\n  go run ./tools/bench -url %s -token %s -email %s -password %s\n\nCtrl-C stops it and drops the database.\n",
			t.URL, t.Token, t.Email, t.Password)
		<-ctx.Done()
		return nil
	}

	need := Needs{AuthUsers: o.authUsers}
	for _, s := range selected {
		need.Session = need.Session || s.NeedSession
		need.SA = need.SA || s.NeedSA
		need.Auth = need.Auth || s.NeedAuth
	}
	if t.Email == "" || t.Password == "" {
		var kept []Scenario
		for _, s := range selected {
			if s.NeedSession || s.Name == "console-login" {
				fmt.Fprintf(os.Stderr, "skipping %s: needs -email and -password\n", s.Name)
				continue
			}
			kept = append(kept, s)
		}
		selected, need.Session = kept, false
	}
	fmt.Fprintf(os.Stderr, "seeding %d projects (3 apps each)…\n", o.projects)
	if need.Auth {
		fmt.Fprintf(os.Stderr, "seeding %d Auth users…\n", o.authUsers)
	}
	fx, err := Seed(ctx, hc, t, o.projects, need)
	if err != nil {
		return err
	}

	if o.cpuProfile != "" {
		f, err := os.Create(o.cpuProfile)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}

	report := Report{Meta: collectMeta(o, mode), Plan: planJSON(o)}
	for i, sc := range selected {
		if ctx.Err() != nil {
			break
		}
		if i > 0 && !sleep(ctx, o.pause) {
			break
		}
		conc := o.concurrency
		if sc.Concurrency > 0 {
			conc = sc.Concurrency
		}
		fmt.Fprintf(os.Stderr, "%-20s c=%-3d %s + %s warm-up…\n", sc.Name, conc, o.duration, o.warmup)
		var before, after *poolSnap
		m := Drive(ctx, Plan{Concurrency: conc, Warmup: o.warmup, Duration: o.duration, Rate: o.rate,
			OnWindow: func(open bool) {
				if open {
					before = poolSnapshot(t)
				} else {
					after = poolSnapshot(t)
				}
			}},
			requester(hc, sc, sc.New(t, fx)))
		r := toResult(sc, conc, o.rate, m)
		if r.Pool = poolDelta(t, before, after); r.Pool != nil && r.Requests > 0 {
			r.Pool.AcquiresPerReq = float64(r.Pool.Acquires) / float64(r.Requests)
		}
		report.Results = append(report.Results, r)
	}
	report.Meta.MaxRSSMiB = maxRSSMiB()

	printTable(os.Stdout, report)
	if o.jsonOut != "" {
		b, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(o.jsonOut, append(b, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", o.jsonOut)
	}
	return ctx.Err()
}

func selectScenarios(list string) ([]Scenario, error) {
	if list == "" || list == "all" {
		return Scenarios, nil
	}
	var out []Scenario
	for _, n := range strings.Split(list, ",") {
		n = strings.TrimSpace(n)
		i := slices.IndexFunc(Scenarios, func(s Scenario) bool { return s.Name == n })
		if i < 0 {
			return nil, fmt.Errorf("unknown scenario %q; known: %s", n, strings.Join(names(), ", "))
		}
		out = append(out, Scenarios[i])
	}
	return out, nil
}

// Report is the JSON output.
type Report struct {
	Meta    Meta     `json:"meta"`
	Plan    PlanJSON `json:"plan"`
	Results []Result `json:"results"`
}

// Meta records where and how the numbers were produced.
type Meta struct {
	Time       time.Time `json:"time"`
	Mode       string    `json:"mode"`
	Commit     string    `json:"commit,omitempty"`
	GoVersion  string    `json:"goVersion"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	CPU        string    `json:"cpu,omitempty"`
	NumCPU     int       `json:"numCpu"`
	GOMAXPROCS int       `json:"gomaxprocs"`
	MaxRSSMiB  float64   `json:"maxRssMiB,omitempty"`
	Args       []string  `json:"args"`
	Note       string    `json:"note,omitempty"`
}

// PlanJSON is the driving plan.
type PlanJSON struct {
	Driver      string  `json:"driver"`
	Concurrency int     `json:"concurrency"`
	Rate        float64 `json:"ratePerSecond,omitempty"`
	WarmupSec   float64 `json:"warmupSeconds"`
	DurationSec float64 `json:"durationSeconds"`
	Projects    int     `json:"projects"`
}

// Result is one scenario's outcome. Latencies are milliseconds.
type Result struct {
	Scenario      string           `json:"scenario"`
	What          string           `json:"what"`
	Concurrency   int              `json:"concurrency"`
	Requests      int64            `json:"requests"`
	OK            int64            `json:"ok"`
	Failed        int64            `json:"failed"`
	Statuses      map[string]int64 `json:"statuses"`
	TransportErrs int64            `json:"transportErrors,omitempty"`
	FirstError    string           `json:"firstError,omitempty"`
	ElapsedSec    float64          `json:"elapsedSeconds"`
	OKPerSec      float64          `json:"okPerSecond"`
	LatencyMs     Latency          `json:"latencyMs"`
	Scheduled     int64            `json:"scheduled,omitempty"`
	Unsent        int64            `json:"unsent,omitempty"`
	MaxLagMs      float64          `json:"maxScheduleLagMs,omitempty"`
	Pool          *PoolStats       `json:"dbPool,omitempty"`
}

// Latency summarises a histogram.
type Latency struct {
	Min  float64 `json:"min"`
	Mean float64 `json:"mean"`
	P50  float64 `json:"p50"`
	P90  float64 `json:"p90"`
	P99  float64 `json:"p99"`
	P999 float64 `json:"p99_9"`
	Max  float64 `json:"max"`
}

// PoolStats is the server's database pool activity during one scenario (in-process only).
type PoolStats struct {
	MaxConns       int32   `json:"maxConns"`
	Acquires       int64   `json:"acquires"`
	AcquiresPerReq float64 `json:"acquiresPerRequest"`
	Waited         int64   `json:"acquiresThatWaited"`
	WaitedPct      float64 `json:"waitedPercent"`
	MeanWaitMs     float64 `json:"meanWaitMsPerWaitingAcquire"`
	MeanAcquireMs  float64 `json:"meanAcquireMs"`
}

func ms(d time.Duration) float64 { return float64(d) / 1e6 }

func summarize(h *Histogram) Latency {
	return Latency{
		Min: ms(h.Min()), Mean: ms(h.Mean()), P50: ms(h.Quantile(0.5)), P90: ms(h.Quantile(0.9)),
		P99: ms(h.Quantile(0.99)), P999: ms(h.Quantile(0.999)), Max: ms(h.Max()),
	}
}

func toResult(sc Scenario, conc int, rate float64, m *Measurement) Result {
	r := Result{
		Scenario: sc.Name, What: sc.What, Concurrency: conc,
		Requests: m.Total(), OK: m.OK(), Failed: m.Total() - m.OK(),
		Statuses: map[string]int64{}, TransportErrs: m.Transport, FirstError: m.FirstError,
		ElapsedSec: m.Elapsed.Seconds(), LatencyMs: summarize(&m.Latency),
	}
	for s, c := range m.Statuses {
		r.Statuses[fmt.Sprint(s)] = c
	}
	if m.Elapsed > 0 {
		r.OKPerSec = float64(r.OK) / m.Elapsed.Seconds()
	}
	if rate > 0 {
		r.Scheduled, r.Unsent, r.MaxLagMs = m.Scheduled, m.Unsent, ms(m.MaxLag)
	}
	return r
}

type poolSnap struct {
	acquires, waited int64
	acquireDur, wait time.Duration
}

func poolSnapshot(t *Target) *poolSnap {
	if t.Pool == nil {
		return nil
	}
	s := t.Pool.Stat()
	return &poolSnap{s.AcquireCount(), s.EmptyAcquireCount(), s.AcquireDuration(), s.EmptyAcquireWaitTime()}
}

// poolDelta is the pool activity between two snapshots taken as the measured window opened
// and closed. Requests in flight at either edge make it approximate by a few acquires.
func poolDelta(t *Target, before, after *poolSnap) *PoolStats {
	if before == nil || after == nil {
		return nil
	}
	p := &PoolStats{
		MaxConns: t.Pool.Stat().MaxConns(),
		Acquires: after.acquires - before.acquires,
		Waited:   after.waited - before.waited,
	}
	if p.Acquires > 0 {
		p.WaitedPct = 100 * float64(p.Waited) / float64(p.Acquires)
		p.MeanAcquireMs = ms(after.acquireDur-before.acquireDur) / float64(p.Acquires)
	}
	if p.Waited > 0 {
		p.MeanWaitMs = ms(after.wait-before.wait) / float64(p.Waited)
	}
	return p
}

func planJSON(o options) PlanJSON {
	p := PlanJSON{Driver: "closed-loop", Concurrency: o.concurrency, WarmupSec: o.warmup.Seconds(), DurationSec: o.duration.Seconds(), Projects: o.projects}
	if o.rate > 0 {
		p.Driver, p.Rate = "open-loop", o.rate
	}
	return p
}

func collectMeta(o options, mode string) Meta {
	m := Meta{
		Time: time.Now().UTC(), Mode: mode, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		NumCPU: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), Args: redactArgs(os.Args[1:]), Note: o.note,
	}
	m.Commit = buildCommit
	if bi, ok := debug.ReadBuildInfo(); ok && m.Commit == "" {
		dirty := false
		for _, s := range bi.Settings {
			switch {
			case s.Key == "vcs.revision":
				m.Commit = s.Value
			case s.Key == "vcs.modified" && s.Value == "true":
				dirty = true
			}
		}
		if m.Commit != "" && dirty {
			m.Commit += "+dirty"
		}
	}
	if m.Commit == "" {
		if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
			m.Commit = strings.TrimSpace(string(out))
		}
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			m.CPU = strings.TrimSpace(string(out))
		}
	}
	return m
}

// redactArgs hides credential flag values so the JSON can be shared.
func redactArgs(args []string) []string {
	out := slices.Clone(args)
	for i := 0; i < len(out); i++ {
		name, _, hasValue := strings.Cut(strings.TrimLeft(out[i], "-"), "=")
		if name != "token" && name != "password" {
			continue
		}
		if hasValue {
			out[i] = out[i][:strings.Index(out[i], "=")+1] + "[REDACTED]"
		} else if i+1 < len(out) {
			out[i+1] = "[REDACTED]"
			i++
		}
	}
	return out
}

func maxRSSMiB() float64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return float64(ru.Maxrss) / (1 << 20) // bytes
	}
	return float64(ru.Maxrss) / (1 << 10) // KiB
}

func printTable(w io.Writer, r Report) {
	fmt.Fprintf(w, "\nAruma bench — %s, %s driver, %gs measured after %gs warm-up, %s/%s %d CPUs, %s\n\n",
		r.Meta.Mode, r.Plan.Driver, r.Plan.DurationSec, r.Plan.WarmupSec, r.Meta.OS, r.Meta.Arch, r.Meta.NumCPU, r.Meta.GoVersion)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "scenario\tc\tok/s\tok\tfailed\tp50 ms\tp90 ms\tp99 ms\tp99.9 ms\tmax ms\tdb acq/req\tdb wait%\t")
	for _, x := range r.Results {
		acq, wait := "-", "-"
		if x.Pool != nil && x.Requests > 0 {
			acq = fmt.Sprintf("%.1f", x.Pool.AcquiresPerReq)
			wait = fmt.Sprintf("%.0f", x.Pool.WaitedPct)
		}
		l := x.LatencyMs
		fmt.Fprintf(tw, "%s\t%d\t%.0f\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n", x.Scenario, x.Concurrency, x.OKPerSec, x.OK, x.Failed,
			f(l.P50), f(l.P90), f(l.P99), f(l.P999), f(l.Max), acq, wait)
	}
	tw.Flush()
	for _, x := range r.Results {
		if x.Failed > 0 {
			var parts []string
			keys := make([]string, 0, len(x.Statuses))
			for k := range x.Statuses {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				parts = append(parts, k+"×"+fmt.Sprint(x.Statuses[k]))
			}
			fmt.Fprintf(w, "  %s: statuses %s, transport errors %d %s\n", x.Scenario, strings.Join(parts, " "), x.TransportErrs, x.FirstError)
		}
		if x.Unsent > 0 || x.MaxLagMs > 100 {
			fmt.Fprintf(w, "  %s: open loop fell behind (max lag %.0f ms, %d of %d scheduled never sent); latency includes the queueing\n",
				x.Scenario, x.MaxLagMs, x.Unsent, x.Scheduled)
		}
	}
	fmt.Fprintln(w)
}

// f formats milliseconds with precision that suits the magnitude.
func f(v float64) string {
	switch {
	case v < 1:
		return fmt.Sprintf("%.3f", v)
	case v < 100:
		return fmt.Sprintf("%.2f", v)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}
