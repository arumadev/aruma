package core

import (
	"context"
	"crypto/subtle"
	"maps"
	"slices"
	"strings"
)

// Download tokens (ADR-022 §9). The server alone makes them: at a /v0 upload (S2-D), at an
// authorized /v0 metadata read of an object without one (Stat), and through create_token. They
// are sealed in the row (sealTokens), shown on GCS under metadata.firebaseStorageDownloadTokens,
// where an admin replaces or removes them, and on /v0 in downloadTokens alone. A token is never
// logged.

// CreateToken adds a download token to an object (?create_token=true on /v0): admins only.
func (s *Service) CreateToken(ctx context.Context, b Bucket, name string, p Principal) (Object, error) {
	if p.Kind != PrincipalAdmin {
		return Object{}, ErrPermissionDenied
	}
	if err := validateName(name); err != nil {
		return Object{}, err
	}
	return s.change(ctx, b, name, func(cur *Object) (Object, error) {
		if cur == nil {
			return Object{}, ErrNotFound
		}
		next := *cur
		next.DownloadTokens = append(slices.Clone(cur.DownloadTokens), newDownloadToken())
		if err := s.checkObjectMetadata(next); err != nil {
			return Object{}, err
		}
		return next, nil
	})
}

// DeleteToken removes one of an object's download tokens (?delete_token= on /v0): admins only.
// Removing the last one makes a new one at once (E §5). A token the object does not have
// changes nothing but the metageneration, as the emulator answers (tokens.json; ADR-023 §3-40).
func (s *Service) DeleteToken(ctx context.Context, b Bucket, name string, p Principal, token string) (Object, error) {
	if p.Kind != PrincipalAdmin {
		return Object{}, ErrPermissionDenied
	}
	if err := validateName(name); err != nil {
		return Object{}, err
	}
	if token == "" {
		return Object{}, &InvalidError{Reason: "required", Message: "The download token to delete is required."}
	}
	return s.change(ctx, b, name, func(cur *Object) (Object, error) {
		if cur == nil {
			return Object{}, ErrNotFound
		}
		next := *cur
		next.DownloadTokens = slices.DeleteFunc(slices.Clone(cur.DownloadTokens), func(t string) bool { return t == token })
		if len(next.DownloadTokens) == 0 {
			next.DownloadTokens = []string{newDownloadToken()}
		}
		return next, nil
	})
}

// matchToken reports whether token is one of tokens, comparing each in constant time and all of
// them whatever matches, so the time taken says nothing of which token or how much of it matched.
func matchToken(tokens []string, token string) bool {
	match := 0
	for _, t := range tokens {
		match |= subtle.ConstantTimeCompare([]byte(t), []byte(token))
	}
	return match == 1
}

// GCSMetadata returns an object's custom metadata as the GCS surface shows it: its tokens under
// firebaseStorageDownloadTokens, comma-separated, as GCS holds them in production (Pyric
// afterSet, E §5). nil when there is neither. The object is not modified.
func GCSMetadata(o Object) map[string]string {
	if len(o.Metadata) == 0 && len(o.DownloadTokens) == 0 {
		return nil
	}
	md := maps.Clone(o.Metadata)
	if md == nil {
		md = map[string]string{}
	}
	if len(o.DownloadTokens) > 0 {
		md[TokensKey] = strings.Join(o.DownloadTokens, ",")
	}
	return md
}

// FirebaseTokens returns /v0's downloadTokens field: the tokens comma-separated, "" when there
// are none, which the field then omits (A §4). The custom metadata /v0 shows is Object.Metadata
// as it is: it never holds the tokens' key (Pyric metadataHasTokenKey: false, E §5).
func FirebaseTokens(o Object) string { return strings.Join(o.DownloadTokens, ",") }
