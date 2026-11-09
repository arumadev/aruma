package acceptance

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	documents "github.com/arumadev/aruma/internal/services/documents/core"
	storage "github.com/arumadev/aruma/internal/services/storage/core"
)

// hardProject is one project of the hardening fixture, seen as each kind of caller.
type hardProject struct {
	id, number string
	owner      *Client // console session of the project's owner
	cli        *Client // the owner's CLI token (aruma_pat_)
	pat        string
	sa         *Client // service account access token (aruma_sat_)
	sat        string
	key        string // web API key
	users      *rtdbUsers
	uid        string // an Aruma Auth user of the project
	idToken    string
	refresh    string
	ns         string // RTDB database name
	bucket     string // Storage bucket name
	marker     string // what open seeded
	saID       string // the service account's id and its key file
	saKey      []byte
}

// hardEnv is a server with every service on and two projects, alpha-app (owner A) and
// bravo-app (owner B), each with a user, a service account and the owner's CLI token.
type hardEnv struct {
	*Env
	a, b *hardProject
}

func newHardEnv(t *testing.T, extra map[string]string) *hardEnv {
	t.Helper()
	vars := map[string]string{
		"ARUMA_RTDB_ENABLED":           "true",
		"ARUMA_STORAGE_ENABLED":        "true",
		"ARUMA_STORAGE_DATA_DIR":       t.TempDir(),
		"ARUMA_STORAGE_MIN_FREE_BYTES": "1048576",
		"ARUMA_DOCUMENTS_ENABLED":      "true",
		"ARUMA_LISTEN":                 "127.0.0.1:0",
	}
	for k, v := range extra {
		vars[k] = v
	}
	e := newEnv(t, vars)
	if err := e.App.Documents.Boot(t.Context(), documents.InstanceOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.App.Documents.Close)
	h := &hardEnv{Env: e}
	ownerA := e.owner()
	ownerB := e.account("b@example.com")
	h.a = h.setUpProject(ownerA, "alpha-app")
	h.b = h.setUpProject(ownerB, "bravo-app")
	return h
}

func (h *hardEnv) setUpProject(owner *Client, id string) *hardProject {
	t := h.t
	t.Helper()
	p := owner.project(id)
	hp := &hardProject{id: id, number: p.str("projectNumber"), owner: owner, ns: id + "-default-rtdb", bucket: p.str("storageBucket")}
	hp.pat = owner.pat()
	hp.cli = h.withBearer(hp.pat)
	hp.users = h.rtdbUsers(owner)
	hp.users.setUp(id)
	hp.key = hp.users.keys[id]
	saID, key := owner.serviceAccountKey(id, "hardening")
	hp.saID, hp.saKey = saID, key
	tok, _, err := exchange(t, key)
	if err != nil {
		t.Fatal(err)
	}
	hp.sat, hp.sa = tok, h.withBearer(tok)
	hp.uid = "alice"
	hp.users.create(id, hp.uid)
	in := hp.users.signIn(id, hp.uid)
	hp.idToken, hp.refresh = in.str("idToken"), in.str("refreshToken")
	if hp.idToken == "" || hp.refresh == "" || !strings.Contains(hp.bucket, id) {
		t.Fatalf("fixture for %s: %s", id, in.Body)
	}
	return hp
}

const (
	hardRTDBRules    = `{"rules":{".read":"auth != null",".write":"auth != null"}}`
	hardStorageRules = `rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    match /{all=**} { allow read, write: if request.auth != null; }
  }
}
`
	hardDocumentsRules = `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /{document=**} { allow read, write: if request.auth != null; }
  }
}
`
)

