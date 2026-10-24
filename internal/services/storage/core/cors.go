package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// A bucket's CORS rules (ADR-022 §11): GCS's cors[], stored in storage.buckets.cors with GCS's
// field names. They govern alt=media alone, and none are set by default, as a new Firebase
// bucket: a page on another origin cannot read an object's bytes until the owner allows it.

// corsJSON is a rule as storage.buckets.cors holds it, and as GCS names its fields.
type corsJSON struct {
	Origin         []string `json:"origin,omitempty"`
	Method         []string `json:"method,omitempty"`
	ResponseHeader []string `json:"responseHeader,omitempty"`
	MaxAgeSeconds  int      `json:"maxAgeSeconds,omitempty"`
}

// CORS returns a bucket's CORS rules; none until they are set (ADR-022 §11).
func (s *Service) CORS(ctx context.Context, b Bucket) ([]CORSRule, error) {
	row, err := s.reads.GetBucketSettings(ctx, b.Project)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read the bucket's settings: %w", ErrUnavailable, err)
	}
	return decodeCORS(row.Cors)
}

// BucketInfo returns the bucket resource: its settings and their metageneration.
func (s *Service) BucketInfo(ctx context.Context, b Bucket) (BucketInfo, error) {
	created, err := s.bucketCreated(ctx, b)
	if err != nil {
		return BucketInfo{}, err
	}
	row, err := s.reads.GetBucketSettings(ctx, b.Project)
	if errors.Is(err, pgx.ErrNoRows) {
		return BucketInfo{Bucket: b, Metageneration: 1, Created: created, Updated: created}, nil
	}
	if err != nil {
		return BucketInfo{}, fmt.Errorf("%w: read the bucket's settings: %w", ErrUnavailable, err)
	}
	return bucketInfo(b, created, row)
}

// SetCORS replaces a bucket's CORS rules, advancing its metageneration: admins only (the GCS
// surface and the dashboard). It returns the bucket as changed, for buckets.patch. An empty
// list removes every rule.
func (s *Service) SetCORS(ctx context.Context, b Bucket, p Principal, rules []CORSRule) (BucketInfo, error) {
	if p.Kind != PrincipalAdmin {
		return BucketInfo{}, ErrPermissionDenied
	}
	leave, err := s.enter()
	if err != nil {
		return BucketInfo{}, err
	}
	defer leave()
	enc, err := s.encodeCORS(rules)
	if err != nil {
		return BucketInfo{}, err
	}
	created, err := s.bucketCreated(ctx, b)
	if err != nil {
		return BucketInfo{}, err
	}
	var row queries.StorageBucket
	err = s.inTx(ctx, func(q *queries.Queries) error {
		var err error
		row, err = q.SetBucketCORS(ctx, queries.SetBucketCORSParams{ProjectNumber: b.Project, Cors: enc, Now: s.now()})
		return err
	})
	switch {
	case errors.Is(err, ErrUnavailable):
		return BucketInfo{}, err
	case err != nil:
		return BucketInfo{}, fmt.Errorf("%w: set the bucket's CORS: %w", ErrUnavailable, err)
	}
	return bucketInfo(b, created, row)
}

// MatchCORS reports whether a cross-origin alt=media request from origin with method is allowed
// by the rules, and by which rule. Both surfaces call it for media.
//
// A rule matches when one of its origins is the request's origin, byte for byte, or "*", and one
// of its methods is the request's method (for a preflight, Access-Control-Request-Method), byte
// for byte, or "*": "'*' is permitted in the list of methods, and means 'any method'" (GCS
// json_api/v1/buckets, cors[].method[]). The first rule that matches decides, as in GCS.
// Patterns within an origin are not matched: provisional (ADR-023 §3-44), never wider than GCS.
func (s *Service) MatchCORS(rules []CORSRule, origin, method string) (allow bool, rule CORSRule) {
	if origin == "" || method == "" {
		return false, CORSRule{}
	}
	for _, r := range rules {
		if containsOrStar(r.Origins, origin) && containsOrStar(r.Methods, method) {
			return true, r
		}
	}
	return false, CORSRule{}
}

