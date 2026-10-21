package core

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/ids"
	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/tenancy/queries"
)

// Platform names as stored and as the Management API spells them.
const (
	PlatformWeb     = "WEB"
	PlatformIOS     = "IOS"
	PlatformAndroid = "ANDROID"
)

var platformIDs = map[string]ids.Platform{PlatformWeb: ids.Web, PlatformIOS: ids.IOS, PlatformAndroid: ids.Android}

// App is a registered client app.
type App struct {
	ID            string
	ProjectNumber int64
	ProjectID     string
	Platform      string
	DisplayName   string
	PackageName   string // Android
	BundleID      string // iOS
	APIKeyUID     *uuid.UUID
	State         string
	ETag          string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeleteTime    *time.Time
	ExpireTime    *time.Time
}

// Namespace is the package name, bundle id or, for web, the app id tail.
func (a App) Namespace() string {
	switch a.Platform {
	case PlatformAndroid:
		return a.PackageName
	case PlatformIOS:
		return a.BundleID
	}
	return a.ID[strings.LastIndexByte(a.ID, ':')+1:]
}

func toApp(a queries.ControlApp, projectID string) App {
	out := App{
		ID: a.AppID, ProjectNumber: a.ProjectNumber, ProjectID: projectID, Platform: a.Platform,
		DisplayName: a.DisplayName, APIKeyUID: a.ApiKeyID, State: a.State, ETag: a.Etag,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, DeleteTime: a.DeleteTime, ExpireTime: a.ExpireTime,
	}
	if a.PackageName != nil {
		out.PackageName = *a.PackageName
	}
	if a.BundleID != nil {
		out.BundleID = *a.BundleID
	}
	return out
}

// NewApp is the input for registering an app. PackageName applies to Android, BundleID to iOS.
type NewApp struct {
	Platform    string
	DisplayName string
	PackageName string
	BundleID    string
}

// CreateApp registers an app in a project. Its platform's API key is created with the first
// app of that platform and shared by the rest (ADR-007).
func (s *Service) CreateApp(ctx context.Context, p *authn.Principal, projectRef string, in NewApp) (App, error) {
	plat, ok := platformIDs[in.Platform]
	if !ok {
		return App{}, apierr.Invalid("platform", "Platform must be WEB, IOS or ANDROID.")
	}
	name, err := validDisplayName(in.DisplayName, false)
	if err != nil {
		return App{}, err
	}
	var pkg, bundle *string
	switch in.Platform {
	case PlatformAndroid:
		if !ids.ValidPackageName(in.PackageName) {
			return App{}, apierr.Invalid("packageName", "Package name must look like com.example.app.")
		}
		pkg = &in.PackageName
	case PlatformIOS:
		if !ids.ValidBundleID(in.BundleID) {
			return App{}, apierr.Invalid("bundleId", "Bundle ID must look like com.example.app.")
		}
		bundle = &in.BundleID
	default:
		if in.PackageName != "" || in.BundleID != "" {
			return App{}, apierr.Invalid("platform", "Web apps have no package name or bundle ID.")
		}
	}

	var out App
	err = s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, err := s.resolveProject(ctx, q, p, projectRef, false)
		if err != nil {
			return err
		}
		key, err := s.platformKey(ctx, q, proj.ProjectNumber, in.Platform)
		if err != nil {
			return err
		}
		app, err := q.CreateApp(ctx, queries.CreateAppParams{
			AppID: ids.AppID(proj.ProjectNumber, plat), ProjectNumber: proj.ProjectNumber, Platform: in.Platform,
			DisplayName: name, PackageName: pkg, BundleID: bundle, ApiKeyID: &key.Uid, Etag: ids.ETag(),
		})
		if err != nil {
			return err
		}
		out = toApp(app, proj.ProjectID)
		return s.audit(ctx, q, p, proj.ProjectNumber, "app.create", app.AppID, map[string]any{"platform": in.Platform, "namespace": out.Namespace()})
	})
	if c, ok := store.UniqueViolation(err); ok {
		switch c {
		case "apps_project_package_key":
			return App{}, apierr.New(apierr.AlreadyExists, "An Android app with package name %q already exists in this project.", in.PackageName)
		case "apps_project_bundle_key":
			return App{}, apierr.New(apierr.AlreadyExists, "An iOS app with bundle ID %q already exists in this project.", in.BundleID)
		}
	}
	return out, internal(err)
}