// open makes every service of the project answer any signed-in user of that project, and seeds
// one datum of each (marker) through the project's own service account, so that a read that
// succeeds proves the caller was taken for a signed-in user (or an admin) of the project.
func (h *hardEnv) open(p *hardProject, marker string) {
	t := h.t
	t.Helper()
	p.marker = marker
	req, _ := http.NewRequest("PUT", h.URL+"/.settings/rules.json?ns="+p.ns, strings.NewReader(hardRTDBRules))
	req.Header.Set("Authorization", "Bearer "+p.pat)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("rtdb rules: %v %v", resp, err)
	}
	resp.Body.Close()
	c := p.owner
	c.mustOK(c.do("PUT", "/console/v1/projects/"+p.id+"/storage/rules", map[string]any{"source": hardStorageRules}))
	c.mustOK(c.do("PUT", "/console/v1/projects/"+p.id+"/documents/rules", map[string]any{"source": hardDocumentsRules}))

	if r := restAs(t, h.Env, "PUT", "/secret.json?ns="+p.ns, `"`+marker+`"`, p.sat, nil); r.status != 200 {
		t.Fatalf("seed rtdb: %d %s", r.status, r.body)
	}
	st := h.App.Storage
	b, err := st.Bucket(t.Context(), p.bucket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Insert(t.Context(), b, storage.Principal{Kind: storage.PrincipalAdmin, ServiceAccount: "seed"},
		storage.UploadSpec{Name: "secret.txt", Declared: int64(len(marker)), Surface: storage.SurfaceGCS},
		strings.NewReader(marker), int64(len(marker))); err != nil {
		t.Fatal(err)
	}
	doc := `{"fields":{"v":{"stringValue":"` + marker + `"}}}`
	if code, body := documentsCall(t, h.Env, p.sat, "PATCH", hardDocPath(p.id), doc); code != 200 {
		t.Fatalf("seed documents: %d %s", code, body)
	}
}

func hardDocPath(project string) string {
	return "/v1/projects/" + project + "/databases/(default)/documents/c/d"
}

// surface is one way a bearer of an end user's ID token reaches project data: read returns the
// marker the project's seed holds when the data was served, "" when it was refused.
type surface struct {
	name string
	read func(h *hardEnv, p *hardProject, idToken string) string
}

func hardSurfaces() []surface {
	return []surface{
		{"rtdb rest ?auth=", func(h *hardEnv, p *hardProject, tok string) string {
			r := restAs(h.t, h.Env, "GET", "/secret.json?ns="+p.ns+"&auth="+url.QueryEscape(tok), "", "", nil)
			return unquoteIf(r.status == 200, r.body)
		}},
		{"rtdb websocket auth", func(h *hardEnv, p *hardProject, tok string) string {
			c, _, err := h.tryDial(p.ns, dialOpts{})
			if err != nil {
				return ""
			}
			if r := c.call("auth", map[string]any{"cred": tok}); r.Status != "ok" {
				return ""
			}
			r := c.call("g", map[string]any{"p": "/secret", "q": map[string]any{}})
			return unquoteIf(r.Status == "ok", string(r.Data))
		}},
		{"storage v0 Firebase scheme", func(h *hardEnv, p *hardProject, tok string) string {
			req, _ := http.NewRequest("GET", h.URL+"/v0/b/"+p.bucket+"/o/secret.txt?alt=media", nil)
			req.Header.Set("Authorization", "Firebase "+tok)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				h.t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return unquoteIf(resp.StatusCode == 200, "\""+string(b)+"\"")
		}},
		{"documents rest bearer", func(h *hardEnv, p *hardProject, tok string) string {
			code, b := documentsCall(h.t, h.Env, tok, "GET", hardDocPath(p.id), "")
			if code != 200 {
				return ""
			}
			var d struct {
				Fields struct{ V struct{ StringValue string } }
			}
			_ = json.Unmarshal(b, &d)
			return d.Fields.V.StringValue
		}},
	}
}

func unquoteIf(ok bool, s string) string {
	if !ok {
		return ""
	}
	var v string
	if json.Unmarshal([]byte(s), &v) != nil {
		return ""
	}
	return v
}

// adminSurface is one way a bearer of an admin credential (a service account's access token or
// an owner's CLI token) reaches a project: read returns the project's marker when it was served.
type adminSurface struct {
	name     string
	sat, pat bool // whether the project's own token of that kind is accepted
	read     func(h *hardEnv, p *hardProject, bearer string) string
}

