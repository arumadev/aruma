// Package documents checks the Document Database's core for linearizability under contention
// (plan v0.4 F2 exit: "contention tests with Porcupine"). Many clients drive core.PGStore directly
// over a few small groups of documents with plain commits, reads, read_time reads and the three
// kinds of transaction; every operation is recorded with its invocation and response times, and
// Porcupine checks each group's history against a sequential model (this file). Cross-group
// properties the model cannot see (the commit order, read times, update times in real time) are
// checked directly (checks_test.go).
//
// The model: each document of a group is a register {exists, n, update_time}, where n is the
// document's one integer field. A commit or a read-write transaction is one atomic step at a
// single point between its call and its return: a transaction's reads must equal the state at
// that point, then its writes apply. The state also carries the group's last commit_time and the
// highest read_time served, so that commit times are strictly increasing in linearization order
// and a read at T sees exactly the commits at or before T. An operation whose outcome is unknown
// (its context ended during the commit, or core answered ErrUnknownOutcome) may or may not have
// happened: the model is nondeterministic and keeps both states, and a document it may have
// written gets an unknown update_time that the first read of it binds.
package documents

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/anishathalye/porcupine"
)

// keysPerGroup is the documents of one group: few, so that clients contend.
const keysPerGroup = 3

// unknownTime is a document's update_time, or a commit's commit_time, that is not known: written
// by an operation whose outcome is unknown.
const unknownTime = -1

type writeKind string

const (
	wSet    writeKind = "set"    // the document's fields become {n: N}
	wDelete writeKind = "delete" // deleting a missing document changes nothing
	wIncr   writeKind = "incr"   // n += N with the increment transform (a missing document: n = N)
	wVerify writeKind = "verify" // Precondition only
)

// precondition is a write's current_document: Exists "" (none), "exists" or "missing";
// UpdateTime in microseconds, 0 for none.
type precondition struct {
	Exists     string `json:"exists,omitempty"`
	UpdateTime int64  `json:"updateTime,omitzero"`
}

type write struct {
	Key  int          `json:"key"`
	Kind writeKind    `json:"kind"`
	N    int64        `json:"n,omitzero"`
	Pre  precondition `json:"pre,omitzero"`
}

// observation is a document as a read returned it; UT is its update_time in microseconds, 0 when
// it does not exist.
type observation struct {
	Key    int   `json:"key"`
	Exists bool  `json:"exists"`
	N      int64 `json:"n,omitzero"`
	UT     int64 `json:"ut,omitzero"`
}

type opKind string

const (
	opCommit   opKind = "commit"   // Commit outside a transaction
	opTxn      opKind = "txn"      // a read-write transaction: its reads, then CommitTransaction
	opRead     opKind = "read"     // Get or BatchGet of the latest
	opReadAt   opKind = "readAt"   // Get or BatchGet at a read_time
	opSnapshot opKind = "snapshot" // a read-only transaction's reads, at its one snapshot
)

// input is what a client asked: the operation's kind, group, writes, and the read time of a
// read at one. Mode names the transaction's model, for the reader of a history.
type input struct {
	Kind   opKind  `json:"kind"`
	Group  int     `json:"group"`
	Mode   string  `json:"mode,omitempty"`
	Writes []write `json:"writes,omitempty"`
	At     int64   `json:"at,omitzero"`
}

type outcome string

const (
	outOK      outcome = "ok"
	outRefused outcome = "refused" // a precondition failed: Code is its code
	outUnknown outcome = "unknown" // may or may not have happened
)

// output is what the client saw. Reads are a transaction's or a read's observations; ReadTime is
// the read_time the reads answered at (0: none returned, e.g. a Get of a missing document).
// CommitTime, Seq, UpdateTimes and Incr (each write's increment result, else 0) are a commit's.
type output struct {
	Outcome     outcome       `json:"outcome"`
	Code        int           `json:"code,omitzero"`
	Reads       []observation `json:"reads,omitempty"`
	ReadTime    int64         `json:"readTime,omitzero"`
	CommitTime  int64         `json:"commitTime,omitzero"`
	Seq         int64         `json:"seq,omitzero"`
	UpdateTimes []int64       `json:"updateTimes,omitempty"`
	Incr        []int64       `json:"incr,omitempty"`
}

// Codes of core.Code that a refused precondition answers with.
const (
	codeNotFound           = 5
	codeAlreadyExists      = 6
	codeFailedPrecondition = 9
)

type docState struct {
	Exists bool
	N      int64
	UT     int64 // 0 when missing; unknownTime when written by an operation of unknown outcome
}