// platformKey returns the project's key for a platform, creating it on first use.
func (s *Service) platformKey(ctx context.Context, q *queries.Queries, project int64, platform string) (queries.ControlApiKey, error) {
	key, err := q.GetPlatformAPIKey(ctx, queries.GetPlatformAPIKeyParams{ProjectNumber: project, Platform: &platform})
	if err == nil || !notFound(err) {
		return key, err
	}
	names := map[string]string{PlatformWeb: "Browser key", PlatformIOS: "iOS key", PlatformAndroid: "Android key"}
	return q.CreateAPIKey(ctx, queries.CreateAPIKeyParams{
		ProjectNumber: project, KeyString: ids.APIKey(), DisplayName: names[platform] + " (auto created)",
		Restrictions: []byte(`{}`), Platform: &platform, Etag: ids.ETag(),
	})
}

// appProjectRef returns the project reference to resolve for an app. The Management API allows
// "-" as the project, meaning "whichever project owns this app"; the app id embeds its number.
func appProjectRef(projectRef, appID string) (string, error) {
	if projectRef != "-" {
		return projectRef, nil
	}
	n, ok := ids.AppIDProjectNumber(appID)
	if !ok {
		return "", errNotFound()
	}
	return formatNumber(n), nil
}

// resolveApp returns an app of a project the caller owns.
func (s *Service) resolveApp(ctx context.Context, q *queries.Queries, p *authn.Principal, projectRef, appID string, allowDeleted bool) (queries.ControlProject, queries.ControlApp, error) {
	ref, err := appProjectRef(projectRef, appID)
	if err != nil {
		return queries.ControlProject{}, queries.ControlApp{}, err
	}
	proj, err := s.resolveProject(ctx, q, p, ref, false)
	if err != nil {
		return proj, queries.ControlApp{}, err
	}
	app, err := q.GetApp(ctx, queries.GetAppParams{ProjectNumber: proj.ProjectNumber, AppID: appID})
	if err != nil {
		if notFound(err) {
			return proj, app, errNotFound()
		}
		return proj, app, internal(err)
	}
	if app.State != "ACTIVE" && !allowDeleted {
		return proj, app, errNotFound()
	}
	return proj, app, nil
}

// GetApp returns one app, including a deleted one still restorable.
func (s *Service) GetApp(ctx context.Context, p *authn.Principal, projectRef, appID string) (App, error) {
	proj, app, err := s.resolveApp(ctx, s.q, p, projectRef, appID, true)
	if err != nil {
		return App{}, err
	}
	return toApp(app, proj.ProjectID), nil
}

// ListApps lists a project's apps, optionally of one platform ("" for all).
func (s *Service) ListApps(ctx context.Context, p *authn.Principal, projectRef, platform string, opt ListOptions) (Page[App], error) {
	if platform != "" {
		if _, ok := platformIDs[platform]; !ok {
			return Page[App]{}, apierr.Invalid("platform", "Platform must be WEB, IOS or ANDROID.")
		}
	}
	proj, err := s.resolveProject(ctx, s.q, p, projectRef, false)
	if err != nil {
		return Page[App]{}, err
	}
	after, err := decodeToken(opt.PageToken)
	if err != nil {
		return Page[App]{}, err
	}
	var plat *string
	if platform != "" {
		plat = &platform
	}
	size := pageSize(opt.PageSize)
	rows, err := s.q.ListApps(ctx, queries.ListAppsParams{
		ProjectNumber: proj.ProjectNumber, Platform: plat, After: after, ShowDeleted: opt.ShowDeleted, PageSize: size + 1,
	})
	if err != nil {
		return Page[App]{}, internal(err)
	}
	var page Page[App]
	if len(rows) > int(size) {
		rows = rows[:size]
		page.NextPageToken = encodeToken(rows[len(rows)-1].AppID)
	}
	page.Items = make([]App, 0, len(rows))
	for _, r := range rows {
		page.Items = append(page.Items, toApp(r, proj.ProjectID))
	}
	return page, nil
}

