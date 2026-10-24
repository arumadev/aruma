package blob

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// recFS is fsys over a real directory that records, in order, every call that changes the disk
// or syncs it; replays them on a model of what a power loss would leave (crashModel); and can
// fail the n-th of them. The store's durability claims are checked against the model, not
// against the file system of the machine that runs the test.
type recFS struct {
	inner fsys

	mu     sync.Mutex
	ops    []string
	calls  int // every call, the reads included
	model  *crashModel
	failAt int    // the failAt-th recorded call fails with errInjected; 0 fails none
	failOp string // the first call that records as failOp fails with errInjected
	failed bool   // a call failed
	// killAt stops the process at the killAt-th recorded call: it and every call after it, the
	// reads included, fail with errKilled, and the disk stays as it is (a new Disk on the same
	// root, with a recorder on the same model, is the next process).
	killAt int
	dead   bool
	onOp   func(op string)
}

var (
	errInjected = errors.New("injected failure")
	errKilled   = errors.New("the process is dead")
)

// record installs a recorder on d, with the model built from what is on disk now, all of it
// taken as durable.
func record(t *testing.T, d *Disk) *recFS {
	t.Helper()
	r := &recFS{inner: d.fs, model: newModel(t, d.root)}
	d.fs = r
	return r
}

// step records op and applies it to the model, or fails it; apply runs only when the real call
// succeeds, under the recorder's lock, so concurrent calls are modelled in the order they ran.
func (r *recFS) step(op string, call func() error, apply func()) error {
	r.mu.Lock()
	r.calls++
	if r.killAt > 0 && len(r.ops)+1 == r.killAt {
		r.dead = true
	}
	if r.dead {
		r.mu.Unlock()
		return errKilled
	}
	if r.failAt > 0 && len(r.ops)+1 == r.failAt || r.failOp == op && !r.failed {
		r.ops = append(r.ops, op+" (failed)")
		r.failed = true
		r.mu.Unlock()
		return errInjected
	}
	err := call()
	if err == nil {
		r.ops = append(r.ops, op)
		if apply != nil {
			apply()
		}
	}
	onOp := r.onOp
	r.mu.Unlock()
	if err == nil && onOp != nil {
		onOp(op)
	}
	return err
}

func (r *recFS) read() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.dead {
		return errKilled
	}
	return nil
}

// trace returns the recorded calls and starts a new trace.
func (r *recFS) trace() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ops := r.ops
	r.ops = nil
	return ops
}

func (r *recFS) OpenFile(name string, flag int, perm fs.FileMode) (file, error) {
	if flag&os.O_CREATE == 0 {
		if err := r.read(); err != nil {
			return nil, err
		}
		f, err := r.inner.OpenFile(name, flag, perm)
		if err != nil {
			return nil, err
		}
		r.mu.Lock()
		n := r.model.lookup(name)
		r.mu.Unlock()
		return &recFile{file: f, r: r, name: name, n: n}, nil
	}
	var f file
	var n *node
	err := r.step("create "+name, func() (err error) {
		f, err = r.inner.OpenFile(name, flag, perm)
		return err
	}, func() {
		if n = r.model.lookup(name); n == nil {
			n = r.model.add(name, &node{})
		}
		if flag&os.O_TRUNC != 0 {
			n.size = 0
		}
	})
	if err != nil {
		return nil, err
	}
	return &recFile{file: f, r: r, name: name, n: n}, nil
}

func (r *recFS) Mkdir(name string, perm fs.FileMode) error {
	return r.step("mkdir "+name, func() error { return r.inner.Mkdir(name, perm) },
		func() { r.model.add(name, &node{dir: true, kids: map[string]*node{}}) })
}

func (r *recFS) Rename(oldname, newname string) error {
	return r.step("rename "+oldname+" "+newname, func() error { return r.inner.Rename(oldname, newname) },
		func() { r.model.add(newname, r.model.drop(oldname)) })
}

