package core

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/services/documents/queries"
)

// IndexEntryRef is one index entry CheckIndexEntries reports: its index, its whole key and its
// document.
type IndexEntryRef struct {
	Index int64
	Key   []byte
	Path  Path
}

func (e IndexEntryRef) String() string {
	return fmt.Sprintf("index %d, %s, key %x", e.Index, e.Path, e.Key)
}

// IndexDiff is how a database's stored index entries differ from those its documents have
// under its catalog: Missing are owed and not stored, Extra stored and not owed.
type IndexDiff struct {
	Missing, Extra []IndexEntryRef
}

// Empty reports whether the stored entries are exactly the documents'.
func (d IndexDiff) Empty() bool { return len(d.Missing) == 0 && len(d.Extra) == 0 }

func (d IndexDiff) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d missing, %d extra", len(d.Missing), len(d.Extra))
	for _, e := range d.Missing[:min(len(d.Missing), 10)] {
		b.WriteString("\n  missing " + e.String())
	}
	for _, e := range d.Extra[:min(len(d.Extra), 10)] {
		b.WriteString("\n  extra " + e.String())
	}
	return b.String()
}

// CheckIndexEntries recomputes the index entries of every document of db under its catalog (the
// single-field configuration and every composite index but the vector ones and those being
// deleted) and compares them with index_entries, all in one snapshot. It is the consistency
// check of the index tests and of tools; run it with no build pending (BuildIndexes), since a
// CREATING composite is not filled yet and a field update's old entries are not yet gone. It
// reads the whole database into memory.
func (s *PGStore) CheckIndexEntries(ctx context.Context, db DB) (IndexDiff, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return IndexDiff{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := queries.New(tx)
	cat, err := readCatalog(ctx, q, db)
	if err != nil {
		return IndexDiff{}, err
	}
	docs, err := q.AllDocuments(ctx, queries.AllDocumentsParams{ProjectNumber: db.Project, DatabaseID: db.ID})
	if err != nil {
		return IndexDiff{}, err
	}
	want := map[entry]Path{}
	for _, d := range docs {
		p, err := pathOfKey(d.Name)
		if err != nil {
			return IndexDiff{}, err
		}
		fields, _, _, err := decodeDocument(d.Doc)
		if err != nil {
			return IndexDiff{}, err
		}
		for e := range entriesOf(cat, cat.fields, p, fields, true).set {
			want[e] = p
		}
	}
	rows, err := q.AllIndexEntries(ctx, queries.AllIndexEntriesParams{ProjectNumber: db.Project, DatabaseID: db.ID})
	if err != nil {
		return IndexDiff{}, err
	}
	var diff IndexDiff
	got := make(map[entry]bool, len(rows))
	for _, r := range rows {
		k := r.Key
		if r.FullKey != nil {
			k = r.FullKey
			if !bytes.Equal(storedKey(k), r.Key) {
				return IndexDiff{}, fmt.Errorf("documents: index %d: an entry's key %x is not its full key's", r.IndexID, r.Key)
			}
		}
		e := entry{r.IndexID, string(k)}
		got[e] = true
		if _, ok := want[e]; !ok {
			p, _ := pathOfEntry(k)
			diff.Extra = append(diff.Extra, IndexEntryRef{Index: e.index, Key: k, Path: p})
		}
	}
	for e, p := range want {
		if !got[e] {
			diff.Missing = append(diff.Missing, IndexEntryRef{Index: e.index, Key: []byte(e.key), Path: p})
		}
	}
	order := func(a, b IndexEntryRef) int {
		return cmp.Or(cmp.Compare(a.Index, b.Index), bytes.Compare(a.Key, b.Key))
	}
	slices.SortFunc(diff.Missing, order)
	slices.SortFunc(diff.Extra, order)
	return diff, nil
}