func bearerDo(h *hardEnv, method, path, body, bearer string) (int, []byte) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, h.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func hardAdminSurfaces() []adminSurface {
	served := func(p *hardProject, ok bool) string {
		if ok {
			return p.marker
		}
		return ""
	}
	lookup := func(ref string) func(h *hardEnv, p *hardProject, tok string) string {
		return func(h *hardEnv, p *hardProject, tok string) string {
			id := p.id
			if ref == "number" {
				id = p.number
			}
			c, b := bearerDo(h, "POST", itk+"projects/"+id+"/accounts:lookup", `{"localId":["`+p.uid+`"]}`, tok)
			var out struct{ Users []struct{ LocalID string } }
			_ = json.Unmarshal(b, &out)
			return served(p, c == 200 && len(out.Users) == 1 && out.Users[0].LocalID == p.uid)
		}
	}
	return []adminSurface{
		{"rtdb rest bearer", true, true, func(h *hardEnv, p *hardProject, tok string) string {
			c, b := bearerDo(h, "GET", "/secret.json?ns="+p.ns, "", tok)
			return unquoteIf(c == 200, string(b))
		}},
		{"rtdb rest access_token=", true, true, func(h *hardEnv, p *hardProject, tok string) string {
			r := restAs(h.t, h.Env, "GET", "/secret.json?ns="+p.ns+"&access_token="+url.QueryEscape(tok), "", "", nil)
			return unquoteIf(r.status == 200, r.body)
		}},
		{"rtdb websocket auth", true, true, func(h *hardEnv, p *hardProject, tok string) string {
			c, _, err := h.tryDial(p.ns, dialOpts{})
			if err != nil {
				return ""
			}
			if r := c.call("auth", map[string]any{"cred": tok}); r.Status != "ok" {
				return ""
			}
			r := c.call("g", map[string]any{"p": "/secret", "q": map[string]any{}})
			return unquoteIf(r.Status == "ok", string(r.Data))
		}},
		{"storage v0 media", true, true, func(h *hardEnv, p *hardProject, tok string) string {
			c, b := bearerDo(h, "GET", "/v0/b/"+p.bucket+"/o/secret.txt?alt=media", "", tok)
			return served(p, c == 200 && string(b) == p.marker)
		}},
		{"storage v0 metadata", true, true, func(h *hardEnv, p *hardProject, tok string) string {
			c, _ := bearerDo(h, "GET", "/v0/b/"+p.bucket+"/o/secret.txt", "", tok)
			return served(p, c == 200)
		}},
		{"storage gcs object", true, false, func(h *hardEnv, p *hardProject, tok string) string {
			c, b := bearerDo(h, "GET", "/storage/v1/b/"+p.bucket+"/o/secret.txt?alt=media", "", tok)
			return served(p, c == 200 && string(b) == p.marker)
		}},
		{"storage gcs bucket", true, false, func(h *hardEnv, p *hardProject, tok string) string {
			c, _ := bearerDo(h, "GET", "/storage/v1/b/"+p.bucket, "", tok)
			return served(p, c == 200)
		}},
		{"documents rest", true, true, func(h *hardEnv, p *hardProject, tok string) string {
			code, b := documentsCall(h.t, h.Env, tok, "GET", hardDocPath(p.id), "")
			if code != 200 {
				return ""
			}
			var d struct {
				Fields struct{ V struct{ StringValue string } }
			}
			_ = json.Unmarshal(b, &d)
			return d.Fields.V.StringValue
		}},
		{"auth admin lookup by id", true, true, lookup("id")},
		{"auth admin lookup by number", true, true, lookup("number")},
		{"management api", true, true, func(h *hardEnv, p *hardProject, tok string) string {
			c, _ := bearerDo(h, "GET", "/v1beta1/projects/"+p.id, "", tok)
			return served(p, c == 200)
		}},
		{"console api", false, true, func(h *hardEnv, p *hardProject, tok string) string {
			c, _ := bearerDo(h, "GET", "/console/v1/projects/"+p.id+"/auth/users", "", tok)
			return served(p, c == 200)
		}},
	}
}
