package ops

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"
)

// verifyIDToken checks an ID token's signature against the server's published keys, and its
// issuer, audience and claims; it returns the claims.
func verifyIDToken(t *testing.T, a *api, p *project, tok string) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("%s: not a JWT: %q", p.ID, tok)
	}
	dec := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var hdr struct{ Alg, Kid string }
	var claims map[string]any
	_ = json.Unmarshal(dec(parts[0]), &hdr)
	_ = json.Unmarshal(dec(parts[1]), &claims)
	iss, _ := claims["iss"].(string)
	disc := a.must("GET", "/issuers/"+p.ID+"/.well-known/openid-configuration", nil, nil)
	if disc.str("issuer") != iss {
		t.Fatalf("%s: token issuer %q, discovery %q", p.ID, iss, disc.str("issuer"))
	}
	jwks := a.must("GET", disc.str("jwks_uri"), nil, nil)
	var set struct {
		Keys []struct{ Kid, N, E string } `json:"keys"`
	}
	_ = json.Unmarshal(jwks.Body, &set)
	for _, k := range set.Keys {
		if k.Kid != hdr.Kid {
			continue
		}
		n := new(big.Int).SetBytes(dec(k.N))
		e := int(new(big.Int).SetBytes(dec(k.E)).Int64())
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(&rsa.PublicKey{N: n, E: e}, crypto.SHA256, sum[:], dec(parts[2])); err != nil {
			t.Fatalf("%s: signature of the ID token: %v", p.ID, err)
		}
		if claims["aud"] != p.ID {
			t.Fatalf("%s: aud = %v", p.ID, claims["aud"])
		}
		return claims
	}
	t.Fatalf("%s: key %q of the ID token is not in the server's JWKS", p.ID, hdr.Kid)
	return nil
}

func signIn(a *api, p *project, email, password string) reply {
	return a.do("POST", itk+"accounts:signInWithPassword?key="+p.Key, nil, map[string]any{"email": email, "password": password, "returnSecureToken": true})
}

func refresh(a *api, p *project, token string) reply {
	return a.do("POST", sts+"?key="+p.Key, nil, map[string]any{"grant_type": "refresh_token", "refresh_token": token})
}

func authErr(r reply) string {
	s := r.str("error", "message")
	code, _, _ := strings.Cut(s, " ")
	return code
}

// verifyBehaviour checks that a deployment holding the fixture works: sign-in, tokens, rules,
// indexes, keys. It writes new data (new users, documents, objects), so it runs after capture.
func verifyBehaviour(t *testing.T, d *deployment, fx *fixture, refreshIssuedBefore bool) {
	t.Helper()
	a := newAPI(t, d.base())
	o := &owner{a: a, pat: fx.PAT}
	for _, p := range fx.Projects {
		t.Run(p.ID, func(t *testing.T) {
			a := newAPI(t, d.base())
			o := &owner{a: a, pat: fx.PAT}
			verifyAuth(t, a, p, refreshIssuedBefore)
			verifyRTDBRules(t, a, p)
			verifyDocumentRules(t, o, a, p)
			verifyStorageRules(t, a, p)
			verifyKeys(t, a, p)
		})
	}
	_ = o
}

