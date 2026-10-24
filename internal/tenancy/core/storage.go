package core

import (
	"context"
	"regexp"
	"strings"
)

// ProjectForBucket resolves a Cloud Storage bucket name to its active project (ADR-022 §2). A
// project has one bucket in v0.3, <project_id>.<project_domain>, the storageBucket its config
// files carry. Any other name, and a deleted project's bucket, is NOT_FOUND; a database failure
// is INTERNAL, so Storage does not mistake an outage for a missing bucket.
func (s *Service) ProjectForBucket(ctx context.Context, bucket string) (Project, error) {
	id, ok := s.bucketProjectID(bucket)
	if !ok {
		return Project{}, errNotFound()
	}
	p, err := s.q.GetProjectByID(ctx, id)
	switch {
	case err != nil && !notFound(err):
		return Project{}, internal(err)
	case err != nil || p.State != "ACTIVE":
		return Project{}, errNotFound()
	}
	return s.toProject(p), nil
}

// projectIDPattern is control.projects' check on project_id.
var projectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// bucketProjectID returns the project id a default bucket name carries. The name must be
// exactly as toProject derives it: no case folding, no trailing dot.
func (s *Service) bucketProjectID(bucket string) (string, bool) {
	id, ok := strings.CutSuffix(bucket, "."+s.opts.ProjectDomain)
	if !ok || s.opts.ProjectDomain == "" || !projectIDPattern.MatchString(id) {
		return "", false
	}
	return id, true
}
