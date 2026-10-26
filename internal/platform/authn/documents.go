package authn

import (
	"cmp"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
)

// The Document Database's callers (ADR-029 §2). Its surface (gRPC, REST and WebChannel) accepts
// two credentials no other surface does, a self-signed service account JWT and, on a
// development server that opted in, Bearer owner; so it authenticates here rather than through
// Authenticator. Resolve classifies the caller from the Authorization header and how the
// request arrived; Admit then checks the project the request names (§3.6), before any data is
// looked at; and a stream runs Supervise for as long as it is open (§3.8).

// DocumentsPolicy is the only policy that accepts a service account JWT or the dev owner token
// (ADR-029 §1). A test fails if any other policy sets either.
var DocumentsPolicy = Policy{AccessToken: true, ServiceToken: true, ServiceAccountJWT: true, DevOwner: true}

// RecheckInterval is how often a stream checks its caller's credential again (ADR-029 §3.8).
const RecheckInterval = VerifiedTokenTTL

// ProjectOwners tells whether an account owns a project. tenancy/core implements it.
type ProjectOwners interface {
	OwnsProject(ctx context.Context, account uuid.UUID, project int64) (bool, error)
}

// DocumentsOptions configure Documents.
type DocumentsOptions struct {
	// Verifier verifies aruma_pat_ and aruma_sat_ tokens.
	Verifier Verifier
	// ServiceAccountJWTs verifies self-signed service account JWTs.
	ServiceAccountJWTs *ServiceAccountJWTVerifier
	// Owners decides which projects an aruma_pat_ token's account may act on.
	Owners ProjectOwners
	// PublicURL is public_url: ID tokens are issued under <PublicURL>/issuers/ (ADR-004).
	PublicURL string
	// TrustedProxies is trusted_proxies: the only peers whose X-Forwarded-Proto is believed.
	TrustedProxies []netip.Prefix
	// LoopbackDeployment is set when public_url and listen are both loopback addresses
	// (config.LoopbackDeployment), so no route reaches the server from another machine.
	LoopbackDeployment bool
	// DevOwnerToken is documents.dev_owner_token: accept Bearer owner from this machine.
	// Ignored unless LoopbackDeployment is set, which config validation also requires.
	DevOwnerToken bool
	Logger        *slog.Logger
}

// Documents resolves the Document Database's callers. It is safe for concurrent use.
type Documents struct {
	opts         DocumentsOptions
	policy       Policy
	issuerPrefix string
}

// NewDocuments returns the Document Database's resolver. With the dev owner token on, it logs
// one warning, since every process on this machine may then administer every project.
func NewDocuments(opts DocumentsOptions) *Documents {
	opts.Logger = cmp.Or(opts.Logger, slog.Default())
	d := &Documents{
		opts:         opts,
		policy:       DocumentsPolicy,
		issuerPrefix: strings.TrimSuffix(opts.PublicURL, "/") + "/issuers/",
	}
	if opts.DevOwnerToken && opts.LoopbackDeployment {
		opts.Logger.Warn("documents.dev_owner_token is on: Bearer owner from this machine administers every project's Document Database")
	}
	return d
}

// Transport is how a request reached Aruma: what the plaintext rule (ADR-029 §3.7) and the
// owner token (§6) look at. Connect interceptors and REST middleware alike build it from the
// *http.Request with TransportOf, since connect's own Peer carries no TLS state.
type Transport struct {
	// TLS is set when the connection to Aruma itself is TLS.
	TLS bool
	// Peer is the connection's remote address. X-Forwarded-For plays no part.
	Peer netip.Addr
	// ForwardedProto is X-Forwarded-Proto, every value joined by ", ". It counts only when
	// Peer is a trusted proxy.
	ForwardedProto string
}

// TransportOf returns how r arrived.
func TransportOf(r *http.Request) Transport {
	return Transport{
		TLS:            r.TLS != nil,
		Peer:           peerAddr(r.RemoteAddr),
		ForwardedProto: strings.Join(r.Header.Values("X-Forwarded-Proto"), ", "),
	}
}

func peerAddr(remote string) netip.Addr {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return ip.Unmap()
}

// Role is what a caller is to the Document Database (ADR-025 §7.1).
type Role uint8

const (
	// RoleAnonymous sent no credential; the security rules decide.
	RoleAnonymous Role = iota + 1
	// RoleEndUser sent a Firebase ID token; the security rules decide.
	RoleEndUser
	// RoleAdmin bypasses the rules: a service account of the project, the project's owner, or
	// the dev owner.
	RoleAdmin
)

// Caller is an authenticated Document Database caller.
type Caller struct {
	Role Role
	// Principal is the service account (by JWT or aruma_sat_) or the owner's account (by
	// aruma_pat_) behind a RoleAdmin caller; nil for the dev owner and for end users.
	Principal *Principal
	// IsDevOwner is set for Bearer owner: an admin of every project (ADR-029 §6).
	IsDevOwner bool
	// UnverifiedIDToken is a RoleEndUser caller's ID token. Its issuer looked like Aruma's,
	// nothing more: it names its project, so the rules layer verifies it against the project
	// the request names (ADR-021) before trusting any claim.
	UnverifiedIDToken string
	// ExpiresAt is when the credential lapses and a stream must close; zero when unknown here.
	ExpiresAt time.Time

	credential string // the admin credential as sent, for Recheck
}

