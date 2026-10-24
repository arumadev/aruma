// Command rules-diff compares the rules engine with the official Firestore emulator, as a black
// box, on conditions and requests generated from a seed (ADR-021 §6).
//
// Each case is one condition, built from the language's grammar (literals, the request's and
// the document's fields, operators, is, ?:, methods, functions, get() and exists()), on one
// method of /c/{doc}, with a random request: signed in or not, the document there or not,
// request.resource on create and update, u/alice there or not for the lookups. The emulator
// decides it over REST, the way the oracle's recorder drives it; the engine decides it through
// rulestest's emulator binding. A case the two decide differently is a disagreement: whether it
// is allowed, or whether it compiles. Each disagreement is shrunk to the smallest condition that
// still disagrees, and printed.
//
//	go run ./tools/rules-diff                       # seed 1, 200 cases, emulator on 8940
//	go run ./tools/rules-diff -seed 7 -n 1000 -depth 5
//	go run ./tools/rules-diff -emulator 127.0.0.1:8940 -out /tmp/diff.json
//
// The tool starts the emulator jar on -port (and its websocket on the port after it) and stops
// it by its pid; -emulator uses one that is already running. It is run by hand, and weekly in
// CI (S7), never by go test. While the engine is the contract's stub it runs the emulator side
// alone and says so.
//
// The seed fixes the cases; the emulator's clock (request.time) is the only other input.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/platform/rules/rulestest"
)

type config struct {
	seed   uint64
	n      int
	depth  int
	port   int
	addr   string
	jar    string
	java   string
	shrink int
	out    string
	show   int
}

func main() {
	home, _ := os.UserHomeDir()
	var cfg config
	flag.Uint64Var(&cfg.seed, "seed", 1, "the seed of the generated cases")
	flag.IntVar(&cfg.n, "n", 200, "how many cases")
	flag.IntVar(&cfg.depth, "depth", 4, "the deepest nesting of a generated condition")
	flag.IntVar(&cfg.port, "port", 8940, "the emulator's port; its websocket takes the next one")
	flag.StringVar(&cfg.addr, "emulator", "", "host:port of an emulator already running (the tool then starts none)")
	flag.StringVar(&cfg.jar, "jar", filepath.Join(home, ".cache/firebase/emulators/cloud-firestore-emulator-v1.22.0.jar"), "the Firestore emulator jar")
	flag.StringVar(&cfg.java, "java", "/opt/homebrew/opt/openjdk@21/bin/java", "the java binary (Java 21)")
	flag.IntVar(&cfg.shrink, "shrink", 80, "the most attempts to shrink one disagreement")
	flag.StringVar(&cfg.out, "out", "", "write every case and both verdicts to this JSON file")
	flag.IntVar(&cfg.show, "show", 20, "the most disagreements to print")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "rules-diff:", err)
		os.Exit(1)
	}
}

// result is one case and what each side decided.
type result struct {
	*genCase
	Emulator verdict  `json:"emulator"`
	Engine   *verdict `json:"engine,omitempty"`
	Differs  bool     `json:"differs"`
	Shrunk   string   `json:"shrunk,omitempty"`
}

