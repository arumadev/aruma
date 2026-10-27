package index

import (
	"errors"
	"fmt"
	"slices"
)

// Scope is an index's or a query's scope. The values are google.firestore.admin.v1.Index
// .QueryScope's, which the console link encodes (suggest.go).
type Scope uint8

const (
	ScopeCollection      Scope = 1
	ScopeCollectionGroup Scope = 2
)

func (s Scope) String() string {
	switch s {
	case ScopeCollection:
		return "COLLECTION"
	case ScopeCollectionGroup:
		return "COLLECTION_GROUP"
	}
	return fmt.Sprintf("Scope(%d)", s)
}

// FieldMode is how an index holds one field: in ascending or descending order, or as the
// elements of an array (array-contains).
type FieldMode uint8

const (
	Ascending FieldMode = iota + 1
	Descending
	Contains
)

func (m FieldMode) String() string {
	switch m {
	case Ascending:
		return "ASCENDING"
	case Descending:
		return "DESCENDING"
	case Contains:
		return "CONTAINS"
	}
	return fmt.Sprintf("FieldMode(%d)", m)
}

// State is an index's build state. The zero value is READY, the only state that serves. An index
// is CREATING from the transaction that creates it: every commit writes its entries from then on
// (dual write), a background build fills the documents already there in batches, and it is READY
// after the last batch alone (ADR-024 §2.5; ADR-025 §3). NEEDS_REPAIR appears only in what an
// import or a failed build leaves.
type State uint8

const (
	StateReady State = iota
	StateCreating
	StateNeedsRepair
)

// IndexField is one field of an index.
type IndexField struct {
	Path string
	Mode FieldMode
}

// Index is a composite index as the Admin API declares it (google.firestore.admin.v1.Index):
// its collection group, scope and fields. The fields end with __name__; Normalize adds it.
type Index struct {
	CollectionGroup string
	Scope           Scope
	Fields          []IndexField
	State           State
}

// maxIndexFields is production's limit on the fields of a composite index (IX §1.5).
const maxIndexFields = 100

// Normalize returns ix with __name__ appended when its last field is not __name__, in the
// direction of the last field (ascending after an array-contains field), as production and
// firebase-tools do (IX §1.7, §4.3). The fields are copied.
func (ix Index) Normalize() Index {
	ix.Fields = slices.Clone(ix.Fields)
	if n := len(ix.Fields); n == 0 || ix.Fields[n-1].Path != NameField {
		mode := Ascending
		if n > 0 && ix.Fields[n-1].Mode == Descending {
			mode = Descending
		}
		ix.Fields = append(ix.Fields, IndexField{Path: NameField, Mode: mode})
	}
	return ix
}

// Validate reports why a normalized index cannot exist: no collection group, an unknown scope
// or mode, more than 100 fields, a field twice, __name__ anywhere but last or as
// array-contains, or more than one array-contains field.
func (ix Index) Validate() error {
	if ix.CollectionGroup == "" {
		return errors.New("index: no collection group")
	}
	if ix.Scope != ScopeCollection && ix.Scope != ScopeCollectionGroup {
		return fmt.Errorf("index: unknown query scope %d", ix.Scope)
	}
	if len(ix.Fields) == 0 || len(ix.Fields) > maxIndexFields {
		return fmt.Errorf("index: %d fields, want 1 to %d", len(ix.Fields), maxIndexFields)
	}
	seen := make(map[string]bool, len(ix.Fields))
	contains := 0
	for i, f := range ix.Fields {
		switch {
		case f.Path == "":
			return errors.New("index: empty field path")
		case f.Mode < Ascending || f.Mode > Contains:
			return fmt.Errorf("index: field %s has unknown mode %d", f.Path, f.Mode)
		case seen[f.Path]:
			return fmt.Errorf("index: field %s appears twice", f.Path)
		case f.Path == NameField && (i != len(ix.Fields)-1 || f.Mode == Contains):
			return errors.New("index: __name__ must be the last field, ordered")
		}
		seen[f.Path] = true
		if f.Mode == Contains {
			contains++
		}
	}
	if contains > 1 {
		return errors.New("index: more than one array-contains field")
	}
	return nil
}

// Catalog is one database's declared index configuration: its composite indexes and its field
// overrides. A nil *Catalog is a database that declares nothing, which is what the corpus
// recorded (indexes {}, and only the default override).
type Catalog struct {
	composites map[string][]Index
	overrides  overrides
}

// NewCatalog validates and normalizes a database's composite indexes and field overrides, as the
// Admin API stores them.
func NewCatalog(indexes []Index, fieldOverrides []FieldOverride) (*Catalog, error) {
	c := &Catalog{composites: make(map[string][]Index)}
	for _, ix := range indexes {
		ix = ix.Normalize()
		if err := ix.Validate(); err != nil {
			return nil, err
		}
		c.composites[ix.CollectionGroup] = append(c.composites[ix.CollectionGroup], ix)
	}
	o, err := newOverrides(fieldOverrides)
	if err != nil {
		return nil, err
	}
	c.overrides = o
	return c, nil
}

func (c *Catalog) indexes(group string) []Index {
	if c == nil {
		return nil
	}
	return c.composites[group]
}

func (c *Catalog) fieldOverrides() overrides {
	if c == nil {
		return nil
	}
	return c.overrides
}
