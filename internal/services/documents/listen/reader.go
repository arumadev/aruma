package listen

import (
	"context"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/core"
)

// Reader is what the hub reads of the store (ADR-026 §2): *core.PGStore implements it.
type Reader interface {
	// Head returns the database's last commit: its seq and commit_time.
	Head(ctx context.Context, db core.DB) (int64, time.Time, error)
	// LoggedCommits returns the commits after seq after, at most limit of them, from the
	// changelog: consecutive, whole and committed, with the last commit's seq and time in the
	// same snapshot. A first commit past after+1, or none under a head past after, means
	// retention pruned the rows between.
	LoggedCommits(ctx context.Context, db core.DB, after int64, limit int) (int64, time.Time, []core.LoggedCommit, error)
	// Scan and BatchGet read a target's result at a read_time: Store's.
	Scan(ctx context.Context, db core.DB, r core.ScanRequest) (core.ListResult, error)
	BatchGet(ctx context.Context, db core.DB, paths []core.Path, o core.ReadOptions) ([]core.Document, time.Time, error)
	// ChangesSince calls each with the documents of a scope that the commits after a time, up
	// to a seq, changed, as they were then and are at that seq; inWindow is false when the
	// changelog or the read_time window no longer holds them all.
	ChangesSince(ctx context.Context, db core.DB, r core.ChangesRequest, each func(before, after core.Document) error) (inWindow bool, err error)
}

var _ Reader = (*core.PGStore)(nil)