func verifyAuth(t *testing.T, a *api, p *project, refreshIssuedBefore bool) {
	t.Helper()
	al, cl, dis := p.Users["alice"], p.Users["claims"], p.Users["disabled"]
	// Passwords still sign in; a wrong one and the disabled user are refused as before.
	for _, u := range []*user{al, cl} {
		r := signIn(a, p, u.Email, u.Password)
		if r.Status != 200 || r.str("localId") != u.UID {
			t.Errorf("%s: sign-in of %s after the change: %s", p.ID, u.Email, r)
			continue
		}
		c := verifyIDToken(t, a, p, r.str("idToken"))
		if u == cl && (c["role"] != "admin" || c["level"] != float64(3)) {
			t.Errorf("%s: custom claims lost: %v", p.ID, c)
		}
	}
	if e := authErr(signIn(a, p, al.Email, "wrong-password")); e != "INVALID_LOGIN_CREDENTIALS" {
		t.Errorf("%s: wrong password: %q", p.ID, e)
	}
	if e := authErr(signIn(a, p, dis.Email, dis.Password)); e != "USER_DISABLED" {
		t.Errorf("%s: disabled user: %q", p.ID, e)
	}
	// An ID token issued before verifies against the keys the server publishes now.
	for name, u := range p.Users {
		if u.IDToken == "" {
			continue
		}
		c := verifyIDToken(t, a, p, u.IDToken)
		if c["user_id"] != u.UID && c["sub"] != u.UID {
			t.Errorf("%s: token of %s names %v", p.ID, name, c["sub"])
		}
	}
	// A refresh token issued before the change: the sessions are rows of the database.
	if refreshIssuedBefore {
		for name, u := range p.Users {
			if u.Refresh == "" {
				continue
			}
			r := refresh(a, p, u.Refresh)
			if r.Status != 200 || r.str("user_id") != u.UID {
				t.Errorf("%s: refresh token of %s: %s", p.ID, name, r)
				continue
			}
			verifyIDToken(t, a, p, r.str("id_token"))
		}
	}
	// New users: the sequences and unique indexes continue.
	n := signUpNew(t, a, p, fmt.Sprintf("after-%d@%s.example", time.Now().UnixNano(), p.ID))
	if n == "" {
		t.Errorf("%s: sign-up after the change failed", p.ID)
	}
}

func signUpNew(t *testing.T, a *api, p *project, email string) string {
	r := a.do("POST", itk+"accounts:signUp?key="+p.Key, nil, map[string]any{"email": email, "password": "after-pass-1", "returnSecureToken": true})
	if r.Status != 200 {
		t.Errorf("sign-up: %s", r)
		return ""
	}
	return r.str("localId")
}

func verifyKeys(t *testing.T, a *api, p *project) {
	t.Helper()
	// The key restricted to a referrer still is: another site is refused, the allowed one is not.
	body := map[string]any{"returnSecureToken": true}
	r := a.do("POST", itk+"accounts:signUp?key="+p.LimitedKey, map[string]string{"Referer": "https://evil.example/"}, body)
	if r.Status != 403 {
		t.Errorf("%s: restricted key from another referrer: %s", p.ID, r)
	}
	r = a.do("POST", itk+"accounts:signUp?key="+p.LimitedKey, map[string]string{"Referer": "https://allowed.example/app"}, body)
	if r.Status != 200 {
		t.Errorf("%s: restricted key from its referrer: %s", p.ID, r)
	}
	if r := a.do("POST", itk+"accounts:signUp?key=not-a-key", nil, body); r.Status == 200 {
		t.Errorf("%s: an unknown key signs up", p.ID)
	}
	// The service account's key file still exchanges for a token.
	if tok, status := saToken(t, a, []byte(p.SAKey)); tok == "" {
		t.Errorf("%s: service account key refused: %d", p.ID, status)
	}
}

func verifyRTDBRules(t *testing.T, a *api, p *project) {
	t.Helper()
	al, cl := p.Users["alice"], p.Users["claims"]
	tokenOf := func(u *user) string {
		return signIn(a, p, u.Email, u.Password).str("idToken")
	}
	alice, claims := tokenOf(al), tokenOf(cl)
	read := func(path, tok string) int {
		q := ""
		if tok != "" {
			q = "&auth=" + url.QueryEscape(tok)
		}
		return a.do("GET", rtdbURL(p, path, q), nil, nil).Status
	}
	if s := read("pub/greeting", ""); s != 200 {
		t.Errorf("%s: rtdb public read: %d", p.ID, s)
	}
	if s := read("private", ""); s == 200 {
		t.Errorf("%s: rtdb private read without a credential: %d", p.ID, s)
	}
	if s := read("users/"+al.UID, alice); s != 200 {
		t.Errorf("%s: rtdb own node: %d", p.ID, s)
	}
	if s := read("users/"+p.Users["anon"].UID, alice); s == 200 {
		t.Errorf("%s: rtdb another user's node read: %d", p.ID, s)
	}
	if s := read("chat", claims); s != 200 {
		t.Errorf("%s: rtdb chat read: %d", p.ID, s)
	}
	put := func(tok string) int {
		return a.do("PUT", rtdbURL(p, "chat/m3", "&auth="+url.QueryEscape(tok)), nil, map[string]any{"text": "after"}).Status
	}
	if s := put(claims); s != 200 {
		t.Errorf("%s: rtdb write by the admin claim: %d", p.ID, s)
	}
	if s := put(alice); s == 200 {
		t.Errorf("%s: rtdb write without the claim: %d", p.ID, s)
	}
}

