package core

import (
	"context"
	"crypto/md5"
	"errors"
	"hash/crc32"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/arumadev/aruma/internal/platform/rules"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// The rules' binding (ADR-022 §8), decided by real compiled rulesets: every operation's method,
// path, resource and request.

var (
	rulesBucket = Bucket{Project: testProject, Name: "chat-app.aruma.test"}
	rulesNow    = time.Date(2026, 10, 24, 9, 30, 0, 123e6, time.UTC)
	ruleAlice   = Principal{Kind: PrincipalUser, UID: "alice", Token: map[string]any{"sub": "alice", "email": "alice@example.com", "admin": true, "level": 2.0}}
	bob         = Principal{Kind: PrincipalUser, UID: "bob", Token: map[string]any{"sub": "bob"}}
	anonymous   = Principal{Kind: PrincipalAnonymous}
)

// releaseProjects is the control plane with one compiled release.
type releaseProjects struct {
	fakeProjects
	rs  *rules.Ruleset
	err error
	// asked records the releases read, by project.
	asked map[int64][]string
}

func (f *releaseProjects) CompiledRelease(ctx context.Context, project int64, release string) (*rules.Ruleset, error) {
	if f.asked != nil {
		f.asked[project] = append(f.asked[project], release)
	}
	return f.rs, f.err
}

func compileStorage(t *testing.T, src string) *rules.Ruleset {
	t.Helper()
	rs, issues := rules.Compile(rules.Storage, rules.File{Name: "storage.rules", Content: src}, rules.DefaultLimits())
	if rs == nil {
		t.Fatalf("the rules do not compile: %v\n%s", issues, src)
	}
	return rs
}

// storageRules wraps match blocks in the service and the bucket's match, rules_version 2.
func storageRules(body string) string {
	return "rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n" + body + "\n  }\n}\n"
}

func rulesService(t *testing.T, src string) *Service {
	t.Helper()
	return &Service{
		projects:  &releaseProjects{rs: compileStorage(t, src), asked: map[int64][]string{}},
		documents: rules.NoDocuments{}, inflight: rules.NewInflight(64 << 20), metrics: NewMetrics(),
		log: slog.New(slog.DiscardHandler), now: func() time.Time { return rulesNow },
	}
}

// authz decides op as an operation does: prepareRules, then authorize with its context.
func authz(s *Service, ctx context.Context, b Bucket, p Principal, op ruleOp) error {
	ctx, err := s.prepareRules(ctx, b, p)
	if err != nil {
		return err
	}
	return s.authorize(ctx, b, p, op)
}

func storedObject(name string) *Object {
	return &Object{
		Bucket: rulesBucket.Name, Name: name, Generation: 1700000000000000, Metageneration: 1, Size: 1024,
		MD5: md5.Sum([]byte("x")), CRC32C: crc32.Checksum([]byte("x"), crc32.MakeTable(crc32.Castagnoli)),
		ContentType: "image/png", Metadata: map[string]string{"owner": "alice"},
		Created: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Updated: time.Date(2026, 1, 3, 3, 4, 5, 0, time.UTC),
	}
}

func str(s string) *string { return &s }

const operationRules = `
    match /users/{uid}/{file=**} {
      allow get: if request.auth != null && request.auth.uid == uid;
      allow list: if request.auth.uid == uid;
      allow create: if request.auth.uid == uid && request.resource.size < 2048
        && request.resource.contentType.matches('image/.*');
      allow update: if request.auth.uid == uid && request.resource.metadata.owner == uid;
      allow delete: if resource.metadata.owner == request.auth.uid;
    }
    match /public/{file} { allow read; }
    match /missing/{file} { allow get, delete: if resource == null; }
    match /verbs/{file} {
      allow create: if request.method == 'create' && resource.size == 1024 && request.resource.size == 5;
      allow update: if true;
    }
    match /params/{file} {
      allow get: if request.params.x == 'y';
      allow list: if !('x' in request.params) && request.params.size() == 0;
    }
    match /paths/{a}/{b} {
      allow get: if request.path == /b/$(bucket)/o/paths/$(a)/$(b) && bucket == 'chat-app.aruma.test';
    }
    match /time/{file} { allow get: if request.time.toMillis() == 1792834200123; }
    match /claims/{file} { allow get: if request.auth.token.admin == true && request.auth.token.level is int; }
    match /fs/{file} {
      allow get: if firestore.exists(/databases/(default)/documents/users/$(request.auth.uid));
      allow list: if firestore.get(/databases/(default)/documents/x/y).data.ok || true;
    }
    match /nulls/{file} {
      allow get: if request.resource == null;
      allow list: if request.resource == null && resource == null;
      allow delete: if request.resource == null;
    }`

// Every operation of ADR-022 §8's table, for owners, strangers and anonymous clients.
func TestAuthorizeOperations(t *testing.T) {
	s := rulesService(t, storageRules(operationRules))
	ctx := context.Background()
	get := func(name string, res *Object) ruleOp {
		return ruleOp{Method: rules.MethodGet, Name: name, Resource: res, Surface: SurfaceFirebase}
	}
	list := func(prefix string) ruleOp {
		return ruleOp{Method: rules.MethodList, List: true, Name: prefix, Surface: SurfaceFirebase}
	}
	create := func(name string, res *Object, size int64, typ string) ruleOp {
		in := &Object{Bucket: rulesBucket.Name, Name: name, Size: size, ContentType: typ}
		return ruleOp{Method: rules.MethodCreate, Name: name, Resource: res, Incoming: in, Surface: SurfaceFirebase}
	}
	update := func(name string, p Patch) ruleOp {
		return ruleOp{Method: rules.MethodUpdate, Name: name, Resource: storedObject(name), Patch: &p, Surface: SurfaceFirebase}
	}
	del := func(name string, res *Object) ruleOp {
		return ruleOp{Method: rules.MethodDelete, Name: name, Resource: res, Surface: SurfaceFirebase}
	}
	for _, c := range []struct {
		name  string
		p     Principal
		op    ruleOp
		allow bool
	}{
		{"get: the owner", ruleAlice, get("users/alice/a.png", storedObject("users/alice/a.png")), true},
		{"get: deep under the owner", ruleAlice, get("users/alice/x/y/z.png", nil), true},
		{"get: a stranger", bob, get("users/alice/a.png", storedObject("users/alice/a.png")), false},
		{"get: anonymous", anonymous, get("users/alice/a.png", storedObject("users/alice/a.png")), false},
		// The rules decide before existence: allowed on a missing object (the surface answers 404),
		// denied on one whatever exists.
		{"get: allowed on a missing object", ruleAlice, get("users/alice/none.png", nil), true},
		{"get: denied on a missing object", bob, get("users/alice/none.png", nil), false},
		{"get: public", anonymous, get("public/x", nil), true},
		{"get: resource null when missing", anonymous, get("missing/x", nil), true},
		{"get: resource set when stored", anonymous, get("missing/x", storedObject("missing/x")), false},
		{"get: no match", ruleAlice, get("elsewhere/x", nil), false},
		{"get: request.path", anonymous, get("paths/p/q", nil), true},
		{"get: request.time", anonymous, get("time/x", nil), true},
		{"get: request.auth.token", ruleAlice, get("claims/x", nil), true},
		{"get: no token for bob's claims", bob, get("claims/x", nil), false},
		{"get: request.params is empty", anonymous, get("params/x", nil), false},
		{"get: firestore.exists denies until v0.4", ruleAlice, get("fs/x", nil), false},
		{"get: request.resource is null", anonymous, get("nulls/x", storedObject("nulls/x")), true},

		{"list: the owner's folder", ruleAlice, list("users/alice/"), true},
		{"list: a folder under it", ruleAlice, list("users/alice/x/"), true},
		{"list: a stranger", bob, list("users/alice/"), false},
		{"list: the parent, whose child is unknown", ruleAlice, list("users/"), false},
		{"list: the root", ruleAlice, list(""), false},
		{"list: public, read covers list", anonymous, list("public/"), true},
		{"list: a prefix without a slash is its folder's", anonymous, list("public/ab"), true},
		{"list: public is not its own child", anonymous, list("public"), false},
		{"list: params", anonymous, list("params/"), true},
		{"list: an absorbed lookup", anonymous, list("fs/"), true},
		{"list: no resource", anonymous, list("nulls/"), true},

		{"create: the owner", ruleAlice, create("users/alice/b.png", nil, 1000, "image/png"), true},
		{"create: too large", ruleAlice, create("users/alice/b.png", nil, 4096, "image/png"), false},
		{"create: not an image", ruleAlice, create("users/alice/b.png", nil, 1000, "text/plain"), false},
		{"create: the default type is not an image", ruleAlice, create("users/alice/b.png", nil, 1000, ""), false},
		{"create: anonymous", anonymous, create("users/alice/b.png", nil, 1000, "image/png"), false},
		// An upload over an object is a create, with the stored object as resource (PY-RR).
		{"create over a stored object", anonymous, create("verbs/x", storedObject("verbs/x"), 5, ""), true},
		{"create over nothing reads resource.size", anonymous, create("verbs/x", nil, 5, ""), false},

		{"update: the owner keeps the owner", ruleAlice, update("users/alice/a.png", Patch{Metadata: map[string]*string{"k": str("v")}}), true},
		{"update: gives it away", ruleAlice, update("users/alice/a.png", Patch{Metadata: map[string]*string{"owner": str("bob")}}), false},
		{"update: deletes the owner", ruleAlice, update("users/alice/a.png", Patch{Metadata: map[string]*string{"owner": nil}}), false},
		{"update: clears the metadata", ruleAlice, update("users/alice/a.png", Patch{ClearMetadata: true}), false},
		{"update: clears and sets", ruleAlice, update("users/alice/a.png", Patch{ClearMetadata: true, Metadata: map[string]*string{"owner": str("alice")}}), true},
		{"update: a stranger", bob, update("users/alice/a.png", Patch{}), false},
		{"update is not create", anonymous, ruleOp{Method: rules.MethodCreate, Name: "verbs/y", Resource: storedObject("verbs/y"),
			Incoming: &Object{Name: "verbs/y", Size: 6}, Surface: SurfaceFirebase}, false},

		{"delete: the owner", ruleAlice, del("users/alice/a.png", storedObject("users/alice/a.png")), true},
		{"delete: a stranger", bob, del("users/alice/a.png", storedObject("users/alice/a.png")), false},
		{"delete: nothing stored reads resource.metadata", ruleAlice, del("users/alice/a.png", nil), false},
		{"delete: resource null when missing", anonymous, del("missing/x", nil), true},
		{"delete: request.resource is null", anonymous, del("nulls/x", storedObject("nulls/x")), true},

		// What the binding refuses to build.
		{"a method out of range", ruleAlice, ruleOp{Method: 0, Name: "public/x"}, false},
		{"List on a get", anonymous, ruleOp{Method: rules.MethodGet, List: true, Name: "public/x"}, false},
	} {
		err := authz(s, ctx, rulesBucket, c.p, c.op)
		if c.allow && err != nil || !c.allow && err != ErrPermissionDenied {
			t.Errorf("%s: %v, want allow %v", c.name, err, c.allow)
		}
	}
	// Admins bypass the rules, even a rule that denies all.
	deny := rulesService(t, storageRules("match /{all=**} { allow read, write: if false; }"))
	if err := authz(deny, ctx, rulesBucket, Principal{Kind: PrincipalAdmin}, get("a", nil)); err != nil {
		t.Errorf("an admin: %v", err)
	}
	if err := authz(deny, ctx, rulesBucket, ruleAlice, get("a", nil)); err != ErrPermissionDenied {
		t.Errorf("a user under the locked rules: %v", err)
	}
	// The package's tests may still decide through the hook, with prepareRules called.
	deny.authorizeHook = func(context.Context, Bucket, Principal, ruleOp) error { return nil }
	if err := authz(deny, ctx, rulesBucket, anonymous, get("a", nil)); err != nil {
		t.Errorf("with a hook: %v", err)
	}
	if err := deny.authorize(ctx, rulesBucket, anonymous, get("a", nil)); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a hook without prepareRules: %v, want ErrUnavailable", err)
	}
}