func run(ctx context.Context, cfg config) (err error) {
	var emu *emulator
	if cfg.addr != "" {
		emu = connectEmulator(cfg.addr)
	} else {
		logDir, err := os.MkdirTemp("", "rules-diff-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(logDir)
		fmt.Printf("starting the Firestore emulator on 127.0.0.1:%d\n", cfg.port)
		if emu, err = startEmulator(cfg.java, cfg.jar, cfg.port, logDir); err != nil {
			return err
		}
		defer emu.stop()
	}

	eng, err := newEngine()
	if err != nil {
		return err
	}
	if eng.stub != "" {
		fmt.Printf("the engine is the contract's stub (%q): running the emulator side alone\n", eng.stub)
	}
	g := newGen(cfg.seed)
	var results []*result
	counts := map[string]int{}
	differs := 0
	start := time.Now()
	for i := range cfg.n {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c := g.next(i, cfg.depth)
		r := &result{genCase: c}
		if r.Emulator, err = emu.run(ctx, c, ruleset(c.Method, c.Condition)); err != nil {
			return fmt.Errorf("case %d: %w", i, err)
		}
		counts["emulator "+r.Emulator.Outcome]++
		if eng.stub == "" && r.Emulator.Outcome != "skip" {
			v := eng.decide(ctx, c, c.Condition)
			r.Engine = &v
			counts["engine "+v.Outcome]++
			if disagree(r.Emulator, v) {
				r.Differs = true
				differs++
				r.Shrunk, err = shrink(c.tree, cfg.shrink, stillDiffers(ctx, emu, eng, c))
				if err != nil {
					return err
				}
			}
		}
		results = append(results, r)
	}

	fmt.Printf("seed %d: %d cases in %v\n", cfg.seed, len(results), time.Since(start).Round(time.Millisecond))
	for _, k := range sortedCounts(counts) {
		fmt.Printf("  %-22s %d\n", k, counts[k])
	}
	if eng.stub == "" {
		fmt.Printf("%d disagreements\n", differs)
		shown := 0
		for _, r := range results {
			if !r.Differs {
				continue
			}
			if shown == cfg.show {
				fmt.Printf("… and %d more\n", differs-shown)
				break
			}
			shown++
			printDisagreement(r)
		}
	}
	if cfg.out != "" {
		b, err := json.MarshalIndent(map[string]any{"seed": cfg.seed, "depth": cfg.depth, "results": results}, "", " ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(cfg.out, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	if differs > 0 {
		return fmt.Errorf("%d of %d cases disagree (seed %d)", differs, len(results), cfg.seed)
	}
	return nil
}

func sortedCounts(m map[string]int) []string { return slices.Sorted(maps.Keys(m)) }

func printDisagreement(r *result) {
	auth := "signed out"
	if r.Auth {
		auth = "alice"
	}
	existing := "absent"
	if r.Existing != nil {
		existing = "present"
	}
	fmt.Printf("\ncase %d: %s, %s, document %s, u/alice %v\n", r.Index, r.Method, auth, existing, r.Alice)
	fmt.Printf("  condition: %s\n", r.Condition)
	if r.Shrunk != "" && r.Shrunk != r.Condition {
		fmt.Printf("  shrunk:    %s\n", r.Shrunk)
	}
	fmt.Printf("  emulator:  %s %s\n", r.Emulator.Outcome, oneLine(r.Emulator.Message))
	fmt.Printf("  engine:    %s %s\n", r.Engine.Outcome, oneLine(r.Engine.Message))
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " | ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// disagree reports whether two verdicts differ in what counts: compiling, and allowing.
// An engine that panicked or failed (outage) disagrees whatever the emulator decided: a crash on
// a file the emulator rejects is exactly what the tool is for.
func disagree(emu, eng verdict) bool {
	if eng.Outcome == "panic" || eng.Outcome == "outage" {
		return true
	}
	return emu.Compiled != eng.Compiled || emu.Allow != eng.Allow
}

// shrink returns the smallest condition, from tree, on which differs still holds, trying at
// most limit candidates.
func shrink(tree *node, limit int, differs func(condition string) (bool, error)) (string, error) {
	best := tree
	tries := 0
	for improved := true; improved && tries < limit; {
		improved = false
		for _, cand := range shrinks(best) {
			if tries == limit {
				break
			}
			if cand.size() >= best.size() {
				continue
			}
			tries++
			d, err := differs(cand.String())
			if err != nil {
				return "", err
			}
			if d {
				best, improved = cand, true
				break
			}
		}
	}
	return best.String(), nil
}

// stillDiffers says whether the emulator and the engine disagree on the case with another
// condition.
func stillDiffers(ctx context.Context, emu *emulator, eng *engine, c *genCase) func(string) (bool, error) {
	return func(cond string) (bool, error) {
		ev, err := emu.run(ctx, c, ruleset(c.Method, cond))
		if err != nil {
			return false, err
		}
		return ev.Outcome != "skip" && disagree(ev, eng.decide(ctx, c, cond)), nil
	}
}

// engine decides cases through the public contract and rulestest's emulator binding.
type engine struct {
	runner *rulestest.Runner
	stub   string
}

// newEngine returns the engine, with stub set while Compile is the contract's stub. A Compile
// that panics otherwise on a plain rules file is an error, not a stub.
func newEngine() (*engine, error) {
	e := &engine{runner: rulestest.NewRunner()}
	var err error
	e.stub, err = e.runner.Probe()
	return e, err
}

// oracleCase is the generated case in the oracle's shape, for the emulator binding.
func oracleCase(c *genCase, condition string) *rulestest.Case {
	oc := &rulestest.Case{
		ID:         fmt.Sprintf("rules-diff/%d", c.Index),
		Service:    rules.Firestore,
		Source:     "rules-diff",
		Provenance: rulestest.Emulator,
		Expression: condition,
		Ruleset:    ruleset(c.Method, condition),
		Request:    &rulestest.Request{Method: c.Method, Path: "/databases/(default)/documents/c/d"},
	}
	if c.Auth {
		oc.Request.Auth = rulestest.FieldOf(map[string]any{"uid": "alice", "token": aliceToken})
	}
	if c.Incoming != nil {
		oc.Request.Resource = rulestest.FieldOf(map[string]any{"data": c.Incoming})
	}
	if c.Existing != nil {
		oc.Resource = rulestest.FieldOf(map[string]any{"data": c.Existing})
	}
	alice := rulestest.Mock{Function: "get", Path: "/databases/(default)/documents/u/alice", Result: rulestest.FieldOf(nil)}
	if c.Alice {
		alice.Result = rulestest.FieldOf(map[string]any{"data": map[string]any{"role": "editor"}})
	}
	oc.Mocks = []rulestest.Mock{alice}
	return oc
}

func (e *engine) decide(ctx context.Context, c *genCase, condition string) verdict {
	res := e.runner.Run(ctx, oracleCase(c, condition))
	g := res.Got
	switch {
	case g.Panic != "":
		return verdict{Outcome: "panic", Message: g.Panic}
	case !g.Compiled:
		first, _ := g.FirstError()
		return verdict{Outcome: "compile-error", Message: first.String()}
	case g.Err != nil:
		return verdict{Compiled: true, Outcome: "outage", Message: g.Err.Error()}
	}
	v := verdict{Compiled: true, Allow: g.Decision.Allow, Outcome: string(g.Outcome)}
	for _, s := range g.Decision.Statements {
		if s.Message != "" {
			v.Message = s.Message
		}
	}
	return v
}
