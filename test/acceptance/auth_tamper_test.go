package acceptance

import (
	"context"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/crypto"
	identity "github.com/arumadev/aruma/internal/services/auth/core"
	"github.com/arumadev/aruma/internal/services/auth/tokens"
)

// T24, X-2, X-3: the 22 Phase 2 tamper scenarios, one named subtest each, indexed in
// docs/security/tamper-phase2.md (keep the names in step). Every forged ID token is put to each of Aruma's three
// verifiers: the server (accounts:lookup, and the refresh grant where a refresh token is
// involved) and the Node and Go verification libraries. Scenarios whose feature is built on
// another branch are written in full against the documented contract and skipped with the
// feature's name; the lead removes the skip at merge.

const (
	pendingNodeLib = "the Node library verifies only with ARUMA_VERIFY_NODE set: run test/smoke/run.sh tamper-libs"
	pendingGoLib   = "the Go library verifies only with ARUMA_VERIFY_GO set: run test/smoke/run.sh tamper-libs"
)

// libVerifier is a verification library's view of an ID token: nil error means accepted.
// checkRevoked asks for the revocation check (verifyIdToken(token, true)).
type libVerifier func(t *testing.T, p *authProject, token string, checkRevoked bool) error

// The library verifiers, set by auth_verifiers_test.go when the helper processes are given.
var (
	nodeLibVerify libVerifier
	goLibVerify   libVerifier
)

// mailboxCode returns the oobCode of the newest dev mailbox message to an address, as the user
// reads it from the link.
func mailboxCode(t *testing.T, p *authProject, email string) string {
	t.Helper()
	_, q := linkCode(t, p.lastMail(email)["link"].(string))
	return q.Get("oobCode")
}

// idpDriver plays the user's browser and the provider for the state and nonce scenarios, on
// the fake Google and Apple of auth_idp_test.go: the web SDK's popup path through Aruma's
// handler pages, and the REST path through createAuthUri.
type idpDriver struct{ *idpEnv }

// popup runs a Google popup sign-in up to the AuthEvent the SDK sends to signInWithIdp.
func (d idpDriver) popup(sub string) authEvent {
	return d.flow(identity.GoogleProvider, "signInViaPopup", googleClaims(sub, sub+"@gmail.com", true))
}

// rest starts a Google sign-in with createAuthUri, consents at the provider and returns the
// callback URL the provider redirects to, with the sessionId the caller kept.
func (d idpDriver) rest(sub string) (callback, sessionID string) {
	t := d.env.t
	t.Helper()
	r := ok(t, d.call(itk+"accounts:createAuthUri", map[string]any{"providerId": identity.GoogleProvider, "continueUri": "http://localhost/cb"}))
	code, state := d.google.authorize(r.str("authUri"), googleClaims(sub, sub+"@gmail.com", true))
	return "http://localhost/cb?" + url.Values{"state": {state}, "code": {code}}.Encode(), r.str("sessionId")
}

// redeem is signInWithIdp with a callback and a sessionId.
func (d idpDriver) redeem(callback, sessionID string) *Response {
	return d.signIn(authEvent{requestURI: callback, sessionID: sessionID}, "")
}

// withState returns the callback URL with its state replaced.
func withState(t *testing.T, callback, state string) string {
	t.Helper()
	u, err := url.Parse(callback)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String()
}

type namedVerifier struct {
	name, pending string
	verify        libVerifier
}

func libVerifiers() []namedVerifier {
	return []namedVerifier{{"node library", pendingNodeLib, nodeLibVerify}, {"go library", pendingGoLib, goLibVerify}}
}