// An operation that forgets prepareRules fails loudly: authorize logs it, answers
// ErrUnavailable, and reads nothing from the control plane.
func TestAuthorizeWithoutPrepareRulesFails(t *testing.T) {
	ctx := context.Background()
	var logs strings.Builder
	s := rulesService(t, storageRules("match /{all=**} { allow read; }"))
	s.log = slog.New(slog.NewTextHandler(&logs, nil))
	op := ruleOp{Method: rules.MethodGet, Name: "a"}
	for _, p := range []Principal{anonymous, ruleAlice} {
		if err := s.authorize(ctx, rulesBucket, p, op); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrPermissionDenied) {
			t.Errorf("unprepared: %v, want ErrUnavailable", err)
		}
	}
	if asked := s.projects.(*releaseProjects).asked; len(asked) != 0 {
		t.Errorf("authorize read the control plane: %v", asked)
	}
	if !strings.Contains(logs.String(), "authorize without prepareRules") || !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("no error logged: %s", logs.String())
	}
	// Prepared for another bucket is not prepared for this one.
	other, err := s.prepareRules(ctx, Bucket{Project: 7, Name: "x"}, anonymous)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.authorize(other, rulesBucket, anonymous, op); !errors.Is(err, ErrUnavailable) {
		t.Errorf("prepared for another project: %v", err)
	}
	// Admins bypass the rules, prepared or not.
	if err := s.authorize(ctx, rulesBucket, Principal{Kind: PrincipalAdmin}, op); err != nil {
		t.Errorf("an admin: %v", err)
	}
}

