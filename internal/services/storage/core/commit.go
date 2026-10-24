package core

import "context"

// commitObject is the one transaction that makes a file visible (ADR-022 §5): the object's row
// with a new generation, the replaced file into blob_gc, and the quota moved from reserved to
// used, reading the replaced row in the statement that writes it.
func (s *Service) commitObject(ctx context.Context, w objectWrite) (Object, error) {
	panic("S2-D: commitObject")
}
