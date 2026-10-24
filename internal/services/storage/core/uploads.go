package core

import "context"

// StartUpload starts a resumable upload session bound to the principal, the bucket and the name
// (ADR-022 §6, §7), and returns its upload id. It reserves nothing: it only checks that a
// declared size fits what is left of the quota. The session expires at start + empty_session_ttl
// until its first durable byte moves expires_at to start + upload_session_ttl
// (console_session_ttl for the dashboard's). The live sessions per address count per project.
func (s *Service) StartUpload(ctx context.Context, b Bucket, p Principal, spec UploadSpec) (id string, err error) {
	panic("S2-D: StartUpload")
}

// WriteUpload applies one request to a session: append the chunk (an overlap is ignored, a gap
// is an OffsetError), and finalize when asked. p is the request's principal, nil when it has
// none; the session's own identity, bound at start, is the one that counts. Only the terminal
// errors (finalError) make the session final; their state, or the committed object's snapshot,
// answers every repeat.
func (s *Service) WriteUpload(ctx context.Context, b Bucket, name, id string, p *Principal, c Chunk) (UploadState, error) {
	panic("S2-D: WriteUpload")
}

// QueryUpload returns a session's state.
func (s *Service) QueryUpload(ctx context.Context, b Bucket, name, id string) (UploadState, error) {
	panic("S2-D: QueryUpload")
}

// CancelUpload cancels a session and frees its staging file and reservation; cancelling twice
// is not an error, cancelling a final session is ErrUploadFinal.
func (s *Service) CancelUpload(ctx context.Context, b Bucket, name, id string) (UploadState, error) {
	panic("S2-D: CancelUpload")
}
