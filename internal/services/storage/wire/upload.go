package wire

import (
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
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

// commandNames are the commands as the three SDKs spell them (A §3.10-3.13), in bit order.
var commandNames = [...]string{"start", "upload", "query", "cancel", "finalize"}

// String writes the commands as a client would send them, in bit order: "upload, finalize".
func (s UploadCommands) String() string {
	var b strings.Builder
	for i, name := range commandNames {
		if s&(1<<i) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(", ")
		}
		b.WriteString(name)
	}
	return b.String()
}

// ErrBadCommand is an X-Goog-Upload-Command with an unknown or empty command.
var ErrBadCommand = errors.New("wire: malformed upload command")

// ParseUploadCommand parses an X-Goog-Upload-Command value. Each command is one of the five, in
// lower case as every SDK sends it, around optional spaces and tabs; an empty entry or an
// unknown word is ErrBadCommand (raw_resumable.json: "bogus"). A repeated command counts once.
// Which combinations make sense is the surface's to judge.
func ParseUploadCommand(v string) (UploadCommands, error) {
	var s UploadCommands
	for entry := range strings.SplitSeq(v, ",") {
		c := commandBit(strings.Trim(entry, " \t"))
		if c == 0 {
			return 0, ErrBadCommand
		}
		s |= c
	}
	return s, nil
}

func commandBit(name string) UploadCommands {
	if i := slices.Index(commandNames[:], name); i >= 0 {
		return 1 << i
	}
	return 0
}

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

// String writes r back in the form ParseContentRange reads.
func (r ContentRange) String() string {
	var b strings.Builder
	b.WriteString("bytes ")
	switch {
	case r.Start < 0:
		b.WriteByte('*')
	case r.Open:
		b.WriteString(strconv.FormatInt(r.Start, 10) + "-*")
	default:
		b.WriteString(strconv.FormatInt(r.Start, 10) + "-" + strconv.FormatInt(r.End, 10))
	}
	b.WriteByte('/')
	if r.Total < 0 {
		b.WriteByte('*')
	} else {
		b.WriteString(strconv.FormatInt(r.Total, 10))
	}
	return b.String()
}

// ErrBadContentRange is a Content-Range in none of the five forms, or with impossible bounds.
var ErrBadContentRange = errors.New("wire: malformed Content-Range")

// ParseContentRange parses a GCS chunk's Content-Range header: bytes a-b/*, bytes a-b/T,
// bytes */T, bytes N-*/*, bytes N-*/T and bytes */* (B §5.4-5.7, §10.2). The unit is "bytes" in
// any case and one space; the numbers are plain decimals that fit an int64. Impossible bounds
// are refused: b < a, b ≥ T, N > T, b = math.MaxInt64. One more form is read, the empty last
// chunk Node writes as bytes N-(N-1)/N (bytes 0--1/0 for an empty file): it is bytes */N.
func ParseContentRange(v string) (ContentRange, error) {
	unit, spec, ok := strings.Cut(v, " ")
	if !ok || !strings.EqualFold(unit, "bytes") {
		return ContentRange{}, ErrBadContentRange
	}
	span, total, ok := strings.Cut(spec, "/")
	if !ok {
		return ContentRange{}, ErrBadContentRange
	}
	r := ContentRange{Start: -1, End: -1, Total: -1}
	if total != "*" {
		t, err := ParseLength(total)
		if err != nil {
			return ContentRange{}, ErrBadContentRange
		}
		r.Total = t
	}
	if span == "*" {
		return r, nil
	}
	first, last, ok := strings.Cut(span, "-")
	if !ok {
		return ContentRange{}, ErrBadContentRange
	}
	start, err := ParseLength(first)
	if err != nil {
		return ContentRange{}, ErrBadContentRange
	}
	r.Start = start
	if last == "*" {
		r.Open = true
		if r.Total >= 0 && start > r.Total {
			return ContentRange{}, ErrBadContentRange
		}
		return r, nil
	}
	end := int64(-1)
	if last != "-1" {
		if end, err = ParseLength(last); err != nil {
			return ContentRange{}, ErrBadContentRange
		}
	}
	if end == start-1 && r.Total == start {
		// bytes N-(N-1)/N: an empty last chunk, the same as bytes */N. Node writes it when the
		// object ends on a chunk's boundary, bytes 0--1/0 for an empty file with chunkSize
		// (@google-cloud/storage 8.4.0 resumable-upload.js:619-623).
		return ContentRange{Start: -1, End: -1, Total: start}, nil
	}
	if end < start || end == math.MaxInt64 || (r.Total >= 0 && end >= r.Total) {
		return ContentRange{}, ErrBadContentRange
	}
	r.End = end
	return r, nil
}

