// Package web serves the dashboard: the Next.js static export embedded in the binary.
//
// Pages are prerendered HTML (trailing-slash layout, ids in query strings), so serving is a
// file lookup with no rewrites. Every HTML page gets its own Content-Security-Policy listing
// the SHA-256 of each inline script Next.js emitted, computed once at startup, so scripts
// never need 'unsafe-inline' (ADR-010 S-9).
//
// The policy loads scripts, stylesheets and fonts from the build's own directory alone,
// <origin>/_next/static/, never from 'self' (ADR-022 §10.1). Storage serves user files from
// this origin, and on plain http browsers send none of the Sec-Fetch-* headers media would
// refuse a <script src> by, so this path is what keeps an uploaded file from running as the
// dashboard's script.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"slices"
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
	page        *pagePolicy // HTML only
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
	styles, err := styleHashes(fsys)
	if err != nil {
		return nil, err
	}
	h := &Handler{files: map[string]*file{}}
	err = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
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
			f.page = &pagePolicy{scripts: scriptHashes(body), styles: styles}
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

var cspHash = regexp.MustCompile(`^'sha256-[A-Za-z0-9+/]{43}='$`)

// styleHashes reads csp.json from the export: the hashes of the fixed <style> elements the
// dashboard's component library inserts at runtime. Anything but a well-formed hash is refused,
// so the file cannot widen the policy.
func styleHashes(fsys fs.FS) ([]string, error) {
	b, err := fs.ReadFile(fsys, "csp.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest struct {
		StyleSrcElem []string `json:"styleSrcElem"`
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		return nil, fmt.Errorf("csp.json: %w", err)
	}
	for _, h := range manifest.StyleSrcElem {
		if !cspHash.MatchString(h) {
			return nil, fmt.Errorf("csp.json: %q is not a sha256 source", h)
		}
	}
	return manifest.StyleSrcElem, nil
}

// scriptHashes lists the hash of each inline script of a page, once each, in page order.
func scriptHashes(html []byte) []string {
	var hashes []string
	for _, m := range inlineScript.FindAllSubmatch(html, -1) {
		attrs, body := m[1], m[2]
		if bytes.Contains(attrs, []byte("src=")) || len(bytes.TrimSpace(body)) == 0 {
			continue
		}
		sum := sha256.Sum256(body)
		if h := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"; !slices.Contains(hashes, h) {
			hashes = append(hashes, h)
		}
	}
	return hashes
}

// pagePolicy is what a page's CSP lists besides its origin: the hashes of its inline scripts
// and of the known runtime stylesheets.
type pagePolicy struct {
	scripts, styles []string
}

// staticDir is where the build's own files are, each named by its content's hash.
const staticDir = "/_next/static/"

// validHost matches a Host header that can stand in a CSP host-source as it is: a name or an
// IPv4 address, or a bracketed IPv6 address, with an optional port. Any other (a space or a ';'
// would start another source or directive) gets a policy that loads nothing.
var validHost = regexp.MustCompile(`^(?:[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?|\[[0-9A-Fa-f:.]{2,45}\])(?::[0-9]{1,5})?$`)

// policy builds a page's CSP for a request to host. Scripts, stylesheets and fonts load from
// host + /_next/static/ alone, never 'self': a file Storage serves on this origin, under /v0
// or as a console download, is never the dashboard's script or stylesheet (ADR-022 §10.1).
// The source has no scheme, so it matches the page's own (and its upgrade to https). It is the
// Host the browser sent, so the dashboard works under every name it is reached by; a proxy in
// front must pass the Host on, as the console's cross-origin check already needs.
//
// The rest stays on the origin: the console API, /v0 for uploads and the prefetched pages
// (connect-src), and images, an object's preview among them (img-src). Nothing frames the
// dashboard or is framed by it, and nothing runs as a worker or a plugin.
func (pp *pagePolicy) policy(host string) string {
	static := "'none'"
	if validHost.MatchString(host) {
		static = host + staticDir
	}
	scripts := strings.Join(append([]string{static}, pp.scripts...), " ")
	styles := strings.Join(append([]string{static}, pp.styles...), " ")
	return "default-src 'none'; script-src " + scripts + "; style-src " + styles + "; font-src " + static +
		"; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; " +
		"frame-ancestors 'none'"
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.built {
		if path.Ext(r.URL.Path) == "" && r.URL.Path != "/metrics" {
			h.placeholder(w)
			return
		}
		http.NotFound(w, r)
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
	if f.page != nil {
		hd.Set("Content-Security-Policy", f.page.policy(r.Host))
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
