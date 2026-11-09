package grpc

import (
	"context"
	"testing"
)

// SetAfterSend makes f run after each Send of h's streams returns, before the stream takes it
// as done, until the test ends.
func SetAfterSend(t testing.TB, h *Handler, f func(context.Context)) {
	h.afterSend.Store(&f)
	t.Cleanup(func() { h.afterSend.Store(nil) })
}
