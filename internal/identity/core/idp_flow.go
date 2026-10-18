package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/identity/queries"
	"github.com/osama300/aurma-project/internal/platform/crypto"
)

// The redirect flow. A sign-in session starts on Aruma's handler page (or createAuthUri): it
// gets a state, which travels to the provider and back, a sessionId, which only the starting
// browser tab keeps, a nonce bound into the provider's ID token, and for Google a PKCE
// verifier. signInWithIdp redeems the session once, and only with both state and sessionId,
// so a leaked callback URL is useless on its own (ADR-014).

// idpSessionTTL is how long a sign-in may take at the provider.
const idpSessionTTL = 10 * time.Minute

// StartInput starts a sign-in with a provider.
type StartInput struct {
	ProviderID       string
	CallbackURL      string // the redirect_uri given to the provider
	RedirectURL      string // the app page to return to (handler flows); checked like CallbackURL
	AuthType         string
	AppName          string
	APIKey           string
	EventID          string
	Scopes           []string
	CustomParameters map[string]string
}

// Started is where to send the browser, the state the provider will echo, and the sessionId
// the tab keeps for signInWithIdp.
type Started struct {
	AuthURI   string
	State     string
	SessionID string
}

// StartIdP creates a sign-in session and returns the provider's authorization URL.
func (s *Service) StartIdP(ctx context.Context, p Project, in StartInput) (*Started, error) {
	idp, err := s.enabledIdP(ctx, p, in.ProviderID)
	if err != nil {
		return nil, err
	}
	urls := []string{in.CallbackURL}
	if in.RedirectURL != "" {
		urls = append(urls, in.RedirectURL)
	}
	for _, u := range urls {
		ok, err := s.AuthorizedURL(ctx, p, u)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrUnauthorizedDomain
		}
	}
	state, sessionID, nonce := crypto.RandomString(crypto.TokenBytes), crypto.RandomString(crypto.TokenBytes), crypto.RandomString(24)
	stateHash := s.keys.HashToken("auth-idp-state:" + state)
	var verifier string
	var verifierEnc []byte
	if idp.provider == GoogleProvider {
		verifier = crypto.RandomString(48)
		if verifierEnc, err = s.keys.Seal([]byte(verifier), verifierAAD(stateHash)); err != nil {
			return nil, err
		}
	}
	err = s.q.CreateIdPSession(ctx, queries.CreateIdPSessionParams{
		SessionHash: s.keys.HashToken("auth-idp-session:" + sessionID), StateHash: stateHash,
		ProjectNumber: p.Number, TenantID: p.Tenant, ProviderID: idp.provider, Nonce: nonce,
		RedirectUrl: in.RedirectURL, CallbackUrl: in.CallbackURL, CodeVerifierEnc: verifierEnc,
		EventID: ptr(in.EventID), AuthType: in.AuthType, AppName: in.AppName, ApiKey: in.APIKey,
		ExpiresAt: s.now().Add(idpSessionTTL),
	})
	if err != nil {
		return nil, err
	}
	return &Started{AuthURI: idp.authURL(in, state, hashNonce(nonce), verifier), State: state, SessionID: sessionID}, nil
}

func verifierAAD(stateHash []byte) []byte {
	return []byte("auth.idp_sessions|code_verifier_enc|" + hex.EncodeToString(stateHash))
}

