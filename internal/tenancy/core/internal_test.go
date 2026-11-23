package core

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/arumadev/aruma/internal/platform/apierr"
)

// A database the control plane cannot reach is UNAVAILABLE, which the SDKs retry, even when
// the driver's error carries a deadline (a connection that timed out opening); only a caller's
// own cancellation is CANCELLED.
func TestInternalMapsAnUnreachableDatabaseToUnavailable(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want apierr.Code
	}{
		"connect timed out": {fmt.Errorf("failed to connect to `user=aruma`: %w", errors.Join(errors.New("timeout"), context.DeadlineExceeded)), apierr.Unavailable},
		"caller gave up":    {fmt.Errorf("query: %w", context.Canceled), apierr.Canceled},
		"other":             {errors.New("boom"), apierr.Internal},
	} {
		if got := apierr.CodeOf(internal(c.err)); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
