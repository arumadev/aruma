package index

import (
	"fmt"
	"slices"
)

// Mode is a database's index enforcement (ADR-025 §4.4): documents.index_enforcement, and its
// per-database override.
type Mode uint8

const (
	// Enforce rejects a query that needs an index, as production does. The default.
	Enforce Mode = iota
	// Warn serves it, and the caller logs and counts the exact suggested index: the escape
	// hatch while a migrated app's indexes are imported.
	Warn
	// Off serves every query without looking, as the emulator does.
	Off
)

// ParseMode reads a configured mode: enforce, warn or off.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "enforce":
		return Enforce, nil
	case "warn":
		return Warn, nil
	case "off":
		return Off, nil
	}
	return 0, fmt.Errorf("index: unknown enforcement mode %q, want enforce, warn or off", s)
}

func (m Mode) String() string {
	switch m {
	case Enforce:
		return "enforce"
	case Warn:
		return "warn"
	case Off:
		return "off"
	}
	return fmt.Sprintf("Mode(%d)", m)
}

// Verdict is Check's answer.
type Verdict uint8

const (
	// Served: the declared and automatic indexes serve the query, by a rule the probe verified.
	Served Verdict = iota + 1
	// NeedsIndex: production rejects the query with FAILED_PRECONDITION. Decision.Missing is
	// the index (or indexes) its error suggests.
	NeedsIndex
	// Uncertain: the answer depends on behaviour the evidence does not settle. The query is
	// served and logged as index_enforcement_uncertain with Decision.Reason, so Aruma never
	// rejects what production might accept (one-sided safety, IX §4.2).
	Uncertain
)

func (v Verdict) String() string {
	switch v {
	case Served:
		return "served"
	case NeedsIndex:
		return "needs_index"
	case Uncertain:
		return "uncertain"
	}
	return fmt.Sprintf("Verdict(%d)", v)
}

// Reason names the unsettled behaviour behind an Uncertain verdict; it is the metric label.
type Reason string

const (
	// ReasonMerge: only a zig-zag merge that includes a declared composite serves the query.
	// Documented (the restaurants example), never probed (R18).
	ReasonMerge Reason = "R18"
	// ReasonSelection: a declared composite serves the query only in a form the probe never
	// tried: a collection-group index for a collection query, an equality-pinned order field
	// moved into the equality prefix, or an aggregation's implicit fields in another order
	// (R19).
	ReasonSelection Reason = "R19"
	// ReasonExemption: the automatic index that would serve is exempted by a field override,
	// and production's message for that is unrecorded (R20).
	ReasonExemption Reason = "R20"
	// ReasonNotReady: the index that would serve is not READY (R21).
	ReasonNotReady Reason = "R21"
	// ReasonUnprobed: a shape the probe never sent, such as array-contains before __name__
	// descending, __name__ ordered before another field, or a kindless query on a field. The
	// query package rejects most of these first.
	ReasonUnprobed Reason = "unprobed"
)

// Required is one index production's error asks for.
type Required struct {
	// Index is the index, normalized (ending with __name__) unless Exemption.
	Index Index
	// Exemption marks a single-field collection-group index, which a field override creates:
	// Index then holds that one field without __name__, and the console link is
	// create_exemption (message template 4).
	Exemption bool
}

// serves reports whether r's index serves the disjunct that asked for o, both asked for by one
// query whose normalized order is orderLen fields long: the same index, or the same order after
// the same equality-like fields in another order (the equality prefix is order-free, R11).
func (r Required) serves(o Required, orderLen int) bool {
	if r.Exemption != o.Exemption || r.Index.CollectionGroup != o.Index.CollectionGroup ||
		r.Index.Scope != o.Index.Scope || len(r.Index.Fields) != len(o.Index.Fields) {
		return false
	}
	if r.Exemption {
		return slices.Equal(r.Index.Fields, o.Index.Fields)
	}
	n := len(r.Index.Fields) - orderLen
	if !slices.Equal(r.Index.Fields[n:], o.Index.Fields[n:]) {
		return false
	}
	return !slices.ContainsFunc(o.Index.Fields[:n], func(f IndexField) bool { return !slices.Contains(r.Index.Fields[:n], f) })
}