func containsOrStar(list []string, v string) bool {
	for _, x := range list {
		if x == v || x == "*" {
			return true
		}
	}
	return false
}

// bucketCreated is the bucket's creation time: its project's.
func (s *Service) bucketCreated(ctx context.Context, b Bucket) (time.Time, error) {
	p, err := s.projects.ProjectForBucket(ctx, b.Name)
	switch {
	case apierr.CodeOf(err) == apierr.NotFound:
		return time.Time{}, ErrBucketNotFound
	case err != nil:
		return time.Time{}, fmt.Errorf("%w: resolve bucket: %w", ErrUnavailable, err)
	case p.Number != b.Project || p.State != "ACTIVE":
		return time.Time{}, ErrBucketNotFound
	}
	return p.CreatedAt, nil
}

func bucketInfo(b Bucket, created time.Time, row queries.StorageBucket) (BucketInfo, error) {
	rules, err := decodeCORS(row.Cors)
	if err != nil {
		return BucketInfo{}, err
	}
	return BucketInfo{Bucket: b, Metageneration: row.Metageneration, Created: created, Updated: row.Updated, CORS: rules}, nil
}

func decodeCORS(raw json.RawMessage) ([]CORSRule, error) {
	var v []corsJSON
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%w: the bucket's CORS: %w", ErrUnavailable, err)
	}
	if len(v) == 0 {
		return nil, nil
	}
	rules := make([]CORSRule, len(v))
	for i, r := range v {
		rules[i] = CORSRule{Origins: r.Origin, Methods: r.Method, ResponseHeaders: r.ResponseHeader, MaxAgeSeconds: r.MaxAgeSeconds}
	}
	return rules, nil
}

// encodeCORS checks rules and returns them as the cors column holds them. Their values become
// response headers (Access-Control-Allow-Origin, -Allow-Methods, -Expose-Headers, -Max-Age): an
// origin is "*" or a value without control characters, spaces or commas; a method and a
// response header are HTTP tokens; maxAgeSeconds is not negative. The whole is at most
// json_body_bytes, which bounds a buckets.patch body anyway.
func (s *Service) encodeCORS(rules []CORSRule) (json.RawMessage, error) {
	v := make([]corsJSON, len(rules))
	for i, r := range rules {
		for _, o := range r.Origins {
			if o == "" || !headerSafe(o) || strings.ContainsAny(o, " ,") {
				return nil, invalid("cors[" + strconv.Itoa(i) + "].origin holds an invalid origin.")
			}
		}
		for _, m := range r.Methods {
			if !httpToken(m) {
				return nil, invalid("cors[" + strconv.Itoa(i) + "].method holds an invalid method.")
			}
		}
		for _, h := range r.ResponseHeaders {
			if !httpToken(h) {
				return nil, invalid("cors[" + strconv.Itoa(i) + "].responseHeader holds an invalid header name.")
			}
		}
		if r.MaxAgeSeconds < 0 || r.MaxAgeSeconds > math.MaxInt32 {
			return nil, invalid("cors[" + strconv.Itoa(i) + "].maxAgeSeconds is out of range.")
		}
		v[i] = corsJSON{Origin: r.Origins, Method: r.Methods, ResponseHeader: r.ResponseHeaders, MaxAgeSeconds: r.MaxAgeSeconds}
	}
	enc, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("storage: encode CORS: %w", err)
	}
	if int64(len(enc)) > s.limits.JSONBodyBytes {
		return nil, invalid("The CORS configuration is too large.")
	}
	return enc, nil
}

// httpToken reports whether v is an HTTP token (RFC 9110 §5.6.2): a method or a header name.
func httpToken(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}
