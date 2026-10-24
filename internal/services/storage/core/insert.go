package core

import (
	"context"
	"io"
)

// Insert stores an object sent in one request (media or multipart): the body straight to a new
// key, the rules, then commitObject. length is the body's size, -1 when unknown.
func (s *Service) Insert(ctx context.Context, b Bucket, p Principal, spec UploadSpec, body io.Reader, length int64) (Object, error) {
	if err := checkPre(spec.Surface, spec.Pre); err != nil {
		return Object{}, err
	}
	panic("S2-D: Insert")
}

// Rewrite copies an object to a new key, within one project (GCS rewriteTo), counted on the
// quota and the disk floor. The tokens go with it, sealed again for the destination's name,
// unless attrs replaces the metadata.
func (s *Service) Rewrite(ctx context.Context, src Bucket, srcName string, dst Bucket, dstName string, p Principal, attrs *Patch, pre Preconditions) (Object, error) {
	panic("S2-D: Rewrite")
}
