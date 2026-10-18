package acceptance

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/osama300/aurma-project/internal/identity/queries"
)

// AC-A3 and AC-A4: for each P0 endpoint, the first error Aruma reports for combined invalid
// input, in the Identity Toolkit envelope, with enumeration protection on and off. Each row
// breaks several rules at once; the expected code is the one that must win. The order follows
// docs/research/phase2-identity-toolkit.md §3; where Aruma deliberately departs from the
// emulator (a disabled account is revealed only after its password is checked) the row says so.

// errFixture holds accounts in every state the tables need.
type errFixture struct {
	p        *authProject
	taken    *Response // a live password user, taken@example.com / secret1
	disabled *Response // off@example.com / secret1, disabled
	deleted  *Response // gone@example.com, deleted
	revoked  *Response // revoked@example.com, sessions revoked
	both     *Response // both@example.com, revoked and then disabled
	oldToken string    // an ID token of taken@ whose sign-in is an hour old

	keyFile, otherKey []byte // service account keys of this project and of another one
}

func newErrFixture(t *testing.T, e *Env) *errFixture {
	t.Helper()
	p := e.authProject("chat-app")
	f := &errFixture{p: p}
	f.taken = p.signUp("taken@example.com", "secret1")
	f.disabled = p.signUp("off@example.com", "secret1")
	f.deleted = p.signUp("gone@example.com", "secret1")
	f.revoked = p.signUp("revoked@example.com", "secret1")
	f.both = p.signUp("both@example.com", "secret1")
	old := p.signUp("old@example.com", "secret1")

	ok(t, p.call(itk+"accounts:delete", map[string]any{"idToken": f.deleted.str("idToken")}))
	// Revocation moves tokens_valid_after; a second ahead keeps the test off second boundaries.
	p.sql(`UPDATE auth.users SET tokens_valid_after = now() + interval '2 seconds' WHERE uid = ANY($1)`,
		[]string{f.revoked.str("localId"), f.both.str("localId")})
	p.sql(`UPDATE auth.users SET disabled = true WHERE uid = ANY($1)`, []string{f.disabled.str("localId"), f.both.str("localId")})
	p.sql(`UPDATE auth.refresh_tokens SET auth_time = now() - interval '1 hour' WHERE uid = $1`, old.str("localId"))
	f.oldToken = ok(t, p.refresh(old.str("refreshToken"))).str("id_token")

	_, f.keyFile = p.owner.serviceAccountKey("chat-app", "backend")
	p.owner.project("other-app")
	_, f.otherKey = p.owner.serviceAccountKey("other-app", "backend")
	// A custom-token user, disabled.
	ok(t, p.call(itk+"accounts:signInWithCustomToken", map[string]any{"token": customToken(t, f.keyFile, "ct-off", nil, nil)}))
	p.sql(`UPDATE auth.users SET disabled = true WHERE uid = 'ct-off'`)
	return f
}

// errRow is one request and the first error it must produce with protection on and off. An
// empty code means the request succeeds; check then inspects the response.
type errRow struct {
	name    string
	path    string
	body    func(f *errFixture) any
	with    func(*queries.AuthProjectConfig) // settings for this row only
	on, off string
	check   func(t *testing.T, r *Response, protection bool)
}

func (row errRow) run(t *testing.T, f *errFixture, protection bool) {
	t.Helper()
	p := f.p
	if row.with != nil {
		before, err := p.env.App.Auth.Config(t.Context(), p.ref())
		if err != nil {
			t.Fatal(err)
		}
		p.configure(row.with)
		defer p.configure(func(c *queries.AuthProjectConfig) { *c = before })
	}
	r := p.call(row.path, row.body(f))
	want := row.on
	if !protection {
		want = row.off
	}
	if want == "" {
		ok(t, r)
	} else {
		authErr(t, r, want)
	}
	if row.check != nil {
		row.check(t, r, protection)
	}
}

func obj(m map[string]any) func(*errFixture) any { return func(*errFixture) any { return m } }

