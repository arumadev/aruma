package acceptance

import (
	"encoding/json"
	"reflect"
	"testing"
)

// AC-A18: every P0 endpoint answers its v3 relyingparty alias (iOS) exactly as its v1 name.
// The same script runs in two projects, one through v1 and one through v3, and each response
// is compared field by field once the values that differ by nature (tokens, ids, timestamps,
// the project number) are masked. Errors are compared the same way, envelope included.

// parityStep is one call: its v1 and v3 paths and a body built from earlier responses.
type parityStep struct {
	name   string
	v1, v3 string
	method string // POST unless set
	body   func(s *parityState) any
}

// parityState carries what later steps need from earlier ones, per project.
type parityState struct {
	p       *authProject
	keyFile []byte
	session *Response // the latest session of the user
}

func (s *parityState) idToken() string { return s.session.str("idToken") }

// mailCode is the oobCode of the newest dev mailbox message to the script's user.
func (s *parityState) mailCode() string {
	_, q := linkCode(s.p.env.t, s.p.lastMail("ios@example.com")["link"].(string))
	return q.Get("oobCode")
}

var parityScript = []parityStep{
	{name: "sign up", v1: "accounts:signUp", v3: "signupNewUser", body: func(*parityState) any {
		return map[string]any{"email": "ios@example.com", "password": "secret1", "displayName": "Ios", "returnSecureToken": true}
	}},
	{name: "sign up again", v1: "accounts:signUp", v3: "signupNewUser", body: func(*parityState) any {
		return map[string]any{"email": "ios@example.com", "password": "secret1"}
	}},
	{name: "sign up, weak password", v1: "accounts:signUp", v3: "signupNewUser", body: func(*parityState) any {
		return map[string]any{"email": "weak@example.com", "password": "1"}
	}},
	{name: "sign in", v1: "accounts:signInWithPassword", v3: "verifyPassword", body: func(*parityState) any {
		return map[string]any{"email": "ios@example.com", "password": "secret1", "returnSecureToken": true}
	}},
	{name: "sign in, wrong password", v1: "accounts:signInWithPassword", v3: "verifyPassword", body: func(*parityState) any {
		return map[string]any{"email": "ios@example.com", "password": "wrong!!"}
	}},
	{name: "lookup", v1: "accounts:lookup", v3: "getAccountInfo", body: func(s *parityState) any {
		return map[string]any{"idToken": s.idToken()}
	}},
	{name: "lookup, bad token", v1: "accounts:lookup", v3: "getAccountInfo", body: func(*parityState) any {
		return map[string]any{"idToken": "a.b.c"}
	}},
	{name: "update profile", v1: "accounts:update", v3: "setAccountInfo", body: func(s *parityState) any {
		return map[string]any{"idToken": s.idToken(), "displayName": "iOS", "photoUrl": "https://example.com/i.png", "returnSecureToken": true}
	}},
	{name: "update password", v1: "accounts:update", v3: "setAccountInfo", body: func(s *parityState) any {
		return map[string]any{"idToken": s.idToken(), "password": "secret2", "returnSecureToken": true}
	}},
	{name: "update, no token", v1: "accounts:update", v3: "setAccountInfo", body: func(*parityState) any {
		return map[string]any{"displayName": "x"}
	}},
	{name: "fetch sign-in methods", v1: "accounts:createAuthUri", v3: "createAuthUri", body: func(*parityState) any {
		return map[string]any{"identifier": "ios@example.com", "continueUri": "http://localhost"}
	}},
	{name: "fetch sign-in methods, bad email", v1: "accounts:createAuthUri", v3: "createAuthUri", body: func(*parityState) any {
		return map[string]any{"identifier": "bad", "continueUri": "http://localhost"}
	}},
	{name: "send verification", v1: "accounts:sendOobCode", v3: "getOobConfirmationCode", body: func(s *parityState) any {
		return map[string]any{"requestType": "VERIFY_EMAIL", "idToken": s.idToken()}
	}},
	{name: "apply verification", v1: "accounts:update", v3: "setAccountInfo", body: func(s *parityState) any {
		return map[string]any{"oobCode": s.mailCode()}
	}},
	{name: "send password reset", v1: "accounts:sendOobCode", v3: "getOobConfirmationCode", body: func(*parityState) any {
		return map[string]any{"requestType": "PASSWORD_RESET", "email": "ios@example.com"}
	}},
	{name: "send password reset, unknown address", v1: "accounts:sendOobCode", v3: "getOobConfirmationCode", body: func(*parityState) any {
		return map[string]any{"requestType": "PASSWORD_RESET", "email": "nobody@example.com"}
	}},
	{name: "send, missing request type", v1: "accounts:sendOobCode", v3: "getOobConfirmationCode", body: func(*parityState) any {
		return map[string]any{"email": "ios@example.com"}
	}},
	{name: "inspect the reset code", v1: "accounts:resetPassword", v3: "resetPassword", body: func(s *parityState) any {
		return map[string]any{"oobCode": s.mailCode()}
	}},
	{name: "reset, weak password", v1: "accounts:resetPassword", v3: "resetPassword", body: func(s *parityState) any {
		return map[string]any{"oobCode": s.mailCode(), "newPassword": "1"}
	}},
	{name: "reset password", v1: "accounts:resetPassword", v3: "resetPassword", body: func(s *parityState) any {
		return map[string]any{"oobCode": s.mailCode(), "newPassword": "secret3"}
	}},
	{name: "reset again with the used code", v1: "accounts:resetPassword", v3: "resetPassword", body: func(s *parityState) any {
		return map[string]any{"oobCode": s.mailCode(), "newPassword": "secret4"}
	}},
	{name: "custom token", v1: "accounts:signInWithCustomToken", v3: "verifyCustomToken", body: func(s *parityState) any {
		return map[string]any{"token": customToken(s.p.env.t, s.keyFile, "custom-1", map[string]any{"tier": "gold"}, nil), "returnSecureToken": true}
	}},
	{name: "custom token, missing", v1: "accounts:signInWithCustomToken", v3: "verifyCustomToken", body: func(*parityState) any {
		return map[string]any{}
	}},
	{name: "project config", v1: "projects", v3: "getProjectConfig", method: "GET"},
	{name: "delete", v1: "accounts:delete", v3: "deleteAccount", body: func(s *parityState) any {
		return map[string]any{"idToken": s.idToken()}
	}},
	{name: "delete again", v1: "accounts:delete", v3: "deleteAccount", body: func(s *parityState) any {
		return map[string]any{"idToken": s.idToken()}
	}},
	// Phone and email-link sign-in are deferred; both families refuse them alike (AC-A17).
	{name: "email link sign-in", v1: "accounts:signInWithEmailLink", v3: "emailLinkSignin", body: func(*parityState) any {
		return map[string]any{"email": "ios@example.com", "oobCode": "x"}
	}},
	{name: "phone sign-in", v1: "accounts:sendVerificationCode", v3: "sendVerificationCode", body: func(*parityState) any {
		return map[string]any{"phoneNumber": "+15555550100"}
	}},
}

