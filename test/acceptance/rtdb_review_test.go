package acceptance

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Properties of the security review of v0.2 (docs/security/review-v0.2.md) that no other test
// covers on a real server.

// getAt sends a GET for target to the test server, naming host in the request when it is
// set, and returns the answer.
func getAt(t *testing.T, e *Env, host, target string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest("GET", e.URL+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

// What Aruma sends a browser cannot be turned into markup or script. Stored data reaches a
// long-poll client escaped, so no value leaves the script it rides in; only a numeric
// callback goes into a script; the dframe page reflects its parameters percent-encoded and
// lets only its own script run; and the errors that quote what a client sent are JSON with
// <, > and & escaped, under nosniff, never HTML.
func TestRTDBOutputSafety(t *testing.T) {
	e := rtdbEnv(t, false, map[string]string{"ARUMA_RTDB_DOMAIN": "rtdb.aruma.test"})
	ns := e.owner().projectNS("chat-app")
	evil := "</script><script>alert(1)</script><!-- \"quoted\" & " + string(rune(0x2028)) + string(rune(0x2029))
	ws := e.dial(ns, dialOpts{})
	ws.put("/x", map[string]any{"v": evil})

	lp := e.startLP(ns)
	listen, _ := json.Marshal(map[string]any{"t": "d", "d": map[string]any{"r": 1, "a": "q", "b": map[string]any{"p": "/x", "h": ""}}})
	body := lp.poll(string(listen))
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(body, "alert"); body += lp.poll() {
		if time.Now().After(deadline) {
			t.Fatalf("no push of the stored value: %q", body)
		}
	}
	if strings.ContainsAny(body, "<>&"+string(rune(0x2028))+string(rune(0x2029))) {
		t.Errorf("the long-poll answer carries markup: %q", body)
	}
	var push struct {
		D struct {
			B struct {
				D map[string]string `json:"d"`
			} `json:"b"`
		} `json:"d"`
	}
	if m := lp.await("the push", isDataAt("x")); json.Unmarshal([]byte(m), &push) != nil || push.D.B.D["v"] != evil {
		t.Errorf("the value does not come back as written: %s", m)
	}

	// Only a numeric callback goes into the script.
	for _, cb := range []string{"1);alert(1);//", "x", "</script>", "1e5", strings.Repeat("9", 21)} {
		code, h, body := getAt(t, e, "", "/.lp?start=t&ser=1&v=5&ns="+ns+"&cb="+url.QueryEscape(cb))
		if code != 400 || strings.Contains(body, "alert") || !strings.HasPrefix(h.Get("Content-Type"), "application/json") {
			t.Errorf("callback %q: %d %s %s", cb, code, h.Get("Content-Type"), body)
		}
	}

	// The dframe page: what it reflects is percent-encoded, and its policy allows its own
	// script only.
	payload := `"</script><script>alert(1)//'` + "\n"
	code, h, page := getAt(t, e, "", "/.lp?dframe=t&ns="+ns+"&id="+url.QueryEscape(payload)+"&pw="+url.QueryEscape(payload))
	if code != 200 || strings.Contains(page, "alert(1)//") || strings.Count(page, "<script>") != 1 || strings.Count(page, "</script>") != 1 {
		t.Fatalf("dframe page reflects markup: %d %s", code, page)
	}
	script := page[strings.Index(page, "<script>")+len("<script>") : strings.Index(page, "</script>")]
	sum := sha256.Sum256([]byte(script))
	want := "default-src 'none'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; connect-src 'self'; base-uri 'none'; form-action 'none'"
	if csp := h.Get("Content-Security-Policy"); csp != want || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("dframe headers: %v, want Content-Security-Policy %q", h, want)
	}

	// Errors that quote what the client sent.
	port := e.URL[strings.LastIndex(e.URL, ":"):]
	dbHost := ns + ".rtdb.aruma.test" + port
	markup := url.QueryEscape("<script>alert(1)</script>")
	for _, r := range []struct {
		target string
		host   string
	}{
		{"/x.json?ns=" + markup, dbHost},                                                // ?ns= against the host's: quoted
		{"/x.json?ns=" + ns + "&orderBy=" + url.QueryEscape(`"<script>alert(1)<"`), ""}, // no .indexOn: quoted
		{"/x.json?ns=" + ns + "&orderBy=" + markup, ""},
		{"/x.json?ns=" + ns + `&orderBy="$key"&startAt=` + markup, ""},
		{"/x.json?ns=" + ns + "&format=" + markup, ""},
		{"/x.json?ns=" + ns + "&timeout=" + markup, ""},
		{"/" + markup + ".json?ns=" + ns, ""},
		{"/x.json?ns=" + ns + "&x-http-method-override=" + markup, ""},
		{"/.lp?ns=" + markup + "&start=t", dbHost},
		{"/.lp?ns=" + ns + "&id=1&pw=" + markup + "&seg0=" + markup, ""},
		{"/.ws?ns=" + markup, dbHost},
		{"/" + markup + "?ns=" + ns, dbHost},
		{"/.inspect" + markup + "?ns=" + ns, dbHost},
	} {
		status, h, body := getAt(t, e, r.host, r.target)
		ct := h.Get("Content-Type")
		if strings.Contains(strings.ToLower(body), "<script") || strings.HasPrefix(ct, "text/html") || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s (host %q): %d %s %q", r.target, r.host, status, ct, body)
		}
	}
	if got := restAs(t, e, "GET", "/x.json?ns="+ns+"&orderBy="+url.QueryEscape(`"<script>alert(1)<"`), "", "", nil); got.status != 400 ||
		!strings.Contains(got.body, `\u003cscript\u003ealert(1)\u003c`) {
		t.Errorf("the quoted index is not escaped JSON: %d %s", got.status, got.body)
	}
}

// ETags compare exactly: a near miss neither answers 304 nor lets a conditional write through.
func TestRTDBETagsCompareExactly(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("chat-app")
	q := "/x.json?ns=" + ns
	restAs(t, e, "PUT", q, `{"a":1}`, "", nil)
	etag := restAs(t, e, "GET", q, "", "", map[string]string{"X-Firebase-ETag": "true"}).header.Get("ETag")
	if got := restAs(t, e, "GET", q, "", "", map[string]string{"If-None-Match": etag}); got.status != 304 {
		t.Fatalf("the ETag itself: %d", got.status)
	}
	near := []string{`"` + etag + `"`, "W/" + etag, strings.ToLower(etag), etag[:len(etag)-1], etag + "=", "*", "null_etag", ""}
	for _, v := range near {
		if got := restAs(t, e, "GET", q, "", "", map[string]string{"If-None-Match": v}); got.status != 200 {
			t.Errorf("If-None-Match %q: %d", v, got.status)
		}
		if v == "*" {
			continue // if-match: * is unconditional, as in Firebase
		}
		if got := restAs(t, e, "PUT", q, `{"a":2}`, "", map[string]string{"If-Match": v}); got.status != 412 {
			t.Errorf("If-Match %q: %d %s", v, got.status, got.body)
		}
	}
	if got := restAs(t, e, "GET", q, "", "", nil); got.body != `{"a":1}` {
		t.Errorf("after the near misses: %s", got.body)
	}
}