// AppUpdate changes an app. Package names and bundle ids are immutable (AC-13), so only the
// display name can change.
type AppUpdate struct {
	DisplayName *string
	ETag        string
}

// UpdateApp renames an app.
func (s *Service) UpdateApp(ctx context.Context, p *authn.Principal, projectRef, appID string, u AppUpdate) (App, error) {
	if u.DisplayName != nil {
		n, err := validDisplayName(*u.DisplayName, false)
		if err != nil {
			return App{}, err
		}
		u.DisplayName = &n
	}
	var out App
	err := s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, app, err := s.resolveApp(ctx, q, p, projectRef, appID, false)
		if err != nil {
			return err
		}
		etag := u.ETag
		if etag == "" {
			etag = app.Etag
		}
		updated, err := q.UpdateApp(ctx, queries.UpdateAppParams{
			ProjectNumber: proj.ProjectNumber, AppID: appID, DisplayName: u.DisplayName, NewEtag: ids.ETag(), Etag: etag,
		})
		if notFound(err) {
			return errStaleETag
		}
		if err != nil {
			return err
		}
		out = toApp(updated, proj.ProjectID)
		return s.audit(ctx, q, p, proj.ProjectNumber, "app.update", appID, nil)
	})
	return out, internal(err)
}

// RemoveOptions mirror the Management API remove request.
type RemoveOptions struct {
	Immediate    bool // delete now instead of after the retention period
	AllowMissing bool // succeed when the app does not exist
	ValidateOnly bool
	ETag         string
}

// RemoveApp deletes an app: for RetentionDays unless Immediate.
func (s *Service) RemoveApp(ctx context.Context, p *authn.Principal, projectRef, appID string, o RemoveOptions) (App, error) {
	var out App
	err := s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, app, err := s.resolveApp(ctx, q, p, projectRef, appID, true)
		if err != nil {
			return err
		}
		if o.ETag != "" && o.ETag != app.Etag {
			return errStaleETag
		}
		if app.State != "ACTIVE" && !o.Immediate {
			return apierr.New(apierr.FailedPrecondition, "The app is already deleted.")
		}
		out = toApp(app, proj.ProjectID)
		if o.ValidateOnly {
			return nil
		}
		if o.Immediate {
			if err := q.PurgeApp(ctx, queries.PurgeAppParams{ProjectNumber: proj.ProjectNumber, AppID: appID}); err != nil {
				return err
			}
			out.State = "DELETED"
		} else {
			deleted, err := q.DeleteApp(ctx, queries.DeleteAppParams{ProjectNumber: proj.ProjectNumber, AppID: appID, RetentionDays: RetentionDays, NewEtag: ids.ETag()})
			if err != nil {
				return err
			}
			out = toApp(deleted, proj.ProjectID)
		}
		return s.audit(ctx, q, p, proj.ProjectNumber, "app.delete", appID, map[string]any{"immediate": o.Immediate})
	})
	if o.AllowMissing && apierr.CodeOf(err) == apierr.NotFound {
		return App{ID: appID, State: "DELETED"}, nil
	}
	return out, internal(err)
}

