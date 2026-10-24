package media

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestFetchMetadataTable is the whole Sec-Fetch-Site × Sec-Fetch-Dest table (ADR-022 §10.1):
// every destination Fetch defines, and the values a browser cannot send, from every site. The
// expected answer is written out per destination, not computed by the rule under test.
func TestFetchMetadataTable(t *testing.T) {
	sites := []string{"", "none", "same-origin", "same-site", "cross-site", "SAME-ORIGIN", "bogus"}
	// For each destination: refused from the dashboard's own origin, refused from elsewhere.
	dests := []struct {
		dest              string
		sameOrigin, else_ bool
	}{
		{"", false, false},
		{"audio", false, false},
		{"audioworklet", true, false},
		{"document", false, false},
		{"embed", false, false},
		{"empty", false, false},
		{"fencedframe", false, false},
		{"font", false, false},
		{"frame", false, false},
		{"iframe", false, false},
		{"image", false, false},
		{"json", false, false},
		{"manifest", false, false},
		{"object", false, false},
		{"paintworklet", true, false},
		{"report", false, false},
		{"script", true, false},
		{"serviceworker", true, true},
		{"sharedworker", true, true},
		{"style", true, false},
		{"track", false, false},
		{"video", false, false},
		{"webidentity", false, false},
		{"worker", true, true},
		{"xslt", true, false},
		{"Script", true, false}, // tokens are lower case; any case is refused
		{"WORKER", true, true},
		{"bogus", false, false},
	}
	data := []byte("self.onfetch = e => e.respondWith(new Response('owned'))")
	for _, site := range sites {
		for _, d := range dests {
			want := d.else_
			if site == "same-origin" || site == "SAME-ORIGIN" {
				want = d.sameOrigin
			}
			var hd []string
			if site != "" {
				hd = append(hd, "Sec-Fetch-Site", site)
			}
			if d.dest != "" {
				hd = append(hd, "Sec-Fetch-Dest", d.dest)
			}
			for _, m := range []Mode{RulesDecided, Token, GCS, Console} {
				it := object("sw.js", "text/javascript", data)
				rec, err := get(t, newGet(hd...), it, data, m, generous)
				if err != nil {
					t.Fatalf("%q × %q: %v", site, d.dest, err)
				}
				if got := rec.Code == http.StatusForbidden; got != want {
					t.Errorf("site %q dest %q mode %d: refused %v, want %v", site, d.dest, m, got, want)
				}
			}
		}
	}
}

func TestServiceWorkerHeaderRefused(t *testing.T) {
	data := []byte("x")
	for _, v := range []string{"script", "", "SCRIPT"} {
		rec, _ := get(t, newGet("Service-Worker", v, "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Dest", "image"),
			object("a.js", "text/javascript", data), data, Token, generous)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Service-Worker: %q: %d", v, rec.Code)
		}
	}
}

// TestRefusalShape: a refusal is the surface's JSON error with the server's general headers,
// none of a media reply's, and no byte of the object.
func TestRefusalShape(t *testing.T) {
	data := []byte("secret")
	cases := []struct {
		m    Mode
		want string
	}{
		{RulesDecided, `{"error":{"code":403,"message":"Permission denied."}}`},
		{Token, `{"error":{"code":403,"message":"Permission denied."}}`},
		{GCS, `{"error":{"code":403,"message":"Permission denied.","errors":[{"domain":"global","reason":"forbidden","message":"Permission denied."}]}}`},
		{Console, `{"error":{"code":403,"message":"Permission denied.","status":"PERMISSION_DENIED"}}`},
	}
	for _, c := range cases {
		rec, err := get(t, newGet("Sec-Fetch-Dest", "serviceworker"), object("a", "text/plain", data), data, c.m, generous)
		if err != nil || rec.Code != http.StatusForbidden {
			t.Fatalf("mode %d: %d %v", c.m, rec.Code, err)
		}
		var got, want any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		_ = json.Unmarshal([]byte(c.want), &want)
		if b1, _ := json.Marshal(got); string(b1) != mustJSON(want) {
			t.Errorf("mode %d: %s", c.m, rec.Body)
		}
		h := rec.Header()
		if field(h, "Content-Type") != "application/json; charset=UTF-8" ||
			field(h, "Cross-Origin-Resource-Policy") != "same-origin" ||
			field(h, "Content-Security-Policy") != "default-src 'none'; frame-ancestors 'none'" ||
			field(h, "Content-Disposition") != "" || field(h, "x-goog-hash") != "" || field(h, "ETag") != "" {
			t.Errorf("mode %d headers: %v", c.m, h)
		}
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
