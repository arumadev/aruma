package core

import (
	"context"
	"errors"
	"testing"
)

// A finalize that resumes after a crash between the move and the commit finds the file at
// blob_key, which Promote reports as moved earlier (S2-A): its bytes are hashed again and must be
// the session's before anything commits.
func TestResumeVerifiesThePlacedFile(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		tamper  func(data []byte) []byte
		commits bool
	}{
		{"the session's bytes", nil, true},
		{"a byte changed", func(d []byte) []byte { d[0] ^= 1; return d }, false},
		{"bytes lost", func(d []byte) []byte { return d[:len(d)-1] }, false},
		{"bytes added", func(d []byte) []byte { return append(d, '!') }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			id := e.start(e.a, asUser("u1"), uploadSpec("a.txt", SurfaceFirebase))
			if _, err := e.chunk(e.a, "a.txt", id, 0, "hello world", false); err != nil {
				t.Fatal(err)
			}
			crashAt("finalize.promoted")
			if !crashed(func() { _, _ = e.chunk(e.a, "a.txt", id, 11, "", true) }) {
				t.Fatal("no crash")
			}
			e.restart()
			var key string
			if err := e.pool.QueryRow(ctx, `SELECT blob_key FROM storage.upload_sessions`).Scan(&key); err != nil {
				t.Fatal(err)
			}
			if c.tamper != nil {
				e.store.mu.Lock()
				e.store.files[key].data = c.tamper(e.store.files[key].data)
				e.store.mu.Unlock()
			}
			got, err := e.chunk(e.a, "a.txt", id, 11, "", true)
			if c.commits {
				if err != nil || got.Status != UploadFinal || e.read(e.a, "a.txt") != "hello world" {
					t.Fatalf("the resumed finalize: %+v, %v", got, err)
				}
				return
			}
			if !errors.Is(err, ErrUnavailable) || got.Status != UploadActive {
				t.Fatalf("a file that is not the session's: %+v, %v; want ErrUnavailable and active", got, err)
			}
			if e.count(`SELECT count(*) FROM storage.objects WHERE name = 'a.txt'`) != 0 || !e.store.has(key) {
				t.Error("the wrong file was committed, or deleted")
			}
			if q, err := e.s.QueryUpload(ctx, e.a, "a.txt", id); err != nil || q.Status != UploadActive {
				t.Errorf("the session after the refusal: %+v, %v", q, err)
			}
			e.checkUsage(projectA)
		})
	}
}
