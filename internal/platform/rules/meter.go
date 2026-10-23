package rules

import (
	"fmt"
	"sync/atomic"
)

// Inflight caps the memory that every evaluation running in the process builds together
// (ADR-021 §4): 16 MiB per evaluation bounds nothing when a thousand run at once. internal/app
// makes one for the whole process and every service passes it in each Request. An evaluation
// reserves what it allocates as it allocates it, and returns all of it when it ends. When the
// cap is reached the evaluation stops with ErrOverloaded: no waiting, which would pile up
// requests and their memory, and no denial, which the SDKs would take as final.
type Inflight struct {
	limit int64
	used  atomic.Int64
}

// NewInflight returns a cap of bytes (at least 1).
func NewInflight(bytes int64) *Inflight {
	return &Inflight{limit: max(bytes, 1)}
}

// reserve takes n bytes, and reports false, taking nothing, when they would pass the cap.
func (in *Inflight) reserve(n int64) bool {
	for {
		used := in.used.Load()
		if n > in.limit-used {
			return false
		}
		if in.used.CompareAndSwap(used, used+n) {
			return true
		}
	}
}

// release returns n bytes reserved earlier.
func (in *Inflight) release(n int64) { in.used.Add(-n) }

// meter is what the library needs from the evaluation that calls it: the account of its work
// and of the values it builds against Aruma's limits, and the ruleset's cache of patterns built
// during evaluation. Evaluate makes a new one for every evaluation, each operation of a batch
// included, and releases it when the evaluation ends. One evaluation runs on one goroutine, so
// a meter needs no lock.
//
// Every operation and function charges the meter before it builds a value, not after (ADR-021
// §4). A charge past a limit returns an *abort, which ends the evaluation with a denial; past
// the Inflight cap the abort carries ErrOverloaded, which Evaluate returns instead of a decision.
type meter struct {
	lim      *Limits
	inflight *Inflight
	regexes  *regexCache

	worked    int64 // work units spent
	allocated int64 // bytes built, all reserved from inflight
}

func newMeter(lim *Limits, inflight *Inflight, regexes *regexCache) *meter {
	return &meter{lim: lim, inflight: inflight, regexes: regexes}
}

// work charges n units of work: a character a pattern runs over times the program's
// instructions, an element a collection function touches, the compilation of a pattern built
// during evaluation.
func (m *meter) work(n int64) error {
	if n < 0 || n > m.lim.Work-m.worked {
		m.worked = m.lim.Work
		return &abort{msg: fmt.Sprintf("evaluation exceeded the work limit of %d", m.lim.Work)}
	}
	m.worked += n
	return nil
}

// alloc charges n bytes that the evaluation is about to build, and reserves them from the
// process-wide Inflight.
func (m *meter) alloc(n int64) error {
	if n < 0 || n > m.lim.AllocBytes-m.allocated {
		return &abort{msg: fmt.Sprintf("evaluation exceeded the allocation limit of %d bytes", m.lim.AllocBytes)}
	}
	if !m.inflight.reserve(n) {
		return &abort{msg: "evaluation memory exhausted", err: ErrOverloaded}
	}
	m.allocated += n
	return nil
}

// value checks the size of one value about to be built: bytes for a string or bytes value,
// elems for a list, map or set. It charges nothing; the caller charges alloc for the memory.
func (m *meter) value(bytes, elems int64) error {
	if bytes > m.lim.ValueBytes {
		return &abort{msg: fmt.Sprintf("value of %d bytes exceeds the limit of %d", bytes, m.lim.ValueBytes)}
	}
	if elems > int64(m.lim.ValueElements) {
		return &abort{msg: fmt.Sprintf("value of %d elements exceeds the limit of %d", elems, m.lim.ValueElements)}
	}
	return nil
}

// release returns everything the evaluation reserved from Inflight. Evaluate defers it; a
// second call returns nothing more.
func (m *meter) release() {
	m.inflight.release(m.allocated)
	m.allocated = 0
}