// Decision is Check's result.
type Decision struct {
	Verdict Verdict
	Mode    Mode
	// Missing is, for NeedsIndex, every index production asks for, one per DNF disjunct that
	// lacks one (duplicates dropped), in disjunct order. More than one makes message template 3.
	Missing []Required
	// MultipleRangeFields selects message template 2: the query has an inequality on a field
	// that is not its first order, so on two or more fields (R7) or after an order on another
	// field (R8).
	MultipleRangeFields bool
	// Reason and Detail say, for Uncertain, which behaviour is unsettled and where.
	Reason Reason
	Detail string
}

// Reject is whether the query fails with Suggest's error.
func (d Decision) Reject() bool { return d.Verdict == NeedsIndex && d.Mode == Enforce }

// Log is whether the query is served but must be logged and counted: an Uncertain verdict
// (index_enforcement_uncertain), or a missing index under Warn.
func (d Decision) Log() bool {
	return d.Verdict == Uncertain || d.Verdict == NeedsIndex && d.Mode == Warn
}

// maxDisjuncts bounds the DNF expansion. Production allows 30 disjuncts and the query package
// rejects more; this only keeps a malformed shape from exploding.
const maxDisjuncts = 1024

// Check decides q against a database's catalogue (nil for one that declares nothing) under
// mode. Off answers Served without looking.
//
// The rule (ADR-025 §4.1, IX §4.1). The query's order is normalized as query.proto states: the
// explicit orders; then every inequality field not ordered, in field path order, in the
// direction of the last explicit order (ascending without one); then __name__ in that direction
// unless ordered. SUM and AVG fields join the implicit fields, sorted with them (R13). The
// filter is expanded to DNF, and each disjunct needs an index that is its equality-like fields
// (in any order, a value field in either direction) followed by exactly the query's whole order
// (R11); an equality on a field the order already holds is applied there. The automatic
// indexes give [f, __name__] per field (R2, R3), and merge to serve any set of equality-like
// fields before __name__ alone (R1); the key index gives __name__ ascending (R5, R12). Any
// disjunct without an index makes the query NeedsIndex.
func Check(q QueryShape, cat *Catalog, mode Mode) Decision {
	if mode == Off {
		return Decision{Verdict: Served, Mode: Off}
	}
	d := check(q, cat)
	d.Mode = mode
	return d
}

// seg is one field of a required index: an equality-like filter's field, whose direction is
// free, or one order.
type seg struct {
	path     string
	contains bool
	desc     bool
}

// shape is a query's normalized order.
type shape struct {
	q QueryShape
	// order is the whole normalized order, ending with __name__.
	order []seg
	// explicit is how many of order's first fields are explicit orders other than __name__;
	// the rest before __name__ are implicit, in field path order.
	explicit int
	// multipleRange is Decision.MultipleRangeFields.
	multipleRange bool
}

func uncertain(r Reason, detail string) Decision {
	return Decision{Verdict: Uncertain, Reason: r, Detail: detail}
}

