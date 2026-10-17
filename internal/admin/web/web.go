// Package web serves the dashboard: the Next.js static export embedded in the binary.
//
// Pages are prerendered HTML (trailing-slash layout, ids in query strings), so serving is a
// file lookup with no rewrites. Every HTML page gets its own Content-Security-Policy listing
// the SHA-256 of each inline script Next.js emitted, computed once at startup, so scripts
// never need 'unsafe-inline' (ADR-010 S-9).
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// dist holds the export copied from dashboard/out by `make dashboard`.
//
//go:embed all:dist
var dist embed.FS

// Embedded returns the dashboard files compiled into the binary.
func Embedded() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

type file struct {
	body        []byte
	contentType string
	etag        string
	csp         string // HTML only
	immutable   bool
}

// Handler serves a static export.
type Handler struct {
	files    map[string]*file // by clean path without leading slash
	notFound *file
	built    bool
}

// New indexes fsys. It reads every file once, so the handler never touches the filesystem
// afterwards.
func New(fsys fs.FS) (*Handler, error) {
	h := &Handler{files: map[string]*file{}}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(path.Base(p), ".") {
			return err
		}
		body, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		f := &file{
			body:        body,
			contentType: contentType(p),
			etag:        `"` + hex.EncodeToString(sum[:16]) + `"`,
			immutable:   strings.HasPrefix(p, "_next/static/"),
		}
		if strings.HasSuffix(p, ".html") {
			f.csp = policy(body)
		}
		h.files[p] = f
		return nil
	})
	if err != nil {
		return nil, err
	}
	h.notFound = h.files["404.html"]
	_, h.built = h.files["index.html"]
	return h, nil
}

func contentType(p string) string {
	if t := mime.TypeByExtension(path.Ext(p)); t != "" {
		return t
	}
	switch path.Ext(p) {
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".woff2":
		return "font/woff2"
	}
	return "application/octet-stream"
}

var inlineScript = regexp.MustCompile(`(?s)<script(\s[^>]*)?>(.*?)</script>`)

// policy builds the page's CSP: same-origin everything, plus the hash of each inline script.
func policy(html []byte) string {
	var hashes []string
	seen := map[string]bool{}
	for _, m := range inlineScript.FindAllSubmatch(html, -1) {
		attrs, body := m[1], m[2]
		if bytes.Contains(attrs, []byte("src=")) || len(bytes.TrimSpace(body)) == 0 {
			continue
		}
		sum := sha256.Sum256(body)
		h := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !seen[h] {
			seen[h] = true
			hashes = append(hashes, h)
		}
	}
	scripts := "'self'"
	if len(hashes) > 0 {
		scripts += " " + strings.Join(hashes, " ")
	}
	return "default-src 'self'; script-src " + scripts + "; style-src 'self'; img-src 'self' data:; " +
		"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.built {
		h.placeholder(w)
		return
	}
	p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if f, ok := h.files[p]; ok && p != "" {
		h.serve(w, r, f, http.StatusOK)
		return
	}
	// Directory paths serve their index.html; the export uses trailing slashes.
	if f, ok := h.files[path.Join(p, "index.html")]; ok {
		if p != "" && !strings.HasSuffix(r.URL.Path, "/") {
			target := "/" + p + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		}
		h.serve(w, r, f, http.StatusOK)
		return
	}
	if h.notFound != nil {
		h.serve(w, r, h.notFound, http.StatusNotFound)
		return
	}
	http.NotFound(w, r)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request, f *file, status int) {
	hd := w.Header()
	hd.Set("Content-Type", f.contentType)
	hd.Set("ETag", f.etag)
	if f.csp != "" {
		hd.Set("Content-Security-Policy", f.csp)
	}
	switch {
	case f.immutable:
		hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	case strings.HasPrefix(f.contentType, "text/html"):
		hd.Set("Cache-Control", "no-cache")
	default:
		hd.Set("Cache-Control", "public, max-age=3600")
	}
	if status == http.StatusOK && r.Header.Get("If-None-Match") == f.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	hd.Set("Content-Length", strconv.Itoa(len(f.body)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(f.body)
	}
}

// placeholder answers when the binary was built without the dashboard.
func (h *Handler) placeholder(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>Aruma</title>` +
		`<p>This build of Aruma does not include the dashboard. Build it with <code>make dashboard build</code>. ` +
		`The APIs under /console/v1 and /v1beta1 are available.</p>`))
}
