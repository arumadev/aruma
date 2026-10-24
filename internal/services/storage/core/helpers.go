package core

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// The helpers S2-C, S2-D and S2-E share. They work from the contract, as rules' position did
// from S1-0, so no task waits for another; changing one goes through the integrator.

// maxNameBytes is the longest object name, in bytes of UTF-8 (GCS, F §4.1).
const maxNameBytes = 1024

// acmePrefix is a prefix GCS refuses in object names.
const acmePrefix = ".well-known/acme-challenge/"

// validateName checks an object name (ADR-022 §17): 1 to 1024 bytes of valid UTF-8, without CR,
// LF or NUL (Postgres cannot keep NUL in text), not "." or "..", and not under
// .well-known/acme-challenge/. A name is otherwise opaque: no normalisation, no case folding.
func validateName(name string) error {
	switch {
	case name == "":
		return &InvalidError{Reason: "required", Message: "The object name is required."}
	case len(name) > maxNameBytes:
		return invalid("The object name is longer than 1024 bytes.")
	case !utf8.ValidString(name):
		return invalid("The object name is not valid UTF-8.")
	case strings.ContainsAny(name, "\r\n\x00"):
		return invalid("The object name may not contain carriage returns, line feeds or NUL.")
	case name == "." || name == "..":
		return invalid("The object name may not be . or ..")
	case strings.HasPrefix(name, acmePrefix):
		return invalid("The object name may not start with " + acmePrefix)
	}
	return nil
}

// segments returns an object name's path segments as the rules match them: the name split on
// "/", without decoding, empty segments kept. The rules path is b/<bucket>/o/ followed by them
// (ADR-022 §8).
func segments(name string) []string { return strings.Split(name, "/") }

// defaultContentType is the type of an object stored without one (A §7).
const defaultContentType = "application/octet-stream"

// applyDefaults fills what a new object gets when its upload did not set it: the content type
// on every surface, and on Firebase REST the stored Content-Disposition production stores
// (ADR-022 §10.4). Dashboard sessions upload over /v0 (ADR-022 §15), so they get it too. GCS
// stores no disposition, and contentEncoding is never stored by default.
func applyDefaults(o *Object, sf Surface) {
	if o.ContentType == "" {
		o.ContentType = defaultContentType
	}
	if o.ContentDisposition == "" && (sf == SurfaceFirebase || sf == SurfaceConsole) {
		o.ContentDisposition = defaultDisposition(o.Name)
	}
}

// defaultDisposition is the Content-Disposition production stores for an object created
// through Firebase REST: inline; filename*=utf-8”<the last segment, escaped>. It is the one
// escaping of it in the code base (wire has none).
//
// The escaping keeps RFC 5987's attr-char (letters, digits and !#$&+-.^_`|~) and percent-encodes
// every other byte in upper-case hex. The one case production shows (PY-RR storedObjects) is
// matched exactly: naïve 'q' 1+1%;x.txt stores as na%C3%AFve%20%27q%27%201+1%25%3Bx.txt, with +
// kept and ' encoded. Provisional beyond it (ADR-023 §3).
func defaultDisposition(name string) string {
	last := name[strings.LastIndexByte(name, '/')+1:]
	var b strings.Builder
	b.WriteString("inline; filename*=utf-8''")
	for i := 0; i < len(last); i++ {
		c := last[i]
		if attrChar(c) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// attrChar reports RFC 5987's attr-char.
func attrChar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}

// StripClientTokens removes firebaseStorageDownloadTokens from custom metadata a Firebase client
// sent, with a value or as null, before the rules see it and before it is stored: a client never
// plants or deletes a download token (ADR-022 §9).
func StripClientTokens(md map[string]*string) { delete(md, TokensKey) }

// newDownloadToken returns a fresh download token: a random (version 4) UUID.
func newDownloadToken() string { return uuid.NewString() }

// sealTokens seals an object's download tokens for its row, bound to the project and the name,
// so a sealed list copied to another object fails to open (ADR-022 §9). No tokens seal to nil,
// stored as NULL.
func (s *Service) sealTokens(project int64, name string, tokens []string) ([]byte, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	for _, t := range tokens {
		if t == "" || strings.Contains(t, ",") {
			return nil, invalid("A download token may not be empty or contain a comma.")
		}
	}
	return s.keys.Seal([]byte(strings.Join(tokens, ",")), tokensAD(project, name))
}

// openTokens opens what sealTokens sealed for the same project and name.
func (s *Service) openTokens(project int64, name string, sealed []byte) ([]string, error) {
	if sealed == nil {
		return nil, nil
	}
	b, err := s.keys.Open(sealed, tokensAD(project, name))
	if err != nil {
		return nil, fmt.Errorf("storage: open the download tokens of %q: %w", name, err)
	}
	return strings.Split(string(b), ","), nil
}

// tokensAD is the associated data of an object's sealed tokens. The project number comes first
// and has no "|", so no other (project, name) gives the same bytes.
func tokensAD(project int64, name string) []byte {
	return []byte("storage.objects|download_tokens|" + strconv.FormatInt(project, 10) + "|" + name)
}