func check(q QueryShape, cat *Catalog) Decision {
	if problem := checkFilter(q.Where); problem != "" {
		return uncertain(ReasonUnprobed, problem)
	}
	sh, problem := normalize(q)
	if problem != "" {
		return uncertain(ReasonUnprobed, problem)
	}
	if q.CollectionID == "" {
		// Kindless (all descendants, no collection id): the key index alone serves it.
		if hasFieldFilter(q.Where) || len(sh.order) != 1 || sh.order[0].desc {
			return uncertain(ReasonUnprobed, "a kindless query on a field")
		}
		return Decision{Verdict: Served}
	}
	disjuncts, ok := dnf(q.Where)
	if !ok {
		return uncertain(ReasonUnprobed, "more than 1024 disjuncts")
	}
	d := Decision{Verdict: Served}
	for _, conj := range disjuncts {
		o := sh.disjunct(conj, cat)
		switch o.Verdict {
		case NeedsIndex:
			// Disjuncts that list the same equalities in another order, (a == 1 && b == 1) ||
			// (b == 2 && a == 2), need one index: the first one's serves both.
			if !slices.ContainsFunc(d.Missing, func(m Required) bool { return m.serves(o.Missing[0], len(sh.order)) }) {
				d.Missing = append(d.Missing, o.Missing[0])
			}
		case Uncertain:
			if d.Reason == "" {
				d.Reason, d.Detail = o.Reason, o.Detail
			}
		}
	}
	switch {
	case len(d.Missing) > 0:
		d.Verdict = NeedsIndex
		d.MultipleRangeFields = sh.multipleRange
		d.Reason, d.Detail = "", ""
	case d.Reason != "":
		d.Verdict = Uncertain
	}
	return d
}

// checkFilter reports a filter tree this package cannot read.
func checkFilter(f *Filter) string {
	if f == nil {
		return ""
	}
	switch {
	case f.Op == OpAnd || f.Op == OpOr:
		for i := range f.Filters {
			if p := checkFilter(&f.Filters[i]); p != "" {
				return p
			}
		}
		return ""
	case f.Field == "":
		return "a filter without a field"
	case !f.Op.equalityLike() && !f.Op.inequality():
		return fmt.Sprintf("unknown operator %d", f.Op)
	}
	return ""
}

// hasFieldFilter is whether the tree filters on a field other than __name__.
func hasFieldFilter(f *Filter) bool {
	if f == nil {
		return false
	}
	if f.Op == OpAnd || f.Op == OpOr {
		return slices.ContainsFunc(f.Filters, func(c Filter) bool { return hasFieldFilter(&c) })
	}
	return f.Field != NameField
}

// inequalityFields are the fields under an inequality anywhere in the tree, but __name__, which
// the key segment that ends every index absorbs.
func inequalityFields(f *Filter, into []string) []string {
	if f == nil {
		return into
	}
	if f.Op == OpAnd || f.Op == OpOr {
		for i := range f.Filters {
			into = inequalityFields(&f.Filters[i], into)
		}
		return into
	}
	if f.Op.inequality() && f.Field != NameField {
		into = append(into, f.Field)
	}
	return into
}

// normalize builds q's whole order (query.proto, StructuredQuery.order_by; the SDKs'
// queryNormalizedOrderBy), with SUM and AVG fields among the implicit ones (R13).
func normalize(q QueryShape) (shape, string) {
	sh := shape{q: q}
	ordered := make(map[string]bool, len(q.OrderBy))
	for _, o := range q.OrderBy {
		if ordered[o.Field] {
			return sh, "a field ordered twice"
		}
		ordered[o.Field] = true
		if o.Field != NameField {
			sh.order = append(sh.order, seg{path: o.Field, desc: o.Desc})
		}
	}
	sh.explicit = len(sh.order)
	if ordered[NameField] && q.OrderBy[len(q.OrderBy)-1].Field != NameField {
		return sh, "__name__ ordered before another field"
	}
	lastDesc := len(q.OrderBy) > 0 && q.OrderBy[len(q.OrderBy)-1].Desc

	ineq := sortedPaths(inequalityFields(q.Where, nil))
	// Template 2 when an inequality field is not the first order the query has without its
	// aggregations: R7 (two inequality fields) and R8 (an explicit order on another field
	// first), but not range1_ord_other (d > 15 orderBy d, a).
	first := ""
	switch {
	case len(q.OrderBy) > 0:
		first = q.OrderBy[0].Field
	case len(ineq) > 0:
		first = ineq[0]
	}
	sh.multipleRange = slices.ContainsFunc(ineq, func(f string) bool { return f != first })

	implicit := ineq
	for _, a := range q.Aggregations {
		if (a.Op == AggSum || a.Op == AggAvg) && a.Field != NameField {
			implicit = append(implicit, a.Field)
		}
	}
	for _, f := range sortedPaths(implicit) {
		if !ordered[f] {
			if ordered[NameField] {
				return sh, "__name__ ordered before an inequality or aggregated field"
			}
			sh.order = append(sh.order, seg{path: f, desc: lastDesc})
		}
	}
	// An explicit __name__ is the last explicit order, so lastDesc is its direction too.
	sh.order = append(sh.order, seg{path: NameField, desc: lastDesc})
	return sh, ""
}

