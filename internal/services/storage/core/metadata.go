package core

import (
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The writable metadata (ADR-022 §17, A §3.5): the five content fields, which become response
// headers, and the custom metadata, a map of strings. checkPatch bounds what a change brings,
// applyPatch applies it, and checkObjectMetadata bounds the result.

// contentFields are the keys of Patch.Fields.
var contentFields = []string{FieldContentType, FieldContentEncoding, FieldContentDisposition, FieldContentLanguage, FieldCacheControl}

// field returns a pointer to an object's content field by its name, nil for another name.
func field(o *Object, name string) *string {
	switch name {
	case FieldContentType:
		return &o.ContentType
	case FieldContentEncoding:
		return &o.ContentEncoding
	case FieldContentDisposition:
		return &o.ContentDisposition
	case FieldContentLanguage:
		return &o.ContentLanguage
	case FieldCacheControl:
		return &o.CacheControl
	}
	return nil
}

// checkPatch refuses a change core cannot store: a content field it does not know, a content
// field over header_field_bytes or holding a control character (CR, LF, anything below U+0020,
// and DEL: they become headers), and a custom metadata key that is empty or holds a control
// character, or a key or value with NUL (jsonb cannot hold it). The size of the custom metadata
// is checked on the result (checkObjectMetadata).
func (s *Service) checkPatch(p Patch) error {
	for name, v := range p.Fields {
		var o Object
		if field(&o, name) == nil {
			return invalid("The metadata field " + strconv.Quote(name) + " cannot be set.")
		}
		if v == nil {
			continue
		}
		if len(*v) > s.limits.HeaderFieldBytes {
			return invalid(fmt.Sprintf("The %s is longer than %d bytes.", name, s.limits.HeaderFieldBytes))
		}
		if !headerSafe(*v) {
			return invalid("The " + name + " may not contain control characters.")
		}
	}
	for k, v := range p.Metadata {
		if k == "" || !headerSafe(k) {
			return invalid("A custom metadata key must be non-empty, without control characters.")
		}
		if v != nil && (!utf8.ValidString(*v) || strings.IndexByte(*v, 0) >= 0) {
			return invalid("The custom metadata value of " + strconv.Quote(k) + " is not valid UTF-8 without NUL.")
		}
	}
	return nil
}

// headerSafe reports whether v is valid UTF-8 without a control character: nothing below U+0020,
// and no DEL.
func headerSafe(v string) bool {
	if !utf8.ValidString(v) {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] == 0x7f {
			return false
		}
	}
	return true
}

// applyPatch returns o with the change applied, as PATCH merges (A §3.5, B §4.3): a content
// field set, or deleted by nil; the custom metadata cleared first when ClearMetadata ("metadata":
// null), then merged key by key, a nil value deleting its key; the tokens replaced when Tokens is
// set. o is not modified.
func applyPatch(o Object, p Patch) Object {
	for name, v := range p.Fields {
		if f := field(&o, name); f != nil {
			*f = ""
			if v != nil {
				*f = *v
			}
		}
	}
	md := map[string]string{}
	if !p.ClearMetadata {
		maps.Copy(md, o.Metadata)
	}
	for k, v := range p.Metadata {
		if v == nil {
			delete(md, k)
		} else {
			md[k] = *v
		}
	}
	o.Metadata = md
	if p.Tokens != nil {
		o.DownloadTokens = append([]string(nil), *p.Tokens...)
	}
	return o
}

// metadataSize is what metadata_bytes bounds: the custom metadata's keys and values, and the
// download tokens as GCS holds them, under firebaseStorageDownloadTokens (ADR-022 §9, F §4.2).
func metadataSize(o Object) int {
	n := 0
	for k, v := range o.Metadata {
		n += len(k) + len(v)
	}
	if len(o.DownloadTokens) > 0 {
		n += len(TokensKey) + len(strings.Join(o.DownloadTokens, ","))
	}
	return n
}

// checkObjectMetadata refuses an object whose custom metadata and tokens are over
// metadata_bytes, or whose custom metadata holds the tokens' key (the row's CHECK refuses it
// too).
func (s *Service) checkObjectMetadata(o Object) error {
	if _, ok := o.Metadata[TokensKey]; ok {
		return invalid("The custom metadata may not hold " + TokensKey + ".")
	}
	if n := metadataSize(o); n > s.limits.MetadataBytes {
		return invalid(fmt.Sprintf("The custom metadata is %d bytes, over the limit of %d.", n, s.limits.MetadataBytes))
	}
	return nil
}

// surfacePatch is a change as core applies it from a surface: on Firebase REST the client's
// token key goes before anything sees the change (StripClientTokens, ADR-022 §9); on GCS the
// key is how an admin sets the tokens, so it becomes Patch.Tokens (B §7), and "metadata": null
// clears them with the rest of the custom metadata, as GCS keeps them there (ADR-023 §3-42).
// Tokens from any surface but GCS are ErrInvalidArgument: the surfaces never fill it.
func surfacePatch(p Patch, sf Surface) (Patch, error) {
	if p.Tokens != nil && sf != SurfaceGCS {
		return Patch{}, ErrInvalidArgument
	}
	if len(p.Metadata) > 0 {
		p.Metadata = maps.Clone(p.Metadata)
	}
	switch sf {
	case SurfaceGCS:
		v, set := p.Metadata[TokensKey]
		delete(p.Metadata, TokensKey)
		switch {
		case p.Tokens != nil:
		case set:
			tokens := splitTokens(v)
			p.Tokens = &tokens
		case p.ClearMetadata:
			p.Tokens = &[]string{}
		}
	default:
		StripClientTokens(p.Metadata)
	}
	return p, nil
}

// splitTokens reads firebaseStorageDownloadTokens as GCS holds it: a comma-separated list,
// spaces around each token ignored, empty entries dropped. nil (the key deleted) is no tokens.
func splitTokens(v *string) []string {
	tokens := []string{}
	if v == nil {
		return tokens
	}
	for t := range strings.SplitSeq(*v, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tokens = append(tokens, t)
		}
	}
	return tokens
}

// encodeMetadata returns the metadata column for custom metadata.
func encodeMetadata(md map[string]string) (json.RawMessage, error) {
	if md == nil {
		md = map[string]string{}
	}
	return json.Marshal(md)
}

// nullable returns a content field as its column holds it: NULL when unset.
func nullable(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