var errTables = map[string][]errRow{
	"accounts:signUp": {
		{name: "empty body is anonymous, disabled by default", path: itk + "accounts:signUp", body: obj(map[string]any{}), on: "OPERATION_NOT_ALLOWED", off: "OPERATION_NOT_ALLOWED"},
		{name: "tenant beats every field check", path: itk + "accounts:signUp", body: obj(map[string]any{"email": "bad", "password": "1", "tenantId": "t1"}), on: "INVALID_TENANT_ID", off: "INVALID_TENANT_ID"},
		{name: "missing email beats weak password", path: itk + "accounts:signUp", body: obj(map[string]any{"password": "1"}), on: "MISSING_EMAIL", off: "MISSING_EMAIL"},
		{name: "missing password beats invalid email", path: itk + "accounts:signUp", body: obj(map[string]any{"email": "bad"}), on: "MISSING_PASSWORD", off: "MISSING_PASSWORD"},
		{name: "disabled method beats invalid email and weak password", path: itk + "accounts:signUp", body: obj(map[string]any{"email": "bad", "password": "1"}),
			with: func(c *queries.AuthProjectConfig) { c.PasswordEnabled = false }, on: "OPERATION_NOT_ALLOWED", off: "OPERATION_NOT_ALLOWED"},
		{name: "invalid email beats weak password", path: itk + "accounts:signUp", body: obj(map[string]any{"email": "bad", "password": "1"}), on: "INVALID_EMAIL", off: "INVALID_EMAIL"},
		{name: "taken email beats weak password, even under protection", path: itk + "accounts:signUp", body: obj(map[string]any{"email": "TAKEN@example.com", "password": "1"}), on: "EMAIL_EXISTS", off: "EMAIL_EXISTS"},
		{name: "weak password beats a bad idToken", path: itk + "accounts:signUp", body: obj(map[string]any{"email": "new1@example.com", "password": "1", "idToken": "garbage"}), on: "WEAK_PASSWORD", off: "WEAK_PASSWORD"},
		{name: "bad idToken on link", path: itk + "accounts:signUp", body: obj(map[string]any{"email": "new2@example.com", "password": "secret1", "idToken": "garbage"}), on: "INVALID_ID_TOKEN", off: "INVALID_ID_TOKEN"},
		{name: "link to a disabled user", path: itk + "accounts:signUp", body: func(f *errFixture) any {
			return map[string]any{"email": "new3@example.com", "password": "secret1", "idToken": f.disabled.str("idToken")}
		}, on: "USER_DISABLED", off: "USER_DISABLED"},
		{name: "link to a deleted user", path: itk + "accounts:signUp", body: func(f *errFixture) any {
			return map[string]any{"email": "new4@example.com", "password": "secret1", "idToken": f.deleted.str("idToken")}
		}, on: "USER_NOT_FOUND", off: "USER_NOT_FOUND"},
		{name: "client sign-up blocked", path: itk + "accounts:signUp", body: obj(map[string]any{"email": "new5@example.com", "password": "secret1"}),
			with: func(c *queries.AuthProjectConfig) { c.ClientSignupEnabled = false }, on: "ADMIN_ONLY_OPERATION", off: "ADMIN_ONLY_OPERATION"},
		{name: "field checks beat the client sign-up block", path: itk + "accounts:signUp", body: obj(map[string]any{"email": "bad", "password": "secret1"}),
			with: func(c *queries.AuthProjectConfig) { c.ClientSignupEnabled = false }, on: "INVALID_EMAIL", off: "INVALID_EMAIL"},
		{name: "anonymous enabled but client sign-up blocked", path: itk + "accounts:signUp", body: obj(map[string]any{}),
			with: func(c *queries.AuthProjectConfig) { c.AnonymousEnabled, c.ClientSignupEnabled = true, false }, on: "ADMIN_ONLY_OPERATION", off: "ADMIN_ONLY_OPERATION"},
	},
	"accounts:signInWithPassword": {
		{name: "disabled method beats missing fields", path: itk + "accounts:signInWithPassword", body: obj(map[string]any{}),
			with: func(c *queries.AuthProjectConfig) { c.PasswordEnabled = false }, on: "PASSWORD_LOGIN_DISABLED", off: "PASSWORD_LOGIN_DISABLED"},
		{name: "tenant beats missing email", path: itk + "accounts:signInWithPassword", body: obj(map[string]any{"tenantId": "t1"}), on: "INVALID_TENANT_ID", off: "INVALID_TENANT_ID"},
		{name: "missing email beats missing password", path: itk + "accounts:signInWithPassword", body: obj(map[string]any{"password": ""}), on: "MISSING_EMAIL", off: "MISSING_EMAIL"},
		{name: "invalid email beats missing password", path: itk + "accounts:signInWithPassword", body: obj(map[string]any{"email": "bad"}), on: "INVALID_EMAIL", off: "INVALID_EMAIL"},
		{name: "missing password", path: itk + "accounts:signInWithPassword", body: obj(map[string]any{"email": "taken@example.com"}), on: "MISSING_PASSWORD", off: "MISSING_PASSWORD"},
		{name: "unknown email", path: itk + "accounts:signInWithPassword", body: obj(map[string]any{"email": "nobody@example.com", "password": "secret1"}), on: "INVALID_LOGIN_CREDENTIALS", off: "EMAIL_NOT_FOUND"},
		{name: "wrong password", path: itk + "accounts:signInWithPassword", body: obj(map[string]any{"email": "taken@example.com", "password": "wrong!!"}), on: "INVALID_LOGIN_CREDENTIALS", off: "INVALID_PASSWORD"},
		// The emulator says USER_DISABLED before the password check; Aruma does not reveal the
		// account's state to someone without the password (research §3.2).
		{name: "disabled account, wrong password: the password wins", path: itk + "accounts:signInWithPassword", body: obj(map[string]any{"email": "off@example.com", "password": "wrong!!"}), on: "INVALID_LOGIN_CREDENTIALS", off: "INVALID_PASSWORD"},
		{name: "disabled account, right password", path: itk + "accounts:signInWithPassword", body: obj(map[string]any{"email": "off@example.com", "password": "secret1"}), on: "USER_DISABLED", off: "USER_DISABLED"},
	},
	"accounts:signInWithCustomToken": {
		{name: "missing token", path: itk + "accounts:signInWithCustomToken", body: obj(map[string]any{}), on: "MISSING_CUSTOM_TOKEN", off: "MISSING_CUSTOM_TOKEN"},
		{name: "malformed token", path: itk + "accounts:signInWithCustomToken", body: obj(map[string]any{"token": "garbage"}), on: "INVALID_CUSTOM_TOKEN", off: "INVALID_CUSTOM_TOKEN"},
		{name: "another project beats forbidden claims", path: itk + "accounts:signInWithCustomToken", body: func(f *errFixture) any {
			return map[string]any{"token": customTokenT(f, f.otherKey, "u1", map[string]any{"sub": "x"}, nil)}
		}, on: "CREDENTIAL_MISMATCH", off: "CREDENTIAL_MISMATCH"},
		{name: "expiry beats forbidden claims", path: itk + "accounts:signInWithCustomToken", body: func(f *errFixture) any {
			return map[string]any{"token": customTokenT(f, f.keyFile, "u1", map[string]any{"sub": "x"}, func(m map[string]any) {
				m["iat"], m["exp"] = time.Now().Add(-2*time.Hour).Unix(), time.Now().Add(-time.Hour).Unix()
			})}
		}, on: "INVALID_CUSTOM_TOKEN", off: "INVALID_CUSTOM_TOKEN"},
		{name: "forbidden claim beats a disabled user", path: itk + "accounts:signInWithCustomToken", body: func(f *errFixture) any {
			return map[string]any{"token": customTokenT(f, f.keyFile, "ct-off", map[string]any{"firebase": 1}, nil)}
		}, on: "FORBIDDEN_CLAIM", off: "FORBIDDEN_CLAIM"},
		{name: "claims over 1000 bytes", path: itk + "accounts:signInWithCustomToken", body: func(f *errFixture) any {
			return map[string]any{"token": customTokenT(f, f.keyFile, "u1", map[string]any{"blob": strings.Repeat("x", 1001)}, nil)}
		}, on: "CLAIMS_TOO_LARGE", off: "CLAIMS_TOO_LARGE"},
		{name: "disabled user", path: itk + "accounts:signInWithCustomToken", body: func(f *errFixture) any {
			return map[string]any{"token": customTokenT(f, f.keyFile, "ct-off", nil, nil)}
		}, on: "USER_DISABLED", off: "USER_DISABLED"},
	},
	"accounts:lookup": {
		{name: "missing token", path: itk + "accounts:lookup", body: obj(map[string]any{}), on: "INVALID_ID_TOKEN", off: "INVALID_ID_TOKEN"},
		{name: "malformed token", path: itk + "accounts:lookup", body: obj(map[string]any{"idToken": "a.b.c"}), on: "INVALID_ID_TOKEN", off: "INVALID_ID_TOKEN"},
		{name: "deleted user", path: itk + "accounts:lookup", body: tokenOf(func(f *errFixture) string { return f.deleted.str("idToken") }), on: "USER_NOT_FOUND", off: "USER_NOT_FOUND"},
		{name: "disabled user", path: itk + "accounts:lookup", body: tokenOf(func(f *errFixture) string { return f.disabled.str("idToken") }), on: "USER_DISABLED", off: "USER_DISABLED"},
		{name: "revoked session", path: itk + "accounts:lookup", body: tokenOf(func(f *errFixture) string { return f.revoked.str("idToken") }), on: "TOKEN_EXPIRED", off: "TOKEN_EXPIRED"},
		{name: "disabled beats revoked", path: itk + "accounts:lookup", body: tokenOf(func(f *errFixture) string { return f.both.str("idToken") }), on: "USER_DISABLED", off: "USER_DISABLED"},
	},
	"accounts:update": {
		{name: "tenant beats a missing token", path: itk + "accounts:update", body: obj(map[string]any{"tenantId": "t1"}), on: "INVALID_TENANT_ID", off: "INVALID_TENANT_ID"},
		{name: "neither idToken nor oobCode", path: itk + "accounts:update", body: obj(map[string]any{"displayName": "x"}), on: "INVALID_REQ_TYPE", off: "INVALID_REQ_TYPE"},
		{name: "oobCode is handled before the idToken", path: itk + "accounts:update", body: func(f *errFixture) any {
			return map[string]any{"oobCode": "nope", "idToken": f.taken.str("idToken"), "displayName": "x"}
		}, on: "INVALID_OOB_CODE", off: "INVALID_OOB_CODE"},
		{name: "bad token beats bad fields", path: itk + "accounts:update", body: obj(map[string]any{"idToken": "garbage", "email": "bad", "password": "1"}), on: "INVALID_ID_TOKEN", off: "INVALID_ID_TOKEN"},
		{name: "disabled user beats bad fields", path: itk + "accounts:update", body: func(f *errFixture) any {
			return map[string]any{"idToken": f.disabled.str("idToken"), "email": "bad", "password": "1"}
		}, on: "USER_DISABLED", off: "USER_DISABLED"},
		{name: "old sign-in beats bad fields", path: itk + "accounts:update", body: func(f *errFixture) any {
			return map[string]any{"idToken": f.oldToken, "email": "bad", "password": "1"}
		}, on: "CREDENTIAL_TOO_OLD_LOGIN_AGAIN", off: "CREDENTIAL_TOO_OLD_LOGIN_AGAIN"},
		{name: "email change: blocked under protection, else invalid email beats weak password", path: itk + "accounts:update", body: func(f *errFixture) any {
			return map[string]any{"idToken": f.taken.str("idToken"), "email": "bad", "password": "1"}
		}, on: "OPERATION_NOT_ALLOWED", off: "INVALID_EMAIL"},
		{name: "email change to a taken address", path: itk + "accounts:update", body: func(f *errFixture) any {
			return map[string]any{"idToken": f.taken.str("idToken"), "email": "off@example.com"}
		}, on: "OPERATION_NOT_ALLOWED", off: "EMAIL_EXISTS"},
		{name: "weak password beats a bad photo URL", path: itk + "accounts:update", body: func(f *errFixture) any {
			return map[string]any{"idToken": f.taken.str("idToken"), "password": "1", "photoUrl": "ftp://x"}
		}, on: "WEAK_PASSWORD", off: "WEAK_PASSWORD"},
		{name: "bad photo URL", path: itk + "accounts:update", body: func(f *errFixture) any {
			return map[string]any{"idToken": f.taken.str("idToken"), "photoUrl": "ftp://x"}
		}, on: "INVALID_PHOTO_URL", off: "INVALID_PHOTO_URL"},
		{name: "unknown deleteAttribute", path: itk + "accounts:update", body: func(f *errFixture) any {
			return map[string]any{"idToken": f.taken.str("idToken"), "deleteAttribute": []string{"EMAIL"}}
		}, on: "INVALID_REQ_TYPE", off: "INVALID_REQ_TYPE"},
	},
	"accounts:delete": {
		{name: "missing token", path: itk + "accounts:delete", body: obj(map[string]any{}), on: "INVALID_ID_TOKEN", off: "INVALID_ID_TOKEN"},
		{name: "deleted user", path: itk + "accounts:delete", body: tokenOf(func(f *errFixture) string { return f.deleted.str("idToken") }), on: "USER_NOT_FOUND", off: "USER_NOT_FOUND"},
		{name: "disabled user", path: itk + "accounts:delete", body: tokenOf(func(f *errFixture) string { return f.disabled.str("idToken") }), on: "USER_DISABLED", off: "USER_DISABLED"},
		{name: "old sign-in", path: itk + "accounts:delete", body: tokenOf(func(f *errFixture) string { return f.oldToken }), on: "CREDENTIAL_TOO_OLD_LOGIN_AGAIN", off: "CREDENTIAL_TOO_OLD_LOGIN_AGAIN"},
	},
	"accounts:createAuthUri": {
		{name: "tenant beats a missing identifier", path: itk + "accounts:createAuthUri", body: obj(map[string]any{"tenantId": "t1"}), on: "INVALID_TENANT_ID", off: "INVALID_TENANT_ID"},
		{name: "missing identifier", path: itk + "accounts:createAuthUri", body: obj(map[string]any{"continueUri": "http://localhost"}), on: "MISSING_IDENTIFIER", off: "MISSING_IDENTIFIER"},
		{name: "invalid identifier", path: itk + "accounts:createAuthUri", body: obj(map[string]any{"identifier": "bad", "continueUri": "http://localhost"}), on: "INVALID_IDENTIFIER", off: "INVALID_IDENTIFIER"},
		{name: "registered email", path: itk + "accounts:createAuthUri", body: obj(map[string]any{"identifier": "taken@example.com", "continueUri": "http://localhost"}),
			check: func(t *testing.T, r *Response, protection bool) {
				methods, _ := r.JSON["signinMethods"].([]any)
				_, hasRegistered := r.JSON["registered"]
				switch {
				case r.str("sessionId") == "":
					t.Fatalf("no sessionId: %s", r.Body)
				case protection && (len(methods) != 0 || hasRegistered):
					t.Fatalf("protection on must hide the account: %s", r.Body)
				case !protection && (r.JSON["registered"] != true || len(methods) != 1 || methods[0] != "password"):
					t.Fatalf("protection off must list the methods: %s", r.Body)
				}
			}},
		{name: "unknown email", path: itk + "accounts:createAuthUri", body: obj(map[string]any{"identifier": "nobody@example.com", "continueUri": "http://localhost"}),
			check: func(t *testing.T, r *Response, protection bool) {
				_, hasRegistered := r.JSON["registered"]
				if protection == hasRegistered || (!protection && r.JSON["registered"] != false) {
					t.Fatalf("registered with protection=%v: %s", protection, r.Body)
				}
			}},
	},
	"token": {
		{name: "missing grant type beats a missing refresh token", path: sts, body: obj(map[string]any{}), on: "MISSING_GRANT_TYPE", off: "MISSING_GRANT_TYPE"},
		{name: "wrong grant type beats a missing refresh token", path: sts, body: obj(map[string]any{"grantType": "password"}), on: "INVALID_GRANT_TYPE", off: "INVALID_GRANT_TYPE"},
		{name: "missing refresh token", path: sts, body: obj(map[string]any{"grantType": "refresh_token"}), on: "MISSING_REFRESH_TOKEN", off: "MISSING_REFRESH_TOKEN"},
		{name: "unknown refresh token", path: sts, body: obj(map[string]any{"grantType": "refresh_token", "refreshToken": "garbage"}), on: "INVALID_REFRESH_TOKEN", off: "INVALID_REFRESH_TOKEN"},
		{name: "deleted user", path: sts, body: refreshOf(func(f *errFixture) *Response { return f.deleted }), on: "USER_NOT_FOUND", off: "USER_NOT_FOUND"},
		{name: "disabled beats revoked", path: sts, body: refreshOf(func(f *errFixture) *Response { return f.both }), on: "USER_DISABLED", off: "USER_DISABLED"},
		{name: "revoked session", path: sts, body: refreshOf(func(f *errFixture) *Response { return f.revoked }), on: "TOKEN_EXPIRED", off: "TOKEN_EXPIRED"},
	},
}

