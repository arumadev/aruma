package core

import (
	"errors"
	"testing"
	"time"
)

// Once Close has started, the shutdown gate refuses every new write: Update, Delete, SetCORS,
// create_token, delete_token and the /v0 mint as well as the uploads, so nothing begins a
// transaction while the lock is being released. A plain read still answers.
func TestChangesRefusedOnceCloseStarts(t *testing.T) {
	e := newEnv(t)
	o := e.insert(e.a, "g/a", "abc") // through GCS: no token yet, so a /v0 read would mint one
	leave, err := e.s.enter()        // a write in flight: Close waits for it
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- e.s.Close() }()
	for deadline := time.Now().Add(10 * time.Second); ; {
		e.s.gate.mu.Lock()
		closing := e.s.gate.closing
		e.s.gate.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Close did not start")
		}
		time.Sleep(time.Millisecond)
	}

	ctx, admin := t.Context(), asAdmin()
	for name, do := range map[string]func() error{
		"update": func() error {
			_, err := e.s.Update(ctx, e.a, o.Name, admin, SurfaceGCS,
				Patch{Fields: map[string]*string{FieldContentType: new("text/plain")}}, Preconditions{})
			return err
		},
		"delete": func() error { return e.s.Delete(ctx, e.a, o.Name, admin, SurfaceGCS, Preconditions{}) },
		"create_token": func() error {
			_, err := e.s.CreateToken(ctx, e.a, o.Name, admin)
			return err
		},
		"delete_token": func() error {
			_, err := e.s.DeleteToken(ctx, e.a, o.Name, admin, "not-a-token-of-it")
			return err
		},
		"the /v0 mint": func() error {
			_, err := e.s.Stat(ctx, e.a, o.Name, asUser("alice"), SurfaceFirebase, 0)
			return err
		},
		"set CORS": func() error {
			_, err := e.s.SetCORS(ctx, e.a, admin, []CORSRule{{Origins: []string{"*"}, Methods: []string{"GET"}}})
			return err
		},
	} {
		if err := do(); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s after Close started: %v, want the gate's ErrUnavailable", name, err)
		}
	}
	got, err := e.s.Stat(ctx, e.a, o.Name, admin, SurfaceGCS, 0)
	if err != nil || got.Metageneration != o.Metageneration || got.ContentType != o.ContentType ||
		len(got.DownloadTokens) != 0 {
		t.Errorf("the object after the refused changes: %+v, %v; want it as it was", got, err)
	}
	leave()
	if err := <-closed; err != nil {
		t.Errorf("Close: %v", err)
	}
}
