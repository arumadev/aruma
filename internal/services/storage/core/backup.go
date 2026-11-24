package core

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// A backup of a server with Storage is two halves that must agree: the dump of the database and
// a copy of the data directory. A copy taken while the server runs, after the dump, can lose
// files the dump names: the blob_gc worker deletes a file gc_grace after its object was
// overwritten or deleted, and a finalized upload moves its staging file to an object key. The
// restored database then names files that are not there. Backup avoids both:
//
//   - it holds storage.backup, shared, on a session of its own from before the dump until the
//     copy is written; the gc and the purge take that lock before they delete a file
//     (TryLockFilesAgainstBackup), so no file is deleted meanwhile;
//   - the dump reads the snapshot of a transaction of Backup's (pg_dump --snapshot), and the same
//     transaction lists the files the database names (ListBackupKeys): the archive holds those
//     files and the INSTANCE marker, and nothing else. A staging file that its session has since
//     moved is copied from where it went, under its staging name: an upload appends only, so the
//     staging file's bytes are the start of the moved file, and the restored session cuts it back
//     to the bytes it had received.

// ErrBackupRunning is a deletion of files (a purge) put off while a backup copies the data
// directory; it is tried again later.
var ErrBackupRunning = errors.New("a backup is copying the data directory; the files are deleted once it ends")

// BackupReport says what a backup copied.
type BackupReport struct {
	// Files is the number of files in the archive besides INSTANCE, and Bytes their size.
	Files int
	Bytes int64
	// Missing are the keys the database names whose file was not in the data directory: the
	// restored database names them too, without their bytes.
	Missing []string
}

// Backup writes a backup of the database and of the Storage data directory dataDir that agree
// with each other: dump writes the database as the snapshot it is given sees (store.BackupSnapshot),
// and archive receives a gzip-compressed tar of INSTANCE and the files that snapshot names, by
// their paths below dataDir, created readable by its owner only. The server may run meanwhile.
// Restore the dump, extract the archive into an empty directory, and start the server on both.
func Backup(ctx context.Context, pool *pgxpool.Pool, dataDir, archive string, dump func(ctx context.Context, snapshot string) error) (BackupReport, error) {
	var rep BackupReport
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return rep, fmt.Errorf("storage: open the data directory: %w", err)
	}
	defer root.Close()
	pc, err := pool.Acquire(ctx)
	if err != nil {
		return rep, fmt.Errorf("storage: connect: %w", err)
	}
	// The session's lock goes with its connection, which is not handed back to the pool.
	conn := pc.Hijack()
	defer conn.Close(context.WithoutCancel(ctx))
	q := queries.New(conn)
	if err := q.HoldFilesForBackup(ctx); err != nil {
		return rep, fmt.Errorf("storage: hold the files: %w", err)
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return rep, fmt.Errorf("storage: begin the snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	snapshot, err := queries.New(tx).ExportSnapshot(ctx)
	if err != nil {
		return rep, fmt.Errorf("storage: export the snapshot: %w", err)
	}
	keys, err := queries.New(tx).ListBackupKeys(ctx)
	if err != nil {
		return rep, fmt.Errorf("storage: list the files: %w", err)
	}
	if err := dump(ctx, snapshot); err != nil {
		return rep, err
	}
	if err := tx.Rollback(ctx); err != nil {
		return rep, fmt.Errorf("storage: end the snapshot: %w", err)
	}
	// Still holding the files: copy them.
	w, err := newArchiveWriter(archive)
	if err != nil {
		return rep, err
	}
	defer w.abort()
	if err := w.file(root, "INSTANCE", "INSTANCE"); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return rep, fmt.Errorf("storage: the data directory %s has no INSTANCE: it is not the directory of a server that started", dataDir)
		}
		return rep, err
	}
	for _, key := range keys {
		name, err := blob.KeyFile(key)
		if err != nil {
			return rep, fmt.Errorf("storage: the database names %q: %w", key, err)
		}
		src := name
		err = w.file(root, src, name)
		if errors.Is(err, fs.ErrNotExist) && strings.HasPrefix(key, "s/") {
			// The session finished since the snapshot and moved its staging file.
			var moved *string
			moved, err = q.GetPromotedKey(ctx, key)
			if err == nil && moved != nil {
				if src, err = blob.KeyFile(*moved); err == nil {
					err = w.file(root, src, name)
				}
			} else if errors.Is(err, pgx.ErrNoRows) {
				err = fs.ErrNotExist
			}
		}
		switch {
		case errors.Is(err, fs.ErrNotExist):
			rep.Missing = append(rep.Missing, key)
		case err != nil:
			return rep, err
		}
	}
	if err := w.close(); err != nil {
		return rep, err
	}
	rep.Files, rep.Bytes = w.files, w.bytes
	return rep, nil
}

// archiveWriter writes a gzip-compressed tar to a file, with the directories of its files.
type archiveWriter struct {
	f     *os.File
	gz    *gzip.Writer
	tw    *tar.Writer
	dirs  map[string]bool
	files int
	bytes int64
	done  bool
}

func newArchiveWriter(out string) (*archiveWriter, error) {
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil { // an existing file keeps its mode otherwise
		f.Close()
		return nil, err
	}
	gz := gzip.NewWriter(f)
	return &archiveWriter{f: f, gz: gz, tw: tar.NewWriter(gz), dirs: map[string]bool{}}, nil
}

// file adds the regular file src of root under name, after the directories above name. A file
// that grows meanwhile is copied to the size it had when opened; one cut shorter is padded with
// zeros past its end, which only a staging file can be, beyond what its session received.
func (w *archiveWriter) file(root *os.Root, src, name string) error {
	f, err := root.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("storage: %s is not a regular file", src)
	}
	if err := w.parents(root, name); err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if err := w.tw.WriteHeader(hdr); err != nil {
		return err
	}
	n, err := io.Copy(w.tw, io.LimitReader(f, fi.Size()))
	if err != nil {
		return fmt.Errorf("storage: copy %s: %w", src, err)
	}
	if n < fi.Size() {
		if _, err := io.CopyN(w.tw, zeros{}, fi.Size()-n); err != nil {
			return err
		}
	}
	if name != "INSTANCE" {
		w.files++
		w.bytes += fi.Size()
	}
	return nil
}

// parents adds the directories above name, each once, with their modes in root.
func (w *archiveWriter) parents(root *os.Root, name string) error {
	dir := path.Dir(name)
	if dir == "." || w.dirs[dir] {
		return nil
	}
	if err := w.parents(root, dir); err != nil {
		return err
	}
	fi, err := root.Stat(dir)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = dir + "/"
	w.dirs[dir] = true
	return w.tw.WriteHeader(hdr)
}

// close finishes the archive and makes it durable.
func (w *archiveWriter) close() error {
	w.done = true
	err := w.tw.Close()
	if err == nil {
		err = w.gz.Close()
	}
	if err == nil {
		err = w.f.Sync()
	}
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// abort removes an archive that was not finished.
func (w *archiveWriter) abort() {
	if w.done {
		return
	}
	w.f.Close()
	_ = os.Remove(w.f.Name())
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }
