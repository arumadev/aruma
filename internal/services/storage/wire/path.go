package wire

import "errors"

// ErrBadPath is a request path that does not name a bucket, or whose escaping is malformed.
var ErrBadPath = errors.New("wire: malformed object path")

// ObjectPath is what a request path names under an API's prefix (/v0, /storage/v1, ...).
type ObjectPath struct {
	Bucket  string // decoded
	Objects bool   // the path goes on to /o
	// Object is the object's name, decoded exactly once; empty when the path stops at /o.
	Object string
	// Rest is what follows the object, still escaped and without its leading slash, such as
	// GCS's rewriteTo/b/<bucket>/o/<object>.
	Rest string
}

// SplitObjectPath splits an escaped path (URL.EscapedPath, after the API prefix) of the form
// b/<bucket>[/o[/<object>[/<rest>]]]. It splits on "/" before decoding, so an encoded %2F stays
// inside the name, then decodes each part once, and refuses malformed escapes (A §2.2).
func SplitObjectPath(escaped string) (ObjectPath, error) { panic("S2-B: SplitObjectPath") }

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
