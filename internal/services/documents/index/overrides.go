package index

import (
	"errors"
	"fmt"
)

const (
	// DefaultCollectionGroup is the collection group of the database-wide default,
	// collectionGroups/__default__/fields/*.
	DefaultCollectionGroup = "__default__"
	// AllFields is the field path of a collection-level override: every field of the
	// collection group that has no override of its own.
	AllFields = "*"
)

// SingleFieldIndex is one single-field index a field override declares.
type SingleFieldIndex struct {
	Scope Scope
	Mode  FieldMode
	State State
}

// FieldOverride is a field's single-field index configuration (google.firestore.admin.v1.Field
// .index_config), keyed by collection group and field path. Indexes is the complete set for the
// field and replaces what it would inherit: an override that lists a COLLECTION_GROUP index
// enables that scope (R12, the console's create_exemption link), and one that leaves an
// automatic index out exempts the field from it (R20). An empty list exempts the field from
// every single-field index.
//
// An override applies to the field and its map subfields that have none of their own. One on
// AllFields applies to every field of its collection group, and one on DefaultCollectionGroup
// and AllFields is the database default (IX §1.4, §1.7). Composite indexes ignore overrides: an
// exempted field can still be part of one.
type FieldOverride struct {
	CollectionGroup string
	Path            string
	Indexes         []SingleFieldIndex
}

type overrideKey struct{ group, path string }

// overrides holds a database's field overrides by collection group and path.
type overrides map[overrideKey][]SingleFieldIndex

func newOverrides(list []FieldOverride) (overrides, error) {
	o := make(overrides, len(list))
	for _, f := range list {
		if f.CollectionGroup == "" || f.Path == "" {
			return nil, errors.New("index: field override without collection group or path")
		}
		if f.CollectionGroup == DefaultCollectionGroup && f.Path != AllFields {
			return nil, fmt.Errorf("index: the database default overrides %q only, not %q", AllFields, f.Path)
		}
		for _, ix := range f.Indexes {
			if ix.Scope != ScopeCollection && ix.Scope != ScopeCollectionGroup || ix.Mode < Ascending || ix.Mode > Contains {
				return nil, fmt.Errorf("index: override %s/%s has an unknown scope or mode", f.CollectionGroup, f.Path)
			}
		}
		k := overrideKey{f.CollectionGroup, f.Path}
		if _, dup := o[k]; dup {
			return nil, fmt.Errorf("index: two overrides for %s/%s", f.CollectionGroup, f.Path)
		}
		o[k] = f.Indexes
	}
	return o, nil
}

// chain is the single-field configurations that could govern field path in collection group
// group, nearest first: the field's own override, its ancestors', the collection group's
// AllFields override, the database default, and the automatic default. The first present one
// governs; the later ones are what an exemption overrode.
func (o overrides) chain(group, path string) [][]SingleFieldIndex {
	var out [][]SingleFieldIndex
	add := func(k overrideKey) {
		if ixs, ok := o[k]; ok {
			out = append(out, ixs)
		}
	}
	add(overrideKey{group, path})
	for _, a := range ancestors(path) {
		add(overrideKey{group, a})
	}
	add(overrideKey{group, AllFields})
	add(overrideKey{DefaultCollectionGroup, AllFields})
	return append(out, automaticDefault)
}