// sortedPaths sorts paths in field path order and drops duplicates.
func sortedPaths(paths []string) []string {
	paths = slices.Clone(paths)
	slices.SortFunc(paths, comparePaths)
	return slices.Compact(paths)
}

// dnf expands f into disjunctive normal form: a list of conjunctions of field filters, in the
// order the tree gives them (left to right, distributing AND over OR). ok is false past
// maxDisjuncts.
func dnf(f *Filter) (out [][]Filter, ok bool) {
	if f == nil {
		return [][]Filter{nil}, true
	}
	switch f.Op {
	case OpAnd:
		out = [][]Filter{nil}
		for i := range f.Filters {
			sub, ok := dnf(&f.Filters[i])
			if !ok || len(out)*len(sub) > maxDisjuncts {
				return nil, false
			}
			if len(sub) == 1 {
				// The common case, a field filter: extend each conjunction in place. Each is
				// owned by out alone, so the appends cost amortized O(1) and an AND of n leaves
				// O(n), not O(n²).
				for j := range out {
					out[j] = append(out[j], sub[0]...)
				}
				continue
			}
			next := make([][]Filter, 0, len(out)*len(sub))
			for _, a := range out {
				for _, b := range sub {
					next = append(next, slices.Concat(a, b))
				}
			}
			out = next
		}
		return out, true
	case OpOr:
		for i := range f.Filters {
			sub, ok := dnf(&f.Filters[i])
			if !ok || len(out)+len(sub) > maxDisjuncts {
				return nil, false
			}
			out = append(out, sub...)
		}
		if len(out) == 0 {
			out = [][]Filter{nil}
		}
		return out, true
	}
	return [][]Filter{{*f}}, true
}

// cover is how one equality-like field of a disjunct is served.
type cover struct {
	ok     bool
	reason Reason // "" when ok and verified
	detail string
}

// better keeps the verified cover over an uncertain one, and the first uncertain one.
func (c cover) better(o cover) cover {
	if !c.ok || c.reason != "" && o.ok && o.reason == "" {
		return o
	}
	return c
}

