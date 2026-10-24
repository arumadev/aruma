package core

import "context"

// CreateToken adds a download token to an object (?create_token=true on /v0): admins only.
func (s *Service) CreateToken(ctx context.Context, b Bucket, name string, p Principal) (Object, error) {
	panic("S2-C: CreateToken")
}

// DeleteToken removes one of an object's download tokens (?delete_token= on /v0): admins only.
// Removing the last one makes a new one at once (E §5).
func (s *Service) DeleteToken(ctx context.Context, b Bucket, name string, p Principal, token string) (Object, error) {
	panic("S2-C: DeleteToken")
}