// rejectEverywhere asserts that every verifier refuses an ID token; the server must answer
// with serverCode.
func rejectEverywhere(t *testing.T, p *authProject, token, serverCode string) {
	t.Helper()
	t.Run("server", func(t *testing.T) {
		authErr(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": token}), serverCode)
	})
	for _, lib := range libVerifiers() {
		t.Run(lib.name, func(t *testing.T) {
			if lib.verify == nil {
				t.Skip(lib.pending)
			}
			if err := lib.verify(t, p, token, true); err == nil {
				t.Fatal("the library accepted a forged token")
			}
		})
	}
}

// acceptEverywhere is the control: every verifier accepts a genuine token, so the rejections
// are not a broken verifier.
func acceptEverywhere(t *testing.T, p *authProject, token string) {
	t.Helper()
	t.Run("server", func(t *testing.T) { ok(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": token})) })
	for _, lib := range libVerifiers() {
		t.Run(lib.name, func(t *testing.T) {
			if lib.verify == nil {
				t.Skip(lib.pending)
			}
			if err := lib.verify(t, p, token, true); err != nil {
				t.Fatalf("the library refused a genuine token: %v", err)
			}
		})
	}
}

// signingKey returns the project's active private key of the purpose, decrypted from the
// database as only the server can. Forging with the real key isolates the check under test
// from the signature check.
func (p *authProject) signingKey(purpose tokens.Purpose) (kid string, key *rsa.PrivateKey) {
	t := p.env.t
	t.Helper()
	ctx := context.Background()
	if _, err := p.env.App.Auth.Tokens().JWKS(ctx, p.ref().Number, purpose); err != nil { // creates the key on first use
		t.Fatal(err)
	}
	var enc []byte
	if err := p.env.Pool.QueryRow(ctx, `SELECT kid, private_key_enc FROM auth.signing_keys
		WHERE project_number = $1 AND purpose = $2 AND state = 'active'`, p.number, string(purpose)).Scan(&kid, &enc); err != nil {
		t.Fatal(err)
	}
	keys, err := crypto.NewKeys(p.env.App.Config.MasterKeyBytes())
	if err != nil {
		t.Fatal(err)
	}
	der, err := keys.Open(enc, []byte("auth.signing_keys|private_key|"+kid))
	if err != nil {
		t.Fatal(err)
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		t.Fatal(err)
	}
	return kid, k.(*rsa.PrivateKey)
}

// tamperUser signs up a user and returns its session, the decoded token parts, and the
// project's real ID token key.
type tamperUser struct {
	session *Response
	header  map[string]any
	payload map[string]any
	sig     string
	kid     string
	key     *rsa.PrivateKey
}

func newTamperUser(t *testing.T, p *authProject, email string) *tamperUser {
	t.Helper()
	u := &tamperUser{session: p.signUp(email, "secret1")}
	u.header, u.payload, u.sig = jwtParts(t, u.session.str("idToken"))
	u.kid, u.key = p.signingKey(tokens.IDToken)
	if u.header["kid"] != u.kid {
		t.Fatalf("token kid %v is not the active key %s", u.header["kid"], u.kid)
	}
	return u
}

// claimsWith copies the genuine payload and applies edits.
func (u *tamperUser) claimsWith(edit func(map[string]any)) map[string]any {
	c := make(map[string]any, len(u.payload))
	for k, v := range u.payload {
		c[k] = v
	}
	if edit != nil {
		edit(c)
	}
	return c
}

// resign signs claims with the project's real key and the genuine header.
func (u *tamperUser) resign(t *testing.T, claims map[string]any) string {
	t.Helper()
	return rsaSign(t, u.key, map[string]any{"alg": "RS256", "kid": u.kid, "typ": "JWT"}, claims)
}

func TestAuthTamper(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	b := p.secondProject("other-app")
	victim := newTamperUser(t, p, "victim@example.com")
	other := p.signUp("other@example.com", "secret1")

	t.Run("00 control: genuine and re-signed tokens are accepted", func(t *testing.T) {
		t.Run("genuine", func(t *testing.T) { acceptEverywhere(t, p, victim.session.str("idToken")) })
		// Re-signing the untouched payload with the extracted key must pass, so every rejection
		// below is caused by the one thing each scenario changes.
		t.Run("re-signed", func(t *testing.T) { acceptEverywhere(t, p, victim.resign(t, victim.claimsWith(nil))) })
	})

	t.Run("01 unsigned token", func(t *testing.T) {
		h, pl := segment(victim.header), segment(victim.payload)
		for name, tok := range map[string]string{
			"two segments":    h + "." + pl,
			"empty signature": h + "." + pl + ".",
			"junk signature":  h + "." + pl + "." + base64.RawURLEncoding.EncodeToString([]byte("not a signature")),
		} {
			t.Run(name, func(t *testing.T) { rejectEverywhere(t, p, tok, "INVALID_ID_TOKEN") })
		}
	})

	t.Run("02 alg none", func(t *testing.T) {
		for _, alg := range []string{"none", "None", "NONE", ""} {
			t.Run("alg="+alg, func(t *testing.T) {
				tok := segment(map[string]any{"alg": alg, "kid": victim.kid, "typ": "JWT"}) + "." + segment(victim.payload) + "."
				rejectEverywhere(t, p, tok, "INVALID_ID_TOKEN")
				// The same with the genuine signature kept.
				rejectEverywhere(t, p, strings.TrimSuffix(tok, ".")+"."+victim.sig, "INVALID_ID_TOKEN")
			})
		}
	})

	t.Run("03 algorithm confusion", func(t *testing.T) {
		pubDER, err := x509.MarshalPKIXPublicKey(&victim.key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		jwks := ok(t, e.client().do("GET", "/issuers/chat-app/.well-known/jwks.json", nil)).Body
		secrets := map[string][]byte{
			"PEM public key": pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}),
			"DER public key": pubDER,
			"modulus":        victim.key.N.Bytes(),
			"published JWKS": jwks,
			"kid":            []byte(victim.kid),
		}
		for name, secret := range secrets {
			t.Run("HS256 keyed with the "+name, func(t *testing.T) {
				signing := segment(map[string]any{"alg": "HS256", "kid": victim.kid, "typ": "JWT"}) + "." + segment(victim.payload)
				mac := hmac.New(sha256.New, secret)
				mac.Write([]byte(signing))
				rejectEverywhere(t, p, signing+"."+base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), "INVALID_ID_TOKEN")
			})
		}
		t.Run("RS256 signed by another key under the real kid", func(t *testing.T) {
			tok := rsaSign(t, newRSAKey(t), map[string]any{"alg": "RS256", "kid": victim.kid, "typ": "JWT"}, victim.payload)
			rejectEverywhere(t, p, tok, "INVALID_ID_TOKEN")
		})
		t.Run("kid of another project's key", func(t *testing.T) {
			bKid, bKey := b.signingKey(tokens.IDToken)
			tok := rsaSign(t, bKey, map[string]any{"alg": "RS256", "kid": bKid, "typ": "JWT"}, victim.payload)
			rejectEverywhere(t, p, tok, "INVALID_ID_TOKEN")
		})
		t.Run("kid of the project's session cookie key", func(t *testing.T) {
			sKid, sKey := p.signingKey(tokens.SessionCookie)
			tok := rsaSign(t, sKey, map[string]any{"alg": "RS256", "kid": sKid, "typ": "JWT"}, victim.payload)
			rejectEverywhere(t, p, tok, "INVALID_ID_TOKEN")
		})
		t.Run("missing kid, real key", func(t *testing.T) {
			tok := rsaSign(t, victim.key, map[string]any{"alg": "RS256", "typ": "JWT"}, victim.payload)
			rejectEverywhere(t, p, tok, "INVALID_ID_TOKEN")
		})
		t.Run("jku and x5u are not followed", func(t *testing.T) {
			tok := rsaSign(t, newRSAKey(t), map[string]any{"alg": "RS256", "kid": "attacker", "jku": "https://evil.example/jwks.json", "x5u": "https://evil.example/c.pem"}, victim.payload)
			rejectEverywhere(t, p, tok, "INVALID_ID_TOKEN")
		})
	})

	t.Run("04 modified payload", func(t *testing.T) {
		for name, edit := range map[string]func(map[string]any){
			"uid swapped": func(c map[string]any) {
				c["sub"], c["user_id"] = other.str("localId"), other.str("localId")
			},
			"exp extended": func(c map[string]any) { c["exp"] = time.Now().Add(30 * 24 * time.Hour).Unix() },
			"aud changed":  func(c map[string]any) { c["aud"] = "other-app" },
			"claim added":  func(c map[string]any) { c["admin"] = true },
		} {
			t.Run(name, func(t *testing.T) {
				tok := segment(victim.header) + "." + segment(victim.claimsWith(edit)) + "." + victim.sig
				rejectEverywhere(t, p, tok, "INVALID_ID_TOKEN")
			})
		}
		t.Run("aud changed, validly signed", func(t *testing.T) {
			tok := victim.resign(t, victim.claimsWith(func(c map[string]any) { c["aud"] = "other-app" }))
			rejectEverywhere(t, p, tok, "INVALID_ID_TOKEN")
		})
	})

	t.Run("05 project A token on project B", func(t *testing.T) {
		rejectEverywhere(t, b, victim.session.str("idToken"), "INVALID_ID_TOKEN")
		authErr(t, b.refresh(victim.session.str("refreshToken")), "INVALID_REFRESH_TOKEN")
		authErr(t, b.call(itk+"accounts:update", map[string]any{"idToken": victim.session.str("idToken"), "displayName": "x"}), "INVALID_ID_TOKEN")
		authErr(t, b.call(itk+"accounts:delete", map[string]any{"idToken": victim.session.str("idToken")}), "INVALID_ID_TOKEN")
		// A token B's key signed with A's claims means nothing in either project.
		bKid, bKey := b.signingKey(tokens.IDToken)
		forged := rsaSign(t, bKey, map[string]any{"alg": "RS256", "kid": bKid}, victim.payload)
		rejectEverywhere(t, b, forged, "INVALID_ID_TOKEN")
	})

	t.Run("06 Google issuer", func(t *testing.T) {
		tok := victim.resign(t, victim.claimsWith(func(c map[string]any) { c["iss"] = "https://securetoken.google.com/chat-app" }))
		rejectEverywhere(t, p, tok, "INVALID_ID_TOKEN")
		tok = victim.resign(t, victim.claimsWith(func(c map[string]any) { c["iss"] = e.URL + "/issuers/other-app" }))
		rejectEverywhere(t, p, tok, "INVALID_ID_TOKEN")
	})

	t.Run("07 expired token", func(t *testing.T) {
		now := time.Now()
		for name, edit := range map[string]func(map[string]any){
			"expired an hour ago": func(c map[string]any) {
				c["iat"], c["exp"] = now.Add(-2*time.Hour).Unix(), now.Add(-time.Hour).Unix()
			},
			// firebase-admin-go, and so the Go library, allow 300 seconds of clock skew on exp.
			"expired beyond the clock skew": func(c map[string]any) { c["exp"] = now.Add(-6 * time.Minute).Unix() },
			"issued in the future": func(c map[string]any) {
				c["iat"], c["exp"] = now.Add(time.Hour).Unix(), now.Add(2*time.Hour).Unix()
			},
		} {
			t.Run(name, func(t *testing.T) {
				rejectEverywhere(t, p, victim.resign(t, victim.claimsWith(edit)), "INVALID_ID_TOKEN")
			})
		}
		// The server allows no skew at all on exp.
		t.Run("server: expired a second ago", func(t *testing.T) {
			tok := victim.resign(t, victim.claimsWith(func(c map[string]any) { c["exp"] = now.Add(-time.Second).Unix() }))
			authErr(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": tok}), "INVALID_ID_TOKEN")
		})
	})

	t.Run("08 revoked token after a password change", func(t *testing.T) {
		u := p.signUp("revoked@example.com", "secret1")
		// validSince has a resolution of one second, as in Firebase: a token from the same
		// second as the revocation is still accepted.
		time.Sleep(1100 * time.Millisecond)
		fresh := ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": u.str("idToken"), "password": "secret2", "returnSecureToken": true}))
		rejectEverywhere(t, p, u.str("idToken"), "TOKEN_EXPIRED")
		authErr(t, p.refresh(u.str("refreshToken")), "TOKEN_EXPIRED")
		// The session the change returned is live.
		ok(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": fresh.str("idToken")}))
		ok(t, p.refresh(fresh.str("refreshToken")))
	})

	t.Run("09 custom token from another project's service account", func(t *testing.T) {
		_, otherKey := p.owner.serviceAccountKey("other-app", "backend")
		authErr(t, p.call(itk+"accounts:signInWithCustomToken", map[string]any{"token": customToken(t, otherKey, "intruder", nil, nil)}), "CREDENTIAL_MISMATCH")
		var n int
		if err := e.Pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.users WHERE uid = 'intruder'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("a refused custom token created a user (%d, %v)", n, err)
		}
	})

	t.Run("10 custom token signed with a deleted service account key", func(t *testing.T) {
		saID, keyFile := p.owner.serviceAccountKey("chat-app", "rotated")
		ok(t, p.call(itk+"accounts:signInWithCustomToken", map[string]any{"token": customToken(t, keyFile, "u-10", nil, nil)}))
		minted := customToken(t, keyFile, "u-10", nil, nil) // minted before the deletion
		p.owner.mustOK(p.owner.do("DELETE", "/console/v1/projects/chat-app/serviceAccounts/"+saID+"/keys/"+parseKeyFile(t, keyFile).PrivateKeyID, nil))
		authErr(t, p.call(itk+"accounts:signInWithCustomToken", map[string]any{"token": minted}), "INVALID_CUSTOM_TOKEN")
		authErr(t, p.call(itk+"accounts:signInWithCustomToken", map[string]any{"token": customToken(t, keyFile, "u-10", nil, nil)}), "INVALID_CUSTOM_TOKEN")
	})

	t.Run("11 used email code", func(t *testing.T) {
		p.signUp("reset@example.com", "secret1")
		ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "reset@example.com"}))
		path, _ := linkCode(t, p.lastMail("reset@example.com")["link"].(string))
		code := mailboxCode(t, p, "reset@example.com")
		ok(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": code, "newPassword": "secret2"}))
		// The link no longer offers a form.
		if r := e.page("GET", path, nil, nil); r.Status != 400 || strings.Contains(string(r.Body), "<form") {
			t.Fatalf("used link page: %d %s", r.Status, r.Body)
		}
		authErr(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": code, "newPassword": "secret3"}), "INVALID_OOB_CODE")
		authErr(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": code}), "INVALID_OOB_CODE")
		ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "reset@example.com", "password": "secret2"}))

		v := p.signUp("verify@example.com", "secret1")
		ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_EMAIL", "idToken": v.str("idToken")}))
		vcode := mailboxCode(t, p, "verify@example.com")
		ok(t, p.call(itk+"accounts:update", map[string]any{"oobCode": vcode}))
		authErr(t, p.call(itk+"accounts:update", map[string]any{"oobCode": vcode}), "INVALID_OOB_CODE")
		// A reset code is not a verification code, nor the reverse.
		ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "verify@example.com"}))
		authErr(t, p.call(itk+"accounts:update", map[string]any{"oobCode": mailboxCode(t, p, "verify@example.com")}), "INVALID_OOB_CODE")
	})

	t.Run("12 expired email code", func(t *testing.T) {
		p.signUp("late@example.com", "secret1")
		ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "late@example.com"}))
		path, _ := linkCode(t, p.lastMail("late@example.com")["link"].(string))
		code := mailboxCode(t, p, "late@example.com")
		p.sql(`UPDATE auth.oob_codes SET expires_at = now() - interval '1 second' WHERE email = 'late@example.com'`)
		if r := e.page("GET", path, nil, nil); r.Status != 400 || strings.Contains(string(r.Body), "<form") {
			t.Fatalf("expired link page: %d %s", r.Status, r.Body)
		}
		authErr(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": code}), "EXPIRED_OOB_CODE")
		authErr(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": code, "newPassword": "secret2"}), "EXPIRED_OOB_CODE")
		authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "late@example.com", "password": "secret2"}), "INVALID_LOGIN_CREDENTIALS")
	})

	t.Run("13 continueUrl to an unauthorised domain", func(t *testing.T) {
		p.signUp("cont@example.com", "secret1")
		for _, u := range []string{"https://evil.example/next", "https://localhost.evil.example/", "https://evil.example/?x=localhost", "javascript:alert(1)"} {
			r := p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "cont@example.com", "continueUrl": u})
			if c := errCode(r); c != "UNAUTHORIZED_DOMAIN" && c != "INVALID_CONTINUE_URI" {
				t.Fatalf("continueUrl %q: %d %s", u, r.Status, r.Body)
			}
		}
		for _, m := range p.mailbox() {
			if m["to"] == "cont@example.com" {
				t.Fatalf("a refused request sent mail: %v", m)
			}
		}
		ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "cont@example.com", "continueUrl": "http://localhost/done"}))
		code := mailboxCode(t, p, "cont@example.com")
		// The action page never offers a way to an unauthorised address, even when the link
		// is edited by hand (AC-A19): only the continueUrl stored with the code counts.
		pg := e.page("GET", "/__/auth/action?mode=resetPassword&oobCode="+url.QueryEscape(code)+"&apiKey="+p.key+"&continueUrl="+url.QueryEscape("https://evil.example/x")+"&lang=ar", nil, nil)
		if strings.Contains(string(pg.Body), "evil.example") {
			t.Fatal("the action page links to an unauthorised continueUrl")
		}
		form := formOf(t, pg)
		form.Set("newPassword", "secret2")
		form.Set("confirmPassword", "secret2")
		done := e.page("POST", "/__/auth/action", form, nil)
		if done.Status != 200 || strings.Contains(string(done.Body), "evil.example") || !strings.Contains(string(done.Body), `href="http://localhost/done"`) {
			t.Fatalf("continue after reset: %d %s", done.Status, done.Body)
		}
	})

	t.Run("14 replayed state", func(t *testing.T) {
		d := idpDriver{newIdPEnv(t)}
		t.Run("popup", func(t *testing.T) {
			ev := d.popup("g-14a")
			ok(t, d.signIn(ev, ""))
			authErr(t, d.signIn(ev, ""), "INVALID_IDP_RESPONSE")
			// The handler page no longer recognises the state either.
			resp, err := http.Get(ev.requestURI)
			if pg := readPage(t, resp, err); pg.status != 400 || pg.data["mode"] != "error" {
				t.Fatalf("replayed callback page: %d %v", pg.status, pg.data)
			}
		})
		t.Run("createAuthUri", func(t *testing.T) {
			callback, sessionID := d.rest("g-14b")
			ok(t, d.redeem(callback, sessionID))
			authErr(t, d.redeem(callback, sessionID), "INVALID_IDP_RESPONSE")
		})
	})

	t.Run("15 swapped state", func(t *testing.T) {
		d := idpDriver{newIdPEnv(t)}
		t.Run("popup: one flow's callback with another's sessionId", func(t *testing.T) {
			a, b := d.popup("g-15a"), d.popup("g-15b")
			authErr(t, d.redeem(a.requestURI, b.sessionID), "INVALID_IDP_RESPONSE")
			// The attempt burns the session it named: a guess cannot be retried.
			authErr(t, d.signIn(a, ""), "INVALID_IDP_RESPONSE")
			ok(t, d.signIn(b, ""))
		})
		t.Run("popup: another flow's state in the callback", func(t *testing.T) {
			a, b := d.popup("g-15c"), d.popup("g-15d")
			authErr(t, d.redeem(withState(t, a.requestURI, b.finish.data["state"]), a.sessionID), "INVALID_IDP_RESPONSE")
		})
		t.Run("createAuthUri: swapped and edited", func(t *testing.T) {
			cb1, _ := d.rest("g-15e")
			_, session2 := d.rest("g-15f")
			authErr(t, d.redeem(cb1, session2), "INVALID_IDP_RESPONSE")
			cb3, session3 := d.rest("g-15g")
			u, _ := url.Parse(cb3)
			authErr(t, d.redeem(withState(t, cb3, u.Query().Get("state")+"x"), session3), "INVALID_IDP_RESPONSE")
			authErr(t, d.redeem(cb3, ""), "INVALID_IDP_RESPONSE")
		})
	})

	t.Run("16 wrong nonce", func(t *testing.T) {
		d := idpDriver{newIdPEnv(t)}
		t.Run("code flow: the provider's token carries another nonce", func(t *testing.T) {
			ev := d.flow(identity.GoogleProvider, "signInViaPopup", map[string]any{"sub": "g-16", "nonce": sha256Hex("not-ours")})
			authErr(t, d.signIn(ev, ""), "MISSING_OR_INVALID_NONCE")
		})
		t.Run("direct Apple token", func(t *testing.T) {
			tok := d.apple.idToken(appleServices, map[string]any{"sub": "apple-16", "email": "a16@example.com", "email_verified": true, "nonce": sha256Hex("nonce-A")})
			authErr(t, d.direct(identity.AppleProvider, tok, "nonce-B", ""), "MISSING_OR_INVALID_NONCE")
			authErr(t, d.direct(identity.AppleProvider, tok, "", ""), "MISSING_OR_INVALID_NONCE")
			// The hashed nonce itself is not the raw nonce.
			authErr(t, d.direct(identity.AppleProvider, tok, sha256Hex("nonce-A"), ""), "MISSING_OR_INVALID_NONCE")
			ok(t, d.direct(identity.AppleProvider, tok, "nonce-A", ""))
		})
		t.Run("direct Google token without a nonce, request with one", func(t *testing.T) {
			tok := d.google.idToken(googleClient, googleClaims("g-16b", "g16b@gmail.com", true))
			authErr(t, d.direct(identity.GoogleProvider, tok, "nonce-A", ""), "MISSING_OR_INVALID_NONCE")
		})
	})

	t.Run("17 admin-only fields from a client", func(t *testing.T) {
		a := p.signUp("a17@example.com", "secret1")
		target := p.signUp("b17@example.com", "secret1")
		adminFields := map[string]any{
			"localId": target.str("localId"), "customAttributes": `{"admin":true}`, "disabled": true, "disableUser": true,
			"emailVerified": true, "validSince": "0", "passwordHash": "AAAA", "salt": "AAAA", "createdAt": "1", "lastLoginAt": "1",
			"providerUserInfo": []map[string]any{{"providerId": "google.com", "rawId": "g-1"}}, "targetProjectId": "other-app",
		}
		withFields := func(m map[string]any) map[string]any {
			for k, v := range adminFields {
				m[k] = v
			}
			return m
		}
		check := func(t *testing.T) {
			t.Helper()
			for _, uid := range []string{a.str("localId"), target.str("localId")} {
				u, err := e.App.Auth.GetUser(context.Background(), p.ref(), uid)
				if err != nil {
					t.Fatal(err)
				}
				if u.Disabled || u.EmailVerified || len(u.CustomClaims) > 0 || len(u.Providers) != 1 {
					t.Fatalf("an admin-only field was applied to %s: %+v", uid, u)
				}
				if uid == target.str("localId") && u.DisplayName != "" {
					t.Fatalf("the other user was changed: %+v", u)
				}
			}
			ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "b17@example.com", "password": "secret1"}))
		}

		t.Run("accounts:update acts on the token's user only", func(t *testing.T) {
			r := p.call(itk+"accounts:update", withFields(map[string]any{"idToken": a.str("idToken"), "displayName": "changed"}))
			if r.Status == 200 && r.str("localId") != a.str("localId") {
				t.Fatalf("update answered for another user: %s", r.Body)
			}
			check(t)
			// The caller's own session survives: validSince was not moved.
			ok(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": a.str("idToken")}))
		})
		t.Run("accounts:update without a token never reaches a localId", func(t *testing.T) {
			authErr(t, p.call(itk+"accounts:update", withFields(map[string]any{"displayName": "changed"})), "INVALID_REQ_TYPE")
			check(t)
		})
		t.Run("accounts:signUp ignores a chosen uid and status", func(t *testing.T) {
			r := p.call(itk+"accounts:signUp", withFields(map[string]any{"email": "c17@example.com", "password": "secret1", "localId": "chosen-uid"}))
			if r.Status == 200 {
				if r.str("localId") == "chosen-uid" {
					t.Fatal("the client chose its uid")
				}
				u, err := e.App.Auth.GetUser(context.Background(), p.ref(), r.str("localId"))
				if err != nil {
					t.Fatal(err)
				}
				if u.Disabled || u.EmailVerified || len(u.CustomClaims) > 0 {
					t.Fatalf("sign-up applied an admin-only field: %+v", u)
				}
			}
			check(t)
		})
		t.Run("accounts:lookup returns the token's user only", func(t *testing.T) {
			r := ok(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": a.str("idToken"), "localId": []string{target.str("localId")}, "email": []string{"b17@example.com"}}))
			users := r.list("users")
			if len(users) != 1 || users[0]["localId"] != a.str("localId") {
				t.Fatalf("lookup revealed another user: %s", r.Body)
			}
		})
		t.Run("accounts:delete removes the token's user only", func(t *testing.T) {
			ok(t, p.call(itk+"accounts:delete", map[string]any{"idToken": a.str("idToken"), "localId": target.str("localId")}))
			if _, err := e.App.Auth.GetUser(context.Background(), p.ref(), target.str("localId")); err != nil {
				t.Fatalf("the other user was deleted: %v", err)
			}
			if _, err := e.App.Auth.GetUser(context.Background(), p.ref(), a.str("localId")); err == nil {
				t.Fatal("the caller was not deleted")
			}
		})
		t.Run("sendOobCode never returns the link to a client", func(t *testing.T) {
			r := p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "b17@example.com", "returnOobLink": true})
			if r.Status != 403 || errCode(r) != "INSUFFICIENT_PERMISSION" || strings.Contains(string(r.Body), "oob") {
				t.Fatalf("returnOobLink from a client: %d %s", r.Status, r.Body)
			}
		})
	})

	adminCalls := []struct{ method, path string }{
		{"POST", "/projects/chat-app/accounts:lookup"},
		{"POST", "/projects/chat-app/accounts:update"},
		{"POST", "/projects/chat-app/accounts:delete"},
		{"POST", "/projects/chat-app/accounts:batchDelete"},
		{"POST", "/projects/chat-app/accounts"},
		{"GET", "/projects/chat-app/accounts:batchGet?maxResults=10"},
		{"POST", "/projects/chat-app:createSessionCookie"},
		{"POST", "/projects/" + p.number + "/accounts:lookup"},
	}
	adminBody := map[string]any{"localId": []string{victim.session.str("localId")}, "email": []string{"victim@example.com"}}
	// noUserData asserts that an admin call was refused without leaking anything.
	noUserData := func(t *testing.T, r *Response) {
		t.Helper()
		if r.Status < 400 || strings.Contains(string(r.Body), "victim@example.com") || strings.Contains(string(r.Body), victim.session.str("localId")) {
			t.Fatalf("admin call answered: %d %s", r.Status, r.Body)
		}
	}
	unauthenticated := func(t *testing.T, r *Response) {
		t.Helper()
		if r.Status != 401 && r.Status != 403 {
			t.Fatalf("want 401 or 403, got %d: %s", r.Status, r.Body)
		}
		if r.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("admin routes must not allow other origins")
		}
	}

	t.Run("18 Bearer owner on admin endpoints", func(t *testing.T) {
		owner := e.withBearer("owner")
		for _, c := range adminCalls {
			t.Run(c.method+" "+c.path, func(t *testing.T) {
				noUserData(t, owner.do(c.method, strings.TrimSuffix(itk, "/")+c.path, adminBody))
			})
		}
		// Every other admin surface refuses it as well.
		for _, path := range []string{"/v1beta1/projects", "/console/v1/projects", "/console/v1/me"} {
			expect(t, owner.do("GET", path, nil), 401, "")
		}
		t.Run("strict status", func(t *testing.T) {
			for _, c := range adminCalls {
				unauthenticated(t, owner.do(c.method, strings.TrimSuffix(itk, "/")+c.path, adminBody))
			}
		})
	})

	t.Run("19 API key on admin endpoints", func(t *testing.T) {
		keyed := e.client()
		keyed.header["X-Goog-Api-Key"] = p.key
		userToken := e.withBearer(victim.session.str("idToken"))
		for _, c := range adminCalls {
			t.Run(c.method+" "+c.path, func(t *testing.T) {
				sep := "?"
				if strings.Contains(c.path, "?") {
					sep = "&"
				}
				noUserData(t, e.client().do(c.method, strings.TrimSuffix(itk, "/")+c.path+sep+"key="+p.key, adminBody))
				noUserData(t, keyed.do(c.method, strings.TrimSuffix(itk, "/")+c.path, adminBody))
				// A user's own ID token is not an admin credential either.
				noUserData(t, userToken.do(c.method, strings.TrimSuffix(itk, "/")+c.path, adminBody))
			})
		}
		t.Run("strict status, and the service account token works", func(t *testing.T) {
			for _, c := range adminCalls {
				unauthenticated(t, e.client().do(c.method, strings.TrimSuffix(itk, "/")+c.path+"?key="+p.key, adminBody))
				unauthenticated(t, userToken.do(c.method, strings.TrimSuffix(itk, "/")+c.path, adminBody))
			}
			_, keyFile := p.owner.serviceAccountKey("chat-app", "admin-sdk")
			sa, _, err := exchange(t, keyFile)
			if err != nil {
				t.Fatal(err)
			}
			r := ok(t, e.withBearer(sa).do("POST", strings.TrimSuffix(itk, "/")+"/projects/chat-app/accounts:lookup", adminBody))
			if len(r.list("users")) != 1 {
				t.Fatalf("control: the service account could not read the user: %s", r.Body)
			}
		})
	})

	t.Run("20 database and log dump hold no raw secret", func(t *testing.T) {
		testNoRawAuthSecrets(t)
	})

	t.Run("21 rate limit on sign-in", func(t *testing.T) {
		testSignInRateLimits(t)
	})

	// Last: it empties this address's sendOobCode bucket.
	t.Run("22 rate limit on email sending", func(t *testing.T) {
		p.signUp("flood@example.com", "secret1")
		for range 5 {
			ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "flood@example.com"}))
		}
		// Per recipient, whatever the case of the address.
		authErr(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "FLOOD@example.com"}), "RESET_PASSWORD_EXCEED_LIMIT")
		n := 0
		for _, m := range p.mailbox() {
			if m["to"] == "flood@example.com" {
				n++
			}
		}
		if n != 5 {
			t.Fatalf("%d messages to one address, want 5", n)
		}
		// Per client address, across recipients.
		var last *Response
		for i := range 25 {
			last = p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "spray" + strconv.Itoa(i) + "@example.com"})
			if last.Status != 200 {
				break
			}
		}
		authErr(t, last, "TOO_MANY_ATTEMPTS_TRY_LATER")
	})
}