// hashNonce is what goes to the provider and comes back in its ID token: the SHA-256 of the
// raw nonce in hex, the form Firebase documents for Apple and OIDC credentials.
func hashNonce(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Custom parameters an app may pass through to the provider. Everything the flow depends on
// (client_id, redirect_uri, state, nonce, scope, response type and mode, PKCE) is Aruma's.
var passThrough = map[string][]string{
	GoogleProvider: {"prompt", "login_hint", "hd", "hl", "include_granted_scopes", "access_type"},
	AppleProvider:  {"locale"},
}

func (idp *idpCreds) authURL(in StartInput, state, nonce, verifier string) string {
	q := url.Values{}
	for k, v := range in.CustomParameters {
		if slices.Contains(passThrough[idp.provider], k) {
			q.Set(k, v)
		}
	}
	q.Set("client_id", idp.clientID)
	q.Set("redirect_uri", in.CallbackURL)
	q.Set("response_type", "code")
	q.Set("state", state)
	q.Set("nonce", nonce)
	switch idp.provider {
	case GoogleProvider:
		scopes := []string{"openid", "email", "profile"}
		for _, sc := range in.Scopes {
			if sc = strings.TrimSpace(sc); sc != "" && !slices.Contains(scopes, sc) {
				scopes = append(scopes, sc)
			}
		}
		q.Set("scope", strings.Join(scopes, " "))
		sum := sha256.Sum256([]byte(verifier))
		q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
		q.Set("code_challenge_method", "S256")
	case AppleProvider:
		// Apple knows only these two scopes, and posts the result back when any is asked for.
		q.Set("scope", "name email")
		q.Set("response_mode", "form_post")
	}
	return idp.endpoints.AuthURL + "?" + q.Encode()
}

// AuthorizedURL reports whether a URL is http(s) on one of the project's authorized domains,
// its authDomain, or Aruma's own host. Hosts match exactly, ports aside, as in Firebase.
func (s *Service) AuthorizedURL(ctx context.Context, p Project, raw string) (bool, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return false, nil
	}
	domains, err := s.AuthorizedDomains(ctx, p)
	if err != nil {
		return false, err
	}
	return slices.Contains(domains, strings.ToLower(u.Hostname())), nil
}

// AuthorizedDomains lists the hosts Auth may redirect to and be framed by: the configured
// authorized domains, the project's authDomain and Aruma's public host.
func (s *Service) AuthorizedDomains(ctx context.Context, p Project) ([]string, error) {
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cfg.AuthorizedDomains)+2)
	for _, d := range cfg.AuthorizedDomains {
		out = append(out, strings.ToLower(d))
	}
	if d, err := s.control.ProjectAuthDomain(ctx, p.Number); err == nil && d != "" {
		out = append(out, strings.ToLower(d))
	}
	if u, err := url.Parse(s.publicURL); err == nil {
		out = append(out, strings.ToLower(u.Hostname()))
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// ProjectAuthDomain returns the project's authDomain.
func (s *Service) ProjectAuthDomain(ctx context.Context, p Project) (string, error) {
	return s.control.ProjectAuthDomain(ctx, p.Number)
}

// PublicURL is Aruma's public base URL.
func (s *Service) PublicURL() string { return s.publicURL }

// PendingSignIn is what the handler page needs when the provider sends the browser back: how
// to hand the result to the SDK. It does not redeem the session.
type PendingSignIn struct {
	ProviderID  string
	AuthType    string
	EventID     string
	AppName     string
	APIKey      string
	RedirectURL string
	CallbackURL string
}

// PendingIdP looks up the live sign-in session a provider callback names by its state.
func (s *Service) PendingIdP(ctx context.Context, state string) (*PendingSignIn, error) {
	if state == "" {
		return nil, errIdPToken("missing state")
	}
	row, err := s.q.FindIdPSessionByState(ctx, s.keys.HashToken("auth-idp-state:"+state))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errIdPToken("the sign-in session is unknown, used or expired")
	}
	if err != nil {
		return nil, err
	}
	return &PendingSignIn{
		ProviderID: row.ProviderID, AuthType: row.AuthType, EventID: str(row.EventID), AppName: row.AppName,
		APIKey: row.ApiKey, RedirectURL: row.RedirectUrl, CallbackURL: row.CallbackUrl,
	}, nil
}

