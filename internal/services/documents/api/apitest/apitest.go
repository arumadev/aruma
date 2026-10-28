// Package apitest builds the Document Database's service layer on a fresh Postgres database for
// the transports' tests: a control plane with an owner, two projects and a service account key,
// the documents store, and the credentials each kind of caller sends. A missing Postgres fails
// the test, never skips it.
package apitest

import (
	"cmp"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/platform/authn"
	acrypto "github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/documents/api"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/listen"
	"github.com/arumadev/aruma/internal/store/storetest"
	tenancy "github.com/arumadev/aruma/internal/tenancy/core"
)

// PublicURL is the control plane's public_url: a loopback development server, so admin
// credentials are accepted without TLS from this machine (ADR-029 §3.7).
const PublicURL = "http://127.0.0.1:8080"

// Projects of the environment: the service account's, and one it may not use. Project is the
// recordings' own project id, so production's messages, whose offsets count its characters,
// replay byte for byte.
const (
	Project      = "chatapp-8ae21"
	OtherProject = "other-app"
)

// Options vary the environment.
type Options struct {
	// DevOwnerToken accepts Bearer owner from loopback peers (documents.dev_owner_token).
	DevOwnerToken bool
	// DevOpenRules opens every database to loopback peers (documents.dev_open_rules).
	DevOpenRules bool
	// Store wraps the documents store, for tests that need to hold a call.
	Store func(documents.Store) documents.Store
	// Transactions bounds the store's transactions; the zero value is production's numbers.
	Transactions documents.TxnConfig
	// Hub configures the Listen hub; the zero value is its defaults.
	Hub listen.Options
	// Notifier wraps the hub as the store tells it of commits, for tests that lose a commit.
	Notifier func(documents.Notifier) documents.Notifier
	// Now is the usage counters' clock; nil is time.Now.
	Now func() time.Time
}

// Env is a service layer on a fresh database.
type Env struct {
	Pool    *pgxpool.Pool
	Tenancy *tenancy.Service
	Store   *documents.PGStore
	// Hub is the Listen hub, told of every commit of Store.
	Hub     *listen.Hub
	Service *api.Service
	// Reference is Service with the planner off: every query on the reference path.
	Reference *api.Service
	Owner     *authn.Principal
	Number    int64 // Project's number
	Key       tenancy.GeneratedKey
	SAEmail   string
	OwnerPAT  string // an aruma_pat_ token of the owner of both projects
	// Usage is Service's usage counters (Reference counts none), which nothing flushes but a
	// test.
	Usage *documents.Usage
}

