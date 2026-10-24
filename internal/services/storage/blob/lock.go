package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The names of the lock and of the marker in the data directory, and the marker's temporary
// name while SetInstance writes it.
const (
	lockName     = "LOCK"
	instanceName = "INSTANCE"
	instanceTemp = "INSTANCE.tmp"
)

// lockPoll is how often AcquireLock tries again, and lockLogEvery how often it reports that it
// is still waiting (ADR-022 §5.1). The tests shorten them.
var (
	lockPoll     = 100 * time.Millisecond
	lockLogEvery = 10 * time.Second
)

// AcquireLock takes flock(LOCK_EX) on <root>/LOCK, waiting while another process holds it, and
// calling log every 10 seconds with how long it has waited, so an operator sees why a new
// instance does not answer. It gives up when ctx ends.
//
// The kernel holds the lock for the open file, so it goes when the process ends, however it
// ends, and a second AcquireLock in the same process waits too. flock is not reliable on NFS,
// which the data directory may not be on (ADR-022 §5.1). Where there is no flock it returns
// an error that wraps errors.ErrUnsupported.
func AcquireLock(ctx context.Context, root string, log func(waited time.Duration)) (Lock, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("blob: the data directory must be an absolute path")
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("blob: open the data directory: %w", err)
	}
	f, err := dir.OpenFile(lockName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		dir.Close()
		return nil, fmt.Errorf("blob: open the lock: %w", err)
	}
	fail := func(err error) (Lock, error) {
		f.Close()
		dir.Close()
		return nil, err
	}
	start := time.Now()
	next := lockLogEvery
	tick := time.NewTicker(lockPoll)
	defer tick.Stop()
	for {
		err := tryLock(f)
		if err == nil {
			return &dirLock{dir: dir, fs: rootFS{dir}, f: f}, nil
		}
		if !errors.Is(err, errLocked) {
			return fail(fmt.Errorf("blob: lock %s: %w", filepath.Join(root, lockName), err))
		}
		select {
		case <-ctx.Done():
			return fail(fmt.Errorf("blob: %s is held by another process: %w", filepath.Join(root, lockName), ctx.Err()))
		case <-tick.C:
		}
		if waited := time.Since(start); waited >= next {
			if log != nil {
				log(waited)
			}
			next += lockLogEvery
		}
	}
}

// errLocked is tryLock's answer when another open file holds the lock.
var errLocked = errors.New("locked")

// dirLock is the lock on a data directory and the access to its INSTANCE marker.
type dirLock struct {
	mu       sync.Mutex
	dir      *os.Root
	fs       fsys
	f        *os.File
	released bool
}

// maxInstance bounds the marker: an id is short, and a large file there is not one.
const maxInstance = 128

// Instance implements Lock. A marker that is empty, too long or holds anything but an id is an
// error: the directory is not in the state any write of this package leaves.
func (l *dirLock) Instance() (string, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return "", false, errReleased
	}
	f, err := l.fs.OpenFile(instanceName, os.O_RDONLY, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("blob: read %s: %w", instanceName, err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxInstance+2))
	if err != nil {
		return "", false, fmt.Errorf("blob: read %s: %w", instanceName, err)
	}
	id := strings.TrimSuffix(string(b), "\n")
	if !validInstance(id) {
		return "", false, fmt.Errorf("blob: %s holds %q, not an instance id", instanceName, b)
	}
	return id, true, nil
}

// SetInstance implements Lock: the id goes to INSTANCE.tmp, which is synced, then renamed to
// INSTANCE, then the directory synced. After a power loss at any point the marker is absent
// or whole, never empty or cut. It refuses to replace a marker: a directory is bound to one
// database for good.
func (l *dirLock) SetInstance(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return errReleased
	}
	if !validInstance(id) {
		return fmt.Errorf("blob: %q is not an instance id", id)
	}
	wrap := func(err error) error { return fmt.Errorf("blob: write %s: %w", instanceName, err) }
	if _, err := l.fs.Lstat(instanceName); err == nil {
		return wrap(fs.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return wrap(err)
	}
	f, err := l.fs.OpenFile(instanceTemp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return wrap(err)
	}
	_, err = io.WriteString(f, id+"\n")
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = l.fs.Rename(instanceTemp, instanceName)
	}
	if err == nil {
		err = syncDir(l.fs, ".")
	}
	if err != nil {
		return wrap(err)
	}
	return nil
}

// Release implements Lock. A second call does nothing.
func (l *dirLock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	err := unlock(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.dir.Close()
	return err
}

var errReleased = errors.New("blob: the lock is released")

// validInstance reports whether id may be an instance id: 1 to 128 letters, digits, '-', '_'
// or '.', so a UUID in any of its spellings, and nothing a file cut short could look like.
func validInstance(id string) bool {
	if id == "" || len(id) > maxInstance {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