func (r *recFS) Remove(name string) error {
	return r.step("remove "+name, func() error { return r.inner.Remove(name) }, func() { r.model.drop(name) })
}

func (r *recFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := r.read(); err != nil {
		return nil, err
	}
	return r.inner.ReadDir(name)
}

func (r *recFS) Lstat(name string) (fs.FileInfo, error) {
	if err := r.read(); err != nil {
		return nil, err
	}
	return r.inner.Lstat(name)
}

// recFile records the writes, truncations and syncs on one open file.
type recFile struct {
	file
	r    *recFS
	name string
	n    *node // nil for a file the model does not know (outside it, through a symlink)
}

func (f *recFile) Write(p []byte) (int, error) {
	var m int
	err := f.r.step("write "+f.name, func() (err error) {
		m, err = f.file.Write(p)
		return err
	}, func() {
		if f.n != nil {
			f.n.size += int64(m)
		}
	})
	return m, err
}

func (f *recFile) Truncate(size int64) error {
	return f.r.step("truncate "+f.name, func() error { return f.file.Truncate(size) }, func() {
		if f.n != nil {
			f.n.size = size
		}
	})
}

func (f *recFile) Sync() error {
	return f.r.step("sync "+f.name, f.file.Sync, func() {
		if f.n != nil {
			f.n.sync()
		}
	})
}

// crashModel is the file system as a power loss leaves it, at its most pessimistic within
// POSIX: a file's bytes and length survive as of its last fsync, and a directory's entries
// (files created, renamed in or out, removed; directories made) as of the last fsync of that
// directory. A name survives only if every directory on its path does.
type crashModel struct{ root *node }

type node struct {
	dir           bool
	kids, durKids map[string]*node // a directory's entries: now, and as a power loss leaves them
	size, durSize int64            // a file's length: now, and as a power loss leaves it
}

func (n *node) sync() {
	if n.dir {
		n.durKids = maps.Clone(n.kids)
	} else {
		n.durSize = n.size
	}
}

// newModel builds the model of the tree at root, all of it durable.
func newModel(t *testing.T, root string) *crashModel {
	t.Helper()
	m := &crashModel{root: &node{dir: true, kids: map[string]*node{}, durKids: map[string]*node{}}}
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		n := &node{dir: e.IsDir()}
		if n.dir {
			n.kids, n.durKids = map[string]*node{}, map[string]*node{}
		} else if fi, err := e.Info(); err == nil {
			n.size, n.durSize = fi.Size(), fi.Size()
		}
		m.add(filepath.ToSlash(rel), n)
		m.parent(filepath.ToSlash(rel)).sync()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func split(name string) (dir, base string) {
	i := strings.LastIndexByte(name, '/')
	if i < 0 {
		return ".", name
	}
	return name[:i], name[i+1:]
}

func (m *crashModel) walk(name string, durable bool) *node {
	n := m.root
	if name == "." {
		return n
	}
	for part := range strings.SplitSeq(name, "/") {
		kids := n.kids
		if durable {
			kids = n.durKids
		}
		if n = kids[part]; n == nil {
			return nil
		}
	}
	return n
}

func (m *crashModel) lookup(name string) *node { return m.walk(name, false) }

func (m *crashModel) parent(name string) *node {
	dir, _ := split(name)
	return m.lookup(dir)
}

func (m *crashModel) add(name string, n *node) *node {
	if p := m.parent(name); p != nil && n != nil {
		_, base := split(name)
		p.kids[base] = n
	}
	return n
}

func (m *crashModel) drop(name string) *node {
	p := m.parent(name)
	if p == nil {
		return nil
	}
	_, base := split(name)
	n := p.kids[base]
	delete(p.kids, base)
	return n
}

// survives reports whether name is there after a power loss now, and a file's length then.
func (r *recFS) survives(name string) (bool, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.model.walk(name, true)
	if n == nil {
		return false, 0
	}
	return true, n.durSize
}
