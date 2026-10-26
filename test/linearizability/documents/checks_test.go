package documents

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anishathalye/porcupine"
)

// checkCommitOrder: the commits that took a seq have distinct seqs, and their commit_times rise
// strictly with it (ADR-024 §2.1), across every group of the database.
func checkCommitOrder(t *testing.T, ops []porcupine.Operation) {
	var stamped []*output
	for _, op := range ops {
		if out := op.Output.(*output); out.Outcome == outOK && out.Seq != 0 {
			stamped = append(stamped, out)
		}
	}
	slices.SortFunc(stamped, func(a, b *output) int { return cmp.Compare(a.Seq, b.Seq) })
	for i := 1; i < len(stamped); i++ {
		a, b := stamped[i-1], stamped[i]
		if a.Seq == b.Seq {
			t.Errorf("two commits took seq %d (commit_times %d and %d)", a.Seq, a.CommitTime, b.CommitTime)
		}
		if b.CommitTime <= a.CommitTime {
			t.Errorf("commit_time does not rise with seq: seq %d at %d, seq %d at %d", a.Seq, a.CommitTime, b.Seq, b.CommitTime)
		}
	}
	t.Logf("%d commits took a seq", len(stamped))
}

// timed is a time an operation saw, for the real-time checks.
type timed struct {
	call, ret int64
	t         int64
	strict    bool // a later observation must be above t, not merely at or above it
	what      string
}

// realTimeOrder checks that an observation made after another returned is not below it (above
// it, when it is strict).
func realTimeOrder(t *testing.T, what string, events []timed) {
	byRet := slices.Clone(events)
	slices.SortFunc(byRet, func(a, b timed) int { return cmp.Compare(a.ret, b.ret) })
	byCall := slices.Clone(events)
	slices.SortFunc(byCall, func(a, b timed) int { return cmp.Compare(a.call, b.call) })
	var best timed
	j, failures := 0, 0
	for _, b := range byCall {
		for ; j < len(byRet) && byRet[j].ret < b.call; j++ {
			if byRet[j].t > best.t {
				best = byRet[j]
			}
		}
		if b.t < best.t || b.strict && b.t == best.t {
			if failures++; failures <= 10 {
				t.Errorf("%s: %s (called at %d) saw %d, after %s (returned at %d) saw %d",
					what, b.what, b.call, b.t, best.what, best.ret, best.t)
			}
		}
	}
}

// checkRealTime: the database's time never goes back in real time. A commit that took a seq is
// above every commit_time and read_time seen before it was called; a read's read_time, a
// read-only transaction's snapshot and a commit that changed nothing are at or above them. A
// read at a given read_time is not a clock, nor (as a later observation) is a commit that changed
// nothing: its commit_time is the clock's, which may pass the last commit's.
func checkRealTime(t *testing.T, ops []porcupine.Operation) {
	var events []timed
	for _, op := range ops {
		in, out := op.Input.(*input), op.Output.(*output)
		if out.Outcome != outOK {
			continue
		}
		e := timed{call: op.Call, ret: op.Return, what: describe(in, out)}
		switch {
		case in.Kind == opCommit || in.Kind == opTxn:
			e.t, e.strict = out.CommitTime, out.Seq != 0
			if !e.strict {
				e.ret = 1 << 62 // never a lower bound of a later one
			}
		case in.Kind == opRead || in.Kind == opSnapshot:
			e.t = out.ReadTime
		default:
			continue
		}
		if e.t != 0 {
			events = append(events, e)
		}
	}
	realTimeOrder(t, "the commit order in real time", events)
}

