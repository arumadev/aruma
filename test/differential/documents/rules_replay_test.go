package documents

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	"github.com/arumadev/aruma/internal/services/documents/rest"
	conformance "github.com/arumadev/aruma/test/conformance/documents"
)

// The security rules' recordings (record-f5-rules.mjs, record-f5-extra.mjs) are cases of steps run
// by an end user under a deployed ruleset, from a seeded state, with the state the owner read back
// afterwards. TestReplayRulesInProcess replays every f5-rules-* file's REST steps against an Aruma
// in this process: the file's ruleset published as cloud.firestore, the recording's fixtures and
// each case's seed written by an admin, and each actor an ID token whose claims are those the
// recording's users had (f5-rules-deployed.json userClaims). Each step's status and normalised
// body, and the state afterwards, must be production's. The gRPC steps answer alike
// (transportsAgree) through the same service layer, and are not replayed here.

// pendingA2a are the rules recordings this REST replay does not run: a file name, or a file and
// a case id. f5-rules-listen.json holds Listen and Write stream frames, gRPC only; its cases are
// replayed by internal/services/documents/grpc/listen_rules_test.go (TestListenRules) and the
// Write stream's by the transactions and streams tests.
var pendingA2a = map[string]bool{
	"f5-rules-listen.json": true,
}

// rulesDiffers are the rules cases Aruma does not answer as production, each with the id of its
// parity exception (parity-exceptions.json). A listed case that matches fails the test.
var rulesDiffers = map[string]string{
	"f5-rules-auth.json user.iss": "rules-token-issuer",
}

// rulesSkipped are the f5-rules files that hold no cases to replay: the deployed source, the
// first lookups attempt (superseded by lookup-limits: its fixtures overlap), the cleanup log.
var rulesSkipped = []string{"f5-rules-deployed.json", "f5-rules-lookups.json", "f5-rules-cleanup.json"}

type rulesFile struct {
	Rules json.RawMessage `json:"rules"`
	Cases []rulesCase     `json:"cases"`
}

type rulesCase struct {
	ID         string                    `json:"id"`
	Actor      string                    `json:"actor"`
	Seed       map[string]map[string]any `json:"seed"`
	Transports map[string]struct {
		After map[string]any `json:"after"`
		Steps []struct {
			Request struct {
				Method string `json:"method"`
				Path   string `json:"path"`
				Body   any    `json:"body"`
			} `json:"request"`
			Response struct {
				HTTP int `json:"http"`
				Body any `json:"body"`
			} `json:"response"`
		} `json:"steps"`
	} `json:"transports"`
}

// rulesSource is a file's ruleset: inline ({file, source}), or the path of the file holding it.
func rulesSource(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var inline struct {
		Source string `json:"source"`
	}
	if json.Unmarshal(raw, &inline) == nil && inline.Source != "" {
		return inline.Source
	}
	var ref string
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatalf("rules: %v", err)
	}
	var f rulesFile
	readJSON(t, filepath.Join(conformance.CorpusDir, strings.TrimPrefix(ref, "corpus/")), &f)
	return rulesSource(t, f.Rules)
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

var (
	ruleUID   = regexp.MustCompile(`d == 'uid-([ab])' && request\.auth\.uid == '([^']+)'`)
	ruleEmail = regexp.MustCompile(`d == 'email' && request\.auth\.token\.email == '([^']+)'`)
)