func verifyDocumentRules(t *testing.T, o *owner, a *api, p *project) {
	t.Helper()
	base := docsBase(p)
	al := p.Users["alice"]
	tok := signIn(a, p, al.Email, al.Password).str("idToken")
	if r := a.do("GET", base+"/documents/pub/x", nil, nil); r.Status != 200 {
		t.Errorf("%s: documents: public read: %s", p.ID, r)
	}
	if r := a.do("GET", base+"/documents/priv/x", nil, nil); r.Status != 403 {
		t.Errorf("%s: documents: private read without a credential: %s", p.ID, r)
	}
	if r := a.do("GET", base+"/documents/priv/x", bearer(tok), nil); r.Status != 403 {
		t.Errorf("%s: documents: private read by a user: %s", p.ID, r)
	}
	if r := a.do("GET", base+"/documents/users/"+al.UID, bearer(tok), nil); r.Status != 200 {
		t.Errorf("%s: documents: own document: %s", p.ID, r)
	}
	if r := a.do("GET", base+"/documents/users/someone-else", bearer(tok), nil); r.Status != 403 {
		t.Errorf("%s: documents: another user's document: %s", p.ID, r)
	}
	q := func(field string) reply {
		return a.do("POST", base+"/documents:runQuery", bearer(o.pat), map[string]any{"structuredQuery": map[string]any{
			"from":    []any{map[string]any{"collectionId": "cities"}},
			"where":   map[string]any{"fieldFilter": map[string]any{"field": map[string]any{"fieldPath": "country"}, "op": "EQUAL", "value": map[string]any{"stringValue": "US"}}},
			"orderBy": []any{map[string]any{"field": map[string]any{"fieldPath": field}, "direction": "DESCENDING"}}}})
	}
	// The composite index (country, pop) serves this query; there is none for (country, name).
	if r := q("pop"); r.Status != 200 || strings.Count(string(r.Body), `"document"`) != 2 {
		t.Errorf("%s: documents: indexed query: %s", p.ID, r)
	}
	if r := q("name"); r.Status != 400 || !strings.Contains(string(r.Body), "FAILED_PRECONDITION") {
		t.Errorf("%s: documents: a query without its index is not refused: %s", p.ID, r)
	}
}

func verifyStorageRules(t *testing.T, a *api, p *project) {
	t.Helper()
	al := p.Users["alice"]
	tok := signIn(a, p, al.Email, al.Password).str("idToken")
	fb := map[string]string{"Authorization": "Firebase " + tok}
	obj := func(n string) string { return "/v0/b/" + p.Bucket + "/o/" + url.PathEscape(n) }
	if r := a.do("GET", obj("pub/small.txt")+"?alt=media", nil, nil); r.Status != 200 || string(r.Body) != string(det(p.ID+"/pub/small.txt", 5)) {
		t.Errorf("%s: storage: public read: %s", p.ID, r)
	}
	if r := a.do("GET", obj("data/multi.bin")+"?alt=media", nil, nil); r.Status != 403 {
		t.Errorf("%s: storage: private read without a credential: %s", p.ID, r)
	}
	if r := a.do("GET", obj("users/"+al.UID+"/avatar.png")+"?alt=media", fb, nil); r.Status != 200 {
		t.Errorf("%s: storage: own object: %s", p.ID, r)
	}
	if r := a.do("GET", obj("users/"+al.UID+"/avatar.png")+"?alt=media", nil, nil); r.Status != 403 {
		t.Errorf("%s: storage: another's object without a credential: %s", p.ID, r)
	}
	h := map[string]string{"Authorization": "Firebase " + tok, "Content-Type": "text/plain"}
	if r := a.do("POST", "/v0/b/"+p.Bucket+"/o?name=pub%2Fclient-write.txt", h, "x"); r.Status != 403 {
		t.Errorf("%s: storage: a user wrote under pub/: %s", p.ID, r)
	}
	if r := a.do("POST", "/v0/b/"+p.Bucket+"/o?name="+url.QueryEscape("users/"+al.UID+"/new.txt"), h, "x"); r.Status != 200 {
		t.Errorf("%s: storage: a user's own write: %s", p.ID, r)
	}
}

