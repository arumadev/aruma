package acceptance

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/rules"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// S2-E's Storage isolation cases (ADR-022 §3); the file is S2-E's alone.

// A project's Storage ruleset is never applied to another project's bucket: each project's
// release is its own, and neither owner reaches the other's rulesets or releases, nor points a
// release at the other's ruleset (ADR-021 §7). The server's own control plane answers.
func TestIsolationStorageRuleset(t *testing.T) {
	ie := newIsolation(t)
	ctx := context.Background()
	svc := ie.App.Service
	principal := func(email string) *authn.Principal {
		var id uuid.UUID
		if err := ie.Pool.QueryRow(ctx, `SELECT id FROM control.accounts WHERE email = $1`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return &authn.Principal{Kind: authn.KindAccount, AccountID: id}
	}
	pa, pb := principal("owner@example.com"), principal("b@example.com")
	a, err := svc.GetProject(ctx, pa, "alpha-app")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.GetProject(ctx, pb, "bravo-app")
	if err != nil {
		t.Fatal(err)
	}
	open := []rules.File{{Name: "storage.rules", Content: "rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n    match /{all=**} { allow read, write: if true; }\n  }\n}\n"}}
	rsA, _, err := svc.CreateRuleset(ctx, pa, "alpha-app", rules.Storage, open)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PublishRelease(ctx, pa, "alpha-app", "firebase.storage/"+a.StorageBucket, rsA.Name); err != nil {
		t.Fatal(err)
	}
	rsB, _, err := svc.CreateRuleset(ctx, pb, "bravo-app", rules.Storage, open)
	if err != nil {
		t.Fatal(err)
	}

	// A's rules open A's bucket; B's bucket keeps the locked default.
	decide := func(project int64, bucket string) bool {
		t.Helper()
		rs, err := svc.CompiledRelease(ctx, project, tcore.StorageRelease)
		if err != nil {
			t.Fatal(err)
		}
		d, err := rs.Evaluate(ctx, &rules.Request{Project: project, Method: rules.MethodGet, Path: rules.Literals("b", bucket, "o", "a.png"),
			Variables: map[string]rules.Value{"request": rules.Map(nil), "resource": rules.Null()},
			Documents: rules.NoDocuments{}, Budget: rules.NewBudget(), Inflight: rules.NewInflight(1 << 20)})
		if err != nil {
			t.Fatal(err)
		}
		return d.Allow
	}
	if !decide(a.Number, a.StorageBucket) {
		t.Error("A's rules do not apply to A's bucket")
	}
	if decide(b.Number, b.StorageBucket) {
		t.Error("B's bucket answers to A's rules")
	}

	// Neither owner reaches the other's rules, by id or by number, and both answers are
	// NOT_FOUND.
	for name, err := range map[string]error{
		"A creates a ruleset on B": func() error {
			_, _, err := svc.CreateRuleset(ctx, pa, "bravo-app", rules.Storage, open)
			return err
		}(),
		"A reads B's release":                 errOf(svc.GetRelease(ctx, pa, "bravo-app", "firebase.storage/"+b.StorageBucket)),
		"A reads it by number":                errOf(svc.GetRelease(ctx, pa, strconv.FormatInt(b.Number, 10), "firebase.storage/"+b.StorageBucket)),
		"A publishes on B":                    errOf(svc.PublishRelease(ctx, pa, "bravo-app", "firebase.storage/"+b.StorageBucket, rsA.Name)),
		"A points its release at B's ruleset": errOf(svc.PublishRelease(ctx, pa, "alpha-app", "firebase.storage/"+a.StorageBucket, rsB.Name)),
		"B points its release at A's ruleset": errOf(svc.PublishRelease(ctx, pb, "bravo-app", "firebase.storage/"+b.StorageBucket, rsA.Name)),
		"B publishes A's bucket's release":    errOf(svc.PublishRelease(ctx, pb, "bravo-app", "firebase.storage/"+a.StorageBucket, rsB.Name)),
	} {
		var e *apierr.Error
		if !errors.As(err, &e) || e.Code != apierr.NotFound {
			t.Errorf("%s: %v, want NOT_FOUND", name, err)
		}
	}
	if decide(b.Number, b.StorageBucket) {
		t.Error("B's bucket opened after the refused requests")
	}
}

func errOf(_ tcore.Release, err error) error { return err }