// New returns the environment.
func New(t testing.TB, o Options) *Env {
	t.Helper()
	t.Setenv("ARUMA_TEST_REQUIRE_DB", "1")
	pool := storetest.NewPool(t)
	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		t.Fatal(err)
	}
	keys, err := acrypto.NewKeys(master)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := tenancy.New(pool, keys, acrypto.NewHasher(acrypto.Argon2Params{Time: 1, Memory: 64, Threads: 1}, 1),
		tenancy.Options{PublicURL: PublicURL, ProjectDomain: "aruma.test", RulesLimits: rules.DefaultLimits()}, log)
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO control.accounts (id, email, password_hash) VALUES ($1, 'owner@example.com', 'x')`, id); err != nil {
		t.Fatal(err)
	}
	owner := &authn.Principal{Kind: authn.KindAccount, Method: authn.MethodSession, AccountID: id}
	proj, err := svc.CreateProject(t.Context(), owner, tenancy.NewProject{ID: Project, DisplayName: Project})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateProject(t.Context(), owner, tenancy.NewProject{ID: OtherProject, DisplayName: OtherProject}); err != nil {
		t.Fatal(err)
	}
	sa, err := svc.CreateServiceAccount(t.Context(), owner, proj.ID, "firestore-admin", "Firestore admin")
	if err != nil {
		t.Fatal(err)
	}
	key, err := svc.CreateServiceAccountKey(t.Context(), owner, proj.ID, sa.ID)
	if err != nil {
		t.Fatal(err)
	}
	pat, err := svc.CreateAccessToken(t.Context(), owner, "cli", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	jwts := authn.NewServiceAccountJWTVerifier(svc, PublicURL, log)
	svc.OnServiceAccountRevoked(jwts)
	resolver := authn.NewDocuments(authn.DocumentsOptions{
		Verifier: svc, ServiceAccountJWTs: jwts, Owners: svc, PublicURL: PublicURL,
		LoopbackDeployment: true, DevOwnerToken: o.DevOwnerToken, Logger: log,
	})
	o.Hub.Log = cmp.Or(o.Hub.Log, log)
	hub := listen.NewHub(o.Hub)
	t.Cleanup(hub.Close)
	var notify documents.Notifier = hub
	if o.Notifier != nil {
		notify = o.Notifier(hub)
	}
	store := documents.NewPGStore(pool, documents.Options{Transactions: o.Transactions, Notifier: notify})
	hub.Bind(store)
	tokens := idtoken.Verifier(idTokens{proj.Number})
	var s documents.Store = store
	if o.Store != nil {
		s = o.Store(store)
	}
	usage := documents.NewUsage(pool, o.Now, log)
	return &Env{
		Pool: pool, Tenancy: svc, Store: store, Hub: hub, Owner: owner, Number: proj.Number, Key: key,
		SAEmail: sa.Email, OwnerPAT: pat.Plaintext, Usage: usage,
		Service: api.New(api.Options{Store: s, Auth: resolver, Projects: svc, DevOpenRules: o.DevOpenRules, Listen: hub, Logger: log,
			Rulesets: svc, IDTokens: tokens, Usage: usage}),
		Reference: api.New(api.Options{Store: s, Auth: resolver, Projects: svc, DevOpenRules: o.DevOpenRules, Listen: hub, Logger: log,
			Rulesets: svc, IDTokens: tokens, ReferenceOnly: true}),
	}
}

// JWT is a self-signed service account JWT as the Admin SDKs send it: RS256 with the key's
// kid, iss and sub the account's email, the datastore scope, an hour long (ADR-029 §3).
func (e *Env) JWT(t testing.TB) string {
	t.Helper()
	now := time.Now().Unix()
	return e.sign(t, map[string]any{
		"iss": e.SAEmail, "sub": e.SAEmail, "aud": "",
		"scope": authn.ScopeCloudPlatform + " " + authn.ScopeDatastore,
		"iat":   now, "exp": now + 3600,
	})
}

// ShortJWT is JWT with an exp lifetime after now, for a stream that must close at it.
func (e *Env) ShortJWT(t testing.TB, lifetime time.Duration) string {
	t.Helper()
	now := time.Now()
	return e.sign(t, map[string]any{
		"iss": e.SAEmail, "sub": e.SAEmail, "aud": "",
		"scope": authn.ScopeCloudPlatform + " " + authn.ScopeDatastore,
		"iat":   now.Unix(), "exp": now.Add(lifetime).Unix(),
	})
}

func (e *Env) sign(t testing.TB, claims map[string]any) string {
	t.Helper()
	block, _ := pem.Decode([]byte(e.Key.PrivateKeyPEM))
	if block == nil {
		t.Fatal("no PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	input := segment(t, map[string]any{"alg": "RS256", "typ": "JWT", "kid": e.Key.Key.KeyID}) + "." + segment(t, claims)
	d := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.(*rsa.PrivateKey), crypto.SHA256, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// IDToken is an end user's ID token of Project, uid user-1: an issuer under public_url's
// /issuers/, which the environment's verifier accepts (UserToken). With no release published the
// locked default denies such a user everything.
func IDToken(t testing.TB) string {
	t.Helper()
	now := time.Now().Unix()
	return segment(t, map[string]any{"alg": "RS256", "typ": "JWT"}) + "." +
		segment(t, map[string]any{"iss": PublicURL + "/issuers/" + Project, "aud": Project, "sub": "user-1", "iat": now, "exp": now + 3600}) +
		"." + fakeSignature
}

func segment(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