// checkUpdateTimes: a document's update_time never moves back in real time, as reads of the
// latest, transactions' reads and commits' results see it.
func checkUpdateTimes(t *testing.T, ops []porcupine.Operation) {
	byKey := map[[2]int][]timed{}
	for _, op := range ops {
		in, out := op.Input.(*input), op.Output.(*output)
		if out.Outcome != outOK || in.Kind == opReadAt || in.Kind == opSnapshot {
			continue
		}
		what := describe(in, out)
		for _, o := range out.Reads {
			if o.Exists {
				k := [2]int{in.Group, o.Key}
				byKey[k] = append(byKey[k], timed{call: op.Call, ret: op.Return, t: o.UT, what: what})
			}
		}
		for i, w := range in.Writes {
			if i < len(out.UpdateTimes) && out.UpdateTimes[i] != 0 {
				k := [2]int{in.Group, w.Key}
				byKey[k] = append(byKey[k], timed{call: op.Call, ret: op.Return, t: out.UpdateTimes[i], what: what})
			}
		}
	}
	for k, events := range byKey {
		realTimeOrder(t, fmt.Sprintf("update_time of lin%d/k%d", k[0], k[1]), events)
	}
}

// checkLinearizable checks each group's history with Porcupine. The first illegal one is
// minimized and saved, with Porcupine's visualization, under the test's artifact directory (go
// test -artifacts keeps it); copy the JSON to testdata/ to keep it as a fixture.
func checkLinearizable(t *testing.T, ops []porcupine.Operation, timeout time.Duration) {
	m := model.ToModel()
	parts := m.Partition(ops)
	results := make([]porcupine.CheckResult, len(parts))
	var wg sync.WaitGroup
	for i, part := range parts {
		wg.Go(func() {
			start := time.Now()
			results[i] = porcupine.CheckOperationsTimeout(m, part, timeout)
			t.Logf("porcupine: group %d, %d operations: %s in %v", part[0].Input.(*input).Group, len(part), results[i],
				time.Since(start).Round(time.Millisecond))
		})
	}
	wg.Wait()
	minimized := false
	for i, part := range parts {
		group, res := part[0].Input.(*input).Group, results[i]
		switch {
		case res == porcupine.Ok:
			continue
		case res == porcupine.Unknown:
			t.Errorf("group %d: porcupine did not finish within %v", group, timeout)
			continue
		case minimized:
			t.Errorf("group %d: not linearizable", group)
			continue
		}
		minimized = true
		small := minimize(m, part, time.Now().Add(minimizeDeadline))
		dir := t.ArtifactDir()
		name := filepath.Join(dir, fmt.Sprintf("violation-group%d.json", group))
		b, _ := json.MarshalIndent(fixture{Note: "found by TestLinearizability", Want: string(porcupine.Illegal), Ops: toFixture(small)}, "", "  ")
		if err := os.WriteFile(name, b, 0o644); err != nil {
			t.Log(err)
		}
		if len(small) <= 2000 {
			_, info := porcupine.CheckOperationsVerbose(m, small, timeout)
			_ = porcupine.VisualizePath(m, info, strings.TrimSuffix(name, ".json")+".html")
		}
		var lines []string
		for _, op := range small[max(0, len(small)-60):] {
			lines = append(lines, fmt.Sprintf("  [%d, %d] c%d %s", op.Call, op.Return, op.ClientId, describe(op.Input.(*input), op.Output.(*output))))
		}
		t.Errorf("group %d is not linearizable: %d operations, minimized to %d, saved to %s; the last of them:\n%s",
			group, len(part), len(small), name, strings.Join(lines, "\n"))
	}
}

