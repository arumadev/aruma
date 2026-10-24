package wire

import "strconv"

// GCSObject is the object resource of the GCS JSON API, as the Node and Go Admin libraries parse
// it (B §7). Numbers are decimal strings (Go's ,string tags refuse numbers, B §1.7), and the
// download tokens show as metadata.firebaseStorageDownloadTokens (ADR-022 §9).
type GCSObject struct {
	Kind                    string            `json:"kind"` // storage#object
	ID                      string            `json:"id,omitempty"`
	SelfLink                string            `json:"selfLink,omitempty"`
	MediaLink               string            `json:"mediaLink,omitempty"`
	Name                    string            `json:"name"`
	Bucket                  string            `json:"bucket"`
	Generation              string            `json:"generation,omitempty"`
	Metageneration          string            `json:"metageneration,omitempty"`
	ContentType             string            `json:"contentType,omitempty"`
	StorageClass            string            `json:"storageClass,omitempty"`
	Size                    string            `json:"size,omitempty"`
	MD5Hash                 string            `json:"md5Hash,omitempty"`
	CRC32C                  string            `json:"crc32c,omitempty"`
	ETag                    string            `json:"etag,omitempty"`
	ContentEncoding         string            `json:"contentEncoding,omitempty"`
	ContentDisposition      string            `json:"contentDisposition,omitempty"`
	ContentLanguage         string            `json:"contentLanguage,omitempty"`
	CacheControl            string            `json:"cacheControl,omitempty"`
	TimeCreated             string            `json:"timeCreated,omitempty"`
	Updated                 string            `json:"updated,omitempty"`
	TimeStorageClassUpdated string            `json:"timeStorageClassUpdated,omitempty"`
	TimeFinalized           string            `json:"timeFinalized,omitempty"`
	Metadata                map[string]string `json:"metadata,omitempty"`
}

// The kinds of the GCS resources.
const (
	KindObject  = "storage#object"
	KindObjects = "storage#objects"
	KindBucket  = "storage#bucket"
	KindRewrite = "storage#rewriteResponse"
)

// GCSObjects is an objects.list page. An empty page is {"kind":"storage#objects"}, as GCS and
// the emulator write it (cleanup.json).
type GCSObjects struct {
	Kind          string      `json:"kind"` // storage#objects
	NextPageToken string      `json:"nextPageToken,omitempty"`
	Prefixes      []string    `json:"prefixes,omitempty"`
	Items         []GCSObject `json:"items,omitempty"`
}

// GCSBucket is the bucket resource of buckets.get and buckets.patch (B §4.6), its fields in
// production's order (bucket_probe.json).
type GCSBucket struct {
	Kind           string    `json:"kind"` // storage#bucket
	SelfLink       string    `json:"selfLink,omitempty"`
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	ProjectNumber  string    `json:"projectNumber"`
	Generation     string    `json:"generation,omitempty"`
	Metageneration string    `json:"metageneration"`
	Location       string    `json:"location"`
	StorageClass   string    `json:"storageClass"`
	ETag           string    `json:"etag,omitempty"`
	TimeCreated    string    `json:"timeCreated,omitempty"`
	Updated        string    `json:"updated,omitempty"`
	LocationType   string    `json:"locationType,omitempty"`
	CORS           []GCSCORS `json:"cors,omitempty"`
}

// GCSCORS is one of a bucket's cors[] rules (ADR-022 §11).
type GCSCORS struct {
	Origin         []string `json:"origin,omitempty"`
	Method         []string `json:"method,omitempty"`
	ResponseHeader []string `json:"responseHeader,omitempty"`
	MaxAgeSeconds  int      `json:"maxAgeSeconds,omitempty"`
}

// GCSRewrite is a rewriteTo reply. Aruma rewrites in one request, so Done is always true and no
// rewriteToken is ever sent (ADR-022 §14).
type GCSRewrite struct {
	Kind                string     `json:"kind"` // storage#rewriteResponse
	TotalBytesRewritten string     `json:"totalBytesRewritten"`
	ObjectSize          string     `json:"objectSize"`
	Done                bool       `json:"done"`
	Resource            *GCSObject `json:"resource,omitempty"`
}

// GCSError is a GCS error body. Code must be a number equal to the HTTP status, or Node's
// exists() does not know a 404 (B §8).
type GCSError struct {
	Error GCSErrorBody `json:"error"`
}

// GCSErrorBody is the inside of a GCSError.
type GCSErrorBody struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Errors  []GCSErrorItem `json:"errors"`
}

// GCSErrorItem is one entry of errors[]: domain "global" and a reason such as notFound,
// conditionNotMet, invalid, forbidden, required or quotaExceeded.
type GCSErrorItem struct {
	Domain  string `json:"domain"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// The reasons of errors[] (B §8).
const (
	ReasonNotFound        = "notFound"
	ReasonConditionNotMet = "conditionNotMet"
	ReasonInvalid         = "invalid"
	ReasonForbidden       = "forbidden"
	ReasonRequired        = "required"
	ReasonQuotaExceeded   = "quotaExceeded"
	ReasonBackendError    = "backendError"
	ReasonNotImplemented  = "notImplemented"
)

// NewGCSError returns a GCS error body with its one errors[] entry, in the global domain.
func NewGCSError(status int, reason, message string) GCSError {
	return GCSError{GCSErrorBody{Code: status, Message: message,
		Errors: []GCSErrorItem{{Domain: "global", Reason: reason, Message: message}}}}
}

// MessageNoSuchBucket is production's message for a bucket that does not exist
// (bucket_probe.json).
const MessageNoSuchBucket = "The specified bucket does not exist."

// MessageNoSuchObject is GCS's message for an object that does not exist: No such object:
// <bucket>/<name> (B §8, admin_go.json).
func MessageNoSuchObject(bucket, name string) string {
	return "No such object: " + bucket + "/" + name
}

// GCSObjectID is an object's id, <bucket>/<name>/<generation> (B §7).
func GCSObjectID(bucket, name string, generation int64) string {
	return bucket + "/" + name + "/" + strconv.FormatInt(generation, 10)
}

// GCSSelfLink is an object's selfLink under base, the server's public URL without a trailing
// slash: <base>/storage/v1/b/<bucket>/o/<name escaped>.
func GCSSelfLink(base, bucket, name string) string {
	return base + GCSPrefix + "b/" + EscapeComponent(bucket) + "/o/" + EscapeComponent(name)
}

// GCSMediaLink is an object's mediaLink under base:
// <base>/download/storage/v1/b/<bucket>/o/<name escaped>?generation=<g>&alt=media.
func GCSMediaLink(base, bucket, name string, generation int64) string {
	return base + GCSDownloadPrefix + "b/" + EscapeComponent(bucket) + "/o/" + EscapeComponent(name) +
		"?generation=" + strconv.FormatInt(generation, 10) + "&alt=media"
}

// GCSBucketSelfLink is a bucket's selfLink under base.
func GCSBucketSelfLink(base, bucket string) string {
	return base + GCSPrefix + "b/" + EscapeComponent(bucket)
}