// The release read is the project's Storage release under its logical name (ADR-022 §2).
func TestAuthorizeReadsTheLogicalRelease(t *testing.T) {
	s := rulesService(t, storageRules("match /{all=**} { allow read; }"))
	if err := authz(s, context.Background(), rulesBucket, anonymous, ruleOp{Method: rules.MethodGet, Name: "a"}); err != nil {
		t.Fatal(err)
	}
	asked := s.projects.(*releaseProjects).asked
	if len(asked) != 1 || len(asked[testProject]) != 1 || asked[testProject][0] != tcore.StorageRelease {
		t.Errorf("read %v, want %s of project %d", asked, tcore.StorageRelease, testProject)
	}
	if tcore.StorageRelease != "firebase.storage/default" {
		t.Errorf("the logical name is %q", tcore.StorageRelease)
	}
}

// list is denied under rules_version 1, whatever the rules say (ADR-022 §8).
func TestListNeedsRulesVersion2(t *testing.T) {
	s := rulesService(t, "service firebase.storage {\n  match /b/{bucket}/o {\n    match /{all=**} { allow read; }\n  }\n}\n")
	ctx := context.Background()
	if err := authz(s, ctx, rulesBucket, ruleAlice, ruleOp{Method: rules.MethodGet, Name: "a/b"}); err != nil {
		t.Errorf("get under version 1: %v", err)
	}
	if err := authz(s, ctx, rulesBucket, ruleAlice, ruleOp{Method: rules.MethodList, List: true, Name: "a/"}); err != ErrPermissionDenied {
		t.Errorf("list under version 1: %v", err)
	}
}