// rulesActors are the ID tokens of a recording's actors, made from the uids and the email its
// ruleset names; "none" is no credential, "owner" an admin.
func rulesActors(t *testing.T, env *apitest.Env, src string) map[string]string {
	t.Helper()
	uids := map[string]string{}
	for _, m := range ruleUID.FindAllStringSubmatch(src, -1) {
		uids[m[1]] = m[2]
	}
	em := ruleEmail.FindStringSubmatch(src)
	if uids["a"] == "" || uids["b"] == "" || em == nil {
		t.Fatal("the ruleset names no users")
	}
	now := time.Now().Unix()
	password := func(uid, email string) string {
		return apitest.UserToken(t, uid, map[string]any{
			"email": email, "email_verified": false, "auth_time": now,
			"firebase": map[string]any{"identities": map[string]any{"email": []any{email}}, "sign_in_provider": "password"},
		})
	}
	return map[string]string{
		"user":  password(uids["a"], em[1]),
		"userB": password(uids["b"], strings.Replace(em[1], "-a@", "-b@", 1)),
		"anon": apitest.UserToken(t, "aruma-ref-anonymous", map[string]any{
			"provider_id": "anonymous", "auth_time": now,
			"firebase": map[string]any{"identities": map[string]any{}, "sign_in_provider": "anonymous"},
		}),
		"none":  "",
		"owner": env.JWT(t),
	}
}

// rulesFixtures are the documents the recorders wrote before any case: the lookup targets and
// the request.auth cases' documents.
func rulesFixtures() map[string]map[string]any {
	out := map[string]map[string]any{"aruma-ref-f5-cx/canary": {"a": 1}}
	for i := 1; i <= 41; i++ {
		out[fmt.Sprintf("aruma-ref-f5-fx/%d", i)] = map[string]any{"v": 1}
	}
	out["aruma-ref-f5-fx/2"]["s"] = "two"
	auth := []string{"authnull", "authnotnull", "uid-a", "uid-b", "uid-is-string", "email", "verified-false", "verified-true",
		"verified-bool", "prov-password", "prov-anonymous", "ident-email", "sub-is-uid", "aud", "iss", "auth-time-int", "iat-int",
		"exp-int", "firebase-map", "token-map", "auth-keys", "auth-keys-only"}
	for _, k := range []string{"name", "email", "email_verified", "sub", "iss", "aud", "iat", "exp", "auth_time", "user_id",
		"firebase", "picture", "phone_number", "admin", "uid", "nonce", "azp"} {
		auth = append(auth, "tok_"+k)
	}
	for _, k := range []string{"sign_in_provider", "identities", "tenant", "sign_in_second_factor", "second_factor_identifier"} {
		auth = append(auth, "fb_"+k)
	}
	for _, d := range auth {
		out["aruma-ref-f5-a/"+d] = map[string]any{"a": 1}
	}
	return out
}

// firestoreValue encodes a seed's plain JSON value as a REST Value.
func firestoreValue(v any) map[string]any {
	switch x := v.(type) {
	case nil:
		return map[string]any{"nullValue": nil}
	case bool:
		return map[string]any{"booleanValue": x}
	case string:
		return map[string]any{"stringValue": x}
	case int:
		return map[string]any{"integerValue": fmt.Sprint(x)}
	case float64:
		if x == float64(int64(x)) {
			return map[string]any{"integerValue": fmt.Sprint(int64(x))}
		}
		return map[string]any{"doubleValue": x}
	case []any:
		vs := make([]any, len(x))
		for i, e := range x {
			vs[i] = firestoreValue(e)
		}
		return map[string]any{"arrayValue": map[string]any{"values": vs}}
	case map[string]any:
		return map[string]any{"mapValue": map[string]any{"fields": firestoreFields(x)}}
	}
	panic(fmt.Sprintf("seed value %T", v))
}

func firestoreFields(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = firestoreValue(v)
	}
	return out
}

