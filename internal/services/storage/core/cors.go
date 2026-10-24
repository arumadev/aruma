package core

import "context"

// CORS returns a bucket's CORS rules; none until they are set (ADR-022 §11).
func (s *Service) CORS(ctx context.Context, b Bucket) ([]CORSRule, error) {
	panic("S2-C: CORS")
}

// BucketInfo returns the bucket resource: its settings and their metageneration.
func (s *Service) BucketInfo(ctx context.Context, b Bucket) (BucketInfo, error) {
	panic("S2-C: BucketInfo")
}

// SetCORS replaces a bucket's CORS rules, advancing its metageneration: admins only (the GCS
// surface and the dashboard). It returns the bucket as changed, for buckets.patch.
func (s *Service) SetCORS(ctx context.Context, b Bucket, p Principal, rules []CORSRule) (BucketInfo, error) {
	panic("S2-C: SetCORS")
}

// MatchCORS reports whether a cross-origin alt=media request from origin with method is allowed
// by the rules, and by which rule. Both surfaces call it for media.
func (s *Service) MatchCORS(rules []CORSRule, origin, method string) (allow bool, rule CORSRule) {
	panic("S2-C: MatchCORS")
}
