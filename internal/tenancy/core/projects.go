package core

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/ids"
	"github.com/osama300/aurma-project/internal/store"
	"github.com/osama300/aurma-project/internal/tenancy/queries"
)

// RetentionDays is how long deleted projects and apps can be restored.
const RetentionDays = 30

// Project is a project as the APIs show it.
type Project struct {
	Number      int64
	ID          string
	DisplayName string
	OwnerID     uuid.UUID
	State       string // ACTIVE or DELETED
	ETag        string
	AuthDomain  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeleteTime  *time.Time
	ExpireTime  *time.Time

	// Derived names (ADR-007).
	StorageBucket string
	DatabaseURL   string
	HostingSite   string
}

func (s *Service) toProject(p queries.ControlProject) Project {
	return Project{
		Number: p.ProjectNumber, ID: p.ProjectID, DisplayName: p.DisplayName, OwnerID: p.OwnerAccountID,
		State: p.State, ETag: p.Etag, AuthDomain: p.AuthDomain, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		DeleteTime: p.DeleteTime, ExpireTime: p.ExpireTime,
		StorageBucket: p.ProjectID + "." + s.opts.ProjectDomain,
		DatabaseURL:   s.opts.PublicURL + "/?ns=" + p.ProjectID + "-default-rtdb",
		HostingSite:   p.ProjectID,
	}
}

// resolveProject is the ownership gate every project-scoped operation passes through. ref is
// a project id or number. Anything the caller does not own is NOT_FOUND, never
// PERMISSION_DENIED, so other people's projects cannot be discovered (ADR-010 S-11). Deleted
// projects resolve only when allowDeleted is set.
func (s *Service) resolveProject(ctx context.Context, q *queries.Queries, p *authn.Principal, ref string, allowDeleted bool) (queries.ControlProject, error) {
	var (
		proj queries.ControlProject
		err  error
	)
	switch p.Kind {
	case authn.KindAccount:
		proj, err = q.GetOwnedProject(ctx, queries.GetOwnedProjectParams{OwnerAccountID: p.AccountID, Ref: ref})
	case authn.KindServiceAccount:
		proj, err = q.GetProject(ctx, p.ProjectNumber)
		if err == nil && proj.ProjectID != ref && formatNumber(proj.ProjectNumber) != ref {
			err = pgx.ErrNoRows
		}
	default:
		return proj, errNotFound()
	}
	if err != nil {
		if notFound(err) {
			return proj, errNotFound()
		}
		return proj, internal(err)
	}
	if proj.State != "ACTIVE" && !allowDeleted {
		return proj, errNotFound()
	}
	return proj, nil
}

// requireAccount refuses operations reserved for project owners, such as creating projects.
func requireAccount(p *authn.Principal) error {
	if p.Kind != authn.KindAccount {
		return apierr.New(apierr.PermissionDenied, "This operation requires a project owner's credentials.")
	}
	return nil
}

// GetProject returns one of the caller's projects, including deleted ones still restorable.
func (s *Service) GetProject(ctx context.Context, p *authn.Principal, ref string) (Project, error) {
	proj, err := s.resolveProject(ctx, s.q, p, ref, true)
	if err != nil {
		return Project{}, err
	}
	return s.toProject(proj), nil
}

// Page is a slice of a listing and the token of the next one ("" at the end).
type Page[T any] struct {
	Items         []T
	NextPageToken string
}

// ListOptions are the common listing parameters.
type ListOptions struct {
	PageSize    int
	PageToken   string
	ShowDeleted bool
}

const (
	defaultPageSize = 100
	maxPageSize     = 1000
)

func pageSize(n int) int32 {
	switch {
	case n <= 0:
		return defaultPageSize
	case n > maxPageSize:
		return maxPageSize
	}
	return int32(n)
}

func encodeToken(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

var errBadPageToken = apierr.Invalid("pageToken", "The page token is invalid.")

func decodeToken(tok string) (string, error) {
	if tok == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return "", errBadPageToken
	}
	return string(b), nil
}

