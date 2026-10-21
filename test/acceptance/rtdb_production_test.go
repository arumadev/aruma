package acceptance

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The REST answers production Firebase gave on 2026-10-21 (wire golden production/*.json,
// see its README), replayed against Aruma: the same requests, from a user signed in the same
// way under the same rules, must get the same status and the same body, numbers written as
// production writes them. The protocol messages of a scenario are replayed too, in order, so
// that the data is the same; their answers are the wire package's to check. Each difference
// left is listed in productionDifferences with its reason.
func TestRTDBRESTMatchesProduction(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	owner := e.owner()
	ns := owner.projectNS("chat-app")
	pat := owner.pat()
	users := e.rtdbUsers(owner)
	users.create("chat-app", "ref-user")
	token := users.signIn("chat-app", "ref-user").str("idToken")
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"ref-user","user_id":"ref-user"}`)) + "."
	// The rules production ran under, beside its own (capture_production.mjs).
	e.putRules(pat, ns, `{"rules": {"aruma-ref": {"$run": {
		"open": {".read": "auth != null && auth.uid === 'ref-user'", ".write": "auth != null && auth.uid === 'ref-user'",
			"v": {".validate": "newData.isNumber()"}},
		"public": {".read": true}}}}}`)
	fill := strings.NewReplacer("<id-token>", token, "<alg-none-token-for-the-same-uid>", unsigned)

	compared, differing := 0, 0
	seen := map[string]bool{}
	// In the order production ran them: rest_auth then rest in the second run.
	for _, name := range []string{"no_index", "oversize", "numbers", "rest_auth", "rest"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "services", "rtdb", "wire", "testdata", "golden", "production", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var g struct {
			Frames []struct {
				Conn       string            `json:"conn"`
				Dir        string            `json:"dir"`
				Text       string            `json:"text"`
				Method     string            `json:"method"`
				Path       string            `json:"path"`
				ReqHeaders map[string]string `json:"reqHeaders"`
				Body       *string           `json:"body"`
				Status     int               `json:"status"`
				Response   string            `json:"response"`
			} `json:"frames"`
		}
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatal(err)
		}
		conns := map[string]*wsClient{}
		for i, f := range g.Frames {
			text := fill.Replace(f.Text)
			switch {
			case f.Dir == "send" && strings.HasPrefix(text, `{"t":"d"`):
				c := conns[f.Conn]
				if c == nil {
					c = e.dial(ns, dialOpts{})
					conns[f.Conn] = c
				}
				var m struct {
					D struct {
						R int64 `json:"r"`
					} `json:"d"`
				}
				json.Unmarshal([]byte(text), &m)
				c.sendRaw(text)
				r := `"r":` + strconv.FormatInt(m.D.R, 10) + `,`
				c.readUntil(5*time.Second, func(s string) bool { return strings.Contains(s, r) })
			case f.Dir == "rest":
				key := name + " " + strconv.Itoa(i)
				body := ""
				if f.Body != nil {
					body = fill.Replace(*f.Body)
				}
				if strings.HasPrefix(body, "<") || strings.Contains(f.Response, "...(") || strings.Contains(f.Path+f.Response, "<expired-id-token>") {
					continue // a body or answer the capture left out, or a token it could not keep
				}
				h := http.Header{}
				for k, v := range f.ReqHeaders {
					h.Set(k, fill.Replace(v))
				}
				target := fill.Replace(f.Path)
				if strings.Contains(target, "?") {
					target += "&ns=" + ns
				} else {
					target += "?ns=" + ns
				}
				got := restDo(t, e, f.Method, target, body, h)
				compared++
				seen[key] = true
				if got.status == f.Status && sameProductionBody(got.body, f.Response) {
					delete(seen, key)
					continue
				}
				differing++
				if reason, ok := productionDifferences[key]; ok {
					t.Logf("known difference, %s: %s", key, reason)
					continue
				}
				t.Errorf("%s: %s %s\n production %d %q\n aruma      %d %q", key, f.Method, f.Path, f.Status, f.Response, got.status, got.body)
			}
		}
	}
	for key := range productionDifferences {
		if !seen[key] {
			t.Errorf("known difference %q no longer differs (or no longer exists): remove it", key)
		}
	}
	if compared < 80 {
		t.Errorf("compared %d answers", compared)
	}
	t.Logf("%d REST answers compared with production's, %d differ, all of them known", compared, differing)
}

// sameProductionBody compares two answers once what changes on every run is masked, server
// timestamps and push ids: as JSON, where the order of keys does not matter but the text of
// each number does (1.0 is not 1), or else as text.
func sameProductionBody(a, b string) bool {
	mask := func(s string) string {
		s = productionTimestamp.ReplaceAllString(s, "TS")
		return pushIDIn.ReplaceAllString(s, "PUSHID")
	}
	a, b = mask(a), mask(b)
	decode := func(s string) (any, bool) {
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		var v any
		return v, d.Decode(&v) == nil && !d.More()
	}
	x, okA := decode(a)
	y, okB := decode(b)
	if okA && okB {
		return reflect.DeepEqual(x, y)
	}
	return a == b
}

var productionTimestamp = regexp.MustCompile(`\b1[7-9]\d{11}\b`)

// productionDifferences are the answers where Aruma knowingly differs from production, by
// "scenario frame", with the reason.
var productionDifferences = map[string]string{
	"numbers 7":  "-0.0 is read back as 0.0: jsonb has no negative zero",
	"numbers 10": "as above",
	"numbers 22": "as above",
	"numbers 32": "as above",
}
