package blob

import (
	"context"
	"time"
)

// AcquireLock takes flock(LOCK_EX) on <root>/LOCK, waiting while another process holds it, and
// calling log every 10 seconds with how long it has waited, so an operator sees why a new
// instance does not answer. It gives up when ctx ends.
func AcquireLock(ctx context.Context, root string, log func(waited time.Duration)) (Lock, error) {
	panic("S2-A: AcquireLock")
}
