package acceptance

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The rules of these tests: each user reads and writes only their own profile, whose age must
// be a number; a user listed under banned cannot write it; members can read a room.
const userRules = `{"rules": {
	"users": {"$uid": {
		".read": "auth != null && auth.uid == $uid",
		".write": "auth != null && auth.uid == $uid && root.child('banned/' + $uid).val() != true",
		"age": {".validate": "newData.isNumber() && newData.val() >= 0"}
	}},
	"rooms": {"$room": {".read": "root.child('members').child($room).child(auth.uid).exists()"}},
	"providers": {".read": "auth.provider == 'password' && auth.token.email_verified == false"}
}}`

// putRules loads a rules document as firebase-tools does, with the owner's credential.
func (e *Env) putRules(pat, ns, doc string) {
	e.t.Helper()
	req, _ := http.NewRequest("PUT", e.URL+"/.settings/rules.json?ns="+ns, strings.NewReader(doc))
	req.Header.Set("Authorization", "Bearer "+pat)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		e.t.Fatalf("rules: %d", resp.StatusCode)
	}
}

// claimsOf decodes an ID token's payload, unverified.
func claimsOf(t *testing.T, idToken string) map[string]any {
	t.Helper()
	parts := strings.Split(idToken, ".")
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// rulesSetUp is a project with the user rules, an admin connection that wrote the data, and
// its users.
func rulesSetUp(t *testing.T, e *Env) (ns string, users *rtdbUsers, pat string, adm *wsClient) {
	owner := e.owner()
	ns = owner.projectNS("chat-app")
	pat = owner.pat()
	users = e.rtdbUsers(owner)
	e.putRules(pat, ns, userRules)
	adm = e.dial(ns, dialOpts{})
	adm.call("auth", map[string]any{"cred": pat}).mustOK(t)
	return ns, users, pat, adm
}

// A user who signed up through Identity Toolkit connects with the ID token: the reply carries
// the auth variable and the token's expiry, and the rules see auth.uid, auth.token and
// auth.provider.
func TestRTDBUserSignIn(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	ns, users, _, adm := rulesSetUp(t, e)
	users.setUp("chat-app")
	up := ok(t, e.client().do("POST", itk+"accounts:signUp?key="+users.keys["chat-app"],
		map[string]any{"email": "sara@example.com", "password": "secret1", "returnSecureToken": true}))
	uid, idToken := up.str("localId"), up.str("idToken")
	adm.put("/users/"+uid, map[string]any{"name": "Sara"})
	adm.put("/users/other", map[string]any{"name": "Other"})
	adm.put("/members/r1/"+uid, true)
	adm.put("/rooms", map[string]any{"r1": map[string]any{"t": "hi"}, "r2": map[string]any{"t": "no"}})
	adm.put("/providers", 1)

	c := e.dial(ns, dialOpts{})
	r := c.call("auth", map[string]any{"cred": idToken})
	r.mustOK(t)
	var d struct {
		Auth    map[string]any `json:"auth"`
		Expires float64        `json:"expires"`
	}
	if err := json.Unmarshal(r.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Auth["uid"] != uid || d.Auth["provider"] != "password" || d.Auth["user_id"] != uid || d.Expires != claimsOf(t, idToken)["exp"] {
		t.Errorf("auth reply: %s", r.Data)
	}
	if tok, _ := d.Auth["token"].(map[string]any); tok["email"] != "sara@example.com" || tok["sub"] != uid {
		t.Errorf("auth.token: %v", tok)
	}

	if got := c.get("/users/" + uid); got != `{"name":"Sara"}` {
		t.Errorf("own profile: %s", got)
	}
	for _, p := range []string{"/users/other", "/users", "/rooms/r2"} {
		if r := c.call("g", map[string]any{"p": p, "q": map[string]any{}}); r.Status != "permission_denied" {
			t.Errorf("read %s: %+v", p, r)
		}
	}
	if got := c.get("/rooms/r1"); got != `{"t":"hi"}` {
		t.Errorf("root.child membership: %s", got)
	}
	if got := c.get("/providers"); got != `1` {
		t.Errorf("auth.provider and auth.token: %s", got)
	}
	c.put("/users/"+uid+"/age", 30)
	for _, w := range []struct {
		path  string
		value any
	}{{"/users/" + uid + "/age", "old"}, {"/users/" + uid + "/age", -1}, {"/users/other/name", "x"}} {
		if r := c.call("p", map[string]any{"p": w.path, "d": w.value}); r.Status != "permission_denied" {
			t.Errorf("write %s %v: %+v", w.path, w.value, r)
		}
	}
	if r := c.call("m", map[string]any{"p": "/users", "d": map[string]any{uid + "/a": 1, "other/a": 1}}); r.Status != "permission_denied" {
		t.Errorf("update reaching another user: %+v", r)
	}
	// root.child sees the writer's data: once banned, the user may no longer write.
	adm.put("/banned/"+uid, true)
	if r := c.call("p", map[string]any{"p": "/users/" + uid + "/age", "d": 31}); r.Status != "permission_denied" {
		t.Errorf("banned user's write: %+v", r)
	}
	// A listen it may read, then one it may not.
	c.call("q", map[string]any{"p": "/users/" + uid, "h": ""}).mustOK(t)
	if r := c.call("q", map[string]any{"p": "/users/other", "h": ""}); r.Status != "permission_denied" {
		t.Errorf("listen elsewhere: %+v", r)
	}

	// Tokens that do not verify, and the session they leave.
	if r := c.call("auth", map[string]any{"cred": idToken[:len(idToken)-4] + "AAAA"}); r.Status != "invalid_token" {
		t.Errorf("tampered token: %+v", r)
	}
	if r := c.call("g", map[string]any{"p": "/users/" + uid, "q": map[string]any{}}); r.Status != "permission_denied" {
		t.Errorf("after a refused token: %+v", r)
	}
}

// An Admin SDK with databaseAuthVariableOverride acts as the client it names (authvar), and
// with null as an unauthenticated one (noauth); without either, it bypasses the rules.
func TestRTDBAuthVariableOverride(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	ns, users, _, adm := rulesSetUp(t, e)
	adm.put("/users/u1", map[string]any{"name": "one"})
	adm.put("/users/u2", map[string]any{"name": "two"})
	users.setUp("chat-app")
	sa := users.admins["chat-app"].bearer

	c := e.dial(ns, dialOpts{})
	c.call("gauth", map[string]any{"cred": sa, "authvar": map[string]any{"uid": "u1"}}).mustOK(t)
	if got := c.get("/users/u1"); got != `{"name":"one"}` {
		t.Errorf("as u1: %s", got)
	}
	if r := c.call("g", map[string]any{"p": "/users/u2", "q": map[string]any{}}); r.Status != "permission_denied" {
		t.Errorf("as u1, u2's profile: %+v", r)
	}
	c.call("gauth", map[string]any{"cred": sa, "noauth": true}).mustOK(t)
	if r := c.call("g", map[string]any{"p": "/users/u1", "q": map[string]any{}}); r.Status != "permission_denied" {
		t.Errorf("noauth: %+v", r)
	}
	if r := c.call("gauth", map[string]any{"cred": sa, "authvar": []int{1}}); r.Status != "invalid_token" {
		t.Errorf("authvar that is not an object: %+v", r)
	}
	c.call("gauth", map[string]any{"cred": sa}).mustOK(t)
	if got := c.get("/users"); got != `{"u1":{"name":"one"},"u2":{"name":"two"}}` {
		t.Errorf("admin: %s", got)
	}
}

// readUntil reads messages until one satisfies match, within timeout, and returns them all.
func (c *wsClient) readUntil(timeout time.Duration, match func(string) bool) []string {
	c.t.Helper()
	var got []string
	deadline := time.Now().Add(timeout)
	for {
		m, err := c.read(time.Until(deadline))
		if err != nil {
			c.t.Fatalf("waiting: %v; got %q", err, got)
		}
		got = append(got, m)
		if match(m) {
			return got
		}
	}
}

// Refused credentials are answered as production answers them (wire golden
// production/auth.json): gauth takes admin credentials only, and a user's ID token on it is
// permission_denied; a token that is not a JWT "Could not parse auth token."; an unsigned one
// "Unsupported jwt algorithm: none".
func TestRTDBAuthRefusalsAsProduction(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	ns, users, _, _ := rulesSetUp(t, e)
	users.create("chat-app", "u1")
	tok := users.signIn("chat-app", "u1").str("idToken")
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"u1","user_id":"u1"}`)) + "."
	c := e.dial(ns, dialOpts{})
	for _, k := range []struct{ action, cred, status, text string }{
		{"gauth", tok, "permission_denied", "Permission denied"},
		{"auth", "garbage", "invalid_token", "Could not parse auth token."},
		{"auth", unsigned, "invalid_token", "Unsupported jwt algorithm: none"},
		{"auth", tok, "ok", ""},
	} {
		r := c.call(k.action, map[string]any{"cred": k.cred})
		if r.Status != k.status || k.text != "" && string(r.Data) != strconv.Quote(k.text) {
			t.Errorf("%s %.10s: %s %s, want %s %q", k.action, k.cred, r.Status, r.Data, k.status, k.text)
		}
	}
}

// A user disabled in the console or through the Admin API is cut off within a second: ac
// invalid_token, then c for each listen it may no longer read, and its writes are denied.
// Enabled again, the client refreshes its token, as the SDKs do after ac, and signs in anew.
func TestRTDBDisabledUserIsCutOff(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	ns, users, _, adm := rulesSetUp(t, e)
	users.create("chat-app", "u1")
	users.create("chat-app", "u2")
	adm.put("/users/u1", map[string]any{"name": "one"})
	in := users.signIn("chat-app", "u1")
	other := e.dial(ns, dialOpts{})
	other.call("auth", map[string]any{"cred": users.signIn("chat-app", "u2").str("idToken")}).mustOK(t)

	c := e.dial(ns, dialOpts{})
	c.call("auth", map[string]any{"cred": in.str("idToken")}).mustOK(t)
	c.call("q", map[string]any{"p": "/users/u1", "h": ""}).mustOK(t)
	c.call("q", map[string]any{"p": "/users/u1", "q": map[string]any{"l": 1, "vf": "l"}, "t": 2, "h": ""}).mustOK(t)
	c.next() // the listen's data
	c.next()

	start := time.Now()
	users.admin("chat-app", "accounts:update", map[string]any{"localId": "u1", "disableUser": true})
	got := c.readUntil(2*time.Second, func(m string) bool { return strings.Contains(m, `"q":null`) })
	if took := time.Since(start); took > time.Second {
		t.Errorf("cut off after %v", took)
	}
	want := []string{
		`{"t":"d","d":{"a":"ac","b":{"s":"invalid_token","d":"Auth token is no longer valid."}}}`,
		`{"t":"d","d":{"a":"c","b":{"p":"users/u1","q":[{"sin":true,"ein":true,"l":1,"vf":"l"}]}}}`,
		`{"t":"d","d":{"a":"c","b":{"p":"users/u1","q":null}}}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("cut off:\n got  %q\n want %q", got, want)
	}
	if r := c.call("p", map[string]any{"p": "/users/u1/name", "d": "x"}); r.Status != "permission_denied" {
		t.Errorf("write after: %+v", r)
	}
	// The same token is refused now, and so is its refresh.
	if r := c.call("auth", map[string]any{"cred": in.str("idToken")}); r.Status != "invalid_token" {
		t.Errorf("the disabled user's token: %+v", r)
	}
	if r := users.refresh("chat-app", in.str("refreshToken")); r.Status == 200 {
		t.Errorf("a disabled user refreshed: %s", r.Body)
	}
	// Another user's session is untouched.
	if got := other.call("g", map[string]any{"p": "/users/u2", "q": map[string]any{}}); got.Status != "ok" {
		t.Errorf("the other user: %+v", got)
	}

	users.admin("chat-app", "accounts:update", map[string]any{"localId": "u1", "disableUser": false})
	fresh := ok(t, users.refresh("chat-app", in.str("refreshToken")))
	c.call("auth", map[string]any{"cred": fresh.str("id_token")}).mustOK(t)
	c.call("q", map[string]any{"p": "/users/u1", "h": ""}).mustOK(t)
	if m := c.next(); m != `{"t":"d","d":{"a":"d","b":{"p":"users/u1","d":{"name":"one"}}}}` {
		t.Errorf("listen after the refresh: %s", m)
	}
}

