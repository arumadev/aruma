package core

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// The S2 integration's tests of the rules' wiring: every operation that decides resolves the
// project's ruleset (prepareRules) and asks it, so the real rules answer on each of them, with no
// authorizeHook. A path that skipped prepareRules answers ErrUnavailable, not a denial, and a
// path that skipped authorize lets the deny-all rules through: either fails here.

// wiredProjects is the env's control plane with a Storage release the test swaps.
type wiredProjects struct {
	*testProjects
	rs    atomic.Pointer[rules.Ruleset]
	asked atomic.Int64
}

func (p *wiredProjects) CompiledRelease(ctx context.Context, project int64, release string) (*rules.Ruleset, error) {
	p.asked.Add(1)
	return p.rs.Load(), nil
}

// newWiredEnv is newEnv deciding with the rules the test publishes, not a hook.
func newWiredEnv(t *testing.T) (*env, *wiredProjects) {
	t.Helper()
	wp := &wiredProjects{}
	e := newEnv(t, func(o *Options) {
		wp.testProjects = o.Projects.(*testProjects)
		o.Projects = wp
	})
	e.s.authorizeHook = nil
	return e, wp
}

var (
	denyAll  = storageRules("    match /{all=**} { allow read, write: if false; }")
	allowAll = storageRules("    match /{all=**} { allow read, write: if true; }")
)

func TestEveryOperationAsksTheRules(t *testing.T) {
	e, wp := newWiredEnv(t)
	deny, allow := compileStorage(t, denyAll), compileStorage(t, allowAll)
	for _, name := range []string{"w/stat", "w/open", "w/list/a", "w/update", "w/delete", "w/rewrite"} {
		e.insert(e.a, name, "abc")
	}
	user := asUser("alice")
	// Each operation runs under the deny-all rules, then under the allow-all ones; mode names
	// what a run may create, so a refused run leaves nothing the next one meets.
	for _, c := range []struct {
		name string
		do   func(ctx context.Context, mode string) error
		// allowed is the answer under allow-all: nil, or the existence's answer once the rules
		// let the request through.
		allowed error
	}{
		{"stat", func(ctx context.Context, _ string) error {
			_, err := e.s.Stat(ctx, e.a, "w/stat", user, SurfaceFirebase, 0)
			return err
		}, nil},
		{"stat of a missing object", func(ctx context.Context, _ string) error {
			_, err := e.s.Stat(ctx, e.a, "w/missing", user, SurfaceFirebase, 0)
			return err
		}, ErrNotFound},
		{"open", func(ctx context.Context, _ string) error {
			_, r, err := e.s.Open(ctx, e.a, "w/open", user, "", 0)
			if err == nil {
				_ = r.Close()
			}
			return err
		}, nil},
		{"list", func(ctx context.Context, _ string) error {
			_, err := e.s.List(ctx, e.a, user, ListOptions{Prefix: "w/list/", Surface: SurfaceFirebase})
			return err
		}, nil},
		{"update", func(ctx context.Context, _ string) error {
			_, err := e.s.Update(ctx, e.a, "w/update", user, SurfaceFirebase,
				Patch{Fields: map[string]*string{FieldContentType: new("text/plain")}}, Preconditions{})
			return err
		}, nil},
		{"delete", func(ctx context.Context, _ string) error {
			return e.s.Delete(ctx, e.a, "w/delete", user, SurfaceFirebase, Preconditions{})
		}, nil},
		{"insert", func(ctx context.Context, mode string) error {
			_, err := e.s.Insert(ctx, e.a, user, uploadSpec("w/insert-"+mode, SurfaceFirebase), strings.NewReader("abc"), 3)
			return err
		}, nil},
		{"upload finalize", func(ctx context.Context, mode string) error {
			name := "w/resumable-" + mode
			id, err := e.s.StartUpload(ctx, e.a, user, uploadSpec(name, SurfaceFirebase))
			if err != nil {
				t.Fatalf("start: %v", err) // start evaluates no rules (ADR-022 §8)
			}
			_, err = e.s.WriteUpload(ctx, e.a, name, id, &user,
				Chunk{Body: strings.NewReader("abc"), Length: 3, Finalize: true, Total: 3})
			return err
		}, nil},
		{"rewrite", func(ctx context.Context, mode string) error {
			_, err := e.s.Rewrite(ctx, e.a, "w/rewrite", e.a, "w/rewrite-"+mode, user, nil, Preconditions{})
			return err
		}, nil},
		{"rewrite of a missing source", func(ctx context.Context, mode string) error {
			_, err := e.s.Rewrite(ctx, e.a, "w/missing", e.a, "w/copy-"+mode, user, nil, Preconditions{})
			return err
		}, ErrNotFound},
	} {
		t.Run(c.name, func(t *testing.T) {
			wp.rs.Store(deny)
			before := wp.asked.Load()
			if err := c.do(t.Context(), "deny"); !errors.Is(err, ErrPermissionDenied) {
				t.Errorf("under deny-all: %v, want ErrPermissionDenied", err)
			}
			if wp.asked.Load() == before {
				t.Error("under deny-all: the control plane was not asked for the release")
			}
			wp.rs.Store(allow)
			if err := c.do(t.Context(), "allow"); !errors.Is(err, c.allowed) {
				t.Errorf("under allow-all: %v, want %v", err, c.allowed)
			}
		})
	}
	e.checkUsage(projectA)
}

// create_token, delete_token and the bucket's CORS are the admins' alone, as in Firebase: no
// rules let a user through, and none stop an admin.
func TestAdminOperationsIgnoreTheRules(t *testing.T) {
	e, wp := newWiredEnv(t)
	o := e.insert(e.a, "w/tokens", "abc")
	if _, err := e.s.CreateToken(t.Context(), e.a, o.Name, asAdmin()); err != nil {
		t.Fatal(err)
	}
	user, admin := asUser("alice"), asAdmin()
	ops := []struct {
		name string
		do   func(ctx context.Context, p Principal) error
	}{
		{"create_token", func(ctx context.Context, p Principal) error {
			_, err := e.s.CreateToken(ctx, e.a, o.Name, p)
			return err
		}},
		{"delete_token", func(ctx context.Context, p Principal) error {
			_, err := e.s.DeleteToken(ctx, e.a, o.Name, p, "not-a-token-of-it")
			return err
		}},
		{"set CORS", func(ctx context.Context, p Principal) error {
			_, err := e.s.SetCORS(ctx, e.a, p, []CORSRule{{Origins: []string{"*"}, Methods: []string{"GET"}}})
			return err
		}},
	}
	for _, c := range ops {
		t.Run(c.name, func(t *testing.T) {
			wp.rs.Store(compileStorage(t, allowAll))
			if err := c.do(t.Context(), user); !errors.Is(err, ErrPermissionDenied) {
				t.Errorf("a user under allow-all: %v, want ErrPermissionDenied", err)
			}
			wp.rs.Store(compileStorage(t, denyAll))
			if err := c.do(t.Context(), admin); err != nil {
				t.Errorf("an admin under deny-all: %v", err)
			}
		})
	}
}
