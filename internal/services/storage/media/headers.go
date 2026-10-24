package media

import (
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// The headers of a media reply (ADR-022 §10.1, §10.2, §10.4, §10.5). Every value is decided
// here, once, for the three surfaces; the error replies (403, 416) keep the server's general
// headers instead (F §2.3), and get them from writeError.

// sandboxCSP is raw.githubusercontent.com's policy, measured on text, PNG and SVG (F §2.1):
// sandbox without allow-scripts gives the document an opaque origin, so no script runs and the
// dashboard's cookies and storage are out of reach. It applies to every type in v0.3 (§10.2).
// X-Frame-Options: DENY from the general headers stays, so no frame-ancestors is needed.
const sandboxCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

// mediaCSP is the policy of video/* and audio/*: the same, with media-src 'self' so the file
// plays when browsed to directly; still under sandbox until the browser matrix (§10.6) says
// otherwise.
const mediaCSP = "default-src 'none'; media-src 'self'; style-src 'unsafe-inline'; sandbox"

// defaultCacheControl is what a reply without a stored Cache-Control sends, as production sends
// on its JSON replies (E §1): nothing shared caches it, and a browser revalidates. It is what
// GCS sends for an object that is not public, and no Aruma object is public.
const defaultCacheControl = "private, max-age=0"

// consoleCacheControl keeps a dashboard download out of every cache: it was fetched on a
// session cookie, and a copy on disk would outlive the session.
const consoleCacheControl = "no-store"

// octetStream is the type of an object stored without one: never a sniffed type (§10.1), or a
// file named x.html would be served as text/html (F §2.2-4).
const octetStream = "application/octet-stream"

// setHeaders writes the headers of a reply serving it in mode m, except the ones that depend on
// the bytes sent (Content-Length, Content-Range, Content-Encoding, Accept-Ranges).
func setHeaders(h http.Header, it Item, m Mode) {
	ct := contentType(it.ContentType)
	mt := mediaType(ct)
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(mt, "video/") || strings.HasPrefix(mt, "audio/") {
		h.Set("Content-Security-Policy", mediaCSP)
	} else {
		h.Set("Content-Security-Policy", sandboxCSP)
	}
	// Cross-origin over the general same-origin, per route: without it every app on another
	// origin is denied <img src=downloadURL>, the commonest use of a download URL (F §2.2-1).
	// The dashboard's own download keeps same-origin: it rides a session cookie, and no other
	// origin has a reason to embed it.
	if m != Console {
		h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	}
	h.Set("Content-Disposition", disposition(it, m, mt == "application/pdf"))
	h.Set("Cache-Control", cacheControl(it.CacheControl, m))
	if it.ETag != "" {
		h.Set("ETag", it.ETag)
	}
	if !it.Updated.IsZero() {
		h.Set("Last-Modified", it.Updated.UTC().Format(http.TimeFormat))
	}
	if lang := cleanValue(it.ContentLanguage); lang != "" {
		h.Set("Content-Language", lang)
	}
	wire.Set(h, wire.GoogGeneration, wire.Int(it.Generation))
	wire.Set(h, wire.GoogMetageneration, wire.Int(it.Metageneration))
	// One header, no space after the comma: Node splits on "," and keeps the spaces, and two
	// headers would reach it joined by ", " (B §6.1).
	wire.Set(h, wire.GoogHash, wire.HashHeader(it.CRC32C, it.MD5))
	// Always present: without it Node skips its checksum without a word (B §6.1, F §3.4).
	wire.Set(h, wire.GoogStoredContentEncoding, storedEncoding(it.ContentEncoding))
	if m == GCS {
		wire.Set(h, wire.GoogStoredContentLength, wire.Int(it.Size))
	}
	if isGzip(it.ContentEncoding) {
		// The reply is the stored bytes or their decompression, by Accept-Encoding.
		h.Set("Vary", "Accept-Encoding")
	}
}

// disposition returns the Content-Disposition of a reply (§10.4): the stored value when there
// is one, and attachment with the object's last segment when there is not. The dashboard's
// download and every PDF are attachment whatever is stored, keeping the stored parameters: a
// PDF under sandbox would show Chromium's viewer broken (§10.2), and the dashboard never
// displays a file on its own origin.
func disposition(it Item, m Mode, pdf bool) string {
	stored := cleanValue(it.ContentDisposition)
	if stored != "" && m != Console && !pdf {
		return stored
	}
	params := ""
	if _, p, ok := strings.Cut(stored, ";"); ok {
		params = strings.TrimSpace(p)
	}
	if params == "" {
		params = filenameParam(it.Name)
	}
	if params == "" {
		return "attachment"
	}
	return "attachment; " + params
}

// filenameParam returns the filename* parameter (UTF-8, no language) of the name's last
// segment, escaped by RFC 8187 the way core's stored default is (core.defaultDisposition;
// PY-RR: + kept, ' as %27), or "" for a name that ends with a slash. media cannot import core,
// so the escaping is written twice, and TestFilenameMatchesStoredDefault holds the two together
// on the published case.
func filenameParam(name string) string {
	last := name[strings.LastIndexByte(name, '/')+1:]
	if last == "" {
		return ""
	}
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.WriteString("filename*=utf-8''")
	for i := 0; i < len(last); i++ {
		c := last[i]
		if attrChar(c) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
	return b.String()
}

// attrChar reports RFC 8187's attr-char.
func attrChar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}

// cacheControl returns the Cache-Control of a reply in mode m (§10.5).
//
// A /v0 download the rules decided answers a request with Authorization, and public (or
// s-maxage, or must-revalidate, RFC 9111 §3.5) would let a shared cache keep a private object
// and serve it to someone else; the client sets the stored value itself. So public and
// s-maxage go, and private is added when absent. A download by token, and GCS, serve the
// stored value as is: the link is the credential, and Go reads ReaderObjectAttrs.CacheControl
// from this header, which must agree with Attrs().CacheControl (http_client.go:1635).
func cacheControl(stored string, m Mode) string {
	if m == Console {
		return consoleCacheControl
	}
	stored = cleanValue(stored)
	if stored == "" {
		return defaultCacheControl
	}
	if m != RulesDecided {
		return stored
	}
	out := []string{"private"}
	for d := range strings.SplitSeq(stored, ",") {
		d = strings.TrimSpace(d)
		name, _, _ := strings.Cut(d, "=")
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "", "public", "private", "s-maxage":
			continue
		}
		out = append(out, d)
	}
	return strings.Join(out, ", ")
}