// Errors of the Document Database's authentication.
var (
	// errNeedsTLS refuses an admin credential sent in the clear (ADR-029 §3.7).
	errNeedsTLS = apierr.New(apierr.Unauthenticated,
		"Service account and Aruma credentials are accepted only over TLS: connect with TLS, or through a proxy listed in trusted_proxies that sets X-Forwarded-Proto: https.")
	// errOwnerRefused is ADR-029 §6's text.
	errOwnerRefused = apierr.New(apierr.Unauthenticated,
		"Bearer owner is accepted only when documents.dev_owner_token is set on a server whose public_url and listen are loopback addresses; connect with TLS to send the service account's credentials.")
	// errNotPermitted refuses a caller the project named by the request is not theirs (§3.6).
	errNotPermitted = apierr.New(apierr.PermissionDenied, "Missing or insufficient permissions.")
	// errCredentialExpired closes a stream whose credential lapsed (ADR-026 §4).
	errCredentialExpired = apierr.New(apierr.Unauthenticated, "The request's credential has expired.")
)

// ResolveHTTP is Resolve for r's Authorization header and transport.
func (d *Documents) ResolveHTTP(r *http.Request) (*Caller, error) {
	return d.Resolve(r.Context(), r.Header.Get("Authorization"), TransportOf(r))
}

// Resolve classifies the caller behind an Authorization header value (ADR-029 §2): none is
// anonymous; an ID token is an end user; a service account JWT, aruma_sat_ or aruma_pat_ is an
// admin, accepted only over a secure transport; owner is the dev owner, on a loopback
// development server that opted in. The JWTs are told apart by issuer, and once one check
// fails no other is tried. A refusal is UNAUTHENTICATED; a failed lookup comes back as it was.
func (d *Documents) Resolve(ctx context.Context, authorization string, t Transport) (*Caller, error) {
	if authorization == "" {
		return &Caller{Role: RoleAnonymous}, nil
	}
	scheme, token, ok := strings.Cut(authorization, " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return nil, errInvalidCredentials
	}
	switch {
	case token == "owner":
		return d.resolveOwner(ctx, t)
	case strings.HasPrefix(token, AccessTokenPrefix) && d.policy.AccessToken:
		return d.resolveAdmin(ctx, token, t, d.opts.Verifier.VerifyAccessToken)
	case strings.HasPrefix(token, ServiceTokenPrefix) && d.policy.ServiceToken:
		return d.resolveAdmin(ctx, token, t, d.verifyServiceToken)
	case looksLikeJWT(token):
		return d.resolveJWT(ctx, token, t)
	default:
		return nil, errInvalidCredentials
	}
}

func (d *Documents) resolveOwner(ctx context.Context, t Transport) (*Caller, error) {
	if d.policy.DevOwner && d.opts.DevOwnerToken && d.opts.LoopbackDeployment && t.Peer.IsValid() && t.Peer.IsLoopback() {
		return &Caller{Role: RoleAdmin, IsDevOwner: true}, nil
	}
	d.opts.Logger.Warn("rejected emulator owner token", "client_ip", d.clientIP(ctx, t).String())
	return nil, errOwnerRefused
}

func (d *Documents) resolveJWT(ctx context.Context, token string, t Transport) (*Caller, error) {
	if unsignedJWT(token) {
		d.opts.Logger.Warn("rejected unsigned JWT (alg none)", "client_ip", d.clientIP(ctx, t).String())
		return nil, errInvalidCredentials
	}
	var c struct {
		Iss string `json:"iss"`
	}
	_, rest, _ := strings.Cut(token, ".")
	payload, _, _ := strings.Cut(rest, ".")
	if decodeJWTSegment(payload, &c) != nil {
		return nil, errInvalidCredentials
	}
	switch {
	case strings.HasPrefix(c.Iss, d.issuerPrefix) && len(c.Iss) > len(d.issuerPrefix):
		// An ID token (ADR-004). Not an admin credential, so the plaintext rule does not apply:
		// it governs the credentials of §3.7 alone.
		return &Caller{Role: RoleEndUser, UnverifiedIDToken: token}, nil
	case isEmail(c.Iss) && d.policy.ServiceAccountJWT && d.opts.ServiceAccountJWTs != nil:
		return d.resolveAdmin(ctx, token, t, d.opts.ServiceAccountJWTs.Verify)
	default:
		return nil, errInvalidCredentials
	}
}

