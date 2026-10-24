package core

import "context"

// CORS returns a bucket's CORS rules; none until they are set (ADR-022 §11).
func (s *Service) CORS(ctx context.Context, b Bucket) ([]CORSRule, error) {
	panic("S2-C: CORS")
}

// SetCORS replaces a bucket's CORS rules: admins only (the GCS surface and the dashboard).
func (s *Service) SetCORS(ctx context.Context, b Bucket, p Principal, rules []CORSRule) error {
	panic("S2-C: SetCORS")
}

// MatchCORS reports whether a cross-origin alt=media request from origin with method is allowed
// by the rules, and by which rule. Both surfaces call it for media.
func (s *Service) MatchCORS(rules []CORSRule, origin, method string) (allow bool, rule CORSRule) {
	panic("S2-C: MatchCORS")
}