// What is not a denial is never 403: the control plane's outage, an engine overloaded, a
// cancelled request (ADR-022 §8, ADR-021 §2.2).
func TestAuthorizeOutagesAreNotDenials(t *testing.T) {
	ctx := context.Background()
	op := ruleOp{Method: rules.MethodGet, Name: "a.png"}
	s := rulesService(t, storageRules("match /{all=**} { allow read: if ('a' + 'b') == 'ab'; }"))
	if err := authz(s, ctx, rulesBucket, anonymous, op); err != nil {
		t.Fatalf("allowed: %v", err)
	}

	s.inflight = rules.NewInflight(1)
	if err := authz(s, ctx, rulesBucket, anonymous, op); err != ErrOverloaded {
		t.Errorf("an overloaded engine: %v, want ErrOverloaded", err)
	}
	if n := counted(t, s.metrics.RulesDecisions.WithLabelValues("overloaded")); n != 1 {
		t.Errorf("%v overloaded decisions counted", n)
	}

	s.inflight = rules.NewInflight(64 << 20)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err := authz(s, cancelled, rulesBucket, anonymous, op)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled request: %v", err)
	}

	boom := errors.New("connection refused")
	s.projects = &releaseProjects{err: boom}
	err = authz(s, ctx, rulesBucket, anonymous, op)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, boom) || errors.Is(err, ErrPermissionDenied) {
		t.Errorf("the control plane down: %v", err)
	}
	// A control plane that answers no ruleset denies.
	s.projects = &releaseProjects{}
	if err := authz(s, ctx, rulesBucket, anonymous, op); err != ErrPermissionDenied {
		t.Errorf("no ruleset: %v", err)
	}
	if n := counted(t, s.metrics.RulesDecisions.WithLabelValues("allow")); n != 1 {
		t.Errorf("%v allow decisions counted", n)
	}
	if n := counted(t, s.metrics.RulesDecisions.WithLabelValues("deny")); n != 1 {
		t.Errorf("%v deny decisions counted", n)
	}
	var m dto.Metric
	if err := s.metrics.RulesSeconds.Write(&m); err != nil || m.GetHistogram().GetSampleCount() != 4 {
		t.Errorf("RulesSeconds holds %d decisions, %v; want 4 (the outage fails in prepareRules)", m.GetHistogram().GetSampleCount(), err)
	}
}