// disjunct decides one DNF conjunction. A NeedsIndex result holds its one required index.
func (sh shape) disjunct(conj []Filter, cat *Catalog) Decision {
	q, order := sh.q, sh.order
	inOrder := make(map[string]bool, len(order))
	for _, s := range order {
		inOrder[s.path] = true
	}
	// eq is the equality-like fields the index must hold before the order. An equality on a
	// field the order holds is applied there (eq1_ord_same, in_ord_same_desc, sum_eq_same);
	// pinned keeps those for the R19 check.
	var eq, pinned []seg
	for _, f := range conj {
		if f.Field == NameField || !f.Op.equalityLike() {
			continue
		}
		s := seg{path: f.Field, contains: f.Op.contains()}
		switch {
		case !s.contains && inOrder[s.path]:
			if !slices.Contains(pinned, s) {
				pinned = append(pinned, s)
			}
		case !slices.Contains(eq, s):
			eq = append(eq, s)
		}
	}
	for _, s := range eq {
		if inOrder[s.path] || slices.Contains(eq, seg{path: s.path, contains: !s.contains}) {
			return uncertain(ReasonUnprobed, "field "+s.path+" both array-contains and compared or ordered")
		}
	}

	// The key index: no equality, __name__ ascending alone (R3, R12).
	if len(eq) == 0 && len(order) == 1 && !order[0].desc {
		return Decision{Verdict: Served}
	}

	ov := cat.fieldOverrides()
	covers := make([]cover, len(eq))
	var exact cover // an exact index that serves only uncertainly

	// Declared composites: exactly eq then order (served), or a part of eq then order, merged
	// with others (R18).
	for _, ix := range cat.indexes(q.CollectionID) {
		if !scopeServes(ix.Scope, q.Scope) {
			continue
		}
		c := cover{ok: true}
		switch {
		case ix.State != StateReady:
			c.reason, c.detail = ReasonNotReady, "a composite index that is not READY"
		case ix.Scope != q.Scope:
			c.reason, c.detail = ReasonSelection, "a collection-group composite for a collection query"
		}
		prefix, ok := splitOrder(ix.Fields, order)
		if !ok || !within(prefix, eq) {
			continue
		}
		switch {
		case len(prefix) == len(eq):
			if c.reason == "" {
				return Decision{Verdict: Served}
			}
			exact = exact.better(c)
		case len(prefix) > 0:
			if c.reason == "" {
				c.reason, c.detail = ReasonMerge, "a zig-zag merge with a declared composite"
			}
			for _, f := range prefix {
				i := slices.IndexFunc(eq, func(s seg) bool { return s.path == f.Path })
				covers[i] = covers[i].better(c)
			}
		}
	}

	// Automatic single-field indexes.
	auto := func(path string, mode FieldMode, verified bool) cover {
		switch ov.singleField(q.CollectionID, path, q.Scope, mode) {
		case enabled:
			if verified {
				return cover{ok: true}
			}
			return cover{ok: true, reason: ReasonUnprobed, detail: "array-contains before __name__ descending"}
		case notReady:
			return cover{ok: true, reason: ReasonNotReady, detail: "a single-field index that is not READY"}
		case exempted:
			return cover{ok: true, reason: ReasonExemption, detail: "an exempted single-field index on " + path}
		}
		return cover{}
	}
	switch {
	case len(order) == 1:
		// Equality-like fields before __name__ alone: one single-field index each, merged (R1).
		nameDesc := order[0].desc
		for i, s := range eq {
			// verified is false for array-contains before __name__ descending: mode is still
			// Contains, but no probe showed that index serving it.
			mode, verified := singleFieldMode(s.contains, nameDesc, nameDesc)
			covers[i] = covers[i].better(auto(s.path, mode, verified))
		}
	case len(eq) == 0 && len(order) == 2:
		// One field then __name__ (R2, R3).
		if mode, ok := singleFieldMode(false, order[0].desc, order[1].desc); ok {
			exact = exact.better(auto(order[0].path, mode, true))
		}
	}

	if len(eq) > 0 && !slices.ContainsFunc(covers, func(c cover) bool { return !c.ok }) {
		i := slices.IndexFunc(covers, func(c cover) bool { return c.reason != "" })
		if i < 0 {
			return Decision{Verdict: Served}
		}
		exact = exact.better(covers[i])
	}
	if exact.ok {
		if exact.reason == "" {
			return Decision{Verdict: Served}
		}
		return uncertain(exact.reason, exact.detail)
	}
	if sh.relaxed(eq, pinned, cat) {
		return uncertain(ReasonSelection, "a composite in an unprobed arrangement")
	}
	return Decision{Verdict: NeedsIndex, Missing: []Required{sh.required(eq)}}
}