// finishHalfUpload resumes the upload left half done and checks the object it makes.
func finishHalfUpload(t *testing.T, d *deployment, fx *fixture) {
	t.Helper()
	for _, p := range fx.Projects {
		h := fx.Half[p.ID]
		a := newAPI(t, d.base())
		tok := p.token(t, a)
		q := bearer(tok)
		q["X-Goog-Upload-Protocol"] = "resumable"
		q["X-Goog-Upload-Command"] = "query"
		r := a.do("POST", h.Session, q, nil)
		if r.Status != 200 || r.Header.Get("X-Goog-Upload-Status") != "active" {
			t.Errorf("%s: the half-done upload after the change: %s %v", p.ID, r, r.Header)
			continue
		}
		got := r.Header.Get("X-Goog-Upload-Size-Received")
		if got != fmt.Sprint(h.Sent) {
			t.Errorf("%s: the half-done upload has %s bytes, %d were sent", p.ID, got, h.Sent)
		}
		data := det(p.ID+"/"+h.Name, h.Total)
		fin := sendChunk(t, a, tok, h.Session, h.Sent, data[h.Sent:], true)
		if fin.str("size") != fmt.Sprint(h.Total) {
			t.Errorf("%s: the finished upload: %s", p.ID, fin)
		}
		dl := a.do("GET", "/v0/b/"+p.Bucket+"/o/"+url.PathEscape(h.Name)+"?alt=media", bearer(tok), nil)
		if string(dl.Body) != string(data) {
			t.Errorf("%s: the finished upload's bytes differ (%d bytes, want %d)", p.ID, len(dl.Body), len(data))
		}
	}
}

// checkMonotonic writes after a restore or an upgrade and checks that time moves on: a new
// document's commit time, a new object's generation and the write counts are later than
// anything the database held.
func checkMonotonic(t *testing.T, a *api, fx *fixture, p *project) {
	t.Helper()
	base := docsBase(p)
	var maxUpdate string
	q := a.must("POST", base+"/documents:runQuery", bearer(fx.PAT), map[string]any{"structuredQuery": map[string]any{
		"from": []any{map[string]any{"allDescendants": true}}}})
	var rows []map[string]any
	_ = json.Unmarshal(q.Body, &rows)
	for _, r := range rows {
		if d, ok := r["document"].(map[string]any); ok {
			if u := fmt.Sprint(d["updateTime"]); u > maxUpdate { // RFC 3339 UTC strings of one precision sort as times
				maxUpdate = u
			}
		}
	}
	name := "projects/" + p.ID + "/databases/(default)/documents/after/restore"
	c := a.must("POST", base+"/documents:commit", bearer(fx.PAT), map[string]any{"writes": []any{
		map[string]any{"update": map[string]any{"name": name, "fields": map[string]any{"x": map[string]any{"integerValue": "1"}}}}}})
	ct, err := time.Parse(time.RFC3339Nano, c.str("commitTime"))
	mu, err2 := time.Parse(time.RFC3339Nano, maxUpdate)
	if err != nil || err2 != nil || !ct.After(mu) {
		t.Errorf("%s: a document written after the change has commit time %s, not after the latest restored update %s", p.ID, c.str("commitTime"), maxUpdate)
	}
	tok := p.token(t, a)
	var maxGen int64
	l := a.must("GET", "/storage/v1/b/"+p.Bucket+"/o?maxResults=1000", bearer(tok), nil)
	items, _ := l.obj()["items"].([]any)
	for _, it := range items {
		var g int64
		fmt.Sscan(fmt.Sprint(it.(map[string]any)["generation"]), &g)
		maxGen = max(maxGen, g)
	}
	up := a.must("POST", "/v0/b/"+p.Bucket+"/o?name=after%2Frestore.txt", map[string]string{"Authorization": "Bearer " + tok, "Content-Type": "text/plain"}, "later")
	var g int64
	fmt.Sscan(up.str("generation"), &g)
	if g <= maxGen {
		t.Errorf("%s: an object written after the change has generation %d, not after %d", p.ID, g, maxGen)
	}
}