// state is a group's documents, the last commit_time applied to it, and the highest read_time a
// read of it was served at: a commit that changes something later must be above both.
type state struct {
	Docs   [keysPerGroup]docState
	LastCT int64
	Floor  int64
}

// model is the porcupine model of one group's history.
var model = porcupine.NondeterministicModel{
	Partition: func(history []porcupine.Operation) [][]porcupine.Operation {
		groups := map[int][]porcupine.Operation{}
		for _, op := range history {
			g := op.Input.(*input).Group
			groups[g] = append(groups[g], op)
		}
		out := make([][]porcupine.Operation, 0, len(groups))
		for _, ops := range groups {
			out = append(out, ops)
		}
		return out
	},
	Init: func() []any { return []any{state{}} },
	Step: func(s, in, out any) []any {
		next := step(s.(state), in.(*input), out.(*output))
		r := make([]any, len(next))
		for i, n := range next {
			r[i] = n
		}
		return r
	},
	Equal: func(a, b any) bool { return a.(state) == b.(state) },
	Hash: func(s any) uint64 {
		st := s.(state)
		h := fnv.New64a()
		fmt.Fprint(h, st)
		return h.Sum64()
	},
	DescribeOperation: func(in, out any) string { return describe(in.(*input), out.(*output)) },
	DescribeState: func(s any) string {
		st := s.(state)
		var b strings.Builder
		for i, d := range st.Docs {
			if d.Exists {
				fmt.Fprintf(&b, "k%d=%d@%d ", i, d.N, d.UT)
			} else {
				fmt.Fprintf(&b, "k%d=∅ ", i)
			}
		}
		fmt.Fprintf(&b, "last=%d floor=%d", st.LastCT, st.Floor)
		return b.String()
	},
}

// step returns the states the operation may leave s in; none when it cannot happen in s.
func step(s state, in *input, out *output) []state {
	switch in.Kind {
	case opRead:
		return readStep(s, out.Reads, out.ReadTime)
	case opReadAt:
		if out.ReadTime != 0 && out.ReadTime != in.At {
			return nil
		}
		return readStep(s, out.Reads, in.At)
	case opSnapshot:
		return readStep(s, out.Reads, out.ReadTime)
	case opCommit, opTxn:
		return commitStep(s, in, out)
	}
	return nil
}

// readStep: reads at t (0: unknown) see the state as of t, which must be the latest commit at or
// before t; no later commit of the group may be at or before t.
func readStep(s state, reads []observation, t int64) []state {
	if t != 0 && t < s.LastCT {
		return nil
	}
	s, ok := observe(s, reads)
	if !ok {
		return nil
	}
	if t != 0 && t < s.LastCT {
		return nil // a bound update_time is later than the read time
	}
	s.Floor = max(s.Floor, t)
	return []state{s}
}

// observe matches observations against s, binding unknown update times.
func observe(s state, reads []observation) (state, bool) {
	for _, o := range reads {
		d := &s.Docs[o.Key]
		if d.Exists != o.Exists || d.N != o.N {
			return s, false
		}
		switch {
		case !d.Exists:
		case d.UT == unknownTime:
			d.UT = o.UT
			s.LastCT = max(s.LastCT, o.UT)
		case d.UT != o.UT:
			return s, false
		}
	}
	return s, true
}

// commitStep is a commit, or a read-write transaction's reads then commit, at one point.
func commitStep(s state, in *input, out *output) []state {
	switch out.Outcome {
	case outRefused:
		if refusedBy(s, in.Writes, out.Code) {
			return []state{s}
		}
		return nil
	case outUnknown:
		// It did not happen; or it happened at one point with a commit_time we never learnt.
		states := []state{s}
		if a, ok := observe(s, out.Reads); ok {
			if a, ok := apply(a, in.Writes, nil, unknownTime); ok {
				states = append(states, a)
			}
		}
		return states
	}
	a, ok := observe(s, out.Reads)
	if !ok {
		return nil
	}
	a, ok = apply(a, in.Writes, out, out.CommitTime)
	if !ok {
		return nil
	}
	return []state{a}
}

// holds evaluates a precondition: true, false, or unknown when it names an update_time and the
// document's is unknown.
func holds(d docState, p precondition) (ok, known bool, code int) {
	switch {
	case p.Exists == "exists" && !d.Exists:
		return false, true, codeNotFound
	case p.Exists == "missing" && d.Exists:
		return false, true, codeAlreadyExists
	}
	if p.UpdateTime != 0 {
		switch {
		case !d.Exists:
			return false, true, codeFailedPrecondition
		case d.UT == unknownTime:
			return false, false, codeFailedPrecondition
		case d.UT != p.UpdateTime:
			return false, true, codeFailedPrecondition
		}
	}
	return true, true, 0
}