// testNoRawAuthSecrets runs every Auth flow on this branch, then dumps every table of every
// Aruma schema and the server log, and looks for each raw secret, as text and as hex.
func testNoRawAuthSecrets(t *testing.T) {
	x := newIdPEnv(t) // Google and Apple enabled against the fake providers
	e, p := x.env, x.authProject
	p.enableAnonymous()
	pat := p.owner.pat()

	up := p.signUp("dump@example.com", "Dump-Pass-1!")
	in := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "dump@example.com", "password": "Dump-Pass-1!", "returnSecureToken": true}))
	p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "dump@example.com", "password": "Mistyped-Pass!"})
	ref := ok(t, p.refresh(in.str("refreshToken")))
	changed := ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": in.str("idToken"), "password": "Dump-Pass-2!", "returnSecureToken": true}))
	anon := ok(t, p.call(itk+"accounts:signUp", map[string]any{"returnSecureToken": true}))
	linked := ok(t, p.call(itk+"accounts:signUp", map[string]any{"idToken": anon.str("idToken"), "email": "linked@example.com", "password": "Link-Pass-1!"}))
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_EMAIL", "idToken": changed.str("idToken")}))
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "dump@example.com"}))
	const smtpPassword = "Smtp-Secret-Pass-1"
	p.owner.mustOK(p.owner.do("PATCH", "/console/v1/projects/chat-app/auth/smtp", map[string]any{"password": smtpPassword, "devMailbox": true}))
	_, keyFile := p.owner.serviceAccountKey("chat-app", "backend")
	ct := customToken(t, keyFile, "dump-custom", map[string]any{"role": "x"}, nil)
	custom := ok(t, p.call(itk+"accounts:signInWithCustomToken", map[string]any{"token": ct}))
	block, _ := pem.Decode([]byte(parseKeyFile(t, keyFile).PrivateKey))
	// Apple: the code exchange returns a refresh token Aruma keeps for revocation.
	ev := x.flow(identity.AppleProvider, "signInViaPopup", map[string]any{"sub": "apple-dump", "email": "apple-dump@example.com", "email_verified": true})
	apple := ok(t, x.signIn(ev, ""))
	post, _ := url.ParseQuery(ev.postBody)
	if post.Get("code") == "" {
		t.Fatalf("no code in the Apple callback: %q", ev.postBody)
	}

	secrets := map[string]string{
		"sign-up password":      "Dump-Pass-1!",
		"changed password":      "Dump-Pass-2!",
		"mistyped password":     "Mistyped-Pass!",
		"linked password":       "Link-Pass-1!",
		"console password":      testPassword,
		"CLI token":             pat,
		"master key":            testMasterKey,
		"service account key":   base64.StdEncoding.EncodeToString(block.Bytes)[64:128],
		"custom token":          ct,
		"refresh token (up)":    up.str("refreshToken"),
		"refresh token (in)":    in.str("refreshToken"),
		"refresh token (new)":   changed.str("refreshToken"),
		"refresh token (anon)":  anon.str("refreshToken"),
		"refresh token (link)":  linked.str("refreshToken"),
		"refresh token (ct)":    custom.str("refreshToken"),
		"ID token (sign-in)":    in.str("idToken"),
		"ID token (refresh)":    ref.str("id_token"),
		"ID token (custom)":     custom.str("idToken"),
		"ID token (after link)": linked.str("idToken"),
		"SMTP password":         smtpPassword,
		"Google client secret":  googleSecret,
		"Apple refresh token":   "refresh-" + post.Get("code"),
		"Apple access token":    apple.str("oauthAccessToken"),
		"Apple ID token":        apple.str("oauthIdToken"),
		"ID token (Apple)":      apple.str("idToken"),
		"refresh token (Apple)": apple.str("refreshToken"),
	}
	mails := p.mailbox()
	if len(mails) != 2 {
		t.Fatalf("%d messages in the dev mailbox, want the verification and the reset", len(mails))
	}
	for _, m := range mails {
		path, q := linkCode(t, m["link"].(string))
		secrets["oob code ("+m["type"].(string)+")"] = q.Get("oobCode")
		e.page("GET", path, nil, nil) // the code travels in the action page's query string
	}
	signingKeys(t, e, secrets)
	for name, s := range secrets {
		if len(s) < 8 {
			t.Fatalf("scenario did not produce a usable %s: %q", name, s)
		}
	}

	dump := dumpDatabase(t, e)
	logs := e.Logs.String()
	if !strings.Contains(dump, "dump@example.com") || !strings.Contains(dump, "auth.user.created") || !strings.Contains(logs, "accounts:signUp") {
		t.Fatal("database dump or log is missing the scenario's public data")
	}
	for name, s := range secrets {
		for _, form := range []string{s, hex.EncodeToString([]byte(s))} {
			if strings.Contains(dump, form) {
				t.Errorf("%s found in the database", name)
			}
			if strings.Contains(logs, form) {
				t.Errorf("%s found in the server log", name)
			}
		}
	}
	if strings.Contains(dump, "PRIVATE KEY") || strings.Contains(logs, "PRIVATE KEY") || strings.Contains(logs, "$argon2") {
		t.Error("a private key or password hash was logged")
	}
}

