package blob

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Disk is the store on a local file system, under one root directory (storage.data_dir). It
// writes nothing outside p/ and s/ but the LOCK and INSTANCE files (lock.go).
//
// Every name it opens goes through an os.Root on the data directory, so no key, and no
// symlink planted inside the directory, reaches a file outside it; and the store follows no
// symlink of its own: one where it expects its directory or its file is refused.
type Disk struct {
	root string
	dir  *os.Root
	fs   fsys

	// mu guards durable: the directories whose entry in their parent this process has made
	// durable, or found and synced its parent once (mkdirDurable).
	mu      sync.Mutex
	durable map[string]bool
}

var _ Store = (*Disk)(nil)

// maxDurableDirs bounds the directories mkdirDurable remembers; past it, it forgets them all
// and syncs a parent again on its next use. A project uses at most 2 + 2×256 of them.
const maxDurableDirs = 1 << 16

// NewDisk returns the store rooted at root, an absolute path. It creates root when missing,
// with the parent of each directory it creates synced; syncs root's parent when it finds root
// (as mkdirDurable does with a directory it finds); and refuses one it cannot write to.
//
// It refuses a system other than darwin and Linux (ErrUnsupported: v0.3 runs Storage there
// alone), and a data directory on a network file system (NFS, SMB, AFP, WebDAV), where flock
// and the order of the syncs are not to be relied on (ADR-022 §5.1).
func NewDisk(root string) (*Disk, error) {
	if err := supportedPlatform(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(root) {
		return nil, errors.New("blob: the data directory must be an absolute path")
	}
	root = filepath.Clean(root)
	if err := mkdirAllDurable(root); err != nil {
		return nil, fmt.Errorf("blob: create the data directory: %w", err)
	}
	name, err := fsName(root)
	if err != nil {
		return nil, fmt.Errorf("blob: the file system of %s: %w", root, err)
	}
	if networkFS(name) {
		return nil, fmt.Errorf("blob: the data directory %s is on %s, a network file system; Storage needs a local one (ADR-022 §5.1)", root, name)
	}
	if err := writable(root); err != nil {
		return nil, fmt.Errorf("blob: the data directory %s is not writable: %w", root, err)
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("blob: open the data directory: %w", err)
	}
	return &Disk{root: root, dir: dir, fs: rootFS{dir}, durable: map[string]bool{}}, nil
}

// Root returns the data directory.
func (d *Disk) Root() string { return d.root }

// Close releases the data directory. The store is unusable after it.
func (d *Disk) Close() error { return d.dir.Close() }

// Create implements Store. The file is new (O_EXCL) and is durable when Commit returns.
func (d *Disk) Create(key string) (Writer, error) {
	dir, name, err := keyPath(key, "p")
	if err != nil {
		return nil, err
	}
	if err := d.mkdirDurable(dir); err != nil {
		return nil, err
	}
	f, err := d.fs.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &writer{fs: d.fs, f: f, dir: dir, name: name}, nil
}

// Open implements Store, for an object's file or a staging file (to hash it again after a
// crash cut it back, ADR-022 §6.3). A missing file is an error that wraps fs.ErrNotExist.
func (d *Disk) Open(key string) (Reader, error) {
	_, name, err := keyPath(key, "ps")
	if err != nil {
		return nil, err
	}
	f, err := d.fs.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	fi, err := regular(d.fs, f, name)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &reader{f: f, size: fi.Size()}, nil
}

// Delete implements Store, for an object's file or a staging file. The removal is not durable on
// return: after a power loss the file may be back, and no row points to it then, so the boot
// sweep removes it (ADR-022 §5.2). A symlink at the key is removed, never followed.
func (d *Disk) Delete(key string) error {
	_, name, err := keyPath(key, "ps")
	if err != nil {
		return err
	}
	if err := d.fs.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Promote implements Store: the staging file's bytes are synced, its name renamed to the
// object key on the same file system, then both directories synced. It never replaces a file
// at key, and both keys must be of the same project. After a power loss at any point, one of
// the two names holds the whole file.
//
// It may be called again after it failed, or after the process stopped, at any step: the
// session's blob_key, written before the first call, names the key (ADR-022 §5). When the
// staging file is gone and a file is at key, the rename happened and its entry may not be
// durable yet; and after a power loss both names may hold the one file. Promote then makes the
// file at key durable (and drops the staging name), and returns an error that wraps
// ErrPromotedEarlier, never nil: it cannot tell that file is the session's, so the caller
// hashes it against the session before it commits a row that points to it. A different file
// at key with the staging file still there is fs.ErrExist, and both stay as they are.
func (d *Disk) Promote(staging, key string) error {
	sdir, sname, err := keyPath(staging, "s")
	if err != nil {
		return err
	}
	ddir, dname, err := keyPath(key, "p")
	if err != nil {
		return err
	}
	if keyProject(staging) != keyProject(key) {
		return fmt.Errorf("blob: promote %s to %s: the keys are of different projects", staging, key)
	}
	wrap := func(err error) error { return fmt.Errorf("blob: promote %s to %s: %w", staging, key, err) }
	// Staging.Sync made the appended bytes durable, but not a Truncate after it: this sync
	// keeps "durable on return" from depending on the caller.
	err = d.syncFile(sname)
	if errors.Is(err, fs.ErrNotExist) {
		// An earlier call renamed it: finish that call's syncs.
		if err := d.syncFile(dname); errors.Is(err, fs.ErrNotExist) {
			return wrap(fs.ErrNotExist)
		} else if err != nil {
			return wrap(err)
		}
		if err := d.syncPromoted(sdir, ddir, wrap); err != nil {
			return err
		}
		return wrap(ErrPromotedEarlier)
	}
	if err != nil {
		return wrap(err)
	}
	if err := d.mkdirDurable(ddir); err != nil {
		return wrap(err)
	}
	// rename(2) replaces its target. The key is fresh and the lock makes this process the only
	// writer, so the check only turns a reused key (a bug) into an error instead of a lost file;
	// or finds the one file under both names, which a power loss after a rename may leave.
	if at, err := d.fs.Lstat(dname); err == nil {
		from, err := d.fs.Lstat(sname)
		if err != nil {
			return wrap(err)
		}
		if !at.Mode().IsRegular() || !os.SameFile(at, from) {
			return wrap(fs.ErrExist)
		}
		if err := d.fs.Remove(sname); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return wrap(err)
		}
		if err := d.syncPromoted(sdir, ddir, wrap); err != nil {
			return err
		}
		return wrap(ErrPromotedEarlier)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return wrap(err)
	}
	if err := d.fs.Rename(sname, dname); err != nil {
		return wrap(err)
	}
	return d.syncPromoted(sdir, ddir, wrap)
}

// syncPromoted makes a rename from sdir into ddir durable: ddir's chain of directories (found
// ones get their parents synced once per process), ddir's entries, then sdir's. A missing
// sdir has no entry left to sync.
func (d *Disk) syncPromoted(sdir, ddir string, wrap func(error) error) error {
	if err := d.mkdirDurable(ddir); err != nil {
		return wrap(err)
	}
	if err := syncDir(d.fs, ddir); err != nil {
		return wrap(err)
	}
	if err := syncDir(d.fs, sdir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return wrap(err)
	}
	return nil
}

// syncFile syncs the regular file at name.
func (d *Disk) syncFile(name string) error {
	f, err := d.fs.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	if _, err := regular(d.fs, f, name); err != nil {
		f.Close()
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// mkdirDurable creates the missing directories of dir down from the root and fsyncs the parent
// of each one it created, so a new shard directory survives a power loss with the rows that
// point into it (ADR-022 §4). Create, CreateStaging and Promote go through it.
//
// A directory it finds rather than creates gets its parent synced as well, once per process: a
// process that stopped (or a goroutine still running) between its mkdir and that sync may have
// left it visible but not durable. A directory that is not one (a file, a symlink) is refused.
func (d *Disk) mkdirDurable(dir string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.durable[dir] {
		return nil
	}
	parent := "."
	for i := 0; i <= len(dir); i++ {
		if i < len(dir) && dir[i] != '/' {
			continue
		}
		sub := dir[:i]
		if !d.durable[sub] {
			if err := d.fs.Mkdir(sub, 0o700); errors.Is(err, fs.ErrExist) {
				fi, err := d.fs.Lstat(sub)
				if err != nil {
					return err
				}
				if !fi.IsDir() {
					return &fs.PathError{Op: "mkdir", Path: sub, Err: errNotDir}
				}
			} else if err != nil {
				return err
			}
			if err := syncDir(d.fs, parent); err != nil {
				return err
			}
			if len(d.durable) >= maxDurableDirs {
				clear(d.durable)
			}
			d.durable[sub] = true
		}
		parent = sub
	}
	return nil
}

// forgetDirs drops what mkdirDurable remembers, after DeletePrefix removed directories.
func (d *Disk) forgetDirs() {
	d.mu.Lock()
	clear(d.durable)
	d.mu.Unlock()
}

var (
	errInvalidKey = errors.New("not a valid key")
	errNotDir     = errors.New("not a directory")
	errClosed     = errors.New("blob: the file is closed")
)

// keyPath checks key, of one of kinds ("p", "s" or "ps"), and returns its file's directory and
// name below the root: p/<n>/<uuid> is at p/<n>/<uuid[:2]>/<uuid>. Nothing touches the disk
// for a key it refuses.
func keyPath(key, kinds string) (dir, name string, err error) {
	if !ValidKey(key) || !strings.Contains(kinds, key[:1]) {
		return "", "", fmt.Errorf("blob: %q: %w", key, errInvalidKey)
	}
	i := strings.LastIndexByte(key, '/')
	dir = key[:i+1] + key[i+1:i+3]
	return dir, dir + key[i:], nil
}

// keyProject returns the project number of a valid key, as text.
func keyProject(key string) string { return key[2:strings.LastIndexByte(key, '/')] }

// mkdirAllDurable is mkdirDurable for the data directory itself, before its os.Root exists:
// the parent of path is synced whether path was made or found, since a process that stopped
// between its mkdir and that sync may have left it visible but not durable.
func mkdirAllDurable(path string) error {
	parent := filepath.Dir(path)
	fi, err := os.Stat(path)
	switch {
	case err == nil:
		if !fi.IsDir() {
			return &fs.PathError{Op: "mkdir", Path: path, Err: errNotDir}
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	default:
		if parent != path {
			if err := mkdirAllDurable(parent); err != nil {
				return err
			}
		}
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	if parent == path {
		return nil // the file system's root
	}
	return syncPath(parent)
}

// syncPath syncs the directory at path, outside any os.Root. The tests record its calls.
var syncPath = func(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// writer is a new object file. It is not safe for concurrent use.
type writer struct {
	fs        fsys
	f         file
	dir, name string
	closed    bool
	committed bool
}

func (w *writer) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errClosed
	}
	return w.f.Write(p)
}

// Commit syncs the file, then its directory, then closes it. On an error the file is closed
// and stays on disk until Abort removes it.
func (w *writer) Commit() error {
	if w.closed {
		return errClosed
	}
	w.closed = true
	err := w.f.Sync()
	if err == nil {
		err = syncDir(w.fs, w.dir)
	}
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("blob: commit %s: %w", w.name, err)
	}
	w.committed = true
	return nil
}

// Abort closes and removes the file. After a Commit that succeeded it does nothing, so a
// deferred Abort is safe; the caller removes a committed file with Store.Delete.
func (w *writer) Abort() error {
	if w.committed {
		return nil
	}
	if !w.closed {
		w.closed = true
		w.f.Close()
	}
	if err := w.fs.Remove(w.name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// reader is an object file open for reading, with its size when opened.
type reader struct {
	f    file
	size int64
}

func (r *reader) Read(p []byte) (int, error)                   { return r.f.Read(p) }
func (r *reader) Seek(offset int64, whence int) (int64, error) { return r.f.Seek(offset, whence) }
func (r *reader) Close() error                                 { return r.f.Close() }
func (r *reader) Size() int64                                  { return r.size }
