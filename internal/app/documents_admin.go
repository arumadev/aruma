package app

import (
	"context"

	documents "github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/longrunningpb"
)

// The Admin API's index configuration (documents.IndexAdmin) through bootStore: errNotBooted
// until Boot opened the store.

func (b *bootStore) CreateIndex(ctx context.Context, db documents.DB, group string, ix *adminpb.Index) (*longrunningpb.Operation, error) {
	s, err := b.get()
	if err != nil {
		return nil, err
	}
	return s.CreateIndex(ctx, db, group, ix)
}

func (b *bootStore) ListIndexes(ctx context.Context, db documents.DB, group string) ([]*adminpb.Index, error) {
	s, err := b.get()
	if err != nil {
		return nil, err
	}
	return s.ListIndexes(ctx, db, group)
}

func (b *bootStore) GetIndex(ctx context.Context, db documents.DB, group, id string) (*adminpb.Index, error) {
	s, err := b.get()
	if err != nil {
		return nil, err
	}
	return s.GetIndex(ctx, db, group, id)
}

func (b *bootStore) DeleteIndex(ctx context.Context, db documents.DB, group, id string) error {
	s, err := b.get()
	if err != nil {
		return err
	}
	return s.DeleteIndex(ctx, db, group, id)
}

func (b *bootStore) GetField(ctx context.Context, db documents.DB, group, path string) (*adminpb.Field, error) {
	s, err := b.get()
	if err != nil {
		return nil, err
	}
	return s.GetField(ctx, db, group, path)
}

func (b *bootStore) ListFields(ctx context.Context, db documents.DB, group string) ([]*adminpb.Field, error) {
	s, err := b.get()
	if err != nil {
		return nil, err
	}
	return s.ListFields(ctx, db, group)
}

func (b *bootStore) UpdateField(ctx context.Context, db documents.DB, group string, f *adminpb.Field, mask []string) (*longrunningpb.Operation, error) {
	s, err := b.get()
	if err != nil {
		return nil, err
	}
	return s.UpdateField(ctx, db, group, f, mask)
}

func (b *bootStore) GetOperation(ctx context.Context, db documents.DB, id string) (*longrunningpb.Operation, error) {
	s, err := b.get()
	if err != nil {
		return nil, err
	}
	return s.GetOperation(ctx, db, id)
}

func (b *bootStore) ListOperations(ctx context.Context, db documents.DB) ([]*longrunningpb.Operation, error) {
	s, err := b.get()
	if err != nil {
		return nil, err
	}
	return s.ListOperations(ctx, db)
}