// signingKeys adds the private half of every signing key, as PKCS#8 base64, to secrets.
func signingKeys(t *testing.T, e *Env, secrets map[string]string) {
	t.Helper()
	rows, err := e.Pool.Query(context.Background(), `SELECT kid, private_key_enc FROM auth.signing_keys WHERE private_key_enc IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	keys, err := crypto.NewKeys(e.App.Config.MasterKeyBytes())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var kid string
		var enc []byte
		if err := rows.Scan(&kid, &enc); err != nil {
			t.Fatal(err)
		}
		der, err := keys.Open(enc, []byte("auth.signing_keys|private_key|"+kid))
		if err != nil {
			t.Fatal(err)
		}
		secrets["signing key "+kid[:8]] = base64.StdEncoding.EncodeToString(der)[64:128]
		n++
	}
	if n == 0 {
		t.Fatal("no signing key to look for")
	}
}

// testSignInRateLimits covers the password guess backoff, the per-address account creation
// limit and the per-address limit on every client call.
func testSignInRateLimits(t *testing.T) {
	t.Run("password guesses back off per account", func(t *testing.T) {
		e := newEnv(t, nil)
		p := e.authProject("chat-app")
		p.signUp("guess@example.com", "secret1")
		var last *Response
		for range 12 {
			last = p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "guess@example.com", "password": "guess!!"})
		}
		authErr(t, last, "TOO_MANY_ATTEMPTS_TRY_LATER")
		// While locked, even the right password is refused, so the lock cannot be probed.
		authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "guess@example.com", "password": "secret1"}), "TOO_MANY_ATTEMPTS_TRY_LATER")
		// Case and spaces do not open a second counter.
		authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": " GUESS@example.com", "password": "secret1"}), "TOO_MANY_ATTEMPTS_TRY_LATER")
		// Another account is unaffected.
		p.signUp("bystander@example.com", "secret1")
		ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "bystander@example.com", "password": "secret1"}))
	})
	t.Run("account creation is limited per address", func(t *testing.T) {
		e := newEnv(t, nil)
		p := e.authProject("chat-app")
		p.enableAnonymous()
		limited := false
		for range 110 {
			r := p.call(itk+"accounts:signUp", map[string]any{})
			if r.Status != 200 {
				authErr(t, r, "TOO_MANY_ATTEMPTS_TRY_LATER")
				limited = true
				break
			}
		}
		if !limited {
			t.Fatal("110 accounts created from one address without a limit")
		}
	})
	t.Run("client calls are limited per address", func(t *testing.T) {
		e := newEnv(t, nil)
		p := e.authProject("chat-app")
		limited := false
		for range 200 {
			r := p.call(itk+"accounts:lookup", map[string]any{"idToken": "x.y.z"})
			if errCode(r) == "TOO_MANY_ATTEMPTS_TRY_LATER" {
				limited = true
				break
			}
		}
		if !limited {
			t.Fatal("200 calls from one address in a burst were all served")
		}
		// The refresh grant shares the address limit.
		resp, err := http.Post(e.URL+sts+"?key="+p.key, "application/x-www-form-urlencoded", strings.NewReader("grant_type=refresh_token&refresh_token=x"))
		if err != nil {
			t.Fatal(err)
		}
		authErr(t, decodeResponse(t, resp), "TOO_MANY_ATTEMPTS_TRY_LATER")
	})
}
