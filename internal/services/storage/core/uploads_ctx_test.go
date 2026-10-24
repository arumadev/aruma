package core

import (
	"context"
	"io"
	"testing"
)

// breakingBody is a request body as net/http hands it over when the connection breaks: data,
// then the request's context cancelled and an error, in that order.
type breakingBody struct {
	data   []byte
	cancel context.CancelFunc
}

func (b *breakingBody) Read(p []byte) (int, error) {
	if len(b.data) > 0 {
		n := copy(p, b.data)
		b.data = b.data[n:]
		return n, nil
	}
	b.cancel()
	return 0, io.ErrUnexpectedEOF
}

// What arrived before a body broke is recorded, though net/http cancels the request's context as
// it breaks (ADR-022 §6.3): on a session's first durable byte, whose transaction counts the
// session caps, and on a later chunk. The session announces the bytes, the account holds them,
// and the client resumes from there.
func TestUploadKeepsBytesOfABrokenRequest(t *testing.T) {
	e := newEnv(t)
	id := e.start(e.a, asUser("u1"), uploadSpec("a.bin", SurfaceFirebase))
	for _, c := range []struct {
		offset int64
		data   string
	}{{0, "abc"}, {3, "defg"}} {
		ctx, cancel := context.WithCancel(context.Background())
		st, err := e.s.WriteUpload(ctx, e.a, "a.bin", id, nil, Chunk{Offset: c.offset,
			Body: &breakingBody{data: []byte(c.data), cancel: cancel}, Length: 100, Total: -1})
		cancel()
		want := c.offset + int64(len(c.data))
		if err == nil || st.Status != UploadActive || st.Received != want {
			t.Fatalf("chunk at %d: %+v, %v; want active with %d received", c.offset, st, err, want)
		}
		if q, err := e.s.QueryUpload(context.Background(), e.a, "a.bin", id); err != nil || q.Received != want {
			t.Fatalf("query after the chunk at %d: %+v, %v", c.offset, q, err)
		}
		e.checkUsage(e.a.Project)
	}
	st, err := e.chunk(e.a, "a.bin", id, 7, "h", true)
	if err != nil || st.Object == nil || st.Object.Size != 8 {
		t.Fatalf("finalize: %+v, %v", st, err)
	}
	if got := e.read(e.a, "a.bin"); got != "abcdefgh" {
		t.Fatalf("stored %q", got)
	}
	e.checkUsage(e.a.Project)
}

// A one-request upload whose body breaks gives back its reservation, though the request's
// context is cancelled by then.
func TestInsertReleasesABrokenRequest(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := e.s.Insert(ctx, e.a, asAdmin(), uploadSpec("b.bin", SurfaceGCS), &breakingBody{data: []byte("abc"), cancel: cancel}, -1)
	if err == nil {
		t.Fatal("a broken body was stored")
	}
	if used, reserved, _, objects := e.usage(e.a.Project); used != 0 || reserved != 0 || objects != 0 {
		t.Fatalf("after the broken insert: used %d reserved %d objects %d", used, reserved, objects)
	}
	e.checkUsage(e.a.Project)
}