// ListProjects lists the caller's projects. A service account sees only its own project.
func (s *Service) ListProjects(ctx context.Context, p *authn.Principal, opt ListOptions) (Page[Project], error) {
	if p.Kind == authn.KindServiceAccount {
		proj, err := s.q.GetProject(ctx, p.ProjectNumber)
		if err != nil || proj.State != "ACTIVE" || opt.PageToken != "" {
			return Page[Project]{Items: []Project{}}, nil
		}
		return Page[Project]{Items: []Project{s.toProject(proj)}}, nil
	}
	after, err := decodeToken(opt.PageToken)
	if err != nil {
		return Page[Project]{}, err
	}
	size := pageSize(opt.PageSize)
	rows, err := s.q.ListOwnedProjects(ctx, queries.ListOwnedProjectsParams{
		OwnerAccountID: p.AccountID, After: after, ShowDeleted: opt.ShowDeleted, PageSize: size + 1,
	})
	if err != nil {
		return Page[Project]{}, internal(err)
	}
	var page Page[Project]
	if len(rows) > int(size) {
		rows = rows[:size]
		page.NextPageToken = encodeToken(rows[len(rows)-1].ProjectID)
	}
	page.Items = make([]Project, 0, len(rows))
	for _, r := range rows {
		page.Items = append(page.Items, s.toProject(r))
	}
	return page, nil
}

// ProjectIDAvailable reports whether a project id is well-formed and unused.
func (s *Service) ProjectIDAvailable(ctx context.Context, id string) (bool, error) {
	if !ids.ValidProjectID(id) {
		return false, errInvalidProjectID
	}
	taken, err := s.q.ProjectIDTaken(ctx, id)
	if err != nil {
		return false, internal(err)
	}
	return !taken, nil
}

// SuggestProjectID proposes an available id derived from a display name.
func (s *Service) SuggestProjectID(ctx context.Context, displayName string) (string, error) {
	for i := 0; i < 5; i++ {
		id := ids.SuggestProjectID(displayName, i > 0)
		ok, err := s.ProjectIDAvailable(ctx, id)
		if err != nil {
			return "", err
		}
		if ok {
			return id, nil
		}
	}
	return ids.SuggestProjectID("project", true), nil
}

var errInvalidProjectID = apierr.Invalid("projectId",
	"Project ID must be 6 to 30 lowercase letters, digits or hyphens, start with a letter and not end with a hyphen.")

// NewProject is the input for creating a project.
type NewProject struct {
	ID          string
	DisplayName string
}

// CreateProject creates a project owned by the calling account.
func (s *Service) CreateProject(ctx context.Context, p *authn.Principal, in NewProject) (Project, error) {
	if err := requireAccount(p); err != nil {
		return Project{}, err
	}
	if !ids.ValidProjectID(in.ID) {
		return Project{}, errInvalidProjectID
	}
	name, err := validDisplayName(in.DisplayName, true)
	if err != nil {
		return Project{}, err
	}
	var proj queries.ControlProject
	for attempt := 0; attempt < 5; attempt++ {
		err = s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
			var err error
			proj, err = q.CreateProject(ctx, queries.CreateProjectParams{
				ProjectNumber:  ids.ProjectNumber(),
				ProjectID:      in.ID,
				DisplayName:    name,
				OwnerAccountID: p.AccountID,
				Etag:           ids.ETag(),
				AuthDomain:     s.opts.ProjectDomain,
			})
			if err != nil {
				return err
			}
			return s.audit(ctx, q, p, proj.ProjectNumber, "project.create", "projects/"+proj.ProjectID, nil)
		})
		c, ok := store.UniqueViolation(err)
		if !ok || c != "projects_pkey" {
			break // project numbers collide with odds of 1 in 9e11; retry only that
		}
	}
	if c, ok := store.UniqueViolation(err); ok && c == "projects_project_id_key" {
		return Project{}, apierr.New(apierr.AlreadyExists, "Project ID %q is already in use. Project IDs are never reused, even after deletion.", in.ID)
	}
	if err != nil {
		return Project{}, internal(err)
	}
	return s.toProject(proj), nil
}

// ProjectUpdate lists the fields to change; nil fields are left alone. A non-empty ETag must
// match the current one.
type ProjectUpdate struct {
	DisplayName *string
	AuthDomain  *string
	ETag        string
}