// refusedBy reports whether the writes may be refused in s with code: the first precondition
// that fails, in order, fails the commit. A commit writes each document once, so every
// precondition is evaluated against s.
func refusedBy(s state, writes []write, code int) bool {
	for _, w := range writes {
		ok, known, c := holds(s.Docs[w.Key], w.Pre)
		switch {
		case ok:
			continue
		case c == code:
			return true
		case known:
			return false
		}
		// Unknown and of another code: it may have held.
	}
	return false
}

// apply applies the writes to s at commit time ct (unknownTime: unknown), checking the results
// in out when it is set: the commit's seq (0 exactly when nothing changed), each write's
// update_time and increment result. A commit that changes something must be above the group's
// last commit and every read time served; one that changes nothing is at or after the last.
func apply(s state, writes []write, out *output, ct int64) (state, bool) {
	changed := false
	for i, w := range writes {
		d := &s.Docs[w.Key]
		ok, known, _ := holds(*d, w.Pre)
		switch {
		case !ok && known:
			return s, false
		case !ok:
			d.UT = w.Pre.UpdateTime // it held: bind
			s.LastCT = max(s.LastCT, d.UT)
		}
		var ut, incr int64
		switch w.Kind {
		case wSet:
			if !d.Exists || d.N != w.N {
				*d = docState{Exists: true, N: w.N, UT: ct}
				changed = true
			}
			ut = d.UT
		case wIncr:
			n := w.N
			if d.Exists {
				n += d.N
			}
			*d = docState{Exists: true, N: n, UT: ct}
			changed = true
			ut, incr = ct, n
		case wDelete:
			if d.Exists {
				*d = docState{}
				changed = true
			}
		case wVerify:
			ut = d.UT
		}
		if out == nil {
			continue
		}
		if i < len(out.Incr) && out.Incr[i] != incr {
			return s, false
		}
		if i < len(out.UpdateTimes) {
			got := out.UpdateTimes[i]
			switch {
			case ut == unknownTime && got != 0:
				d.UT = got // a verify or an unchanged set of a document of unknown update_time
				s.LastCT = max(s.LastCT, got)
			case ut != got:
				return s, false
			}
		}
	}
	switch {
	case ct == unknownTime:
	case changed:
		if out != nil && out.Seq == 0 || ct <= max(s.LastCT, s.Floor) {
			return s, false
		}
		s.LastCT = ct
	default:
		if out != nil && out.Seq != 0 || ct < s.LastCT {
			return s, false
		}
	}
	return s, true
}

func describe(in *input, out *output) string {
	var b strings.Builder
	b.WriteString(string(in.Kind))
	if in.Mode != "" {
		b.WriteString("[" + in.Mode + "]")
	}
	if in.At != 0 {
		fmt.Fprintf(&b, "@%d", in.At)
	}
	for _, o := range out.Reads {
		if o.Exists {
			fmt.Fprintf(&b, " r(k%d=%d@%d)", o.Key, o.N, o.UT)
		} else {
			fmt.Fprintf(&b, " r(k%d=∅)", o.Key)
		}
	}
	for _, w := range in.Writes {
		fmt.Fprintf(&b, " %s(k%d", w.Kind, w.Key)
		if w.Kind == wSet || w.Kind == wIncr {
			fmt.Fprintf(&b, ",%d", w.N)
		}
		if w.Pre.Exists != "" {
			b.WriteString("," + w.Pre.Exists)
		}
		if w.Pre.UpdateTime != 0 {
			fmt.Fprintf(&b, ",ut=%d", w.Pre.UpdateTime)
		}
		b.WriteString(")")
	}
	fmt.Fprintf(&b, " -> %s", out.Outcome)
	switch out.Outcome {
	case outRefused:
		fmt.Fprintf(&b, " code %d", out.Code)
	case outOK:
		if out.CommitTime != 0 {
			fmt.Fprintf(&b, " ct=%d seq=%d", out.CommitTime, out.Seq)
		}
		if out.ReadTime != 0 {
			fmt.Fprintf(&b, " rt=%d", out.ReadTime)
		}
	}
	return b.String()
}

// sortOps orders a history by call time, for fixtures and messages.
func sortOps(a, b porcupine.Operation) int {
	return cmp.Or(cmp.Compare(a.Call, b.Call), cmp.Compare(a.Return, b.Return), cmp.Compare(a.ClientId, b.ClientId))
}