// Length returns how many bytes the request's body carries by its header: End-Start+1, 0 when it
// carries none (bytes */T, bytes */*), and -1 when it is open ended and only its body tells.
// ParseContentRange never returns an End of math.MaxInt64, so the sum cannot overflow.
func (r ContentRange) Length() int64 {
	switch {
	case r.Start < 0:
		return 0
	case r.Open:
		return -1
	}
	return r.End - r.Start + 1
}

// ErrBadLength is a length or offset that is not a plain non-negative decimal fitting an int64.
var ErrBadLength = errors.New("wire: malformed length")

// ParseLength parses a byte count or offset as the upload headers carry it: X-Goog-Upload-Offset,
// the declared sizes, X-Upload-Content-Length and the numbers of a Content-Range. Only ASCII
// digits, at most 19 of them, and no more than math.MaxInt64; no sign, space or empty value.
func ParseLength(v string) (int64, error) {
	if v == "" || len(v) > 19 {
		return 0, ErrBadLength
	}
	var n int64
	for i := range len(v) {
		c := v[i]
		if c < '0' || c > '9' {
			return 0, ErrBadLength
		}
		if n > (math.MaxInt64-int64(c-'0'))/10 {
			return 0, ErrBadLength
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// DeclaredLength returns the size a Firebase client declared at start: the web SDK's
// X-Goog-Upload-Header-Content-Length or iOS's X-Goog-Upload-Content-Length, or -1 when it
// declared none (Android, A §3.10). A malformed value counts as none here; ParseDeclaredLength
// tells it apart.
func DeclaredLength(h http.Header) int64 {
	n, err := ParseDeclaredLength(h)
	if err != nil {
		return -1
	}
	return n
}

// ParseDeclaredLength is DeclaredLength with the malformed case kept: -1 and nil when no size is
// declared, ErrBadLength when one is but is not a length. The web SDK's header comes first.
func ParseDeclaredLength(h http.Header) (int64, error) {
	for _, name := range [...]string{UploadHeaderContentLength, UploadContentLength} {
		if v, ok := headerValue(h, name); ok {
			return ParseLength(strings.Trim(v, " \t"))
		}
	}
	return -1, nil
}

// DeclaredType returns the content type a Firebase client declared at start in its headers, or
// "" (the body's contentType comes first, ADR-022 §6.1): the web and Android SDKs'
// X-Goog-Upload-Header-Content-Type, then iOS's X-Goog-Upload-Content-Type.
func DeclaredType(h http.Header) string {
	for _, name := range [...]string{UploadHeaderContentType, UploadContentType} {
		if v, ok := headerValue(h, name); ok {
			if v = strings.Trim(v, " \t"); v != "" {
				return v
			}
		}
	}
	return ""
}

// headerValue returns a header's first value under its canonical name or as Google spells it,
// so a header map built by hand reads the same as one net/http parsed.
func headerValue(h http.Header, name string) (string, bool) {
	if vs := h[http.CanonicalHeaderKey(name)]; len(vs) > 0 {
		return vs[0], true
	}
	if vs := h[name]; len(vs) > 0 {
		return vs[0], true
	}
	return "", false
}

// SetUploadStatus writes the X-Goog-Upload-Status of a /v0 upload reply, and with it
// X-Goog-Upload-Size-Received when received is not negative: every reply on an active session
// carries both, since iOS fails a query without the size (ADR-022 §6.1).
func SetUploadStatus(h http.Header, status string, received int64) {
	Set(h, UploadStatus, status)
	if received >= 0 {
		Set(h, UploadSizeReceived, strconv.FormatInt(received, 10))
	}
}

// ReceivedRange returns the Range header of a GCS reply that is not final, bytes=0-<last byte
// kept>, and false when no byte is kept: then the header is left out (B §5.5-5.6).
func ReceivedRange(received int64) (string, bool) {
	if received <= 0 {
		return "", false
	}
	return "bytes=0-" + strconv.FormatInt(received-1, 10), true
}

// WriteIncomplete answers a GCS upload request that leaves the object unfinished. Go asks with
// X-GUploader-No-308: yes and reads only 200 with X-Http-Status-Code-Override: 308 (B §5.7);
// Node reads a real 308 (B §5.5). Both get Range when a byte is kept, neither gets Location (a
// fetch would follow it) nor a body.
func WriteIncomplete(w http.ResponseWriter, r *http.Request, received int64) {
	h := w.Header()
	if v, ok := ReceivedRange(received); ok {
		h.Set("Range", v)
	}
	h.Del("Location")
	if v, _ := headerValue(r.Header, GUploaderNo308); strings.EqualFold(strings.Trim(v, " \t"), "yes") {
		Set(h, HTTPStatusCodeOverride, "308")
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusPermanentRedirect)
}
