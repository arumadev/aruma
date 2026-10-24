package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// acquireLock takes the data directory's lock; a test replaces it with a lock of its own.
var acquireLock = blob.AcquireLock

// Boot runs before the listener opens (ADR-022 §5.1): it takes the data directory's lock, binds
// the directory to this database through the INSTANCE marker and storage.instance, lists the
// files under p/ and s/ as deletion candidates, reconciles every project's quota, and warns when
// Postgres runs with fsync off (warnUnsafeDurability, durability.go).
func (s *Service) Boot(ctx context.Context) error {
	if s.lock != nil {
		return errors.New("storage: Boot runs once")
	}
	start := time.Now()
	lock, err := acquireLock(ctx, s.dataDir, func(waited time.Duration) {
		s.log.Warn("storage: waiting for the data directory's lock, which another instance holds; stop it to let this one start",
			"data_dir", s.dataDir, "waited", waited.Round(time.Second))
	})
	if err != nil {
		return fmt.Errorf("storage: lock the data directory %s: %w", s.dataDir, err)
	}
	s.metrics.LockWaitSeconds.Set(time.Since(start).Seconds())
	booted := false
	defer func() {
		if !booted {
			_ = lock.Release()
			s.lock = nil
		}
	}()
	s.lock = lock
	if err := s.bindInstance(ctx); err != nil {
		return err
	}
	// Every file there now predates every write of this process, and the lock keeps any other
	// writer out: those no row points to are orphans, which Run deletes.
	s.candidates = nil
	for _, prefix := range []string{"p/", "s/"} {
		err := s.store.Walk(prefix, func(key string, _ time.Time) error {
			s.candidates = append(s.candidates, key)
			return nil
		})
		if err != nil {
			return fmt.Errorf("storage: list %s: %w", prefix, err)
		}
	}
	if err := s.reconcileUsage(ctx); err != nil {
		return err
	}
	if err := s.warnUnsafeDurability(ctx); err != nil {
		return err
	}
	if err := s.reportGauges(ctx); err != nil {
		return err
	}
	booted = true
	return nil
}

// errStopWalk ends a walk at its first key.
var errStopWalk = errors.New("stop")

// dataDirEmpty reports whether p/ and s/ hold no file.
func (s *Service) dataDirEmpty() (bool, error) {
	for _, prefix := range []string{"p/", "s/"} {
		err := s.store.Walk(prefix, func(string, time.Time) error { return errStopWalk })
		switch {
		case errors.Is(err, errStopWalk):
			return false, nil
		case err != nil:
			return false, fmt.Errorf("storage: list %s: %w", prefix, err)
		}
	}
	return true, nil
}

