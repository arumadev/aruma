package wire

import (
	"errors"
	"io"
)

// ErrBadMultipart is a multipart/related body that is not one JSON part and one media part, or
// whose JSON part is over its cap.
var ErrBadMultipart = errors.New("wire: malformed multipart/related body")

// RelatedReader is a multipart/related upload, read as a stream: the JSON part whole, then the
// media part as it arrives (ADR-022 §6.1). Nothing is held in memory but the JSON.
type RelatedReader struct {
	JSON      []byte    // the metadata part, at most maxJSON bytes
	MediaType string    // the media part's Content-Type, "" when it has none
	Media     io.Reader // the media part's bytes, streamed from the body
}

// NewRelatedReader reads the JSON part of a multipart/related body with the given Content-Type
// header, refusing one longer than maxJSON, and returns the reader positioned on the media
// part. It accepts a closing boundary without its trailing CRLF (A §3.9, §6.6). It never uses
// ParseMultipartForm.
func NewRelatedReader(body io.Reader, contentType string, maxJSON int64) (*RelatedReader, error) {
	panic("S2-B: NewRelatedReader")
}
