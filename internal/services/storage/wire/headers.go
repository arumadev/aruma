package wire

import "net/http"

// Header names in Google's spelling (A §6.2). The libraries measured do not care (P, condition
// 9); Set writes them as spelled here anyway, in case a client does.
const (
	// Firebase REST uploads (/v0, A §3.10-3.12).
	UploadProtocol            = "X-Goog-Upload-Protocol"
	UploadCommand             = "X-Goog-Upload-Command"
	UploadOffset              = "X-Goog-Upload-Offset"
	UploadURL                 = "X-Goog-Upload-URL"
	UploadStatus              = "X-Goog-Upload-Status"
	UploadSizeReceived        = "X-Goog-Upload-Size-Received"
	UploadHeaderContentLength = "X-Goog-Upload-Header-Content-Length" // the web SDK's declared size
	UploadHeaderContentType   = "X-Goog-Upload-Header-Content-Type"
	UploadContentLength       = "X-Goog-Upload-Content-Length" // iOS's declared size
	UploadContentType         = "X-Goog-Upload-Content-Type"

	// GCS JSON uploads (B §5).
	XUploadContentType     = "X-Upload-Content-Type"
	XUploadContentLength   = "X-Upload-Content-Length"
	GUploaderNo308         = "X-GUploader-No-308"
	HTTPStatusCodeOverride = "X-Http-Status-Code-Override"
	HTTPMethodOverride     = "X-HTTP-Method-Override" // Android's PUT that means PATCH

	// Media replies (ADR-022 §10.1).
	GoogHash                  = "x-goog-hash"
	GoogGeneration            = "x-goog-generation"
	GoogMetageneration        = "x-goog-metageneration"
	GoogStoredContentEncoding = "x-goog-stored-content-encoding"
	GoogStoredContentLength   = "x-goog-stored-content-length"
)

// The values of UploadStatus.
const (
	StatusActive    = "active"
	StatusFinal     = "final"
	StatusCancelled = "cancelled"
)

// Set sets a header under exactly the name given, without the canonical form http.Header.Set
// would give it.
func Set(h http.Header, name, value string) { h[name] = []string{value} }
