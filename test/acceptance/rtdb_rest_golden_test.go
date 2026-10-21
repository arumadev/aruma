package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestRTDBRESTMatchesEmulator replays the REST requests recorded from the official emulator
// 4.11.2 (internal/services/rtdb/api/testdata/emulator/rest.golden.json, capture_rest.mjs)
// against Aruma, in order, group by group, and compares each answer: the status, the body as
// JSON (or as text when it is not JSON), and the ETag, Content-Type and Allow headers. Every
// difference must be listed in restKnownDifferences with its reason; the test logs those, so
// a run's log is the comparison itself.
//
// What varies by nature is masked on both sides before comparing: push ids, timestamps
// (numbers within a few years of now), and the ETags of bodies that hold either. Numbers are
// compared as numbers.
//
// Each group runs on a project of its own, with the group's rules (the emulator's open default
// when it set none) and data. The emulator's admin is "Bearer owner"; here it is the owner's
// personal CLI token, sent the same way. Its users signed in with unsigned tokens; here each
// is a real Aruma user of the same uid and custom claims, who sends an ID token instead.
func TestRTDBRESTMatchesEmulator(t *testing.T) {
	file := "../../internal/services/rtdb/api/testdata/emulator/rest.golden.json"
	if f := os.Getenv("RTDB_REST_GOLDEN"); f != "" {
		file = f
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Groups []restGroup `json:"groups"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	e := rtdbEnv(t, true, nil)
	owner := e.owner()
	pat := owner.pat()
	users := e.rtdbUsers(owner)

	seen := map[string]bool{}
	compared, differing := 0, 0
	for gi, g := range golden.Groups {
		project := fmt.Sprintf("rest-golden-%d", gi)
		ns := owner.projectNS(project)
		rulesDoc := `{"rules": {".read": true, ".write": true}}`
		if len(g.Rules) > 0 && string(g.Rules) != "null" {
			rulesDoc = string(g.Rules)
		}
		e.putRules(pat, ns, rulesDoc)
		if len(g.Data) > 0 && string(g.Data) != "null" {
			resp := restDo(t, e, "PUT", "/.json?ns="+ns, string(g.Data), http.Header{"Authorization": {"Bearer " + pat}})
			if resp.status != 200 {
				t.Fatalf("%s: seeding: %d %s", g.Name, resp.status, resp.body)
			}
		}
		idTokens := map[string]string{}
		credential := func(auth json.RawMessage) string {
			var s string
			if json.Unmarshal(auth, &s) == nil {
				if s == "admin" {
					return pat
				}
				return ""
			}
			var a struct {
				UID    string         `json:"uid"`
				Claims map[string]any `json:"claims"`
				Token  *string        `json:"token"`
			}
			if err := json.Unmarshal(auth, &a); err != nil {
				t.Fatalf("auth %s: %v", auth, err)
			}
			if a.Token != nil {
				return *a.Token
			}
			if tok, ok := idTokens[a.UID]; ok {
				return tok
			}
			users.create(project, a.UID)
			if custom := customClaims(a.Claims); len(custom) > 0 {
				b, _ := json.Marshal(custom)
				users.admin(project, "accounts:update", map[string]any{"localId": a.UID, "customAttributes": string(b)})
			}
			idTokens[a.UID] = users.signIn(project, a.UID).str("idToken")
			return idTokens[a.UID]
		}

		lastETag := ""
		for _, c := range g.Cases {
			key := g.Name + " / " + c.Name
			rq := c.Request
			query := "ns=" + ns
			if rq.Query != "" {
				query += "&" + rq.Query
			}
			h := http.Header{}
			for k, v := range rq.Headers {
				h.Set(k, strings.ReplaceAll(v, "{{etag}}", lastETag))
			}
			if cred := credential(rq.Auth); cred != "" || string(rq.Auth) != `"none"` {
				via := rq.AuthVia
				if via == "" {
					via = "auth"
					if string(rq.Auth) == `"admin"` {
						via = "bearer"
					}
				}
				switch via {
				case "bearer":
					h.Set("Authorization", "Bearer "+cred)
				case "header":
					h.Set("Authorization", cred)
				case "auth", "access_token":
					query += "&" + via + "=" + url.QueryEscape(cred)
				}
			}
			if rq.Override != nil {
				query += "&auth_variable_override=" + url.QueryEscape(string(rq.Override))
			}
			body := ""
			if rq.Body != nil {
				body = *rq.Body
			}
			ours := restDo(t, e, rq.Method, rq.Path+"?"+query, body, h)
			if et := ours.header.Get("ETag"); et != "" {
				lastETag = et
			}
			compared++
			if diff := restDiff(c.Response, ours); diff != "" {
				differing++
				reason, known := restKnownDifferences[key]
				seen[key] = true
				if known {
					t.Logf("known difference, %s: %s\n    %s", key, reason, diff)
				} else {
					t.Errorf("%s: %s %s?%s\n    %s", key, rq.Method, rq.Path, rq.Query, diff)
				}
			}
		}
	}
	for key := range restKnownDifferences {
		if !seen[key] {
			t.Errorf("known difference %q no longer differs (or no longer exists): remove it", key)
		}
	}
	t.Logf("%d REST answers compared, %d differ, all of them known", compared, differing)
}

type restGroup struct {
	Name  string          `json:"name"`
	Rules json.RawMessage `json:"rules"`
	Data  json.RawMessage `json:"data"`
	Cases []struct {
		Name    string `json:"name"`
		Request struct {
			Method   string            `json:"method"`
			Path     string            `json:"path"`
			Query    string            `json:"query"`
			Headers  map[string]string `json:"headers"`
			Auth     json.RawMessage   `json:"auth"`
			AuthVia  string            `json:"authVia"`
			Override json.RawMessage   `json:"authVariableOverride"`
			Body     *string           `json:"body"`
		} `json:"request"`
		Response restAnswer `json:"response"`
	} `json:"cases"`
}

type restAnswer struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type restReply struct {
	status int
	header http.Header
	body   string
}

func restDo(t *testing.T, e *Env, method, target, body string, h http.Header) restReply {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.URL+target, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = h
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return restReply{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

// customClaims are the claims of an emulator user beyond those every ID token has.
func customClaims(claims map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range claims {
		switch k {
		case "exp", "iat", "sub", "uid", "user_id", "aud", "iss", "auth_time", "firebase":
		default:
			out[k] = v
		}
	}
	return out
}

var (
	pushIDRE = regexp.MustCompile(`^-[0-9A-Za-z_-]{19}$`)
	pushIDIn = regexp.MustCompile(`-[0-9A-Za-z_-]{19}`)
)

// restDiff describes how our answer differs from the emulator's; "" when it does not.
func restDiff(want restAnswer, got restReply) string {
	var diffs []string
	if want.Status != got.status {
		diffs = append(diffs, fmt.Sprintf("status %d, want %d", got.status, want.Status))
	}
	wantBody, wantVolatile := maskJSON(want.Body)
	gotBody, gotVolatile := maskJSON(got.body)
	if (wantBody == nil) != (gotBody == nil) || wantBody == nil && strings.TrimSpace(want.Body) != strings.TrimSpace(got.body) ||
		wantBody != nil && !reflect.DeepEqual(wantBody, gotBody) {
		diffs = append(diffs, fmt.Sprintf("body %s, want %s", abbreviate(compact(got.body)), abbreviate(compact(want.Body))))
	}
	if et := want.Headers["etag"]; et != "" || got.header.Get("ETag") != "" {
		switch {
		case et == "" || got.header.Get("ETag") == "":
			diffs = append(diffs, fmt.Sprintf("ETag %q, want %q", got.header.Get("ETag"), et))
		case !wantVolatile && !gotVolatile && et != got.header.Get("ETag"):
			diffs = append(diffs, fmt.Sprintf("ETag %q, want %q", got.header.Get("ETag"), et))
		}
	}
	if ct := want.Headers["content-type"]; ct != got.header.Get("Content-Type") && (ct != "" || got.status != 204 && got.status != 304) {
		diffs = append(diffs, fmt.Sprintf("Content-Type %q, want %q", got.header.Get("Content-Type"), ct))
	}
	if a := want.Headers["allow"]; a != "" && a != got.header.Get("Allow") {
		diffs = append(diffs, fmt.Sprintf("Allow %q, want %q", got.header.Get("Allow"), a))
	}
	return strings.Join(diffs, "; ")
}

// compact is a JSON body on one line, for the log.
func compact(s string) string {
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		return s
	}
	return b.String()
}

// maskJSON parses a body and masks push ids and timestamps; nil when it is not JSON. volatile
// reports whether anything was masked.
func maskJSON(s string) (v any, volatile bool) {
	if strings.TrimSpace(s) == "" || json.Unmarshal([]byte(s), &v) != nil {
		return nil, false
	}
	var mask func(any) any
	mask = func(x any) any {
		switch x := x.(type) {
		case map[string]any:
			// Push ids sort as they were made: the n-th of an object's ids becomes <push id n>.
			var ids []string
			for k := range x {
				if pushIDRE.MatchString(k) {
					ids = append(ids, k)
				}
			}
			sort.Strings(ids)
			out := map[string]any{}
			for k, c := range x {
				if i := sort.SearchStrings(ids, k); i < len(ids) && ids[i] == k {
					k, volatile = fmt.Sprintf("<push id %d>", i+1), true
				}
				out[k] = mask(c)
			}
			return out
		case []any:
			for i := range x {
				x[i] = mask(x[i])
			}
			return x
		case string:
			if pushIDRE.MatchString(x) {
				volatile = true
				return "<push id>"
			}
			if m := pushIDIn.FindString(x); m != "" && strings.Contains(x, "/") {
				volatile = true
				return strings.ReplaceAll(x, m, "<push id>")
			}
		case float64:
			if x > 1.7e12 && x < 2.1e12 && x == math.Trunc(x) {
				volatile = true
				return "<timestamp>"
			}
		}
		return x
	}
	return mask(v), volatile
}

// restKnownDifferences are the answers where Aruma knowingly differs from the emulator, by
// "group / case", with the reason.
var restKnownDifferences = map[string]string{
	"methods / HEAD": "Go's HTTP server sends no body after HEAD, as HTTP requires; the status is the same",
	"methods / missing .json suffix": "on the main host only ?ns= with .json, /.ws and /.lp reach the database; other " +
		"paths are the dashboard's. A database host answers as the emulator does (TestRTDBRESTRoutes)",
	"methods / missing .json suffix at the root": "as above",
	"methods / /.inspect.json":                   "the emulator's own endpoints are not served; every dot path is an invalid path",
	"methods / /.settings.json":                  "as above",
	"queries without an index / orderBy empty string": "\"\" names no child: Aruma refuses it as an invalid path, the " +
		"emulator asks for an index on it",
	"etag / If-None-Match matching":                      "Go's HTTP server drops Content-Type from a 304, which has no body",
	"etag / If-None-Match matching with X-Firebase-ETag": "as above",
	"errors / key making the path 769 bytes": "over the protocol the emulator counts a path's bytes without separators " +
		"(wire golden limits.json), over REST with them; Aruma counts one way for both, the protocol's",
	"errors / writeSizeLimit=bogus": "the same error; the set of values in production's order, {tiny, large, small, " +
		"medium, unlimited} (wire golden production/oversize.json)",
	"errors / disableTriggers=true": "Aruma runs no triggers: the parameter is accepted and changes nothing",
	"errors / callback (JSONP)":     "JSONP is not served: no SDK sends it, and it would let any page read data",
	"errors / download":             "download= is not served; the body is the same",
	"rules and auth / user token without a uid": "the emulator accepts unsigned tokens; Aruma verifies ID tokens, " +
		"and production refuses one that does not verify",
	"rules and auth / admin via auth= is not admin": "the same refusal, 401, with the text of a token that does not parse",
	"rules and auth / admin with override a string": "an override that is not an object is a bad request (400), not " +
		"an inactive database (503)",
	"rules and auth / admin with override a number": "as above",
	"rules and auth / admin with override an array": "as above",
	"rules and auth / admin with override not JSON": "as above",
	"rules and auth / auth=garbage on a public path": "the emulator ignores a credential it cannot parse; production " +
		"and Aruma refuse it with 401",
	"rules and auth / auth=garbage on a protected path":         "as above",
	"rules and auth / access_token=garbage on a protected path": "the emulator takes any access token for its owner",
	"rules and auth / Bearer garbage on a protected path":       "as above",
	"rules and auth / Bearer garbage on a public path":          "as above",
	"rules and auth / Bearer with a user token on another user": "the emulator takes any bearer for its owner; Aruma " +
		"verifies the user's ID token and applies the rules",
	"rules and auth / access_token with a user token on another user": "as above",
	"rules endpoint / PUT without the rules key":                      "the rules parser names the token it expected, not the property",
	"rules endpoint / DELETE the rules":                               "only GET and PUT exist: 405, not the emulator's 404",
	"rules endpoint / POST the rules":                                 "only GET and PUT exist: 405, not the emulator's 400",
}
