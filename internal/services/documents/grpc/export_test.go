package grpc

import (
	"context"
	"testing"
)

// SetAfterSend makes f run after each Send returns, before the stream takes it as done, until
// the test ends.
func SetAfterSend(t testing.TB, f func(context.Context)) {
	afterSend.Store(&f)
	t.Cleanup(func() { afterSend.Store(nil) })
}