// Revoking a user's sessions ends theirs too; signing in again starts a new one.
func TestRTDBRevokedSessionsAreCutOff(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	ns, users, _, _ := rulesSetUp(t, e)
	users.create("chat-app", "u1")
	c := e.dial(ns, dialOpts{})
	c.call("auth", map[string]any{"cred": users.signIn("chat-app", "u1").str("idToken")}).mustOK(t)
	nextSecond() // a session started in the same second as the revocation survives it
	users.owner.mustOK(users.owner.do("POST", consoleAuth("chat-app")+"/users/u1:revokeSessions", nil))
	if m := c.next(); m != `{"t":"d","d":{"a":"ac","b":{"s":"invalid_token","d":"Auth token is no longer valid."}}}` {
		t.Errorf("revoked: %s", m)
	}
	nextSecond()
	c.call("auth", map[string]any{"cred": users.signIn("chat-app", "u1").str("idToken")}).mustOK(t)
	c.put("/users/u1/name", "back")
}

// Editing the rules re-checks every open listen: those the new rules refuse are cancelled with
// c, the rest keep their data coming.
func TestRTDBRulesEditCancelsListens(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	ns, users, pat, adm := rulesSetUp(t, e)
	users.create("chat-app", "u1")
	adm.put("/users/u1", map[string]any{"name": "one"})
	adm.put("/members/r1/u1", true)
	adm.put("/rooms/r1", map[string]any{"t": "hi"})
	c := e.dial(ns, dialOpts{})
	c.call("auth", map[string]any{"cred": users.signIn("chat-app", "u1").str("idToken")}).mustOK(t)
	c.call("q", map[string]any{"p": "/users/u1", "h": ""}).mustOK(t)
	c.call("q", map[string]any{"p": "/rooms/r1", "h": ""}).mustOK(t)
	c.next()
	c.next()

	e.putRules(pat, ns, strings.Replace(userRules, `"auth != null && auth.uid == $uid"`, `false`, 1))
	if m := c.next(); m != `{"t":"d","d":{"a":"c","b":{"p":"users/u1","q":null}}}` {
		t.Errorf("after the edit: %s", m)
	}
	adm.put("/rooms/r1/t", "still")
	adm.put("/users/u1/name", "gone")
	if m := c.next(); m != `{"t":"d","d":{"a":"d","b":{"p":"rooms/r1/t","d":"still"}}}` {
		t.Errorf("the listen left alone: %s", m)
	}
	if m, err := c.read(300 * time.Millisecond); err == nil {
		t.Errorf("a push for the cancelled listen: %s", m)
	}
}
