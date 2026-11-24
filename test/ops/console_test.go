package ops

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

const (
	itk = "/identitytoolkit.googleapis.com/v1/"
	sts = "/securetoken.googleapis.com/v1/token"
)

// owner is the operator's account, signed in with a CLI token as arumactl does.
type owner struct {
	a   *api
	pat string
}

// createOwner makes the first account with `aruma accounts create`, as the docs do, signs in
// and creates a CLI token.
func createOwner(t *testing.T, d *deployment, email, password string) *owner {
	t.Helper()
	d.mustRun(password+"\n", "accounts", "create", "--email", email)
	a := newAPI(t, d.base())
	login := a.must("POST", "/console/v1/auth:login", nil, map[string]any{"email": email, "password": password})
	cookie := ""
	for _, c := range login.Header.Values("Set-Cookie") {
		if i := strings.Index(c, ";"); i > 0 {
			cookie += c[:i] + "; "
		}
	}
	tok := a.must("POST", "/console/v1/me/tokens", map[string]string{"Cookie": cookie, "X-Aruma-CSRF": login.str("csrfToken")},
		map[string]any{"name": "ops", "currentPassword": password})
	return &owner{a: a, pat: tok.str("token")}
}

func (o *owner) call(method, path string, body any) reply {
	o.a.t.Helper()
	return o.a.must(method, path, bearer(o.pat), body)
}

// project is what the tests know about one project.
type project struct {
	ID         string           `json:"id"`
	Number     string           `json:"number"`
	Bucket     string           `json:"bucket"`
	NS         string           `json:"ns"`
	Key        string           `json:"key"`
	LimitedKey string           `json:"limitedKey"`
	SAKey      string           `json:"saKey"`
	SAID       string           `json:"saId"`
	Users      map[string]*user `json:"users"`
}

type user struct {
	Email    string         `json:"email,omitempty"`
	Password string         `json:"password,omitempty"`
	UID      string         `json:"uid"`
	IDToken  string         `json:"idToken,omitempty"`
	Refresh  string         `json:"refresh,omitempty"`
	Claims   map[string]any `json:"claims,omitempty"`
	Disabled bool           `json:"disabled,omitempty"`
}

func (o *owner) newProject(id string) *project {
	o.a.t.Helper()
	pr := o.call("POST", "/console/v1/projects", map[string]any{"projectId": id, "displayName": "Ops " + id})
	p := &project{ID: id, Number: pr.str("projectNumber"), Bucket: pr.str("storageBucket"), NS: id + "-default-rtdb", Users: map[string]*user{}}
	o.call("PATCH", "/console/v1/projects/"+id+"/auth/config", map[string]any{"anonymousEnabled": true})
	app := o.call("POST", "/console/v1/projects/"+id+"/apps", map[string]any{"platform": "WEB", "displayName": "web"})
	p.Key = o.call("GET", "/console/v1/projects/"+id+"/apiKeys/"+app.str("apiKeyId"), nil).str("keyString")
	lim := o.call("POST", "/console/v1/projects/"+id+"/apiKeys", map[string]any{
		"displayName":  "limited",
		"restrictions": map[string]any{"browserKeyRestrictions": map[string]any{"allowedReferrers": []string{"https://allowed.example/*"}}},
	})
	p.LimitedKey = lim.str("keyString")
	sa := o.call("POST", "/console/v1/projects/"+id+"/serviceAccounts", map[string]any{"accountId": "backend"})
	p.SAID = sa.str("id")
	p.SAKey = string(o.call("POST", "/console/v1/projects/"+id+"/serviceAccounts/"+p.SAID+"/keys", nil).Body)
	return p
}

// token returns an access token of the project's service account.
func (p *project) token(t *testing.T, a *api) string {
	t.Helper()
	tok, status := saToken(t, a, []byte(p.SAKey))
	if tok == "" {
		t.Fatalf("service account token of %s: status %d", p.ID, status)
	}
	return tok
}

func esc(s string) string { return url.PathEscape(s) }

func jsonString(v any) string { b, _ := json.Marshal(v); return string(b) }
