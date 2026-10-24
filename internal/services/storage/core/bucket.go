package core

import (
	"context"
	"fmt"

	"github.com/arumadev/aruma/internal/platform/apierr"
)

// Bucket resolves a bucket name through the control plane (tenancy.ProjectForBucket). A name
// that is no active project's default bucket is ErrBucketNotFound, so a deleted project's
// bucket answers as one that never existed (ADR-022 §2, §3).
func (s *Service) Bucket(ctx context.Context, name string) (Bucket, error) {
	p, err := s.projects.ProjectForBucket(ctx, name)
	switch {
	case apierr.CodeOf(err) == apierr.NotFound:
		return Bucket{}, ErrBucketNotFound
	case err != nil:
		return Bucket{}, fmt.Errorf("%w: resolve bucket: %w", ErrUnavailable, err)
	case p.State != "ACTIVE" || p.StorageBucket != name:
		return Bucket{}, ErrBucketNotFound
	}
	return Bucket{Project: p.Number, Name: name}, nil
}
