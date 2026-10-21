package core

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/ids"
	"github.com/arumadev/aruma/internal/tenancy/queries"
)

// APIKey identifies a project to client SDKs. It is not a secret and grants no administrative
// access (ADR-010 S-10).
type APIKey struct {
	UID           uuid.UUID
	ProjectNumber int64
	KeyString     string
	DisplayName   string
	Restrictions  Restrictions
	State         string // ACTIVE or DISABLED
	Platform      string // the platform whose config files carry it, "" if none
	RotatedFrom   *uuid.UUID
	ETag          string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Restrictions limit where a key is accepted. The shape follows Google's API keys: API targets
// name the services the key may call, and at most one kind of application restriction names
// the websites, servers, Android apps or iOS apps it may be used from. Every data service checks
// them with Check (keycheck.go) when it resolves a key; Auth does so on every client call, the
// token refresh and the pages it serves. Method lists in API targets are not supported.
type Restrictions struct {
	BrowserKeyRestrictions *struct {
		AllowedReferrers []string `json:"allowedReferrers"`
	} `json:"browserKeyRestrictions,omitempty"`
	ServerKeyRestrictions *struct {
		AllowedIPs []string `json:"allowedIps"`
	} `json:"serverKeyRestrictions,omitempty"`
	AndroidKeyRestrictions *struct {
		AllowedApplications []struct {
			SHA1Fingerprint string `json:"sha1Fingerprint"`
			PackageName     string `json:"packageName"`
		} `json:"allowedApplications"`
	} `json:"androidKeyRestrictions,omitempty"`
	IOSKeyRestrictions *struct {
		AllowedBundleIDs []string `json:"allowedBundleIds"`
	} `json:"iosKeyRestrictions,omitempty"`
	APITargets []struct {
		Service string `json:"service"`
	} `json:"apiTargets,omitempty"`
}

const maxRestrictionEntries = 100

func (r Restrictions) validate() error {
	kinds := 0
	if b := r.BrowserKeyRestrictions; b != nil {
		kinds++
		if len(b.AllowedReferrers) > maxRestrictionEntries {
			return errTooManyRestrictions
		}
		for _, ref := range b.AllowedReferrers {
			if !ValidReferrerPattern(ref) {
				return apierr.Invalid("restrictions.browserKeyRestrictions",
					"Each website must look like example.com, *.example.com/* or https://example.com:8080/path; a * may only stand for a subdomain or end the path.")
			}
		}
	}
	if s := r.ServerKeyRestrictions; s != nil {
		kinds++
		if len(s.AllowedIPs) > maxRestrictionEntries {
			return errTooManyRestrictions
		}
		for _, ip := range s.AllowedIPs {
			if _, ok := parseIPEntry(ip); !ok {
				return apierr.Invalid("restrictions.serverKeyRestrictions", "Each IP address must be an IPv4 or IPv6 address or a CIDR range such as 198.51.100.0/24.")
			}
		}
	}
	if a := r.AndroidKeyRestrictions; a != nil {
		kinds++
		if len(a.AllowedApplications) > maxRestrictionEntries {
			return errTooManyRestrictions
		}
		for _, app := range a.AllowedApplications {
			if _, typ, ok := ids.NormalizeSHA(app.SHA1Fingerprint); !ok || typ != ids.SHA1 || !ids.ValidPackageName(app.PackageName) {
				return apierr.Invalid("restrictions.androidKeyRestrictions", "Each allowed application needs a SHA-1 fingerprint and a package name.")
			}
		}
	}
	if i := r.IOSKeyRestrictions; i != nil {
		kinds++
		if len(i.AllowedBundleIDs) > maxRestrictionEntries {
			return errTooManyRestrictions
		}
		for _, b := range i.AllowedBundleIDs {
			if !ids.ValidBundleID(b) {
				return apierr.Invalid("restrictions.iosKeyRestrictions", "Each allowed bundle ID must look like com.example.app.")
			}
		}
	}
	if kinds > 1 {
		return apierr.Invalid("restrictions", "A key can carry website, IP address, Android or iOS restrictions, not several kinds.")
	}
	if len(r.APITargets) > maxRestrictionEntries {
		return errTooManyRestrictions
	}
	for _, t := range r.APITargets {
		if strings.TrimSpace(t.Service) == "" {
			return apierr.Invalid("restrictions.apiTargets", "Each API target names a service, such as identitytoolkit.googleapis.com.")
		}
	}
	return nil
}

var errTooManyRestrictions = apierr.Invalid("restrictions", "At most 100 entries per restriction.")

func parseRestrictions(raw []byte) Restrictions {
	var r Restrictions
	_ = json.Unmarshal(raw, &r)
	return r
}

func toAPIKey(k queries.ControlApiKey) APIKey {
	out := APIKey{
		UID: k.Uid, ProjectNumber: k.ProjectNumber, KeyString: k.KeyString, DisplayName: k.DisplayName,
		Restrictions: parseRestrictions(k.Restrictions), State: k.State, RotatedFrom: k.RotatedFrom,
		ETag: k.Etag, CreatedAt: k.CreatedAt, UpdatedAt: k.UpdatedAt,
	}
	if k.Platform != nil {
		out.Platform = *k.Platform
	}
	return out
}

// ListAPIKeys lists a project's keys.
func (s *Service) ListAPIKeys(ctx context.Context, p *authn.Principal, projectRef string) ([]APIKey, error) {
	proj, err := s.resolveProject(ctx, s.q, p, projectRef, false)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListAPIKeys(ctx, proj.ProjectNumber)
	if err != nil {
		return nil, internal(err)
	}
	out := make([]APIKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAPIKey(r))
	}
	return out, nil
}