// noTransform reports a stored Cache-Control with no-transform, which forbids decompressing
// (GCS transcoding; §10.7).
func noTransform(stored string) bool {
	for d := range strings.SplitSeq(stored, ",") {
		if strings.EqualFold(strings.TrimSpace(d), "no-transform") {
			return true
		}
	}
	return false
}

// isGzip reports a stored Content-Encoding of gzip.
func isGzip(enc string) bool { return strings.EqualFold(strings.TrimSpace(enc), "gzip") }

// identity reports a stored Content-Encoding that is no encoding at all.
func identity(enc string) bool {
	enc = strings.TrimSpace(enc)
	return enc == "" || strings.EqualFold(enc, "identity")
}

// storedEncoding returns x-goog-stored-content-encoding: identity, gzip, or another stored
// token as it is (Node validates only identity, and gzip served compressed).
func storedEncoding(enc string) string {
	switch {
	case identity(enc):
		return "identity"
	case isGzip(enc):
		return "gzip"
	}
	if t := cleanValue(enc); t != "" {
		return strings.ToLower(t)
	}
	return "identity"
}

// contentType returns the stored type, or octet-stream when there is none, or when it is not
// one well-formed media type. Fetch's "extract a MIME type" splits a Content-Type on commas and
// keeps the last valid one, so text/plain,application/pdf is a PDF to the browser while a check
// on its first type reads text/plain: served as stored it would escape the forced attachment.
// Any type the browser could read otherwise than mime.ParseMediaType does is not served.
func contentType(stored string) string {
	ct := cleanValue(stored)
	if ct == "" || strings.ContainsRune(ct, ',') {
		return octetStream
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return octetStream
	}
	// Fetch wants type/subtype; ParseMediaType takes a bare token too.
	if typ, sub, ok := strings.Cut(mt, "/"); !ok || typ == "" || sub == "" || strings.Contains(sub, "/") {
		return octetStream
	}
	return ct
}

// mediaType returns a Content-Type's type and subtype, lower case, without parameters.
func mediaType(ct string) string {
	t, _, _ := strings.Cut(ct, ";")
	return strings.ToLower(strings.TrimSpace(t))
}

// cleanValue returns v trimmed, or "" when it holds a control character. Writes refuse such a
// value (§17); this keeps one that slipped past from reaching a header, where Go would turn a
// CR or LF into a space and serve the rest.
func cleanValue(v string) string {
	for i := 0; i < len(v); i++ {
		if c := v[i]; c < 0x20 && c != '\t' || c == 0x7f {
			return ""
		}
	}
	return strings.TrimSpace(v)
}

// modifiedSince reports whether an object updated at t changed after the If-Modified-Since
// date ims, at the second HTTP dates have.
func modifiedSince(t time.Time, ims string) bool {
	d, err := http.ParseTime(ims)
	if err != nil || t.IsZero() {
		return true
	}
	return t.Truncate(time.Second).After(d)
}
