package core

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/store/storetest"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// A ruleset deployed through the control plane decides Storage's requests, from the first
// publish on, with no restart: tenancy saves and publishes it under the logical name, and the
// binding reads it there (ADR-021 §7, ADR-022 §2, §8). Until then the locked default denies.
func TestRulesDeployedThroughTenancyDecide(t *testing.T) {
	pool := storetest.NewPool(t)
	ctx := context.Background()
	tsvc := tcore.New(pool, nil, nil, tcore.Options{ProjectDomain: "aruma.test", PublicURL: "http://127.0.0.1",
		RulesLimits: rules.DefaultLimits()}, slog.New(slog.DiscardHandler))
	owner := &authn.Principal{Kind: authn.KindAccount, AccountID: uuid.New()}
	if _, err := pool.Exec(ctx, `INSERT INTO control.accounts (id, email, password_hash) VALUES ($1, 'o@example.com', 'x')`, owner.AccountID); err != nil {
		t.Fatal(err)
	}
	proj, err := tsvc.CreateProject(ctx, owner, tcore.NewProject{ID: "chat-app", DisplayName: "Chat"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{projects: tsvc, documents: rules.NoDocuments{}, inflight: rules.NewInflight(64 << 20), metrics: NewMetrics(),
		log: slog.New(slog.DiscardHandler), now: time.Now}
	b, err := s.Bucket(ctx, proj.StorageBucket)
	if err != nil {
		t.Fatal(err)
	}
	get := ruleOp{Method: rules.MethodGet, Name: "users/alice/a.png", Resource: storedObject("users/alice/a.png"), Surface: SurfaceFirebase}
	create := ruleOp{Method: rules.MethodCreate, Name: "users/alice/b.png", Surface: SurfaceFirebase,
		Incoming: &Object{Bucket: b.Name, Name: "users/alice/b.png", Size: 10, ContentType: "image/png"}}

	if err := authz(s, ctx, b, ruleAlice, get); err != ErrPermissionDenied {
		t.Errorf("before any deploy: %v, want the locked default's denial", err)
	}
	deploy := func(src string) {
		t.Helper()
		rs, issues, err := tsvc.CreateRuleset(ctx, owner, proj.ID, rules.Storage, []rules.File{{Name: "storage.rules", Content: src}})
		if err != nil {
			t.Fatalf("CreateRuleset: %v %v", err, issues)
		}
		if _, err := tsvc.PublishRelease(ctx, owner, proj.ID, "firebase.storage/"+proj.StorageBucket, rs.Name); err != nil {
			t.Fatal(err)
		}
	}
	deploy(storageRules(`
    match /users/{uid}/{file=**} {
      allow read: if request.auth.uid == uid;
      allow create: if request.auth.uid == uid && request.resource.contentType.matches('image/.*');
    }`))
	for _, c := range []struct {
		p    Principal
		op   ruleOp
		want error
	}{
		{ruleAlice, get, nil}, {bob, get, ErrPermissionDenied}, {anonymous, get, ErrPermissionDenied},
		{ruleAlice, create, nil}, {bob, create, ErrPermissionDenied},
		{ruleAlice, ruleOp{Method: rules.MethodList, List: true, Name: "users/alice/"}, nil},
		{ruleAlice, ruleOp{Method: rules.MethodDelete, Name: "users/alice/a.png", Resource: storedObject("users/alice/a.png")}, ErrPermissionDenied},
	} {
		if err := authz(s, ctx, b, c.p, c.op); err != c.want {
			t.Errorf("%s %s of %s: %v, want %v", c.p.UID, c.op.Method, c.op.Name, err, c.want)
		}
	}
	// A new deploy takes effect on the next request.
	deploy(storageRules("match /{all=**} { allow read, write: if false; }"))
	if err := authz(s, ctx, b, ruleAlice, get); err != ErrPermissionDenied {
		t.Errorf("after the locking deploy: %v", err)
	}
}