// UpdateProject changes a project's display name and/or auth domain.
func (s *Service) UpdateProject(ctx context.Context, p *authn.Principal, ref string, u ProjectUpdate) (Project, error) {
	if u.DisplayName != nil {
		n, err := validDisplayName(*u.DisplayName, true)
		if err != nil {
			return Project{}, err
		}
		u.DisplayName = &n
	}
	if u.AuthDomain != nil {
		d := strings.ToLower(strings.TrimSpace(*u.AuthDomain))
		if !validHostname(d) {
			return Project{}, apierr.Invalid("authDomain", "Auth domain must be a host name, such as auth.example.com.")
		}
		u.AuthDomain = &d
	}
	var out queries.ControlProject
	err := s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, err := s.resolveProject(ctx, q, p, ref, false)
		if err != nil {
			return err
		}
		etag := u.ETag
		if etag == "" {
			etag = proj.Etag
		}
		out, err = q.UpdateProject(ctx, queries.UpdateProjectParams{
			ProjectNumber: proj.ProjectNumber, DisplayName: u.DisplayName, AuthDomain: u.AuthDomain,
			NewEtag: ids.ETag(), Etag: etag,
		})
		if notFound(err) {
			return errStaleETag
		}
		if err != nil {
			return err
		}
		return s.audit(ctx, q, p, proj.ProjectNumber, "project.update", "projects/"+proj.ProjectID, map[string]any{
			"displayName": u.DisplayName != nil, "authDomain": u.AuthDomain,
		})
	})
	if err != nil {
		return Project{}, internal(err)
	}
	return s.toProject(out), nil
}

var errStaleETag = apierr.New(apierr.Aborted, "The resource was modified since it was read (etag mismatch). Read it again and retry.")

// DeleteProject soft-deletes a project for RetentionDays. Its apps, keys and service accounts
// stop working at once and come back with it on restore (AC-11).
func (s *Service) DeleteProject(ctx context.Context, p *authn.Principal, ref string) (Project, error) {
	if err := requireAccount(p); err != nil {
		return Project{}, err
	}
	var out queries.ControlProject
	err := s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, err := s.resolveProject(ctx, q, p, ref, false)
		if err != nil {
			return err
		}
		out, err = q.DeleteProject(ctx, queries.DeleteProjectParams{ProjectNumber: proj.ProjectNumber, RetentionDays: RetentionDays, NewEtag: ids.ETag()})
		if err != nil {
			return err
		}
		return s.audit(ctx, q, p, proj.ProjectNumber, "project.delete", "projects/"+proj.ProjectID, nil)
	})
	if err != nil {
		return Project{}, internal(err)
	}
	return s.toProject(out), nil
}

// RestoreProject undoes a deletion within the retention period.
func (s *Service) RestoreProject(ctx context.Context, p *authn.Principal, ref string) (Project, error) {
	if err := requireAccount(p); err != nil {
		return Project{}, err
	}
	var out queries.ControlProject
	err := s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, err := s.resolveProject(ctx, q, p, ref, true)
		if err != nil {
			return err
		}
		if proj.State == "ACTIVE" {
			return apierr.New(apierr.FailedPrecondition, "The project is not deleted.")
		}
		out, err = q.RestoreProject(ctx, queries.RestoreProjectParams{ProjectNumber: proj.ProjectNumber, NewEtag: ids.ETag()})
		if notFound(err) {
			return apierr.New(apierr.FailedPrecondition, "The project was deleted more than %d days ago and can no longer be restored.", RetentionDays)
		}
		if err != nil {
			return err
		}
		return s.audit(ctx, q, p, proj.ProjectNumber, "project.restore", "projects/"+proj.ProjectID, nil)
	})
	if err != nil {
		return Project{}, internal(err)
	}
	return s.toProject(out), nil
}

// PurgeExpired removes the contents of projects and the apps whose retention period has ended,
// and long-expired service account tokens. Project rows stay, so their ids remain reserved
// forever.
func (s *Service) PurgeExpired(ctx context.Context) (int, error) {
	numbers, err := s.q.ListExpiredProjects(ctx)
	if err != nil {
		return 0, internal(err)
	}
	for _, n := range numbers {
		err := s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
			if err := q.PurgeProjectApps(ctx, n); err != nil {
				return err
			}
			if err := q.PurgeProjectServiceAccounts(ctx, n); err != nil {
				return err
			}
			return q.PurgeProjectAPIKeys(ctx, n)
		})
		if err != nil {
			return 0, internal(err)
		}
		s.log.Info("purged expired project", "project_number", n)
	}
	apps, err := s.q.PurgeExpiredApps(ctx)
	if err != nil {
		return 0, internal(err)
	}
	if apps > 0 {
		s.log.Info("purged expired apps", "count", apps)
	}
	if _, err := s.PurgeExpiredTokens(ctx); err != nil {
		return 0, err
	}
	return len(numbers), nil
}

func validDisplayName(raw string, required bool) (string, error) {
	n := strings.TrimSpace(raw)
	if (required && n == "") || utf8.RuneCountInString(n) > 100 {
		return "", apierr.Invalid("displayName", "Display name must be 1 to 100 characters.")
	}
	return n, nil
}

func validHostname(h string) bool {
	if len(h) == 0 || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func formatNumber(n int64) string { return strconv.FormatInt(n, 10) }