// redeemIdP marks the session named by state used and checks that sessionID belongs to it. The
// session is burnt even when the sessionId is wrong, so neither can be guessed at leisure.
func (s *Service) redeemIdP(ctx context.Context, p Project, state, sessionID string) (queries.AuthIdpSession, error) {
	row, err := s.q.UseIdPSession(ctx, queries.UseIdPSessionParams{ProjectNumber: p.Number, StateHash: s.keys.HashToken("auth-idp-state:" + state)})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, errIdPToken("the sign-in session is unknown, used or expired")
	}
	if err != nil {
		return row, err
	}
	if sessionID == "" || !crypto.EqualHash(row.SessionHash, s.keys.HashToken("auth-idp-session:"+sessionID)) {
		s.audit(ctx, s.q, p, "", "signin.idp", "denied", map[string]any{"reason": "session mismatch", "provider": row.ProviderID})
		return row, errIdPToken("the sign-in session does not match this browser")
	}
	return row, nil
}

// providerTokens is a provider's token endpoint response.
type providerTokens struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// exchangeCode trades an authorization code for the provider's tokens, server to server.
func (s *Service) exchangeCode(ctx context.Context, p Project, idp *idpCreds, row queries.AuthIdpSession, code string) (*providerTokens, error) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {row.CallbackUrl}, "client_id": {idp.clientID},
	}
	switch idp.provider {
	case GoogleProvider:
		form.Set("client_secret", idp.secret)
		if row.CodeVerifierEnc != nil {
			v, err := s.keys.Open(row.CodeVerifierEnc, verifierAAD(row.StateHash))
			if err != nil {
				return nil, err
			}
			form.Set("code_verifier", string(v))
		}
	case AppleProvider:
		secret, err := s.appleClientSecret(idp)
		if err != nil {
			return nil, err
		}
		form.Set("client_secret", secret)
	}
	var out providerTokens
	if err := idp.post(ctx, idp.endpoints.TokenURL, form, &out); err != nil {
		s.log.Info("idp code exchange", "provider", idp.provider, "error", err)
		return nil, errIdPToken("the provider did not accept the authorization code")
	}
	if out.IDToken == "" {
		return nil, errIdPToken("the provider returned no id_token")
	}
	return &out, nil
}

// post sends a form to a provider endpoint and decodes its JSON answer.
func (idp *idpCreds) post(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := idp.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, e.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// appleClientSecret signs the short-lived ES256 client secret Apple's token and revoke
// endpoints take in place of a static secret: iss is the team, sub the Services ID.
func (s *Service) appleClientSecret(idp *idpCreds) (string, error) {
	now := s.now()
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": idp.keyID})
	claims, _ := json.Marshal(map[string]any{
		"iss": idp.teamID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"aud": "https://appleid.apple.com", "sub": idp.clientID,
	})
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sig, err := signES256(idp.appleKey, []byte(signing))
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// signES256 returns a JWS ES256 signature: r and s as fixed 32-byte big-endian values.
func signES256(key *ecdsa.PrivateKey, data []byte) ([]byte, error) {
	sum := sha256.Sum256(data)
	r, sv, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	sv.FillBytes(out[32:])
	return out, nil
}

// WebConfig is a web app's firebaseConfig, as /__/firebase/init.json serves it.
type WebConfig struct {
	APIKey            string `json:"apiKey"`
	AppID             string `json:"appId"`
	AuthDomain        string `json:"authDomain"`
	DatabaseURL       string `json:"databaseURL"`
	MessagingSenderID string `json:"messagingSenderId"`
	ProjectID         string `json:"projectId"`
	StorageBucket     string `json:"storageBucket"`
}

// WebConfigForHost returns the web config of the project whose own authDomain is host.
func (s *Service) WebConfigForHost(ctx context.Context, host string) (*WebConfig, error) {
	c, err := s.control.WebAppForAuthDomain(ctx, host)
	if err != nil {
		return nil, err
	}
	return &WebConfig{
		APIKey: c.App.APIKey, AppID: c.App.App.ID, AuthDomain: c.Project.AuthDomain, DatabaseURL: c.Project.DatabaseURL,
		MessagingSenderID: strconv.FormatInt(c.Project.Number, 10), ProjectID: c.Project.ID, StorageBucket: c.Project.StorageBucket,
	}, nil
}