// Email action tables, written against research §3.7 and §3.8 for agent A's implementation.
var pendingErrTables = map[string][]errRow{
	"accounts:sendOobCode": {
		{name: "missing request type", path: itk + "accounts:sendOobCode", body: obj(map[string]any{"email": "bad"}), on: "MISSING_REQ_TYPE", off: "MISSING_REQ_TYPE"},
		{name: "missing email beats a bad continueUrl", path: itk + "accounts:sendOobCode", body: obj(map[string]any{"requestType": "PASSWORD_RESET", "continueUrl": "https://evil.example/"}), on: "MISSING_EMAIL", off: "MISSING_EMAIL"},
		{name: "invalid email", path: itk + "accounts:sendOobCode", body: obj(map[string]any{"requestType": "PASSWORD_RESET", "email": "bad"}), on: "INVALID_EMAIL", off: "INVALID_EMAIL"},
		{name: "unknown email: silent under protection", path: itk + "accounts:sendOobCode", body: obj(map[string]any{"requestType": "PASSWORD_RESET", "email": "nobody@example.com"}), on: "", off: "EMAIL_NOT_FOUND"},
		{name: "unauthorised continueUrl", path: itk + "accounts:sendOobCode", body: obj(map[string]any{"requestType": "PASSWORD_RESET", "email": "taken@example.com", "continueUrl": "https://evil.example/next"}), on: "UNAUTHORIZED_DOMAIN", off: "UNAUTHORIZED_DOMAIN"},
		{name: "malformed continueUrl", path: itk + "accounts:sendOobCode", body: obj(map[string]any{"requestType": "PASSWORD_RESET", "email": "taken@example.com", "continueUrl": "not a url"}), on: "INVALID_CONTINUE_URI", off: "INVALID_CONTINUE_URI"},
		{name: "verify email needs an idToken", path: itk + "accounts:sendOobCode", body: obj(map[string]any{"requestType": "VERIFY_EMAIL"}), on: "INVALID_ID_TOKEN", off: "INVALID_ID_TOKEN"},
		{name: "email-link sign-in is deferred", path: itk + "accounts:sendOobCode", body: obj(map[string]any{"requestType": "EMAIL_SIGNIN", "email": "taken@example.com"}), on: "OPERATION_NOT_ALLOWED", off: "OPERATION_NOT_ALLOWED"},
	},
	"accounts:resetPassword": {
		{name: "missing code", path: itk + "accounts:resetPassword", body: obj(map[string]any{"newPassword": "1"}), on: "MISSING_OOB_CODE", off: "MISSING_OOB_CODE"},
		{name: "unknown code beats a weak password", path: itk + "accounts:resetPassword", body: obj(map[string]any{"oobCode": "nope", "newPassword": "1"}), on: "INVALID_OOB_CODE", off: "INVALID_OOB_CODE"},
	},
}

