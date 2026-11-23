package ops

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
)

// capture reads everything a deployment serves through its public APIs, as one JSON-shaped
// value: the same call on the original and on a restored or upgraded deployment must give equal
// values, byte for byte, times and tokens included. It reads and changes nothing.
func capture(t *testing.T, d *deployment, fx *fixture) map[string]any {
	t.Helper()
	a := newAPI(t, d.base())
	o := &owner{a: a, pat: fx.PAT}
	out := map[string]any{}
	get := func(path string) any {
		r := o.a.do("GET", path, bearer(fx.PAT), nil)
		if r.Status != 200 {
			t.Errorf("GET %s: %s", path, r)
		}
		var v any
		if err := json.Unmarshal(r.Body, &v); err != nil {
			return fmt.Sprintf("%d %s", r.Status, r.Body)
		}
		return map[string]any{"status": r.Status, "body": v}
	}
	out["projects"] = get("/console/v1/projects")
	for _, p := range fx.Projects {
		c := "/console/v1/projects/" + p.ID
		pm := map[string]any{}
		out[p.ID] = pm
		for k, path := range map[string]string{
			"project": c, "apiKeys": c + "/apiKeys", "serviceAccounts": c + "/serviceAccounts",
			"serviceAccountKeys": c + "/serviceAccounts/" + p.SAID + "/keys",
			"authConfig":         c + "/auth/config", "users": c + "/auth/users?pageSize=100", "signingKeys": c + "/auth/signingKeys",
			"rtdbRules": c + "/database/rules", "adminSdkConfig": c + "/adminSdkConfig",
		} {
			pm[k] = get(path)
		}
		pm["rtdb"] = o.a.do("GET", rtdbURL(p, "", "&format=export"), bearer(fx.PAT), nil).full()
		pm["rtdbCount"] = get(c + "/database")
		pm["documents"] = captureDocuments(t, o, p)
		pm["storage"] = captureStorage(t, a, p)
	}
	return out
}

func captureDocuments(t *testing.T, o *owner, p *project) map[string]any {
	base := docsBase(p)
	m := map[string]any{}
	q := o.a.do("POST", base+"/documents:runQuery", bearer(o.pat), map[string]any{"structuredQuery": map[string]any{
		"from": []any{map[string]any{"allDescendants": true}}, "orderBy": []any{map[string]any{"field": map[string]any{"fieldPath": "__name__"}, "direction": "ASCENDING"}}}})
	var rows []map[string]any
	_ = json.Unmarshal(q.Body, &rows)
	docs := map[string]any{}
	for _, r := range rows {
		if d, ok := r["document"].(map[string]any); ok {
			docs[fmt.Sprint(d["name"])] = d
		}
	}
	if len(docs) < 8 {
		t.Errorf("%s: the document query returned %d documents: %s", p.ID, len(docs), q)
	}
	m["documents"] = docs
	m["indexes"] = o.a.do("GET", base+"/collectionGroups/-/indexes", bearer(o.pat), nil).full()
	m["fields"] = o.a.do("GET", base+"/collectionGroups/-/fields?filter=indexConfig.usesAncestorConfig%3Dfalse", bearer(o.pat), nil).full()
	rel := o.a.do("GET", "/v1/projects/"+p.ID+"/releases/cloud.firestore", bearer(o.pat), nil)
	m["release"] = rel.full()
	if rs := rel.str("rulesetName"); rs != "" {
		m["ruleset"] = o.a.do("GET", "/v1/"+rs, bearer(o.pat), nil).full()
	}
	return m
}

// captureStorage lists every object with its metadata and the digests of its bytes.
func captureStorage(t *testing.T, a *api, p *project) map[string]any {
	tok := p.token(t, a)
	m := map[string]any{}
	var names []string
	for page := ""; ; {
		r := a.do("GET", "/storage/v1/b/"+p.Bucket+"/o?maxResults=1000"+page, bearer(tok), nil)
		if r.Status != 200 {
			t.Fatalf("%s: storage listing: %s", p.ID, r)
		}
		var l struct {
			Items         []struct{ Name string } `json:"items"`
			NextPageToken string                  `json:"nextPageToken"`
		}
		_ = json.Unmarshal(r.Body, &l)
		for _, it := range l.Items {
			names = append(names, it.Name)
		}
		if l.NextPageToken == "" {
			break
		}
		page = "&pageToken=" + url.QueryEscape(l.NextPageToken)
	}
	sort.Strings(names)
	if len(names) < 7 {
		t.Fatalf("%s: the storage listing holds %d objects: %v", p.ID, len(names), names)
	}
	objs := map[string]any{}
	for _, n := range names {
		meta := a.do("GET", "/v0/b/"+p.Bucket+"/o/"+url.PathEscape(n), bearer(tok), nil)
		body := a.do("GET", "/v0/b/"+p.Bucket+"/o/"+url.PathEscape(n)+"?alt=media", bearer(tok), nil)
		if meta.Status != 200 || body.Status != 200 {
			t.Errorf("%s: object %q: metadata %d, bytes %d: %s", p.ID, n, meta.Status, body.Status, body.Body[:min(len(body.Body), 200)])
		}
		sum := sha256.Sum256(body.Body)
		md := md5.Sum(body.Body)
		e := map[string]any{"meta": meta.obj(), "status": body.Status, "sha256": hex.EncodeToString(sum[:]), "length": len(body.Body),
			"md5": base64.StdEncoding.EncodeToString(md[:])}
		// The download link of the SDKs: the token alone opens the object.
		if tk := meta.str("downloadTokens"); tk != "" {
			first, _, _ := strings.Cut(tk, ",")
			dl := a.do("GET", "/v0/b/"+p.Bucket+"/o/"+url.PathEscape(n)+"?alt=media&token="+first, nil, nil)
			dsum := sha256.Sum256(dl.Body)
			e["tokenDownload"] = fmt.Sprintf("%d %s", dl.Status, hex.EncodeToString(dsum[:]))
		}
		objs[n] = e
	}
	m["objects"] = objs
	rel := a.do("GET", "/v1/projects/"+p.ID+"/releases/firebase.storage/"+p.Bucket, bearer(tok), nil)
	m["release"] = rel.full()
	return m
}

// diffValues returns a description of the first difference between two captures, or "".
func diffValues(path string, a, b any) string {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s: %T vs %T", path, a, b)
		}
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		for k := range y {
			keys[k] = true
		}
		var ks []string
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			xv, xok := x[k]
			yv, yok := y[k]
			switch {
			case !xok:
				return fmt.Sprintf("%s/%s: only in the second: %s", path, k, short(yv))
			case !yok:
				return fmt.Sprintf("%s/%s: only in the first: %s", path, k, short(xv))
			}
			if d := diffValues(path+"/"+k, xv, yv); d != "" {
				return d
			}
		}
		return ""
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return fmt.Sprintf("%s: %s vs %s", path, short(a), short(b))
		}
		for i := range x {
			if d := diffValues(fmt.Sprintf("%s[%d]", path, i), x[i], y[i]); d != "" {
				return d
			}
		}
		return ""
	}
	if fmt.Sprint(a) != fmt.Sprint(b) {
		return fmt.Sprintf("%s: %s vs %s", path, short(a), short(b))
	}
	return ""
}

func short(v any) string {
	s := fmt.Sprint(v)
	if b, err := json.Marshal(v); err == nil {
		s = string(b)
	}
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// normalize round-trips a capture through JSON so numbers and nesting compare alike.
func normalize(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
