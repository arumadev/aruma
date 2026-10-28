package api

import (
	"context"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/core"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// UsageHours returns the usage of the project of database, an admin's alone, an hour a row from
// since's hour on (core.Usage.Hours); false when the server counts none.
func (s *Service) UsageHours(ctx context.Context, c Call, database string, since time.Time) ([]core.UsageHour, bool, error) {
	a, err := s.admitAs(ctx, c, &pb.ListenRequest{Database: database}, databaseOf(database), true)
	if err != nil {
		return nil, false, err
	}
	if s.o.Usage == nil {
		return nil, false, nil
	}
	hours, err := s.o.Usage.Hours(ctx, a.db.Project, since)
	if err != nil {
		return nil, false, s.fail(ctx, err)
	}
	return hours, true, nil
}
