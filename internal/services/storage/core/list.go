package core

import "context"

// List returns one page of a bucket's objects and prefixes after the rules allow a list, walking
// the index with one seek per prefix, so a page costs at most MaxResults+1 index lookups
// (ADR-022 §13).
func (s *Service) List(ctx context.Context, b Bucket, p Principal, o ListOptions) (ListPage, error) {
	panic("S2-C: List")
}
