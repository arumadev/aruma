package core

import (
	"errors"
	"fmt"
	"time"
)

// Limits bound what Storage accepts and holds, against a hostile project choosing its own names
// and rules (ADR-022 §17). The configuration sets them under storage.limits, with the keys named
// in the comments; config.StorageLimits mirrors this type field for field, and config checks
// the same rules when it loads.
//
// core enforces the limits on objects, sessions, quota and disk; the surfaces (api, gcs) enforce
// the ones on requests: the rates per IP, the concurrent requests per IP and the minimum rate.
type Limits struct {
	ObjectBytes                int64         // object_bytes: one object, one request's body, and what decompression may produce
	ProjectBytes               int64         // project_bytes: a project's quota, staging included; 0 is unlimited
	MinFreeBytes               int64         // min_free_bytes: the disk space no write may take, shared with Postgres
	EmptySessionTTL            time.Duration // empty_session_ttl: the life of a session that received no byte
	UploadIdleTTL              time.Duration // upload_idle_ttl: after it without a durable byte, a session's unused reservation goes
	UploadSessionTTL           time.Duration // upload_session_ttl: a session's life from its start
	ConsoleSessionTTL          time.Duration // console_session_ttl: the life of a session the dashboard issues
	AnonymousStagedFraction    float64       // anonymous_staged_fraction: the share of project_bytes sessions without an identity may hold
	MetadataBytes              int           // metadata_bytes: the custom metadata's keys and values together
	JSONBodyBytes              int64         // json_body_bytes: a PATCH body, a start body, a multipart JSON part
	HeaderFieldBytes           int           // header_field_bytes: one of the five content fields
	UploadSessionsPerProject   int           // upload_sessions_per_project: live sessions that received a byte
	UploadSessionsPerUser      int           // upload_sessions_per_user: the same, per uid
	UploadSessionsPerIP        int           // upload_sessions_per_ip: the same, per client address
	UploadStartsPerMinutePerIP int           // upload_starts_per_minute_per_ip: upload starts from one address
	UploadStartsPerSecond      int           // upload_starts_per_second: upload starts in the whole process
	UploadRequestsPerIP        int           // upload_requests_per_ip: concurrent upload and write requests from one address
	MinBytesPerSecond          int64         // min_bytes_per_second: below it over 30 seconds, an upload request is cut
	ListMaxResults             int           // list_max_results: the largest list page
}

// DefaultLimits returns ADR-022 §17's defaults.
func DefaultLimits() Limits {
	return Limits{
		ObjectBytes:                5 << 30,
		ProjectBytes:               5 << 30,
		MinFreeBytes:               2 << 30,
		EmptySessionTTL:            10 * time.Minute,
		UploadIdleTTL:              time.Hour,
		UploadSessionTTL:           7 * 24 * time.Hour,
		ConsoleSessionTTL:          time.Hour,
		AnonymousStagedFraction:    0.2,
		MetadataBytes:              8 << 10,
		JSONBodyBytes:              64 << 10,
		HeaderFieldBytes:           1 << 10,
		UploadSessionsPerProject:   1000,
		UploadSessionsPerUser:      50,
		UploadSessionsPerIP:        100,
		UploadStartsPerMinutePerIP: 60,
		UploadStartsPerSecond:      1000,
		UploadRequestsPerIP:        64,
		MinBytesPerSecond:          4 << 10,
		ListMaxResults:             1000,
	}
}

// Validate reports every limit that is not positive (project_bytes may be 0, unlimited), and
// the cross checks of ADR-022 §17: an object fits in a limited quota, the session lifetimes
// nest, and the anonymous share lies between 0 and 1.
func (l Limits) Validate() error {
	var errs []error
	for _, f := range []struct {
		name string
		v    int64
	}{
		{"object_bytes", l.ObjectBytes},
		{"min_free_bytes", l.MinFreeBytes},
		{"empty_session_ttl", int64(l.EmptySessionTTL)},
		{"upload_idle_ttl", int64(l.UploadIdleTTL)},
		{"upload_session_ttl", int64(l.UploadSessionTTL)},
		{"console_session_ttl", int64(l.ConsoleSessionTTL)},
		{"metadata_bytes", int64(l.MetadataBytes)},
		{"json_body_bytes", l.JSONBodyBytes},
		{"header_field_bytes", int64(l.HeaderFieldBytes)},
		{"upload_sessions_per_project", int64(l.UploadSessionsPerProject)},
		{"upload_sessions_per_user", int64(l.UploadSessionsPerUser)},
		{"upload_sessions_per_ip", int64(l.UploadSessionsPerIP)},
		{"upload_starts_per_minute_per_ip", int64(l.UploadStartsPerMinutePerIP)},
		{"upload_starts_per_second", int64(l.UploadStartsPerSecond)},
		{"upload_requests_per_ip", int64(l.UploadRequestsPerIP)},
		{"min_bytes_per_second", l.MinBytesPerSecond},
		{"list_max_results", int64(l.ListMaxResults)},
	} {
		if f.v < 1 {
			errs = append(errs, fmt.Errorf("%s must be positive", f.name))
		}
	}
	if l.ProjectBytes < 0 {
		errs = append(errs, errors.New("project_bytes must not be negative (0 is unlimited)"))
	}
	if l.ProjectBytes > 0 && l.ObjectBytes > l.ProjectBytes {
		errs = append(errs, errors.New("object_bytes must not exceed project_bytes"))
	}
	if !(l.EmptySessionTTL < l.UploadIdleTTL && l.UploadIdleTTL < l.UploadSessionTTL) {
		errs = append(errs, errors.New("empty_session_ttl, upload_idle_ttl and upload_session_ttl must increase in that order"))
	}
	if l.ConsoleSessionTTL > l.UploadSessionTTL {
		errs = append(errs, errors.New("console_session_ttl must not exceed upload_session_ttl"))
	}
	if !(l.AnonymousStagedFraction >= 0 && l.AnonymousStagedFraction <= 1) {
		errs = append(errs, errors.New("anonymous_staged_fraction must lie between 0 and 1"))
	}
	if l.ListMaxResults > 1000 {
		// The clients' page is at most 1000 (A §3.7); more would only grow one request.
		errs = append(errs, errors.New("list_max_results must not exceed 1000"))
	}
	return errors.Join(errs...)
}