// GetAPIKey returns one key of a project.
func (s *Service) GetAPIKey(ctx context.Context, p *authn.Principal, projectRef string, uid uuid.UUID) (APIKey, error) {
	_, key, err := s.resolveKey(ctx, s.q, p, projectRef, uid)
	if err != nil {
		return APIKey{}, err
	}
	return toAPIKey(key), nil
}

func (s *Service) resolveKey(ctx context.Context, q *queries.Queries, p *authn.Principal, projectRef string, uid uuid.UUID) (queries.ControlProject, queries.ControlApiKey, error) {
	proj, err := s.resolveProject(ctx, q, p, projectRef, false)
	if err != nil {
		return proj, queries.ControlApiKey{}, err
	}
	key, err := q.GetAPIKey(ctx, queries.GetAPIKeyParams{ProjectNumber: proj.ProjectNumber, Uid: uid})
	if err != nil || key.State == "DELETED" {
		if err == nil || notFound(err) {
			return proj, key, errNotFound()
		}
		return proj, key, internal(err)
	}
	return proj, key, nil
}

// CreateAPIKey creates an extra key by hand, not tied to a platform.
func (s *Service) CreateAPIKey(ctx context.Context, p *authn.Principal, projectRef, displayName string, r Restrictions) (APIKey, error) {
	name, err := validDisplayName(displayName, false)
	if err != nil {
		return APIKey{}, err
	}
	if err := r.validate(); err != nil {
		return APIKey{}, err
	}
	raw, _ := json.Marshal(r)
	var out APIKey
	err = s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, err := s.resolveProject(ctx, q, p, projectRef, false)
		if err != nil {
			return err
		}
		key, err := q.CreateAPIKey(ctx, queries.CreateAPIKeyParams{
			ProjectNumber: proj.ProjectNumber, KeyString: ids.APIKey(), DisplayName: name, Restrictions: raw, Etag: ids.ETag(),
		})
		if err != nil {
			return err
		}
		out = toAPIKey(key)
		return s.audit(ctx, q, p, proj.ProjectNumber, "apikey.create", key.Uid.String(), nil)
	})
	return out, internal(err)
}

// APIKeyUpdate changes a key's name and/or restrictions.
type APIKeyUpdate struct {
	DisplayName  *string
	Restrictions *Restrictions
	ETag         string
}

// UpdateAPIKey changes a key's name or restrictions.
func (s *Service) UpdateAPIKey(ctx context.Context, p *authn.Principal, projectRef string, uid uuid.UUID, u APIKeyUpdate) (APIKey, error) {
	if u.DisplayName != nil {
		n, err := validDisplayName(*u.DisplayName, false)
		if err != nil {
			return APIKey{}, err
		}
		u.DisplayName = &n
	}
	var raw []byte
	if u.Restrictions != nil {
		if err := u.Restrictions.validate(); err != nil {
			return APIKey{}, err
		}
		raw, _ = json.Marshal(u.Restrictions)
	}
	var out APIKey
	err := s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, key, err := s.resolveKey(ctx, q, p, projectRef, uid)
		if err != nil {
			return err
		}
		etag := u.ETag
		if etag == "" {
			etag = key.Etag
		}
		updated, err := q.UpdateAPIKey(ctx, queries.UpdateAPIKeyParams{
			ProjectNumber: proj.ProjectNumber, Uid: uid, DisplayName: u.DisplayName, Restrictions: raw, NewEtag: ids.ETag(), Etag: etag,
		})
		if notFound(err) {
			return errStaleETag
		}
		if err != nil {
			return err
		}
		out = toAPIKey(updated)
		return s.audit(ctx, q, p, proj.ProjectNumber, "apikey.update", uid.String(), map[string]any{"restrictions": raw != nil})
	})
	return out, internal(err)
}

