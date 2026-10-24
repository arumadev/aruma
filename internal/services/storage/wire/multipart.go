package wire

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strings"
)

// ErrBadMultipart is a multipart/related body that is not one JSON part and one media part, or
// whose JSON part is over its cap.
var ErrBadMultipart = errors.New("wire: malformed multipart/related body")

// ErrBadContentType is the ErrBadMultipart of a request whose Content-Type is not
// multipart/related with a valid boundary: the body is not read at all. The surfaces answer it
// "Bad content type." (CT, firebase.endpoints.test.ts:144-152, gcs.endpoints.test.ts:320-330).
var ErrBadContentType = fmt.Errorf("%w: Content-Type is not multipart/related with a boundary", ErrBadMultipart)

// RelatedReader is a multipart/related upload, read as a stream: the JSON part whole, then the
// media part as it arrives (ADR-022 §6.1). Nothing is held in memory but the JSON.
type RelatedReader struct {
	JSON      []byte    // the metadata part, at most maxJSON bytes
	MediaType string    // the media part's Content-Type, "" when it has none
	Media     io.Reader // the media part's bytes, streamed from the body
}

// headerBudget bounds what the body may make the parser read outside a part's content: the
// preamble, a boundary line and a part's headers. With the reader's look-ahead it caps the
// memory a hostile body can make the headers take, far below mime/multipart's own 10 MiB.
const headerBudget = 16 << 10

// maxBoundary is RFC 2046's limit on a boundary's length (§5.1.1).
const maxBoundary = 70

// NewRelatedReader reads the JSON part of a multipart/related body with the given Content-Type
// header, refusing one longer than maxJSON, and returns the reader positioned on the media
// part. It accepts a closing boundary without its trailing CRLF (A §3.9, §6.6). It never uses
// ParseMultipartForm.
//
// The body must be exactly two parts: the first application/json, the second the media, each
// with no transfer encoding but 7bit, 8bit or binary. Media reads ErrBadMultipart when its
// body is cut before the closing boundary or a third part follows, so a truncated or padded
// upload never passes for a whole one. An error of body itself (a size cap, a broken
// connection) comes through as it is, from here or from Media.
func NewRelatedReader(body io.Reader, contentType string, maxJSON int64) (*RelatedReader, error) {
	boundary, err := relatedBoundary(contentType)
	if err != nil {
		return nil, err
	}
	maxJSON = min(max(maxJSON, 0), 1<<40) // so the budgets below never overflow
	src := &budgetReader{r: body, left: headerBudget}
	mr := multipart.NewReader(src, boundary)

	meta, err := mr.NextRawPart()
	if err != nil {
		return nil, src.fail(err)
	}
	if err := plainPart(meta); err != nil {
		return nil, err
	}
	if t, _, err := mime.ParseMediaType(meta.Header.Get("Content-Type")); err != nil || t != "application/json" {
		return nil, fmt.Errorf("%w: the first part is not application/json", ErrBadMultipart)
	}
	src.left = maxJSON + 1 + headerBudget
	js, err := io.ReadAll(io.LimitReader(meta, maxJSON+1))
	if err != nil {
		return nil, src.fail(err)
	}
	if int64(len(js)) > maxJSON {
		return nil, fmt.Errorf("%w: the JSON part is over %d bytes", ErrBadMultipart, maxJSON)
	}

	media, err := mr.NextRawPart()
	if err != nil {
		// == and not errors.Is: NextRawPart returns a bare io.EOF at the closing boundary
		// alone, and one wrapped in its own error when the body ends mid-line.
		if err == io.EOF {
			return nil, fmt.Errorf("%w: no media part", ErrBadMultipart)
		}
		return nil, src.fail(err)
	}
	if err := plainPart(media); err != nil {
		return nil, err
	}
	src.left = -1 // the media streams unbounded; its caller caps it
	return &RelatedReader{
		JSON:      js,
		MediaType: strings.Trim(media.Header.Get("Content-Type"), " \t"),
		Media:     &mediaReader{part: media, mr: mr, src: src},
	}, nil
}

// relatedBoundary returns the boundary of a multipart/related Content-Type: present, at most 70
// characters of RFC 2046's bchars, and not ending in a space.
func relatedBoundary(contentType string) (string, error) {
	t, params, err := mime.ParseMediaType(contentType)
	if err != nil || t != "multipart/related" {
		return "", ErrBadContentType
	}
	b := params["boundary"]
	if b == "" || len(b) > maxBoundary || b[len(b)-1] == ' ' {
		return "", fmt.Errorf("%w: bad boundary", ErrBadContentType)
	}
	for i := range len(b) {
		if !bchar(b[i]) {
			return "", fmt.Errorf("%w: bad boundary", ErrBadContentType)
		}
	}
	return b, nil
}

// bchar reports RFC 2046's bchars: letters, digits, '()+_,-./:=? and space.
func bchar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("'()+_,-./:=? ", c) >= 0
}

// plainPart refuses a part whose bytes are encoded: they would not be what is stored.
func plainPart(p *multipart.Part) error {
	switch strings.ToLower(strings.Trim(p.Header.Get("Content-Transfer-Encoding"), " \t")) {
	case "", "7bit", "8bit", "binary":
		return nil
	}
	return fmt.Errorf("%w: a part has a transfer encoding", ErrBadMultipart)
}

// mediaReader streams the media part and, at its end, checks that the closing boundary follows.
type mediaReader struct {
	part *multipart.Part
	mr   *multipart.Reader
	src  *budgetReader
	err  error
}

func (m *mediaReader) Read(p []byte) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	n, err := m.part.Read(p)
	switch {
	case err == nil:
		return n, nil
	case errors.Is(err, io.EOF):
		m.err = m.end()
	case m.src.err != nil:
		m.err = m.src.err
	default: // io.ErrUnexpectedEOF: the body ended inside the part
		m.err = fmt.Errorf("%w: the body ends inside the media part", ErrBadMultipart)
	}
	return n, m.err
}

// end checks what follows the media part: the closing boundary, and no third part.
func (m *mediaReader) end() error {
	m.src.left = headerBudget // a third part's headers are read under the budget again
	next, err := m.mr.NextRawPart()
	switch {
	case err == io.EOF: // bare at the closing boundary; wrapped when the body is cut
		return io.EOF
	case err != nil:
		if m.src.err != nil {
			return m.src.err
		}
		return fmt.Errorf("%w: no closing boundary", ErrBadMultipart)
	default:
		_ = next
		return fmt.Errorf("%w: more than two parts", ErrBadMultipart)
	}
}

// errOverBudget is what budgetReader reads once its budget is spent.
var errOverBudget = errors.New("wire: multipart headers over budget")

// budgetReader caps how many bytes may be read from the body while left is not negative, and
// keeps the body's own error, so it reaches the caller unwrapped.
type budgetReader struct {
	r    io.Reader
	left int64 // < 0: no cap
	err  error // the body's own error, other than io.EOF
}

func (b *budgetReader) Read(p []byte) (int, error) {
	if b.left == 0 {
		return 0, errOverBudget
	}
	if b.left > 0 && int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	if b.left > 0 {
		b.left -= int64(n)
	}
	if err != nil && !errors.Is(err, io.EOF) && b.err == nil {
		b.err = err
	}
	return n, err
}

// fail turns an error of the multipart reader into the caller's: the body's own, or
// ErrBadMultipart.
func (b *budgetReader) fail(err error) error {
	if b.err != nil {
		return b.err
	}
	return fmt.Errorf("%w: %v", ErrBadMultipart, err)
}