// UndeleteApp restores a deleted app within the retention period.
func (s *Service) UndeleteApp(ctx context.Context, p *authn.Principal, projectRef, appID string, validateOnly bool) (App, error) {
	var out App
	err := s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, app, err := s.resolveApp(ctx, q, p, projectRef, appID, true)
		if err != nil {
			return err
		}
		if app.State == "ACTIVE" {
			return apierr.New(apierr.FailedPrecondition, "The app is not deleted.")
		}
		out = toApp(app, proj.ProjectID)
		if validateOnly {
			return nil
		}
		restored, err := q.RestoreApp(ctx, queries.RestoreAppParams{ProjectNumber: proj.ProjectNumber, AppID: appID, NewEtag: ids.ETag()})
		if notFound(err) {
			return apierr.New(apierr.FailedPrecondition, "The app was deleted more than %d days ago and can no longer be restored.", RetentionDays)
		}
		if err != nil {
			return err
		}
		out = toApp(restored, proj.ProjectID)
		return s.audit(ctx, q, p, proj.ProjectNumber, "app.undelete", appID, nil)
	})
	return out, internal(err)
}

// ShaCertificate is an Android signing certificate fingerprint.
type ShaCertificate struct {
	ID       uuid.UUID
	AppID    string
	Hash     string
	CertType string
}

func toSha(c queries.ControlAppShaCertificate) ShaCertificate {
	return ShaCertificate{ID: c.ID, AppID: c.AppID, Hash: c.ShaHash, CertType: c.CertType}
}

func (s *Service) androidApp(ctx context.Context, q *queries.Queries, p *authn.Principal, projectRef, appID string) (queries.ControlProject, queries.ControlApp, error) {
	proj, app, err := s.resolveApp(ctx, q, p, projectRef, appID, false)
	if err == nil && app.Platform != PlatformAndroid {
		err = apierr.New(apierr.FailedPrecondition, "SHA certificates apply to Android apps only.")
	}
	return proj, app, err
}

// ListShaCertificates lists an Android app's fingerprints.
func (s *Service) ListShaCertificates(ctx context.Context, p *authn.Principal, projectRef, appID string) ([]ShaCertificate, error) {
	proj, _, err := s.androidApp(ctx, s.q, p, projectRef, appID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListShaCertificates(ctx, queries.ListShaCertificatesParams{ProjectNumber: proj.ProjectNumber, AppID: appID})
	if err != nil {
		return nil, internal(err)
	}
	out := make([]ShaCertificate, 0, len(rows))
	for _, r := range rows {
		out = append(out, toSha(r))
	}
	return out, nil
}

// AddShaCertificate adds a fingerprint, given as hex with or without colons.
func (s *Service) AddShaCertificate(ctx context.Context, p *authn.Principal, projectRef, appID, hash string) (ShaCertificate, error) {
	h, typ, ok := ids.NormalizeSHA(hash)
	if !ok {
		return ShaCertificate{}, apierr.Invalid("shaHash", "The fingerprint must be a SHA-1 (40 hex) or SHA-256 (64 hex) hash.")
	}
	var out ShaCertificate
	err := s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, _, err := s.androidApp(ctx, q, p, projectRef, appID)
		if err != nil {
			return err
		}
		c, err := q.AddShaCertificate(ctx, queries.AddShaCertificateParams{ProjectNumber: proj.ProjectNumber, AppID: appID, ShaHash: h, CertType: string(typ)})
		if err != nil {
			return err
		}
		out = toSha(c)
		return s.audit(ctx, q, p, proj.ProjectNumber, "app.sha.add", appID, map[string]any{"certType": string(typ)})
	})
	if _, ok := store.UniqueViolation(err); ok {
		return ShaCertificate{}, apierr.New(apierr.AlreadyExists, "This fingerprint is already registered for the app.")
	}
	return out, internal(err)
}

// DeleteShaCertificate removes a fingerprint.
func (s *Service) DeleteShaCertificate(ctx context.Context, p *authn.Principal, projectRef, appID string, id uuid.UUID) error {
	return internal(s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, _, err := s.androidApp(ctx, q, p, projectRef, appID)
		if err != nil {
			return err
		}
		n, err := q.DeleteShaCertificate(ctx, queries.DeleteShaCertificateParams{ProjectNumber: proj.ProjectNumber, AppID: appID, ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return errNotFound()
		}
		return s.audit(ctx, q, p, proj.ProjectNumber, "app.sha.delete", appID, nil)
	}))
}
