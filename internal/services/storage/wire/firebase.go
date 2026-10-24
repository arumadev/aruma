package wire

// FirebaseObject is the object resource of Firebase REST (/v0), as the three client SDKs parse
// it (A §4). Numbers are decimal strings, times wire.Time, crc32c and md5Hash base64. Download
// tokens travel in DownloadTokens, comma separated, and never in Metadata (ADR-022 §9).
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
	ContentEncoding    string            `json:"contentEncoding,omitempty"`
	ContentDisposition string            `json:"contentDisposition,omitempty"`
	ContentLanguage    string            `json:"contentLanguage,omitempty"`
	CacheControl       string            `json:"cacheControl,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	DownloadTokens     string            `json:"downloadTokens,omitempty"`
}

// FirebaseList is a /v0 list page. Prefixes and Items are present even when empty, and
// NextPageToken is absent on the last page: an empty one loops listAll forever on every
// platform (ADR-022 §13).
type FirebaseList struct {
	Prefixes      []string           `json:"prefixes"`
	Items         []FirebaseListItem `json:"items"`
	NextPageToken string             `json:"nextPageToken,omitempty"`
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