// maskedFields differ between two projects by nature and are compared for presence only.
var maskedFields = map[string]bool{
	"idToken": true, "refreshToken": true, "localId": true, "sessionId": true, "projectId": true,
	"createdAt": true, "lastLoginAt": true, "lastRefreshAt": true, "passwordUpdatedAt": true, "validSince": true,
}

// mask replaces masked values with a marker of their JSON type, recursively.
func mask(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if maskedFields[k] {
				out[k] = "<" + reflect.TypeOf(val).String() + ">"
			} else {
				out[k] = mask(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = mask(x[i])
		}
		return out
	}
	return v
}

func (step parityStep) call(s *parityState, path string) *Response {
	if step.method == "GET" {
		return s.p.env.client().do("GET", path+"?key="+s.p.key, nil)
	}
	return s.p.call(path, step.body(s))
}

func runParity(t *testing.T, script []parityStep) {
	e := newEnv(t, nil)
	v1p := e.authProject("ios-v1")
	v3p := v1p.secondProject("ios-v3")
	states := map[string]*parityState{"v1": {p: v1p}, "v3": {p: v3p}}
	_, states["v1"].keyFile = v1p.owner.serviceAccountKey("ios-v1", "backend")
	_, states["v3"].keyFile = v1p.owner.serviceAccountKey("ios-v3", "backend")

	for _, step := range script {
		t.Run(step.name, func(t *testing.T) {
			a := step.call(states["v1"], itk+step.v1)
			b := step.call(states["v3"], rp+step.v3)
			if a.Status != b.Status {
				t.Fatalf("status v1 %d, v3 %d\nv1: %s\nv3: %s", a.Status, b.Status, a.Body, b.Body)
			}
			ma, mb := mask(a.JSON), mask(b.JSON)
			if !reflect.DeepEqual(ma, mb) {
				ja, _ := json.Marshal(ma)
				jb, _ := json.Marshal(mb)
				t.Fatalf("responses differ\nv1: %s\nv3: %s", ja, jb)
			}
			for name, r := range map[string]*Response{"v1": a, "v3": b} {
				if r.Status == 200 && r.str("idToken") != "" {
					states[name].session = r
				}
			}
		})
	}
}

func TestAuthV3Parity(t *testing.T) {
	runParity(t, parityScript)
}