// timestampValue is a stored timestamp in a REST answer; only its precision is compared, which
// shows the precision of the server timestamps the writes were given.
var timestampValue = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z$`)

func stampPrecision(v any) any {
	switch x := v.(type) {
	case map[string]any:
		o := make(map[string]any, len(x))
		for k, y := range x {
			if s, ok := y.(string); ok && k == "timestampValue" {
				if m := timestampValue.FindStringSubmatch(s); m != nil {
					o[k] = fmt.Sprintf("<timestamp %d fractional digits>", max(len(m[1])-1, 0))
					continue
				}
			}
			o[k] = stampPrecision(y)
		}
		return o
	case []any:
		o := make([]any, len(x))
		for i, y := range x {
			o[i] = stampPrecision(y)
		}
		return o
	}
	return v
}

// stampNow gives the timestamps a request writes the time of the replay, at the recording's
// millisecond: the client's clock, which a rule compares with request.time (t.c-clock).
func stampNow(v any) any {
	switch x := v.(type) {
	case map[string]any:
		o := make(map[string]any, len(x))
		for k, y := range x {
			if _, ok := y.(string); ok && k == "timestampValue" {
				o[k] = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
				continue
			}
			o[k] = stampNow(y)
		}
		return o
	case []any:
		o := make([]any, len(x))
		for i, y := range x {
			o[i] = stampNow(y)
		}
		return o
	}
	return v
}

func TestReplayRulesInProcess(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(conformance.CorpusDir, "f5-rules-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	env := apitest.New(t, apitest.Options{})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(httpx.CommonWithLimit(rest.New(rest.Options{Service: env.Service, Logger: log}), log, nil, 17<<20))
	t.Cleanup(ts.Close)
	target := func(token string) *conformance.HTTPTarget {
		return &conformance.HTTPTarget{BaseURL: ts.URL, ProjectID: apitest.Project, Token: token, Client: ts.Client()}
	}
	admin := target(env.JWT(t))
	root := "projects/" + apitest.Project + "/databases/(default)/documents"
	commit := func(writes []any) {
		t.Helper()
		if len(writes) == 0 {
			return
		}
		res, err := admin.Send(t.Context(), conformance.Request{Method: "POST", Path: root + ":commit", Body: map[string]any{"writes": writes}})
		if err != nil || res.HTTP != 200 {
			t.Fatalf("admin commit: %v HTTP %d %.300v", err, res.HTTP, res.Body)
		}
	}
	set := func(docs map[string]map[string]any) []any {
		var ws []any
		for _, p := range slices.Sorted(maps.Keys(docs)) {
			ws = append(ws, map[string]any{"update": map[string]any{"name": root + "/" + p, "fields": firestoreFields(docs[p])}})
		}
		return ws
	}
	// state reads paths as the owner, as the recorders read back "after".
	state := func(paths []string) map[string]any {
		t.Helper()
		docs := make([]any, len(paths))
		for i, p := range paths {
			docs[i] = root + "/" + p
		}
		res, err := admin.Send(t.Context(), conformance.Request{Method: "POST", Path: root + ":batchGet", Body: map[string]any{"documents": docs}})
		if err != nil || res.HTTP != 200 {
			t.Fatalf("admin batchGet: %v HTTP %d %.300v", err, res.HTTP, res.Body)
		}
		out := map[string]any{}
		for _, r := range res.Body.([]any) {
			r := r.(map[string]any)
			if f, ok := r["found"].(map[string]any); ok {
				out[strings.TrimPrefix(f["name"].(string), root+"/")] = cmp.Or[any](f["fields"], map[string]any{})
			} else {
				out[strings.TrimPrefix(r["missing"].(string), root+"/")] = nil
			}
		}
		return out
	}

	compared, pending, seen := 0, 0, map[string]bool{}
	touched := map[string]bool{} // the documents the cases seeded or read back
	for _, path := range paths {
		name := filepath.Base(path)
		if slices.Contains(rulesSkipped, name) {
			continue
		}
		var f rulesFile
		readJSON(t, path, &f)
		if pendingA2a[name] {
			pending += len(f.Cases)
			continue
		}
		src := rulesSource(t, f.Rules)
		env.PublishFirestoreRules(t, src)
		actors := rulesActors(t, env, src)
		// Each recording began with its fixtures alone: the documents an earlier file's cases
		// left are deleted (transactions limit.reads-two-batches reads hl/r3-a as missing).
		var wipe []any
		for _, p := range slices.Sorted(maps.Keys(touched)) {
			wipe = append(wipe, map[string]any{"delete": root + "/" + p})
		}
		commit(append(wipe, set(rulesFixtures())...))
		clear(touched)
		for _, c := range f.Cases {
			id := name + " " + c.ID
			if pendingA2a[id] {
				pending++
				continue
			}
			tr, ok := c.Transports["rest"]
			if !ok {
				t.Fatalf("%s: no REST transport", id)
			}
			token, ok := actors[c.Actor]
			if !ok {
				t.Fatalf("%s: actor %q", id, c.Actor)
			}
			// Reset what the case touches, as the recorder did: its seed set, every other document
			// it read back deleted.
			var resets []any
			for _, p := range slices.Sorted(maps.Keys(tr.After)) {
				touched[p] = true
				if _, seeded := c.Seed[p]; !seeded {
					resets = append(resets, map[string]any{"delete": root + "/" + p})
				}
			}
			for p := range c.Seed {
				touched[p] = true
			}
			commit(append(resets, set(c.Seed)...))

			compared++
			var diffs []string
			actor, txn := target(token), ""
			for i, s := range tr.Steps {
				req := conformance.Request{Method: s.Request.Method, Path: strings.ReplaceAll(s.Request.Path, "{project}", apitest.Project), NoAuth: token == ""}
				if s.Request.Body != nil {
					var sb strings.Builder
					enc := json.NewEncoder(&sb)
					enc.SetEscapeHTML(false)
					if err := enc.Encode(stampNow(s.Request.Body)); err != nil {
						t.Fatal(err)
					}
					b := strings.ReplaceAll(sb.String(), "{project}", apitest.Project)
					b = strings.ReplaceAll(b, `"<transaction>"`, fmt.Sprintf("%q", txn))
					if err := json.Unmarshal([]byte(b), &req.Body); err != nil {
						t.Fatal(err)
					}
				}
				res, err := actor.Send(t.Context(), req)
				if err != nil {
					t.Fatalf("%s step %d: %v", id, i, err)
				}
				txn = cmp.Or(transactionOf(res.Body), txn)
				// The recorder left the project id in an index link (q1.where-match-order-other).
				d := Diff(stampPrecision(Stable(Normalise(s.Response.Body, apitest.Project, "{project}"))),
					stampPrecision(Stable(Normalise(res.Body, apitest.Project, "{project}"))))
				if res.HTTP != s.Response.HTTP {
					d = append([]string{fmt.Sprintf("HTTP %d, want %d", res.HTTP, s.Response.HTTP)}, d...)
				}
				for _, l := range d {
					diffs = append(diffs, fmt.Sprintf("step %d (%s %s): %s", i, s.Request.Method, lastSegments(s.Request.Path), l))
				}
			}
			if len(tr.After) > 0 {
				got := state(slices.Sorted(maps.Keys(tr.After)))
				for _, l := range Diff(stampPrecision(any(tr.After)), stampPrecision(any(got))) {
					diffs = append(diffs, "after: "+l)
				}
			}
			if exception, ok := rulesDiffers[id]; ok {
				seen[id] = true
				if len(diffs) == 0 {
					t.Errorf("%s: matches production, but is listed as differing (%s)", id, exception)
				}
				continue
			}
			if len(diffs) > 0 {
				t.Errorf("%s differs from production:\n  %s", id, strings.Join(diffs, "\n  "))
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(rulesDiffers)) {
		if !seen[id] {
			t.Errorf("%s is listed as differing but is not a replayed case", id)
		}
	}
	t.Logf("replayed %d rules cases, %d listed as differing, %d pending A2a", compared, len(rulesDiffers), pending)
}

// transactionOf is the transaction an answer opened: BeginTransaction's, or the first a read's
// stream carries.
func transactionOf(body any) string {
	switch x := body.(type) {
	case map[string]any:
		s, _ := x["transaction"].(string)
		return s
	case []any:
		for _, e := range x {
			if s := transactionOf(e); s != "" {
				return s
			}
		}
	}
	return ""
}

func lastSegments(p string) string {
	parts := strings.Split(p, "/")
	return strings.Join(parts[max(len(parts)-2, 0):], "/")
}