// SetAPIKeyEnabled disables or re-enables a key. A disabled key is refused wherever it is
// presented (AC-16).
func (s *Service) SetAPIKeyEnabled(ctx context.Context, p *authn.Principal, projectRef string, uid uuid.UUID, enabled bool) (APIKey, error) {
	state, action := "DISABLED", "apikey.disable"
	if enabled {
		state, action = "ACTIVE", "apikey.enable"
	}
	var out APIKey
	err := s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, _, err := s.resolveKey(ctx, q, p, projectRef, uid)
		if err != nil {
			return err
		}
		key, err := q.SetAPIKeyState(ctx, queries.SetAPIKeyStateParams{ProjectNumber: proj.ProjectNumber, Uid: uid, State: state, NewEtag: ids.ETag()})
		if err != nil {
			return err
		}
		out = toAPIKey(key)
		return s.audit(ctx, q, p, proj.ProjectNumber, action, uid.String(), nil)
	})
	return out, internal(err)
}

// RotateAPIKey issues a replacement key with the same restrictions. The new key takes over the
// old one's platform and apps, so freshly downloaded config files carry it; the old key keeps
// working until it is disabled, giving released apps time to update.
func (s *Service) RotateAPIKey(ctx context.Context, p *authn.Principal, projectRef string, uid uuid.UUID) (APIKey, error) {
	var out APIKey
	err := s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, old, err := s.resolveKey(ctx, q, p, projectRef, uid)
		if err != nil {
			return err
		}
		if old.Platform != nil {
			if err := q.ReleaseAPIKeyPlatform(ctx, queries.ReleaseAPIKeyPlatformParams{ProjectNumber: proj.ProjectNumber, Uid: uid, NewEtag: ids.ETag()}); err != nil {
				return err
			}
		}
		name := old.DisplayName
		if !bytes.Contains([]byte(name), []byte("(rotated)")) {
			name += " (rotated)"
		}
		key, err := q.CreateAPIKey(ctx, queries.CreateAPIKeyParams{
			ProjectNumber: proj.ProjectNumber, KeyString: ids.APIKey(), DisplayName: name,
			Restrictions: old.Restrictions, Platform: old.Platform, RotatedFrom: &old.Uid, Etag: ids.ETag(),
		})
		if err != nil {
			return err
		}
		if err := q.SetAppsAPIKey(ctx, queries.SetAppsAPIKeyParams{ProjectNumber: proj.ProjectNumber, OldKey: &old.Uid, NewKey: &key.Uid, NewEtag: ids.ETag()}); err != nil {
			return err
		}
		out = toAPIKey(key)
		return s.audit(ctx, q, p, proj.ProjectNumber, "apikey.rotate", uid.String(), map[string]any{"newKey": key.Uid.String()})
	})
	return out, internal(err)
}

// ResolvedKey is the project behind an API key presented by a client SDK.
type ResolvedKey struct {
	UID           uuid.UUID
	ProjectNumber int64
	Restrictions  Restrictions
}

// LookupAPIKey resolves a key string for the data services. Unknown, disabled keys and keys of
// deleted projects are all refused the same way.
func (s *Service) LookupAPIKey(ctx context.Context, key string) (ResolvedKey, error) {
	invalid := apierr.Invalid("key", "API key not valid. Please pass a valid API key.")
	if !ids.ValidAPIKey(key) {
		return ResolvedKey{}, invalid
	}
	row, err := s.q.LookupAPIKey(ctx, key)
	if err != nil {
		if notFound(err) {
			return ResolvedKey{}, invalid
		}
		return ResolvedKey{}, internal(err)
	}
	if row.State != "ACTIVE" || row.ProjectState != "ACTIVE" {
		return ResolvedKey{}, invalid
	}
	return ResolvedKey{UID: row.Uid, ProjectNumber: row.ProjectNumber, Restrictions: parseRestrictions(row.Restrictions)}, nil
}

// appKey returns the key string an app's config carries.
func (s *Service) appKey(ctx context.Context, q *queries.Queries, app queries.ControlApp) (string, error) {
	if app.ApiKeyID == nil {
		return "", apierr.New(apierr.FailedPrecondition, "The app has no API key.")
	}
	key, err := q.GetAPIKey(ctx, queries.GetAPIKeyParams{ProjectNumber: app.ProjectNumber, Uid: *app.ApiKeyID})
	if err != nil {
		return "", internal(err)
	}
	return key.KeyString, nil
}
