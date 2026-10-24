// Package blob stores Cloud Storage's bytes: one file per object generation on the local disk,
// and one staging file per upload session (ADR-022 §4). It imports Go's standard library alone,
// so no object name or header reaches the disk except through core.
//
// A key is never derived from an object name: p/<project_number>/<uuid> for an object's file and
// s/<project_number>/<uuid> for a session's staging file, with a fresh UUID for every
// generation. The store refuses any other key before it touches the disk, so a path traversal
// cannot be built, and two names that differ in case or in characters a file system forbids never
// meet. On disk a key's directory fans out by the UUID's first two characters
// (p/<n>/ab/<uuid>); the key itself does not change.
package blob

import (
	"crypto/rand"
	"fmt"
	"io"
	"regexp"
	"time"
)

// Store holds the bytes. Disk is its one implementation in v0.3; the interface leaves room for an
// object store, whose staging stays local (ADR-022 §4, §6.3).
type Store interface {
	// Create opens a new file at key for writing. It never writes over an existing file
	// (O_EXCL): every generation has a key of its own.
	Create(key string) (Writer, error)
	// Open opens the file at key for reading.
	Open(key string) (Reader, error)
	// Delete removes the file at key; a missing file is not an error.
	Delete(key string) error
	// Promote moves a staging file to an object key, durably on return.
	Promote(staging, key string) error
	// CreateStaging creates a session's staging file. Only an upload's start calls it.
	CreateStaging(key string) (Staging, error)
	// OpenStaging opens an existing staging file for a chunk. It never creates one, so a
	// request on a cancelled session cannot bring its file back.
	OpenStaging(key string) (Staging, error)
	// Walk calls fn for every key under prefix ("p/", "s/" or "p/<n>/"), in a stable order.
	Walk(prefix string, fn func(key string, modTime time.Time) error) error
	// DeletePrefix removes every file under prefix. It may be called again after a failure and
	// continues where it stopped (PurgeProject).
	DeletePrefix(prefix string) error
	// FreeBytes returns the space left on the store's file system.
	FreeBytes() (int64, error)
}

// Writer is a new object file. Nothing may be read at its key before Commit returns.
type Writer interface {
	io.Writer
	// Commit makes the file durable: fsync of the file, then of its directory, then close.
	Commit() error
	// Abort closes and removes the file.
	Abort() error
}

// Reader is an object file open for reading. A file deleted while open stays readable to its
// reader on POSIX.
type Reader interface {
	io.ReadSeekCloser
	Size() int64
}

// Staging is a session's staging file (ADR-022 §6.3).
type Staging interface {
	// Append copies at most limit bytes of r to the end of the file and returns how many it
	// wrote; the bytes are not durable until Sync.
	Append(r io.Reader, limit int64) (int64, error)
	// Truncate cuts the file back to n bytes, the durable length the session announced.
	Truncate(n int64) error
	// Sync makes every appended byte durable.
	Sync() error
	// Size returns the file's current length.
	Size() (int64, error)
	Close() error
}

// Lock is the exclusive hold on a data directory, kept for the life of the process (ADR-022
// §5.1). It also reads and writes the directory's INSTANCE marker, the id that binds it to one
// database.
type Lock interface {
	// Instance returns the id in <root>/INSTANCE, and false when there is no marker yet.
	Instance() (string, bool, error)
	// SetInstance writes the marker durably (fsync of the file and of the directory).
	SetInstance(id string) error
	// Release gives the lock up. Closing the process releases it as well.
	Release() error
}

// keyPattern is the only form of key the store accepts.
var keyPattern = regexp.MustCompile(`^(p|s)/[0-9]+/[0-9a-f-]{36}$`)

// ValidKey reports whether key is an object or staging key the store accepts.
func ValidKey(key string) bool { return keyPattern.MatchString(key) }

// ObjectKey returns a fresh key for an object generation's file in project.
func ObjectKey(project int64) string { return fmt.Sprintf("p/%d/%s", project, newUUID()) }

// StagingKey returns a fresh key for an upload session's staging file in project.
func StagingKey(project int64) string { return fmt.Sprintf("s/%d/%s", project, newUUID()) }

// ProjectPrefix returns the prefix of a project's object files, and StagingPrefix of its
// staging files, for Walk and DeletePrefix.
func ProjectPrefix(project int64) string { return fmt.Sprintf("p/%d/", project) }

// StagingPrefix returns the prefix of a project's staging files.
func StagingPrefix(project int64) string { return fmt.Sprintf("s/%d/", project) }

// newUUID returns a random (version 4) UUID in its lower-case text form, from crypto/rand.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never fails on supported platforms
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
