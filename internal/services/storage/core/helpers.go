package core

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/disposition"
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

// checkPre refuses preconditions outside the GCS surface (ErrInvalidArgument). Only GCS has
// them (ADR-022 §6.2), and a 412 keeps an upload session active: a /v0 or dashboard path that
// filled them would put Android and iOS in a loop of queries and retries (§6.1). StartUpload,
// Insert, Update and Delete check it before anything else.
func checkPre(sf Surface, pre Preconditions) error {
	if sf != SurfaceGCS && pre != (Preconditions{}) {
		return ErrInvalidArgument
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
// through Firebase REST: inline; filename*=utf-8”<the last segment, escaped>. The escaping is
// platform/disposition's, the one in the code base, which media's download default shares.
func defaultDisposition(name string) string {
	return "inline; " + disposition.Prefix + disposition.Escape(disposition.LastSegment(name))
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

// sealSessionTokens seals the download tokens an upload session holds (an admin's at start, then
// those of the object it committed), bound to the project, the name and the session's id hash.
func (s *Service) sealSessionTokens(project int64, name string, idHash []byte, tokens []string) ([]byte, error) {
	if len(tokens) == 0 {
		return nil, nil
	}
	for _, t := range tokens {
		if t == "" || strings.Contains(t, ",") {
			return nil, invalid("A download token may not be empty or contain a comma.")
		}
	}
	return s.keys.Seal([]byte(strings.Join(tokens, ",")), sessionTokensAD(project, name, idHash))
}

// openSessionTokens opens what sealSessionTokens sealed for the same session.
func (s *Service) openSessionTokens(project int64, name string, idHash []byte, sealed []byte) ([]string, error) {
	if sealed == nil {
		return nil, nil
	}
	b, err := s.keys.Open(sealed, sessionTokensAD(project, name, idHash))
	if err != nil {
		return nil, fmt.Errorf("storage: open an upload session's download tokens: %w", err)
	}
	return strings.Split(string(b), ","), nil
}

// sessionTokensAD is the associated data of a session's sealed tokens: its own table, so an
// object's sealed list never opens as a session's, and the id hash in hex, which has no "|".
func sessionTokensAD(project int64, name string, idHash []byte) []byte {
	return []byte("storage.upload_sessions|download_tokens|" + strconv.FormatInt(project, 10) + "|" +
		hex.EncodeToString(idHash) + "|" + name)
}

// sessionAttrs is how upload_sessions.attrs holds a session's incoming metadata. The tokens are
// never in it: Patch.Tokens goes to the session's sealed download_tokens.
type sessionAttrs struct {
	Fields        map[string]*string `json:"fields,omitempty"`
	Metadata      map[string]*string `json:"metadata,omitempty"`
	ClearMetadata bool               `json:"clearMetadata,omitempty"`
}

// encodeAttrs returns a session's attrs column for its incoming metadata, refusing the token key
// in the custom metadata (the row's CHECK refuses it too).
func encodeAttrs(p Patch) (json.RawMessage, error) {
	if _, ok := p.Metadata[TokensKey]; ok {
		return nil, invalid("The custom metadata may not hold " + TokensKey + ".")
	}
	return json.Marshal(sessionAttrs{Fields: p.Fields, Metadata: p.Metadata, ClearMetadata: p.ClearMetadata})
}

// decodeAttrs reads what encodeAttrs wrote, without the tokens, which the caller opens from the
// session's download_tokens.
func decodeAttrs(raw json.RawMessage) (Patch, error) {
	var a sessionAttrs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Patch{}, fmt.Errorf("storage: an upload session's attrs: %w", err)
	}
	return Patch{Fields: a.Fields, Metadata: a.Metadata, ClearMetadata: a.ClearMetadata}, nil
}

// objectSnapshot is a committed object as a final session keeps it, so a repeated finalize
// answers with what the first reply sent, even after the object was overwritten or deleted. Its
// download tokens are kept sealed in the session's download_tokens, never here.
type objectSnapshot struct {
	Bucket             string            `json:"bucket"`
	Name               string            `json:"name"`
	Generation         int64             `json:"generation"`
	Metageneration     int64             `json:"metageneration"`
	Size               int64             `json:"size"`
	MD5                []byte            `json:"md5"`
	CRC32C             uint32            `json:"crc32c"`
	ContentType        string            `json:"contentType,omitempty"`
	ContentEncoding    string            `json:"contentEncoding,omitempty"`
	ContentDisposition string            `json:"contentDisposition,omitempty"`
	ContentLanguage    string            `json:"contentLanguage,omitempty"`
	CacheControl       string            `json:"cacheControl,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	Created            time.Time         `json:"created"`
	Updated            time.Time         `json:"updated"`
}

// encodeSnapshot returns a session's result_object for the object it committed.
func encodeSnapshot(o Object) (json.RawMessage, error) {
	return json.Marshal(objectSnapshot{
		Bucket: o.Bucket, Name: o.Name, Generation: o.Generation, Metageneration: o.Metageneration, Size: o.Size,
		MD5: o.MD5[:], CRC32C: o.CRC32C, ContentType: o.ContentType, ContentEncoding: o.ContentEncoding,
		ContentDisposition: o.ContentDisposition, ContentLanguage: o.ContentLanguage, CacheControl: o.CacheControl,
		Metadata: o.Metadata, Created: o.Created, Updated: o.Updated,
	})
}

// decodeSnapshot reads what encodeSnapshot wrote; the caller adds the tokens it opens.
func decodeSnapshot(raw json.RawMessage) (Object, error) {
	var v objectSnapshot
	if err := json.Unmarshal(raw, &v); err != nil || len(v.MD5) != 16 {
		return Object{}, fmt.Errorf("storage: an upload session's result: %w", err)
	}
	o := Object{
		Bucket: v.Bucket, Name: v.Name, Generation: v.Generation, Metageneration: v.Metageneration, Size: v.Size,
		CRC32C: v.CRC32C, ContentType: v.ContentType, ContentEncoding: v.ContentEncoding,
		ContentDisposition: v.ContentDisposition, ContentLanguage: v.ContentLanguage, CacheControl: v.CacheControl,
		Metadata: v.Metadata, Created: v.Created, Updated: v.Updated,
	}
	copy(o.MD5[:], v.MD5)
	return o, nil
}

// tokensAD is the associated data of an object's sealed tokens. The project number comes first
// and has no "|", so no other (project, name) gives the same bytes.
func tokensAD(project int64, name string) []byte {
	return []byte("storage.objects|download_tokens|" + strconv.FormatInt(project, 10) + "|" + name)
}
