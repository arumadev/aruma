package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
)

// report is one run's outcome.
type report struct {
	Seed     int64  `json:"seed"`
	Clients  int    `json:"clients"`
	Database string `json:"database"`
	Kills    int    `json:"kills"`
	Terms    int    `json:"terms"`
	Elapsed  string `json:"elapsed"`
	stats
	// Sessions counts the connections; OnDisconnectRan those whose onDisconnect writes ran,
	// OnDisconnectLost those that ended with a SIGKILL, which loses them.
	Sessions         int   `json:"sessions"`
	OnDisconnectRan  int   `json:"onDisconnectRan"`
	OnDisconnectLost int   `json:"onDisconnectLostToKill"`
	SharedIncrements int   `json:"sharedIncrements"`
	Transactions     int   `json:"sharedTransactions"`
	Seq              int64 `json:"seq"`
	ExpectedSeq      int64 `json:"expectedSeq"`
	// Violations are the failed checks; none is a pass.
	Violations []string `json:"violations"`
}

func (rep *report) print(w io.Writer) {
	verdict := "PASS"
	if len(rep.Violations) > 0 {
		verdict = fmt.Sprintf("FAIL (%d violations)", len(rep.Violations))
	}
	fmt.Fprintf(w, "seed %d: %s in %s — %d clients, %d SIGKILL + %d SIGTERM; %d writes acked (%d over REST); "+
		"%d writes in flight at a stop, %d of them applied, %d settled by shared state; %d datastale retries; "+
		"%d sessions, onDisconnect ran for %d, lost to SIGKILL for %d; seq %d (expected %d)\n",
		rep.Seed, verdict, rep.Elapsed, rep.Clients, rep.Kills, rep.Terms, rep.Writes, rep.REST,
		rep.InFlight, rep.InFlightLanded, rep.Ambiguous, rep.Stale,
		rep.Sessions, rep.OnDisconnectRan, rep.OnDisconnectLost, rep.Seq, rep.ExpectedSeq)
	for _, v := range rep.Violations {
		fmt.Fprintf(w, "  - %s\n", v)
	}
}

// check reads the whole database back and compares it with the ground truth.
func (r *runner) check(ctx context.Context, workers []*worker) {
	rep := r.report
	root, err := r.restGet(ctx, "/")
	if err != nil {
		r.violation("final read: %v", err)
		return
	}
	top, _ := root.(map[string]any)
	for k := range top {
		if k != "c" && k != "shared" && k != "presence" {
			r.violation("unexpected top-level key %q: %s", k, short(top[k]))
		}
	}

	// Each client's subtree is exactly what its acknowledged writes, and the in-flight ones
	// that landed, make it.
	cs, _ := getAt(root, []string{"c"}).(map[string]any)
	known := map[string]bool{}
	var applied int64
	for _, w := range workers {
		rep.stats.add(w.st)
		known[w.key] = true
		applied += w.writesApplied
		if len(w.pending) > 0 {
			r.violation("%s: %d writes still unsettled", w.key, len(w.pending))
		}
		if got := cs[w.key]; !equal(w.model, got) {
			r.violation("%s: subtree differs from its acknowledged writes: %s", w.key, diff(w.model, got))
		}
	}
	for k := range cs {
		if !known[k] {
			r.violation("unexpected subtree /c/%s", k)
		}
	}

	// Increments: each ran exactly once.
	var inc, plainA, plainU int
	for _, w := range workers {
		inc += w.sharedInc
		plainA += w.plainApplied
		plainU += w.plainUnknown
	}
	rep.SharedIncrements = inc
	if got := num(getAt(root, []string{"shared", "inc"})); got != float64(inc) {
		r.violation("/shared/inc is %v, but %d increments ran (each with its receipt)", got, inc)
	}
	plain := num(getAt(root, []string{"shared", "plain"}))
	if plain < float64(plainA) || plain > float64(plainA+plainU) {
		r.violation("/shared/plain is %v, but %d increments were acknowledged and %d more were in flight", plain, plainA, plainU)
	}
	extra := int64(plain) - int64(plainA)

	// Transactions: every acknowledged one is there, once; in-flight ones may be.
	tx, _ := getAt(root, []string{"shared", "tx"}).(map[string]any)
	present := map[string]bool{}
	for b, v := range tx {
		m, _ := v.(map[string]any)
		for k := range m {
			present[b+"/"+k] = true
		}
	}
	for _, w := range workers {
		for k := range w.txApplied {
			if !present[k] {
				r.violation("acknowledged transaction %s is missing", k)
			}
			delete(present, k)
		}
		for k := range w.txUnknown {
			if present[k] {
				extra++
				delete(present, k)
			}
		}
		rep.Transactions += len(w.txApplied)
	}
	for _, k := range slices.Sorted(maps.Keys(present)) {
		r.violation("transaction %s is there but was never sent", k)
	}

	// onDisconnect: a session's writes run once it ends normally or the server stops with
	// SIGTERM, never after a SIGKILL, never when cancelled; nothing else is under /presence.
	pres, _ := getAt(root, []string{"presence"}).(map[string]any)
	for _, w := range workers {
		mine, _ := pres[w.key].(map[string]any)
		seen := map[string]bool{}
		for _, s := range w.sessions {
			rep.Sessions++
			ran := false
			for k, want := range s.entries {
				got, ok := mine[k]
				seen[k] = true
				if ok {
					ran = true
					if !equal(want, got) {
						r.violation("%s: presence %s is %s, registered %s", w.key, k, short(got), short(want))
					}
				}
			}
			if _, ok := mine[s.cancelled]; ok {
				ran = true
				if s.cancelAcked {
					r.violation("%s: onDisconnect write %s ran although it was cancelled", w.key, s.cancelled)
				}
			}
			seen[s.cancelled] = true
			must := s.ended == "term" || (s.ended == "close" && (s.confirmed || s.waitedFailed))
			switch {
			case s.ended == "kill" && ran:
				r.violation("%s: onDisconnect writes of session %s ran although the server was killed", w.key, s.key)
			case s.ended == "kill":
				rep.OnDisconnectLost++
			case must:
				for k := range s.entries {
					if _, ok := mine[k]; s.acked[k] && !ok {
						r.violation("%s: onDisconnect write %s of session %s (ended by %s) did not run", w.key, k, s.key, s.ended)
					}
				}
			case s.ended == "":
				r.violation("%s: session %s never ended", w.key, s.key)
			}
			if ran {
				rep.OnDisconnectRan++
				extra++ // the registered writes run as one write
			}
		}
		for k := range mine {
			if !seen[k] {
				r.violation("%s: presence %s was never registered", w.key, k)
			}
		}
	}
	for k := range pres {
		if !known[k] {
			r.violation("unexpected /presence/%s", k)
		}
	}

	// seq counts the writes that ran, one per write however many paths it changed.
	rep.ExpectedSeq = applied + extra
	seq, err := r.srv.Seq(ctx, r.ns)
	if err != nil {
		r.violation("read seq: %v", err)
		return
	}
	rep.Seq = seq
	if seq != rep.ExpectedSeq {
		r.violation("seq is %d, but %d writes ran (%d known applied, %d settled from shared state)", seq, rep.ExpectedSeq, applied, extra)
	}
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}
