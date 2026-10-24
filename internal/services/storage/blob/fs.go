package blob

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// fsys is every call the store makes on the file system, with paths relative to the root
// ("p/1/ab/<uuid>", "." for the root itself). The store goes through it alone, so the tests
// can record the order of the calls, replay them on a model of what survives a power loss,
// and fail any one of them.
type fsys interface {
	OpenFile(name string, flag int, perm fs.FileMode) (file, error)
	Mkdir(name string, perm fs.FileMode) error
	Rename(oldname, newname string) error
	Remove(name string) error
	ReadDir(name string) ([]fs.DirEntry, error) // sorted by name
	Lstat(name string) (fs.FileInfo, error)
}

// file is an open file or directory.
type file interface {
	io.Reader
	io.Writer
	io.Seeker
	io.Closer
	Sync() error
	Truncate(size int64) error
	Stat() (fs.FileInfo, error)
}

// rootFS is fsys on an os.Root: no name, whatever it holds and whatever symlink it meets,
// resolves outside the root ("path escapes from parent").
//
// On darwin (*os.File).Sync is fcntl(F_FULLFSYNC), which asks the drive to flush its cache
// too, where fsync(2) there does not; on Linux it is fsync(2). Both apply to directories.
type rootFS struct{ root *os.Root }

func (r rootFS) OpenFile(name string, flag int, perm fs.FileMode) (file, error) {
	f, err := r.root.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err // a nil *os.File must not become a non-nil file
	}
	return f, nil
}

func (r rootFS) Mkdir(name string, perm fs.FileMode) error { return r.root.Mkdir(name, perm) }
func (r rootFS) Rename(oldname, newname string) error      { return r.root.Rename(oldname, newname) }
func (r rootFS) Remove(name string) error                  { return r.root.Remove(name) }
func (r rootFS) Lstat(name string) (fs.FileInfo, error)    { return r.root.Lstat(name) }

func (r rootFS) ReadDir(name string) ([]fs.DirEntry, error) {
	f, err := r.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1) // in directory order, unlike os.ReadDir
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, err
}

// syncDir makes the entries of dir durable: the files created in it, renamed into or out of it,
// and removed from it ("Calling fsync() does not necessarily ensure that the entry in the
// directory containing the file has also reached disk", fsync(2)).
func syncDir(fsys fsys, dir string) error {
	f, err := fsys.OpenFile(dir, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// regular checks that the file f opened at name is a regular file reached by no symlink at its
// last element, and returns its FileInfo. os.Root follows a symlink that stays inside the
// root, and the store follows none of its own.
func regular(fsys fsys, f file, name string) (fs.FileInfo, error) {
	got, err := f.Stat()
	if err != nil {
		return nil, err
	}
	at, err := fsys.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !at.Mode().IsRegular() || !os.SameFile(got, at) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: errNotRegular}
	}
	return got, nil
}

var errNotRegular = errors.New("not a regular file")
