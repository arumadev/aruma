package core

import (
	"context"

	"github.com/arumadev/aruma/internal/services/documents/index"
)

// STUB (F3 track A): the index catalogue before the catalogue's storage lands. Track B
// implements IndexCatalog in indexes.go from the indexes and field_overrides tables; delete this
// file when merging it. Until then every database declares nothing: no composite index and the
// default field override alone, which is what indexes-105.json was recorded with.

// IndexCatalog implements Queries.
func (s *PGStore) IndexCatalog(ctx context.Context, db DB) (*index.Catalog, error) {
	if err := s.serving(); err != nil {
		return nil, err
	}
	return index.NewCatalog(nil, nil)
}
