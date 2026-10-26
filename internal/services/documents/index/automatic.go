package index

import "slices"

// automaticDefault is what every field gets when no override governs it: an ascending, a
// descending and an array-contains index, at collection scope only (index-overview, IX §1.1).
// "Automatic indexes with collection group scope are not maintained by default", so every
// collection-group query that filters or orders on a field needs an override or a composite
// (R12).
//
// Each single-field index ends with __name__: ascending after an ascending or array-contains
// field, descending after a descending one. Firestore never scans an index backwards, so the
// ascending index cannot serve a descending order and the reverse (R5). Whether the
// array-contains index can serve __name__ descending was never probed; Check answers Uncertain
// there.
//
// Besides these, every collection and collection group has the key index, __name__ ascending
// alone: it serves a query with no field filter and no field order at either scope (R3, R12),
// and nothing descends it (name_desc, cg_name_desc).
var automaticDefault = []SingleFieldIndex{
	{Scope: ScopeCollection, Mode: Ascending},
	{Scope: ScopeCollection, Mode: Descending},
	{Scope: ScopeCollection, Mode: Contains},
}

// availability is whether a single-field index exists to serve a query.
type availability uint8

const (
	// absent: no configuration in the chain has the index; production rejects (R12).
	absent availability = iota
	// enabled: the governing configuration has it, READY.
	enabled
	// notReady: the governing configuration has it, not READY (R21).
	notReady
	// exempted: an override left it out although what it overrides has it (R20). A
	// collection-scope index is always exempted rather than absent, since the automatic
	// default has it.
	exempted
)

// singleField is whether field path of collection group group has the single-field index of
// scope and mode.
func (o overrides) singleField(group, path string, scope Scope, mode FieldMode) availability {
	chain := o.chain(group, path)
	has := func(ixs []SingleFieldIndex, scope Scope) (SingleFieldIndex, bool) {
		i := slices.IndexFunc(ixs, func(ix SingleFieldIndex) bool { return ix.Scope == scope && ix.Mode == mode })
		if i < 0 {
			return SingleFieldIndex{}, false
		}
		return ixs[i], true
	}
	if ix, ok := has(chain[0], scope); ok {
		if ix.State == StateReady {
			return enabled
		}
		return notReady
	}
	for _, inherited := range chain[1:] {
		if _, ok := has(inherited, scope); ok {
			return exempted
		}
	}
	return absent
}

// singleFieldMode is the mode of the single-field index whose order is a field in mode then
// __name__ in nameDesc's direction, if there is one: ascending and array-contains indexes end
// ascending, descending ones descending. ok is false for array-contains before __name__
// descending, which no automatic index is known to hold.
func singleFieldMode(contains, desc, nameDesc bool) (mode FieldMode, ok bool) {
	switch {
	case contains:
		return Contains, !nameDesc
	case desc != nameDesc:
		return 0, false
	case desc:
		return Descending, true
	}
	return Ascending, true
}
