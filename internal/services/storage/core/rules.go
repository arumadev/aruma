package core

import "context"

// authorize decides an operation (ADR-022 §8): admins bypass the rules; everyone else answers
// to the project's Storage rules, ErrPermissionDenied or ErrOverloaded.
//
// Until S2-E binds the rules, the contract's body refuses everyone but admins, and this
// package's tests decide through authorizeHook.
func (s *Service) authorize(ctx context.Context, b Bucket, p Principal, op ruleOp) error {
	if p.Kind == PrincipalAdmin {
		return nil
	}
	if s.authorizeHook != nil {
		return s.authorizeHook(ctx, b, p, op)
	}
	return ErrPermissionDenied
}