// resolveAdmin applies the plaintext rule, then verifies an admin credential.
func (d *Documents) resolveAdmin(ctx context.Context, token string, t Transport, verify func(context.Context, string) (*Principal, error)) (*Caller, error) {
	if !d.Secure(t) {
		d.opts.Logger.Warn("rejected a credential sent without TLS", "client_ip", d.clientIP(ctx, t).String(), "peer", t.Peer.String())
		return nil, errNeedsTLS
	}
	p, err := verify(ctx, token)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errInvalidCredentials
	}
	return &Caller{Role: RoleAdmin, Principal: p, ExpiresAt: p.ExpiresAt, credential: token}, nil
}

// verifyServiceToken verifies an aruma_sat_ token, whose registered scopes must name the
// Document Database as a JWT's scope claim must (ADR-029 §2, §3.4).
func (d *Documents) verifyServiceToken(ctx context.Context, token string) (*Principal, error) {
	p, err := d.opts.Verifier.VerifyServiceToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if p == nil || p.Kind != KindServiceAccount || !hasDocumentsScope(p.Scopes) {
		return nil, errInvalidCredentials
	}
	return p, nil
}

// Secure applies the plaintext rule (ADR-029 §3.7): a credential is accepted over TLS; without
// it, only on a loopback development server from a loopback peer, or from a trusted proxy that
// says the client spoke https. A loopback peer alone is not enough on any other server: a proxy
// on the same machine makes every client look local.
func (d *Documents) Secure(t Transport) bool {
	switch {
	case t.TLS:
		return true
	case !t.Peer.IsValid():
		return false
	case d.opts.LoopbackDeployment && t.Peer.IsLoopback():
		return true
	default:
		return t.ForwardedProto == "https" &&
			slices.ContainsFunc(d.opts.TrustedProxies, func(p netip.Prefix) bool { return p.Contains(t.Peer) })
	}
}

// Admit checks that c may act on project, the one every resource name in the request resolves
// to (ADR-029 §3.6), before any data is looked at. A service account may act on its own
// project, an aruma_pat_ account on the projects it owns, the dev owner on every project;
// another project is PERMISSION_DENIED. End users and anonymous callers are admitted to any
// project: the security rules decide what they may do.
func (d *Documents) Admit(ctx context.Context, c *Caller, project int64) error {
	switch {
	case c == nil:
		return errInvalidCredentials
	case c.Role != RoleAdmin || c.IsDevOwner:
		return nil
	case c.Principal == nil:
		return errInvalidCredentials
	}
	p := c.Principal
	switch p.Kind {
	case KindServiceAccount:
		if p.ProjectNumber == project {
			return nil
		}
	case KindAccount:
		if d.opts.Owners == nil {
			break
		}
		owns, err := d.opts.Owners.OwnsProject(ctx, p.AccountID, project)
		if err != nil {
			return apierr.From(err)
		}
		if owns {
			return nil
		}
	}
	return errNotPermitted
}

// Recheck verifies c's credential again, bypassing every cache, and admits it to project once
// more: a stream does so every RecheckInterval, so a disabled account, a deleted key or project,
// or a revoked token closes it within that interval (ADR-029 §3.8).
func (d *Documents) Recheck(ctx context.Context, c *Caller, project int64) error {
	if c == nil {
		return errInvalidCredentials
	}
	if c.Role != RoleAdmin || c.IsDevOwner {
		return nil
	}
	if c.Principal == nil || c.credential == "" {
		return errInvalidCredentials
	}
	var (
		p   *Principal
		err error
	)
	switch c.Principal.Method {
	case MethodServiceAccountJWT:
		p, err = d.opts.ServiceAccountJWTs.Recheck(ctx, c.credential)
	case MethodServiceToken:
		p, err = d.verifyServiceToken(ctx, c.credential)
	case MethodAccessToken:
		p, err = d.opts.Verifier.VerifyAccessToken(ctx, c.credential)
	default:
		return errInvalidCredentials
	}
	if err != nil {
		return err
	}
	if p == nil || p.Kind != c.Principal.Kind || p.ActorID() != c.Principal.ActorID() {
		return errInvalidCredentials
	}
	return d.Admit(ctx, c, project)
}

// Supervise watches a stream's caller while ctx lives: it returns UNAUTHENTICATED at the
// credential's expiry, the error of the first failed Recheck, or nil once ctx is done. The
// stream runs it beside its work and closes with what it returns.
func (d *Documents) Supervise(ctx context.Context, c *Caller, project int64) error {
	var expired <-chan time.Time
	if c != nil && !c.ExpiresAt.IsZero() {
		t := time.NewTimer(time.Until(c.ExpiresAt))
		defer t.Stop()
		expired = t.C
	}
	tick := time.NewTicker(RecheckInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-expired:
			return errCredentialExpired
		case <-tick.C:
			if err := d.Recheck(ctx, c, project); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

// clientIP is the client's address for the log: the one the request middleware resolved
// through trusted_proxies, else the peer. Never the credential (ADR-010 S-15).
func (d *Documents) clientIP(ctx context.Context, t Transport) netip.Addr {
	if a := httpx.ClientAddr(ctx); a.IsValid() {
		return a
	}
	return t.Peer
}

// isEmail tells a service account's iss (its client_email) from an issuer URL.
func isEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, "/: ")
}
