// Package media serves an object's bytes safely, for every surface: alt=media on /v0, media on
// GCS, and the dashboard's downloads (ADR-022 §10). The headers are one security decision, made
// once here: the stored type and never a sniffed one, nosniff, one sandboxing CSP, CORP
// cross-origin over the entry point's same-origin, Content-Disposition and Cache-Control by
// surface, Range on one range, and gzip decompression as a stream. It imports wire and Go's
// standard library; it takes what it serves as a value, not from core.
package media

import (
	"io"
	"net/http"
	"time"
)

// Item is what Serve needs to know of an object.
type Item struct {
	Name               string // the object's name; its last segment names a default disposition
	ContentType        string // as stored; "" serves application/octet-stream
	ContentEncoding    string // as stored; gzip may be decompressed
	ContentDisposition string // as stored; "" is attachment on download
	ContentLanguage    string
	CacheControl       string // as stored
	Size               int64
	ETag               string // the content's, wire.MediaETag: it follows the generation alone
	Updated            time.Time
	Generation         int64
	Metageneration     int64
	CRC32C             uint32
	MD5                [16]byte
}

// Mode is how a download was authorised, which decides Cache-Control and Content-Disposition
// (ADR-022 §10.4, §10.5).
type Mode uint8

const (
	// RulesDecided is a /v0 download the rules allowed, for a request with Authorization: a
	// stored public becomes private, and s-maxage goes.
	RulesDecided Mode = iota + 1
	// Token is a /v0 download by a matching download token: the link is the credential, and the
	// stored Cache-Control serves as is.
	Token
	// Console is the dashboard's download, on a session cookie: always attachment.
	Console
	// GCS is a media request on the GCS surface: the stored Cache-Control as is, and every
	// x-goog-* header.
	GCS
)

// Limits are what serving may spend.
type Limits struct {
	// DecompressedBytes caps what gzip decompression may produce (object_bytes); past it the
	// connection is cut, since the headers are gone.
	DecompressedBytes int64
	// MinBytesPerSecond is the slowest a client may read before the write deadline cuts it.
	MinBytesPerSecond int64
}

// Serve writes the object's bytes from body with the headers of its mode, or refuses the
// request (403 for service workers, and for scripts, styles, workers and xslt loaded from the
// dashboard's own origin, ADR-022 §10.1). It returns why serving stopped early, for the
// metrics: nil when the reply was complete.
//
// A complete reply includes a refusal, a 416 and a 304. An error means the client got less
// than the reply promised: the body ended or failed, the client stopped reading (the write
// deadline, below), the request's context ended, or gzip decompression passed
// lim.DecompressedBytes (ErrDecompressedTooLarge). The headers are gone by then, and a
// decompressed reply has no Content-Length, so ending the handler normally would close a
// chunked body as if it were whole. **The caller, once it has counted the error, ends the
// handler with panic(http.ErrAbortHandler)**, which cuts the connection (HTTP/1) or resets
// the stream (HTTP/2) on every surface alike.
//
// Serve lifts the server's write deadline itself, through http.ResponseController, a window
// at a time (pace.go): a client that reads fewer than lim.MinBytesPerSecond × 30 bytes in a
// 30 seconds window is cut. The caller sets nothing; on a writer without deadlines (a test
// recorder) the pacing is off.
//
// Serve reads body from its start, never all at once: memory is a few buffers of 32 KiB
// whatever the object's size or its decompressed size.
func Serve(w http.ResponseWriter, r *http.Request, it Item, body io.ReadSeeker, m Mode, lim Limits) error {
	return serve(w, r, it, body, m, lim)
}