// counted reads a counter.
func counted(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

// countingDocuments holds documents that exist, and counts the reads.
type countingDocuments struct {
	exist map[string]bool
	reads int
}

func (d *countingDocuments) Get(_ context.Context, project int64, path []string, _ bool) (rules.Value, bool, error) {
	d.reads++
	if project != testProject {
		return rules.Value{}, false, errors.New("another project's documents")
	}
	if d.exist[strings.Join(path, "/")] {
		return rules.Map(map[string]rules.Value{"data": rules.Map(nil)}), true, nil
	}
	return rules.Value{}, false, nil
}

// Storage's lookups are two per evaluation, as the engine counts them for a Storage ruleset;
// rules.NoDocuments, the Documents of v0.3, makes every lookup an error.
func TestStorageLookups(t *testing.T) {
	ctx := context.Background()
	s := rulesService(t, storageRules(`
    match /two/{f} { allow get: if firestore.exists(/databases/(default)/documents/c/a) && firestore.exists(/databases/(default)/documents/c/b); }
    match /three/{f} { allow get: if firestore.exists(/databases/(default)/documents/c/a) && firestore.exists(/databases/(default)/documents/c/b)
      && firestore.exists(/databases/(default)/documents/c/c); }`))
	docs := &countingDocuments{exist: map[string]bool{
		"databases/(default)/documents/c/a": true, "databases/(default)/documents/c/b": true, "databases/(default)/documents/c/c": true,
	}}
	s.documents = docs
	if err := authz(s, ctx, rulesBucket, anonymous, ruleOp{Method: rules.MethodGet, Name: "two/x"}); err != nil || docs.reads != 2 {
		t.Errorf("two lookups: %v after %d reads", err, docs.reads)
	}
	if err := authz(s, ctx, rulesBucket, anonymous, ruleOp{Method: rules.MethodGet, Name: "three/x"}); err != ErrPermissionDenied {
		t.Errorf("three lookups: %v", err)
	}
	s.documents = rules.NoDocuments{}
	if err := authz(s, ctx, rulesBucket, anonymous, ruleOp{Method: rules.MethodGet, Name: "two/x"}); err != ErrPermissionDenied {
		t.Errorf("NoDocuments: %v", err)
	}
}

// The match path: the bucket's, then the name the surface decoded once, split on "/" and never
// decoded again; a list's folder, then an unknown segment.
func TestRulePaths(t *testing.T) {
	for _, c := range []struct {
		name  string
		list  bool
		match string // segments joined by |, ? for the unknown one
		path  string
	}{
		{"a.png", false, "b|bk|o|a.png", "b|bk|o|a.png"},
		{"a/b/c.png", false, "b|bk|o|a|b|c.png", "b|bk|o|a|b|c.png"},
		{"a%2Fb", false, "b|bk|o|a%2Fb", "b|bk|o|a%2Fb"}, // a name holding %2F stays one segment
		{"a+b c", false, "b|bk|o|a+b c", "b|bk|o|a+b c"},
		{"a//b/", false, "b|bk|o|a||b|", "b|bk|o|a||b|"},
		{"/a", false, "b|bk|o||a", "b|bk|o||a"},
		{"é/ü", false, "b|bk|o|é|ü", "b|bk|o|é|ü"},
		{"", true, "b|bk|o|?", "b|bk|o"},
		{"p/", true, "b|bk|o|p|?", "b|bk|o|p"},
		{"p/a/", true, "b|bk|o|p|a|?", "b|bk|o|p|a"},
		{"p/a", true, "b|bk|o|p|?", "b|bk|o|p"},
		{"p", true, "b|bk|o|?", "b|bk|o"},
		{"/", true, "b|bk|o||?", "b|bk|o|"},
		{"p//", true, "b|bk|o|p||?", "b|bk|o|p|"},
	} {
		match, path := rulePaths("bk", c.name, c.list)
		var segs []string
		for _, s := range match {
			n, ok := s.Name()
			if !ok {
				n = "?"
			}
			segs = append(segs, n)
		}
		if got := strings.Join(segs, "|"); got != c.match {
			t.Errorf("%q list %v: match %s, want %s", c.name, c.list, got, c.match)
		}
		if got := strings.Join(path, "|"); got != c.path {
			t.Errorf("%q list %v: path %s, want %s", c.name, c.list, got, c.path)
		}
	}
}

// A name of more segments than the engine takes is denied, not an outage the SDKs would retry.
func TestLongPathsAreDenied(t *testing.T) {
	s := rulesService(t, storageRules("match /{all=**} { allow read; }"))
	ctx := context.Background()
	name := strings.Repeat("/", maxNameBytes-1) + "x"
	if err := validateName(name); err != nil {
		t.Fatal(err)
	}
	if err := authz(s, ctx, rulesBucket, anonymous, ruleOp{Method: rules.MethodGet, Name: name}); err != ErrPermissionDenied {
		t.Errorf("1024 segments: %v", err)
	}
	if err := authz(s, ctx, rulesBucket, anonymous, ruleOp{Method: rules.MethodGet, Name: strings.Repeat("a/", 500) + "x"}); err != nil {
		t.Errorf("501 segments: %v", err)
	}
	// The limit is the ruleset's own request_segments, as configured.
	lim := rules.DefaultLimits()
	lim.RequestSegments = 10
	low, issues := rules.Compile(rules.Storage, rules.File{Name: "storage.rules", Content: storageRules("match /{all=**} { allow read; }")}, lim)
	if low == nil {
		t.Fatal(issues)
	}
	s.projects = &releaseProjects{rs: low}
	if err := authz(s, ctx, rulesBucket, anonymous, ruleOp{Method: rules.MethodGet, Name: "1/2/3/4/5/6/7/8"}); err != ErrPermissionDenied {
		t.Errorf("11 segments under a limit of 10: %v, want a denial, not 503", err)
	}
	if err := authz(s, ctx, rulesBucket, anonymous, ruleOp{Method: rules.MethodGet, Name: "1/2/3/4/5/6/7"}); err != nil {
		t.Errorf("10 segments under a limit of 10: %v", err)
	}
}

// prepareRules resolves a bucket's ruleset once, before a locked transaction; authorize then
// decides with it and asks the control plane nothing, for each bucket of a request (Rewrite has
// two).
func TestPrepareRules(t *testing.T) {
	ctx := context.Background()
	s := rulesService(t, storageRules("match /{all=**} { allow read; }"))
	other := Bucket{Project: testProject + 1, Name: "other-app.aruma.test"}
	op := ruleOp{Method: rules.MethodGet, Name: "a"}
	pctx, err := s.prepareRules(ctx, rulesBucket, anonymous)
	if err != nil {
		t.Fatal(err)
	}
	if pctx, err = s.prepareRules(pctx, other, anonymous); err != nil {
		t.Fatal(err)
	}
	if again, err := s.prepareRules(pctx, other, anonymous); err != nil || again != pctx {
		t.Errorf("preparing a bucket twice: %v", err)
	}
	fp := s.projects.(*releaseProjects)
	if len(fp.asked[testProject]) != 1 || len(fp.asked[testProject+1]) != 1 {
		t.Fatalf("asked %v", fp.asked)
	}
	// The control plane goes down: the prepared request still decides, and asks nothing.
	fp.err, fp.rs = errors.New("connection refused"), nil
	for _, b := range []Bucket{rulesBucket, other} {
		if err := s.authorize(pctx, b, anonymous, op); err != nil {
			t.Errorf("%s with its prepared ruleset: %v", b.Name, err)
		}
	}
	if len(fp.asked[testProject]) != 1 || len(fp.asked[testProject+1]) != 1 {
		t.Errorf("authorize asked the control plane again: %v", fp.asked)
	}
	// A bucket not prepared is read as before; here the outage shows.
	if err := s.authorize(pctx, Bucket{Project: 7, Name: "x"}, anonymous, op); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a bucket not prepared: %v", err)
	}
	if _, err := s.prepareRules(ctx, rulesBucket, anonymous); !errors.Is(err, ErrUnavailable) {
		t.Errorf("preparing during an outage: %v", err)
	}
	// An admin, and a hook, get the marker without a read.
	fp.asked = map[int64][]string{}
	got, err := s.prepareRules(ctx, rulesBucket, Principal{Kind: PrincipalAdmin})
	if _, ok := preparedRuleset(got, rulesBucket.Project); err != nil || !ok || len(fp.asked) != 0 {
		t.Errorf("an admin: %v, marked %v, asked %v", err, ok, fp.asked)
	}
	s.authorizeHook = func(context.Context, Bucket, Principal, ruleOp) error { return nil }
	got, err = s.prepareRules(ctx, rulesBucket, anonymous)
	if _, ok := preparedRuleset(got, rulesBucket.Project); err != nil || !ok || len(fp.asked) != 0 {
		t.Errorf("under the hook: %v, marked %v, asked %v", err, ok, fp.asked)
	}
}