func tokenOf(fn func(f *errFixture) string) func(*errFixture) any {
	return func(f *errFixture) any { return map[string]any{"idToken": fn(f)} }
}

func refreshOf(fn func(f *errFixture) *Response) func(*errFixture) any {
	return func(f *errFixture) any {
		return map[string]any{"grantType": "refresh_token", "refreshToken": fn(f).str("refreshToken")}
	}
}

func customTokenT(f *errFixture, key []byte, uid string, extra map[string]any, mutate func(map[string]any)) string {
	return customToken(f.p.env.t, key, uid, extra, mutate)
}

func TestAuthErrorTables(t *testing.T) {
	for _, protection := range []bool{true, false} {
		name := "protection on"
		if !protection {
			name = "protection off"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, nil)
			f := newErrFixture(t, e)
			f.p.protection(protection)
			for endpoint, rows := range errTables {
				t.Run(endpoint, func(t *testing.T) {
					for _, row := range rows {
						t.Run(row.name, func(t *testing.T) { row.run(t, f, protection) })
					}
				})
			}
			for endpoint, rows := range pendingErrTables {
				t.Run(endpoint, func(t *testing.T) {
					t.Skip("pending email actions (sendOobCode, resetPassword), enabled at merge")
					for _, row := range rows {
						t.Run(row.name, func(t *testing.T) { row.run(t, f, protection) })
					}
				})
			}
		})
	}
}

// The API key is checked before anything in the body.
func TestAuthErrorAPIKeyFirst(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	bad := map[string]any{"email": "bad", "password": "1", "tenantId": "t1"}
	for _, path := range []string{itk + "accounts:signUp", itk + "accounts:signInWithPassword", itk + "accounts:lookup", sts} {
		t.Run(path, func(t *testing.T) {
			r := e.client().do("POST", path, bad)
			if r.Status != 403 || r.str("error", "message") != "The request is missing a valid API key." || r.errStatus() != "PERMISSION_DENIED" {
				t.Fatalf("missing key: %d %s", r.Status, r.Body)
			}
			authErr(t, e.client().do("POST", path+"?key=AIzaNotARealKeyAtAllxxxxxxxxxxxxxxxxxxx", bad), "API key not valid. Please pass a valid API key.")
			// Malformed JSON is reported after the key, as INVALID_JSON.
			resp, err := http.Post(e.URL+path+"?key="+p.key, "application/json", strings.NewReader("{not json"))
			if err != nil {
				t.Fatal(err)
			}
			authErr(t, decodeResponse(t, resp), "INVALID_JSON")
		})
	}
}
