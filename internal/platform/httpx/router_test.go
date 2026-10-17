package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func echo(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, name+" project="+r.PathValue("project")+" app="+r.PathValue("app")+" name="+r.PathValue("name"))
	}
}

func testRouter() *Router {
	r := &Router{}
	r.Handle("GET", "/v1beta1/projects", echo("list"))
	r.Handle("GET", "/v1beta1/projects/{project}", echo("get"))
	r.Handle("PATCH", "/v1beta1/projects/{project}", echo("patch"))
	r.Handle("GET", "/v1beta1/projects/{project}:searchApps", echo("search"))
	r.Handle("GET", "/v1beta1/projects/{project}/webApps/{app}", echo("getApp"))
	r.Handle("POST", "/v1beta1/projects/{project}/webApps/{app}:remove", echo("remove"))
	r.Handle("GET", "/v1beta1/projects/{project}/webApps/{app}/config", echo("config"))
	r.Handle("GET", "/v1beta1/operations/{name...}", echo("op"))
	r.Handle("POST", "/console/v1/auth:login", echo("login"))
	return r
}

func TestRouter(t *testing.T) {
	r := testRouter()
	cases := []struct {
		method, path, want string
		code               int
	}{
		{"GET", "/v1beta1/projects", "list project= app= name=", 200},
		{"GET", "/v1beta1/projects/chat-app", "get project=chat-app app= name=", 200},
		{"GET", "/v1beta1/projects/chat-app:searchApps", "search project=chat-app app= name=", 200},
		{"GET", "/v1beta1/projects/chat-app%3AsearchApps", "search project=chat-app app= name=", 200},
		{"GET", "/v1beta1/projects/-/webApps/1:482915736204:web:3f9a1c7e2b4d8a60", "getApp project=- app=1:482915736204:web:3f9a1c7e2b4d8a60 name=", 200},
		{"POST", "/v1beta1/projects/-/webApps/1:482915736204:web:3f9a1c7e2b4d8a60:remove", "remove project=- app=1:482915736204:web:3f9a1c7e2b4d8a60 name=", 200},
		{"GET", "/v1beta1/projects/p/webApps/1:2:web:ab/config", "config project=p app=1:2:web:ab name=", 200},
		{"GET", "/v1beta1/operations/workflows/abc/def", "op project= app= name=workflows/abc/def", 200},
		{"POST", "/console/v1/auth:login", "login project= app= name=", 200},
		{"HEAD", "/v1beta1/projects", "", 200},
		{"DELETE", "/v1beta1/projects/chat-app", "", 405},
		{"GET", "/v1beta1/projects/chat-app:unknownVerb", "get project=chat-app:unknownVerb app= name=", 200},
		{"GET", "/v1beta1/nope", "", 404},
		{"GET", "/v1beta1/operations/", "", 404},
		{"GET", "/v1beta1/projects/a%2Fb", "", 404},
		{"POST", "/console/v1/auth:logout", "", 404},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.code {
			t.Errorf("%s %s: code %d, want %d", c.method, c.path, rec.Code, c.code)
			continue
		}
		if c.code == 200 && c.method != "HEAD" && rec.Body.String() != c.want {
			t.Errorf("%s %s: %q, want %q", c.method, c.path, rec.Body.String(), c.want)
		}
	}
}

func TestRouterRejectsBadPatterns(t *testing.T) {
	for _, p := range []string{"/a/{rest...}/b", "/a/{x}:verb/b", "/a/{x"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("pattern %q should panic", p)
				}
			}()
			(&Router{}).Handle("GET", p, echo(""))
		}()
	}
}
