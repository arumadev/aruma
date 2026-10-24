package wire

import (
	"errors"
	"net/http"
)

// UploadCommands are the commands of an X-Goog-Upload-Command header, a list separated by commas
// and spaces ("upload, finalize").
type UploadCommands uint8

const (
	CommandStart UploadCommands = 1 << iota
	CommandUpload
	CommandQuery
	CommandCancel
	CommandFinalize
)

// Has reports whether every command in c is in s.
func (s UploadCommands) Has(c UploadCommands) bool { return s&c == c }

// ErrBadCommand is an X-Goog-Upload-Command with an unknown or empty command.
var ErrBadCommand = errors.New("wire: malformed upload command")

// ParseUploadCommand parses an X-Goog-Upload-Command value.
func ParseUploadCommand(v string) (UploadCommands, error) { panic("S2-B: ParseUploadCommand") }

// ContentRange is a GCS chunk's Content-Range, in its five forms (B §10.2-5).
type ContentRange struct {
	// Start is the first byte the body carries; -1 when the request carries none (bytes */T
	// and bytes */*).
	Start int64
	// End is the last byte the body carries, inclusive; -1 when Open or when there is no body.
	End int64
	// Open is bytes N-*: the rest of the object comes in this request and ends with its body,
	// even an empty one (Node's save(), B §5.4).
	Open bool
	// Total is the object's size, -1 when the header says * (more chunks may follow).
	Total int64
}

// Query reports bytes */*: a status query without a body.
func (r ContentRange) Query() bool { return r.Start < 0 && r.Total < 0 }

// Final reports whether this request completes the object: it gives the total, or it is open
// ended (bytes N-*), and so closes the object with its body.
func (r ContentRange) Final() bool { return r.Total >= 0 || r.Open }

// ErrBadContentRange is a Content-Range in none of the five forms, or with impossible bounds.
var ErrBadContentRange = errors.New("wire: malformed Content-Range")

// ParseContentRange parses a GCS chunk's Content-Range header.
func ParseContentRange(v string) (ContentRange, error) { panic("S2-B: ParseContentRange") }

// DeclaredLength returns the size a Firebase client declared at start: the web SDK's
// X-Goog-Upload-Header-Content-Length or iOS's X-Goog-Upload-Content-Length, or -1 when it
// declared none (Android, A §3.10).
func DeclaredLength(h http.Header) int64 { panic("S2-B: DeclaredLength") }

// DeclaredType returns the content type a Firebase client declared at start in its headers, or
// "" (the body's contentType comes first, ADR-022 §6.1).
func DeclaredType(h http.Header) string { panic("S2-B: DeclaredType") }