// relaxed is whether a declared composite would serve the disjunct in a form production was
// never probed on, which R19 leaves open:
//   - with the equality-pinned order fields moved into the equality prefix (a == 1 orderBy d,
//     a and an index on a, d). Automatic indexes do not do this (eq_eq_ord_eqfield), so it
//     is only tried on declared composites;
//   - for an aggregation, whose result has no order, with the implicit fields (inequality, SUM
//     and AVG) in another order or direction.
func (sh shape) relaxed(eq, pinned []seg, cat *Catalog) bool {
	q := sh.q
	unpinned := slices.DeleteFunc(slices.Clone(sh.order), func(s seg) bool {
		return s.path != NameField && slices.Contains(pinned, seg{path: s.path})
	})
	widened := slices.Concat(eq, pinned)
	implicit := sh.order[sh.explicit : len(sh.order)-1]
	for _, ix := range cat.indexes(q.CollectionID) {
		if !scopeServes(ix.Scope, q.Scope) {
			continue
		}
		if len(pinned) > 0 {
			if prefix, ok := splitOrder(ix.Fields, unpinned); ok && len(prefix) == len(widened) && within(prefix, widened) {
				return true
			}
		}
		if len(q.Aggregations) == 0 || len(ix.Fields) != len(eq)+len(sh.order) {
			continue
		}
		prefix, rest := ix.Fields[:len(eq)], ix.Fields[len(eq):len(ix.Fields)-1]
		if !within(prefix, eq) || !matchOrder(rest[:sh.explicit], sh.order[:sh.explicit]) {
			continue
		}
		if !slices.ContainsFunc(rest[sh.explicit:], func(f IndexField) bool {
			return f.Mode == Contains || !slices.ContainsFunc(implicit, func(s seg) bool { return s.path == f.Path })
		}) {
			return true
		}
	}
	return false
}

// required is the index production asks for when the disjunct has none: the equality-like
// fields in query order (a value ascending, R16), then the order. A single field then __name__
// at collection-group scope is a single-field index, created by an exemption (R12).
func (sh shape) required(eq []seg) Required {
	q, order := sh.q, sh.order
	if q.Scope == ScopeCollectionGroup {
		var field seg
		mode, single := FieldMode(0), false
		switch {
		case len(eq) == 1 && len(order) == 1:
			field = eq[0]
			mode, single = singleFieldMode(field.contains, order[0].desc, order[0].desc)
		case len(eq) == 0 && len(order) == 2:
			field = order[0]
			mode, single = singleFieldMode(false, field.desc, order[1].desc)
		}
		if single {
			return Required{Exemption: true, Index: Index{
				CollectionGroup: q.CollectionID, Scope: q.Scope,
				Fields: []IndexField{{Path: field.path, Mode: mode}},
			}}
		}
	}
	fields := make([]IndexField, 0, len(eq)+len(order))
	for _, s := range eq {
		fields = append(fields, IndexField{Path: s.path, Mode: segMode(s)})
	}
	for _, s := range order {
		fields = append(fields, IndexField{Path: s.path, Mode: segMode(s)})
	}
	return Required{Index: Index{CollectionGroup: q.CollectionID, Scope: q.Scope, Fields: fields}}
}

func segMode(s seg) FieldMode {
	switch {
	case s.contains:
		return Contains
	case s.desc:
		return Descending
	}
	return Ascending
}

// scopeServes is whether an index of scope ix could serve a query of scope q: its own, or a
// collection-group index for a collection query, which R19 leaves open.
func scopeServes(ix, q Scope) bool {
	return ix == q || ix == ScopeCollectionGroup && q == ScopeCollection
}

// splitOrder returns the fields before order, when fields end with exactly order.
func splitOrder(fields []IndexField, order []seg) ([]IndexField, bool) {
	n := len(fields) - len(order)
	if n < 0 || !matchOrder(fields[n:], order) {
		return nil, false
	}
	return fields[:n], true
}

// matchOrder is whether fields are exactly order, path and direction.
func matchOrder(fields []IndexField, order []seg) bool {
	return slices.EqualFunc(fields, order, func(f IndexField, s seg) bool {
		return f.Path == s.path && f.Mode != Contains && (f.Mode == Descending) == s.desc
	})
}

// within is whether every field of prefix is one of eq: array-contains for an array-contains
// filter, either direction for a value. Index fields and eq are each free of duplicates, so
// equal lengths mean the same set.
func within(prefix []IndexField, eq []seg) bool {
	for _, f := range prefix {
		if !slices.ContainsFunc(eq, func(s seg) bool { return s.path == f.Path && s.contains == (f.Mode == Contains) }) {
			return false
		}
	}
	return true
}
