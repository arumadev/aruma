package wire

import (
	"bytes"
	"encoding/json"
)

// FirebaseObject is the object resource of Firebase REST (/v0), as the three client SDKs parse
// it (A §4). Numbers are decimal strings, times wire.Time, crc32c and md5Hash base64. Download
// tokens travel in DownloadTokens, comma separated, and never in Metadata (ADR-022 §9).
//
// The fields are in the order the emulator writes them (test/oracle/storage/emulator), so a
// recorded reply encodes back byte for byte; no client depends on the order.
type FirebaseObject struct {
	Name               string            `json:"name,omitempty"`
	Bucket             string            `json:"bucket,omitempty"`
	Generation         string            `json:"generation,omitempty"`
	Metageneration     string            `json:"metageneration,omitempty"`
	ContentType        string            `json:"contentType,omitempty"`
	TimeCreated        string            `json:"timeCreated,omitempty"`
	Updated            string            `json:"updated,omitempty"`
	StorageClass       string            `json:"storageClass,omitempty"`
	Size               string            `json:"size,omitempty"`
	MD5Hash            string            `json:"md5Hash,omitempty"`
	CRC32C             string            `json:"crc32c,omitempty"`
	ETag               string            `json:"etag,omitempty"`
	DownloadTokens     string            `json:"downloadTokens,omitempty"`
	ContentEncoding    string            `json:"contentEncoding,omitempty"`
	ContentDisposition string            `json:"contentDisposition,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	ContentLanguage    string            `json:"contentLanguage,omitempty"`
	CacheControl       string            `json:"cacheControl,omitempty"`
}

// FirebaseList is a /v0 list page. Prefixes and Items are present even when empty, and
// NextPageToken is absent on the last page: an empty one loops listAll forever on every
// platform (ADR-022 §13). Marshal writes a nil Prefixes or Items as [], never null.
type FirebaseList struct {
	NextPageToken string             `json:"nextPageToken,omitempty"`
	Prefixes      []string           `json:"prefixes"`
	Items         []FirebaseListItem `json:"items"`
}

// MarshalJSON writes the page with [] for an empty list, whatever the slice: Android would read
// a null prefixes as no array and iOS drops a page whose items is not a list (A §3.7).
func (l FirebaseList) MarshalJSON() ([]byte, error) {
	type page FirebaseList // without the method
	p := page(l)
	if p.Prefixes == nil {
		p.Prefixes = []string{}
	}
	if p.Items == nil {
		p.Items = []FirebaseListItem{}
	}
	return marshal(p)
}

// FirebaseListItem is one object of a /v0 list page: two strings, as iOS requires (A §3.7).
type FirebaseListItem struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
}

// FirebaseError is a /v0 error body: {"error":{"code":404,"message":"Not Found."}}.
type FirebaseError struct {
	Error FirebaseErrorBody `json:"error"`
}

// FirebaseErrorBody is the inside of a FirebaseError. Code is the HTTP status.
type FirebaseErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// NewFirebaseError returns the /v0 error body of a status. No client reads the message (A §5.1),
// so it is for people; the ones production writes are below.
func NewFirebaseError(status int, message string) FirebaseError {
	return FirebaseError{FirebaseErrorBody{Code: status, Message: message}}
}

// The messages production writes on /v0 (bucket_probe.json, E §1, PY-RR diagnostics).
const (
	MessageNotFound       = "Not Found."
	MessagePermission     = "Permission denied."
	MessageBadMethodOrURL = "Invalid HTTP method/URL pair."
)

// Marshal encodes a reply of either surface as compact JSON without a trailing newline and
// without HTML escaping, as Google and the emulator write it, so a recorded body encodes back
// byte for byte.
func Marshal(v any) ([]byte, error) { return marshal(v) }

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
