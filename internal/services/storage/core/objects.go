package core

import (
	"context"

	"github.com/arumadev/aruma/internal/services/storage/blob"
)

// Stat returns an object's metadata after the rules allow a get. On SurfaceFirebase an object
// without a download token gets one, atomically with a new metageneration (ADR-022 §9). gen is
// a generation the caller pins (GCS ?generation=); 0 is the current one, and any other than the
// current is ErrNotFound.
func (s *Service) Stat(ctx context.Context, b Bucket, name string, p Principal, sf Surface, gen int64) (Object, error) {
	panic("S2-C: Stat")
}

// Open opens an object's bytes. A token that matches one of the object's opens it without the
// rules; a token that is present and matches none is ErrPermissionDenied without the rules
// (ADR-022 §9); no token, and the rules decide a get. The caller closes the reader.
func (s *Service) Open(ctx context.Context, b Bucket, name string, p Principal, token string, gen int64) (Object, blob.Reader, error) {
	panic("S2-C: Open")
}

// Update applies a metadata change after the rules allow an update, and advances the
// metageneration (ADR-022 §8, §14).
func (s *Service) Update(ctx context.Context, b Bucket, name string, p Principal, sf Surface, u Patch, pre Preconditions) (Object, error) {
	if err := checkPre(sf, pre); err != nil {
		return Object{}, err
	}
	panic("S2-C: Update")
}

// Delete removes an object after the rules allow a delete; its file goes to blob_gc in the same
// transaction (releaseObject). Preconditions come from the GCS surface alone.
func (s *Service) Delete(ctx context.Context, b Bucket, name string, p Principal, sf Surface, pre Preconditions) error {
	if err := checkPre(sf, pre); err != nil {
		return err
	}
	panic("S2-C: Delete")
}
