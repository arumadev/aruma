package wire

import (
	"errors"
	"net/url"
	"strings"
)

// ErrBadPath is a request path that does not name a bucket, or whose escaping is malformed.
var ErrBadPath = errors.New("wire: malformed object path")

// ObjectPath is what a request path names under an API's prefix (/v0, /storage/v1, ...).
type ObjectPath struct {
	Bucket  string // decoded
	Objects bool   // the path goes on to /o
	// Object is the object's name, decoded exactly once; empty when the path stops at /o.
	Object string
	// Rest is what follows the object, still escaped and without its leading slash, such as
	// GCS's rewriteTo/b/<bucket>/o/<object>. When the path does not go on to /o, it is what
	// follows the bucket instead (GCS's acl, iam, ...), so a surface can refuse it by name.
	Rest string
}

// SplitObjectPath splits an escaped path (URL.EscapedPath, after the API prefix) of the form
// b/<bucket>[/o[/<object>[/<rest>]]]. It splits on "/" before decoding, so an encoded %2F stays
// inside the name, then decodes each part once, and refuses malformed escapes (A §2.2).
//
// The object is one segment: every client escapes the "/" of a name (A §2.2, B §2.3), so a raw
// "/" after it starts Rest, which /v0 has none of. An empty object (b/<bucket>/o/, the web SDK's
// root reference) is the same as b/<bucket>/o, and an empty bucket is ErrBadPath. The path is
// decoded as a path, so "+" stays a plus (B §2.3).
func SplitObjectPath(escaped string) (ObjectPath, error) {
	rest, ok := strings.CutPrefix(escaped, "b/")
	if !ok {
		return ObjectPath{}, ErrBadPath
	}
	var p ObjectPath
	bucket, rest, more := strings.Cut(rest, "/")
	if bucket == "" {
		return ObjectPath{}, ErrBadPath
	}
	var err error
	if p.Bucket, err = url.PathUnescape(bucket); err != nil || p.Bucket == "" {
		return ObjectPath{}, ErrBadPath
	}
	if !more {
		return p, nil
	}
	o, after, more := strings.Cut(rest, "/")
	if o != "o" {
		// Not an object path: what follows the bucket is for the surface to judge.
		if !validEscapes(rest) {
			return ObjectPath{}, ErrBadPath
		}
		p.Rest = rest
		return p, nil
	}
	p.Objects = true
	if !more {
		return p, nil
	}
	object, tail, more := strings.Cut(after, "/")
	if p.Object, err = url.PathUnescape(object); err != nil {
		return ObjectPath{}, ErrBadPath
	}
	if more {
		if !validEscapes(tail) {
			return ObjectPath{}, ErrBadPath
		}
		p.Rest = tail
	}
	return p, nil
}

// validEscapes reports whether every % in s starts a well-formed escape, so Rest decodes later.
func validEscapes(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
			return false
		}
		i += 2
	}
	return true
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// CutFirebasePrefix removes either Firebase REST prefix from an escaped path, the plain /v0/ or
// the prefixed /firebasestorage.googleapis.com/v0/ (ADR-022 §1), and reports whether one was
// there.
func CutFirebasePrefix(escaped string) (string, bool) {
	if rest, ok := strings.CutPrefix(escaped, FirebasePrefix); ok {
		return rest, true
	}
	return strings.CutPrefix(escaped, PrefixedFirebasePrefix)
}

// FirebasePrefix and PrefixedFirebasePrefix are the two prefixes of Firebase REST: the plain one
// the unified download URL uses, and the one a runtime redirect of firebasestorage.googleapis.com
// produces (ADR-022 §1).
const (
	FirebasePrefix         = "/v0/"
	PrefixedFirebasePrefix = "/firebasestorage.googleapis.com/v0/"
)

// The GCS JSON API's prefixes (ADR-022 §1).
const (
	GCSPrefix         = "/storage/v1/"
	GCSUploadPrefix   = "/upload/storage/v1/"
	GCSDownloadPrefix = "/download/storage/v1/"
)

// EscapeComponent escapes s as JavaScript's encodeURIComponent does, which is how the web SDK
// and Node escape a name in a path or a query (A §2.2, B §2.3): every byte but the letters,
// digits and -_.!~*'() is percent-encoded, "/" and space included. Every client and
// SplitObjectPath read it back unchanged, in a path and in a query alike.
func EscapeComponent(s string) string {
	n := 0
	for i := 0; i < len(s); i++ {
		if !unreservedComponent(s[i]) {
			n++
		}
	}
	if n == 0 {
		return s
	}
	const hex = "0123456789ABCDEF"
	b := make([]byte, 0, len(s)+2*n)
	for i := 0; i < len(s); i++ {
		if c := s[i]; unreservedComponent(c) {
			b = append(b, c)
		} else {
			b = append(b, '%', hex[c>>4], hex[c&15])
		}
	}
	return string(b)
}

func unreservedComponent(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("-_.!~*'()", c) >= 0
}

// FirebaseUploadURL is the absolute session URL a /v0 start returns in X-Goog-Upload-URL, under
// base, the server's public URL without a trailing slash, never the Host header (ADR-022 §6.1):
// <base>/v0/b/<bucket>/o?name=<name>&upload_id=<id>&upload_protocol=resumable, the emulator's
// shape (raw_resumable.json).
func FirebaseUploadURL(base, bucket, name, uploadID string) string {
	return base + FirebasePrefix + "b/" + EscapeComponent(bucket) + "/o?name=" + EscapeComponent(name) +
		"&upload_id=" + EscapeComponent(uploadID) + "&upload_protocol=resumable"
}

// GCSUploadURL is the absolute session URL a GCS resumable start returns in Location, under
// base: <base>/upload/storage/v1/b/<bucket>/o?uploadType=resumable&name=<name>&upload_id=<id>
// (B §5.3). Its upload_id routes every later request to the session, whatever its method or
// uploadType (B §5.3, ADR-022 §6.2).
func GCSUploadURL(base, bucket, name, uploadID string) string {
	return base + GCSUploadPrefix + "b/" + EscapeComponent(bucket) + "/o?uploadType=resumable&name=" +
		EscapeComponent(name) + "&upload_id=" + EscapeComponent(uploadID)
}
