package core

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/services/auth/queries"
	"github.com/arumadev/aruma/internal/services/auth/tokens"
)

// The operations below serve only the console: searching users and managing the signing keys.
// Like the admin operations, their callers have already authorised the request.

// SearchUsers returns a page of users whose email starts with query (case-insensitively) or
// whose uid is exactly query, ordered by uid, with the number of matches. An empty query lists
// every user. Page tokens work as in ListUsers.
func (s *Service) SearchUsers(ctx context.Context, p Project, query, pageToken string, max int) ([]User, string, int64, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		users, next, err := s.ListUsers(ctx, p, pageToken, max)
		if err != nil {
			return nil, "", 0, err
		}
		total, err := s.CountUsers(ctx, p)
		return users, next, total, err
	}
	if max <= 0 || max > maxBatch {
		max = maxBatch
	}
	prefix := strings.ToLower(query)
	rows, err := s.q.SearchUsers(ctx, queries.SearchUsersParams{
		ProjectNumber: p.Number, TenantID: p.Tenant, EmailPrefix: prefix, Uid: query, After: pageToken, PageSize: int32(max + 1),
	})
	if err != nil {
		return nil, "", 0, err
	}
	next := ""
	if len(rows) > max {
		rows = rows[:max]
		next = rows[max-1].Uid
	}
	total, err := s.q.CountSearchUsers(ctx, queries.CountSearchUsersParams{ProjectNumber: p.Number, TenantID: p.Tenant, EmailPrefix: prefix, Uid: query})
	if err != nil {
		return nil, "", 0, err
	}
	users, err := s.loadUsers(ctx, p, rows)
	return users, next, total, err
}

// SigningKey is the public description of a token signing key; the private part never leaves
// the token manager.
type SigningKey struct {
	Kid         string
	Purpose     tokens.Purpose
	State       string // next, active or retired; destroyed keys are gone
	CreatedAt   time.Time
	ActivatedAt *time.Time
	RetiredAt   *time.Time
}

// signingPurposes are listed in this order.
var signingPurposes = []tokens.Purpose{tokens.IDToken, tokens.SessionCookie}

// SigningKeys lists the project's published keys for every purpose, oldest first. A project
// that has never issued a token has none yet.
func (s *Service) SigningKeys(ctx context.Context, p Project) ([]SigningKey, error) {
	out := []SigningKey{}
	for _, purpose := range signingPurposes {
		rows, err := s.q.ListSigningKeys(ctx, queries.ListSigningKeysParams{ProjectNumber: p.Number, Purpose: string(purpose)})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, SigningKey{
				Kid: r.Kid, Purpose: purpose, State: r.State, CreatedAt: r.CreatedAt, ActivatedAt: r.ActivatedAt, RetiredAt: r.RetiredAt,
			})
		}
	}
	return out, nil
}

// RotateSigningKey advances the key cycle of one purpose (tokens.Manager.Rotate). A normal
// rotation keeps issued tokens valid until they expire; an emergency one destroys the active
// key, so every token it signed stops verifying at once (AC-A15).
func (s *Service) RotateSigningKey(ctx context.Context, p Project, purpose string, emergency bool) error {
	pu := tokens.Purpose(purpose)
	if pu != tokens.IDToken && pu != tokens.SessionCookie {
		return failf("INVALID_ARGUMENT", "purpose must be id_token or session_cookie")
	}
	if err := s.tokens.Rotate(ctx, p.Number, pu, emergency); err != nil {
		return err
	}
	s.audit(ctx, s.q, p, "", "signing_key.rotate", "ok", map[string]any{"purpose": purpose, "emergency": emergency})
	return nil
}

// maxAuthorizedDomains bounds the authorised domains of a project.
const maxAuthorizedDomains = 100

// checkConfig normalises the authorised domains (lower case, no duplicates) and refuses any
// entry that is not a bare host name or IP address: no scheme, port, path or wildcard, since
// they decide where email links and redirects may go.
func checkConfig(c *queries.AuthProjectConfig) error {
	if !validDisplayName(c.AppName) {
		return failf("INVALID_CONFIG", "app name must be at most 256 characters")
	}
	if len(c.AuthorizedDomains) > maxAuthorizedDomains {
		return failf("INVALID_CONFIG", "at most 100 authorized domains")
	}
	out := make([]string, 0, len(c.AuthorizedDomains))
	for _, d := range c.AuthorizedDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if !validDomain(d) {
			return failf("INVALID_CONFIG", "invalid authorized domain: "+d)
		}
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	c.AuthorizedDomains = out
	return nil
}

func validDomain(d string) bool {
	if _, err := netip.ParseAddr(d); err == nil {
		return !strings.Contains(d, "%")
	}
	if d == "" || len(d) > 253 {
		return false
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}