// minimize shrinks an illegal history to a smaller one that is still illegal, soundly, so that
// what it returns is a violation as much as the history it was given:
//
//   - it cuts the history at the earliest time X at which it is already illegal: the operations
//     called before X stay, those that returned before it as they were; of those still running
//     at X, a write becomes of unknown outcome (with no reads) and an observation goes. A
//     linearization of the whole history gives one of the cut history;
//   - it drops operations that only observe (reads, refused commits, transactions that wrote
//     nothing);
//   - it weakens the last commits to an unknown outcome (they may or may not have happened, at
//     the same times), which can only make a history more legal.
//
// It never drops a write, whose absence would make any read of its value illegal. It is best
// effort: it stops at deadline with what it has.
func minimize(m porcupine.Model, ops []porcupine.Operation, deadline time.Time) []porcupine.Operation {
	ops = slices.Clone(ops)
	slices.SortFunc(ops, sortOps)
	illegal := func(h []porcupine.Operation) bool {
		r := porcupine.CheckOperationsTimeout(m, h, min(time.Until(deadline), 20*time.Second))
		return r == porcupine.Illegal
	}
	observes := func(op porcupine.Operation) bool {
		in, out := op.Input.(*input), op.Output.(*output)
		return in.Kind == opRead || in.Kind == opReadAt || in.Kind == opSnapshot || out.Outcome == outRefused ||
			len(in.Writes) == 0
	}
	cut := func(x int64) []porcupine.Operation {
		var h []porcupine.Operation
		for _, op := range ops {
			switch {
			case op.Call >= x:
			case op.Return < x:
				h = append(h, op)
			case !observes(op):
				op.Output = &output{Outcome: outUnknown}
				h = append(h, op)
			}
		}
		return h
	}
	rets := make([]int64, len(ops))
	for i, op := range ops {
		rets[i] = op.Return + 1
	}
	slices.Sort(rets)
	// The smallest cut that is illegal; the whole history is.
	lo, hi := 0, len(rets)-1
	for lo < hi && time.Now().Before(deadline) {
		mid := (lo + hi) / 2
		if illegal(cut(rets[mid])) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	if h := cut(rets[hi]); illegal(h) {
		ops = h
	}
	// Drop observations, in chunks halving down to one, from the end.
	for n := max(1, len(ops)/2); time.Now().Before(deadline); n /= 2 {
		for end := len(ops); end > 0 && time.Now().Before(deadline); {
			i := max(0, end-n)
			if !slices.ContainsFunc(ops[i:end], observes) {
				end = i
				continue
			}
			try := slices.Concat(ops[:i], slices.DeleteFunc(slices.Clone(ops[i:end]), observes), ops[end:])
			if illegal(try) {
				ops = try
			}
			end = i
		}
		if n == 1 {
			break
		}
	}
	// Weaken the last commits, one at a time.
	for i := len(ops) - 1; i >= max(0, len(ops)-200) && time.Now().Before(deadline); i-- {
		in, out := ops[i].Input.(*input), ops[i].Output.(*output)
		if (in.Kind != opCommit && in.Kind != opTxn) || out.Outcome != outOK {
			continue
		}
		try := slices.Clone(ops)
		try[i].Output = &output{Outcome: outUnknown, Reads: out.Reads}
		if illegal(try) {
			ops = try
		}
	}
	return ops
}

// fixture is a history saved as JSON: Want is porcupine's verdict, "Ok" or "Illegal".
type fixture struct {
	Note string      `json:"note"`
	Want string      `json:"want"`
	Ops  []fixtureOp `json:"ops"`
}

type fixtureOp struct {
	Client int     `json:"client"`
	Call   int64   `json:"call"`
	Return int64   `json:"return"`
	In     *input  `json:"in"`
	Out    *output `json:"out"`
}

func toFixture(ops []porcupine.Operation) []fixtureOp {
	out := make([]fixtureOp, len(ops))
	for i, op := range ops {
		out[i] = fixtureOp{Client: op.ClientId, Call: op.Call, Return: op.Return, In: op.Input.(*input), Out: op.Output.(*output)}
	}
	return out
}

func fromFixture(ops []fixtureOp) []porcupine.Operation {
	out := make([]porcupine.Operation, len(ops))
	for i, op := range ops {
		out[i] = porcupine.Operation{ClientId: op.Client, Call: op.Call, Return: op.Return, Input: op.In, Output: op.Out}
	}
	return out
}