// bindInstance binds the data directory to this database (ADR-022 §5.1). On a first boot the
// INSTANCE file is written durably first, then the row. A marker without its other half (a boot
// that crashed between the two, or a directory or database restored alone) is adopted only while
// p/ and s/ are empty, where nothing could be deleted by mistake; any other mismatch refuses to
// boot, saying what to restore. So two instances on two databases never share one directory:
// p/1/… exists in both, as project numbers come from each database's sequence.
func (s *Service) bindInstance(ctx context.Context) error {
	file, hasFile, err := s.lock.Instance()
	if err != nil {
		return fmt.Errorf("storage: read %s: %w", s.markerPath(), err)
	}
	row, err := s.reads.GetInstance(ctx)
	hasRow := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("storage: read storage.instance: %w", err)
	}
	if hasFile && hasRow {
		if file != row.ID.String() {
			return fmt.Errorf("storage: the data directory %s belongs to another database: its INSTANCE is %s, "+
				"and this database's storage.instance is %s. Point storage.data_dir at this database's directory, "+
				"or restore the database that goes with this directory", s.dataDir, file, row.ID)
		}
		s.instance = file
		return nil
	}
	empty, err := s.dataDirEmpty()
	if err != nil {
		return err
	}
	if empty {
		// An empty directory is adopted only when this database expects no file in it. Objects
		// or live sessions with an empty directory are a disk that is not mounted (its mount
		// point is an empty directory) or a data_dir that names the wrong one: adopting it would
		// fail every read, and put every new upload on whatever disk holds the mount point,
		// hidden again once the real disk is back.
		points, err := s.reads.PointsToFiles(ctx)
		if err != nil {
			return fmt.Errorf("storage: read storage.objects: %w", err)
		}
		if points {
			lost := "write this database's instance id (storage.instance) to it"
			if hasRow {
				lost = "write " + row.ID.String() + " to it"
			}
			return fmt.Errorf("storage: the data directory %s holds no file and no INSTANCE that matches, while this "+
				"database holds objects or uploads whose files belong in it: the disk is not mounted, or "+
				"storage.data_dir names another directory. Mount the disk or correct storage.data_dir. If its "+
				"files are lost for good, %s as %s to start with what is left", s.dataDir, lost, s.markerPath())
		}
	}
	switch {
	case hasFile && !empty:
		return fmt.Errorf("storage: the data directory %s has an INSTANCE (%s) and files, but this database has no "+
			"storage.instance row: it is another database's directory, or this database was restored without its "+
			"Storage rows. Restore the database that goes with the directory, or use an empty directory", s.dataDir, file)
	case hasRow && !empty:
		return fmt.Errorf("storage: the data directory %s holds files but no INSTANCE, while this database's "+
			"storage.instance is %s: it is not this database's directory. Restore the directory whose INSTANCE "+
			"holds %s, or its INSTANCE file", s.dataDir, row.ID, row.ID)
	case !hasFile && !hasRow && !empty:
		return fmt.Errorf("storage: the data directory %s holds files but no INSTANCE, and this database has no "+
			"storage.instance row: nothing binds them. Use an empty directory, or restore the database and the "+
			"INSTANCE file that went with it", s.dataDir)
	case hasFile:
		id, err := uuid.Parse(file)
		if err != nil {
			return fmt.Errorf("storage: %s holds %q, not an instance id", s.markerPath(), file)
		}
		s.log.Info("storage: adopting the empty data directory's INSTANCE", "instance", file)
		if err := s.insertInstance(ctx, id); err != nil {
			return err
		}
		s.instance = file
	case hasRow:
		s.log.Info("storage: writing the INSTANCE of an empty data directory from the database", "instance", row.ID)
		if err := s.lock.SetInstance(row.ID.String()); err != nil {
			return fmt.Errorf("storage: write %s: %w", s.markerPath(), err)
		}
		s.instance = row.ID.String()
	default:
		// A first boot: the file durably first, then the row.
		id := uuid.New()
		if err := s.lock.SetInstance(id.String()); err != nil {
			return fmt.Errorf("storage: write %s: %w", s.markerPath(), err)
		}
		hook("boot.instance_file")
		if err := s.insertInstance(ctx, id); err != nil {
			return err
		}
		s.instance = id.String()
	}
	return nil
}

func (s *Service) insertInstance(ctx context.Context, id uuid.UUID) error {
	if err := s.inTx(ctx, func(q *queries.Queries) error { return q.InsertInstance(ctx, id) }); err != nil {
		return fmt.Errorf("storage: write storage.instance: %w", err)
	}
	return nil
}

func (s *Service) markerPath() string { return filepath.Join(s.dataDir, "INSTANCE") }

// reconcileUsage rebuilds every project's quota account from the rows (ADR-022 §6.5): what a
// crash left reserved, by an Insert or a chunk in flight, goes.
func (s *Service) reconcileUsage(ctx context.Context) error {
	var r queries.ReconcileUsageRow
	err := s.inTx(ctx, func(q *queries.Queries) error {
		var err error
		r, err = q.ReconcileUsage(ctx)
		return err
	})
	if err != nil {
		return fmt.Errorf("storage: reconcile the quota: %w", err)
	}
	if r.Corrected > 0 {
		s.metrics.QuotaReconciledBytes.Add(float64(r.Corrected))
		s.log.Info("storage: the quota accounts were corrected", "bytes", r.Corrected, "projects", r.Projects)
	}
	return nil
}

// Close releases the data directory's lock, once the server has drained its requests and Run
// has returned. app calls it after Server.Run.
func (s *Service) Close() error {
	if s.lock == nil {
		return nil
	}
	if !s.shut() {
		s.log.Error("storage: writes still run after the shutdown wait; the data directory's lock stays until the process exits",
			"waited", closeWait)
		return fmt.Errorf("storage: writes still run after %v; the lock stays until the process exits", closeWait)
	}
	err := s.lock.Release()
	s.lock = nil
	return err
}
