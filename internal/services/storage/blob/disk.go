package blob

import (
	"errors"
	"path/filepath"
)

// Disk is the store on a local file system, under one root directory (storage.data_dir). It
// writes nothing outside p/ and s/ but the LOCK and INSTANCE files (lock.go).
type Disk struct {
	root string
}

var _ Store = (*Disk)(nil)

// NewDisk returns the store rooted at root, an absolute path.
//
// S2-A completes it: it creates root when missing and refuses one it cannot write to.
func NewDisk(root string) (*Disk, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("blob: the data directory must be an absolute path")
	}
	return &Disk{root: filepath.Clean(root)}, nil
}

// Root returns the data directory.
func (d *Disk) Root() string { return d.root }

// Create implements Store.
func (d *Disk) Create(key string) (Writer, error) { panic("S2-A: Disk.Create") }

// Open implements Store.
func (d *Disk) Open(key string) (Reader, error) { panic("S2-A: Disk.Open") }

// Delete implements Store.
func (d *Disk) Delete(key string) error { panic("S2-A: Disk.Delete") }

// Promote implements Store: rename on the same file system, then fsync of both directories.
func (d *Disk) Promote(staging, key string) error { panic("S2-A: Disk.Promote") }

// mkdirDurable creates the missing directories of dir down from the root and fsyncs the parent
// of each one it created, so a new shard directory survives a power loss with the rows that
// point into it (ADR-022 §4). Create, CreateStaging and Promote go through it.
func (d *Disk) mkdirDurable(dir string) error { panic("S2-A: mkdirDurable") }
