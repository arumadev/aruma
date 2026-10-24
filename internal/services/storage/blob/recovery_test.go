package blob

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// What a later call, or a later process, finds after an operation stopped part way.

const (
	promoteStaging = "s/1/" + uuidA
	promoteObject  = "p/1/" + uuidB
	promoteSName   = "s/1/ab/" + uuidA
	promoteOName   = "p/1/0b/" + uuidB
)

// stagedDisk returns a store with a durable 14-byte staging file at promoteStaging, and the
// recorder on it.
func stagedDisk(t *testing.T) (*Disk, *recFS) {
	t.Helper()
	d := newTestDisk(t)
	r := record(t, d)
	s, err := d.CreateStaging(promoteStaging)
	if err != nil {
		t.Fatal(err)
	}
	s.Append(strings.NewReader("the whole file"), 1<<20)
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	r.trace()
	return d, r
}

// promoted checks that the object survives a power loss whole, and the staging name does not.
func promoted(t *testing.T, r *recFS, after string) {
	t.Helper()
	if ok, n := r.survives(promoteOName); !ok || n != 14 {
		t.Errorf("%s: the object survives a power loss %v, %d bytes", after, ok, n)
	}
	if ok, _ := r.survives(promoteSName); ok {
		t.Errorf("%s: the staging name survives a power loss", after)
	}
}

// A sync after the rename fails: the file is at the key, its entry not durable. The next call
// must make it durable before it says yes, or the row it lets commit points at nothing after a
// power loss, and the sweep deletes the bytes left under the staging name.
func TestPromoteResumesAfterAFailedSync(t *testing.T) {
	for _, op := range []string{"sync p/1/0b", "sync s/1/ab"} {
		t.Run(op, func(t *testing.T) {
			d, r := stagedDisk(t)
			r.failOp = op
			if err := d.Promote(promoteStaging, promoteObject); !errors.Is(err, errInjected) {
				t.Fatalf("Promote with %q failing: %v", op, err)
			}
			if op == "sync p/1/0b" {
				if ok, _ := r.survives(promoteOName); ok {
					t.Error("the model keeps an entry that was not synced")
				}
			}
			r.trace()
			if err := d.Promote(promoteStaging, promoteObject); !errors.Is(err, ErrPromotedEarlier) {
				t.Fatalf("Promote again: %v, want ErrPromotedEarlier", err)
			}
			want := []string{"sync " + promoteOName, "sync p/1/0b", "sync s/1/ab"}
			if got := r.trace(); !slices.Equal(got, want) {
				t.Errorf("the resumed Promote:\n got %q\nwant %q", got, want)
			}
			promoted(t, r, "Promote again")
		})
	}
}

// The process dies at each call of Promote in turn; the next process, with a store of its own
// and a cold memory of directories, calls Promote again and gets a durable object.
func TestPromoteResumesAfterTheProcessDies(t *testing.T) {
	for k := 1; ; k++ {
		d, r := stagedDisk(t)
		r.killAt = k
		err := d.Promote(promoteStaging, promoteObject)
		if !r.dead {
			if err != nil {
				t.Fatal(err)
			}
			if k < 8 {
				t.Fatalf("only %d calls", k-1)
			}
			break
		}
		if !errors.Is(err, errKilled) {
			t.Fatalf("killed at call %d: %v", k, err)
		}
		next, err := NewDisk(d.root)
		if err != nil {
			t.Fatal(err)
		}
		r2 := &recFS{inner: next.fs, model: r.model}
		next.fs = r2
		// Killed before the rename, the next call does it; after it, the next call says so.
		renamed := slices.ContainsFunc(r.ops, func(op string) bool { return strings.HasPrefix(op, "rename ") })
		err = next.Promote(promoteStaging, promoteObject)
		if renamed && !errors.Is(err, ErrPromotedEarlier) || !renamed && err != nil {
			t.Errorf("the next process, after a kill at call %d (%q): %v", k, r.ops, err)
		}
		promoted(t, r2, "the next process")
		next.Close()
	}
}

// A promote that never happened is no promote: no staging file and no object is an error.
func TestPromoteOfNothingFails(t *testing.T) {
	d := newTestDisk(t)
	if err := d.Promote(promoteStaging, promoteObject); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Promote with neither file: %v", err)
	}
}

// Any file at the key with the staging file gone is "promoted earlier", never a plain yes: the
// store cannot tell it is the session's, and the caller hashes it. Its bytes are not touched.
func TestPromoteNeverTrustsAFileAtTheKey(t *testing.T) {
	d := newTestDisk(t)
	writeObject(t, d, promoteObject, []byte("someone else's"))
	err := d.Promote(promoteStaging, promoteObject)
	if !errors.Is(err, ErrPromotedEarlier) {
		t.Fatalf("Promote onto another file with no staging file: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(d.root, promoteOName)); string(b) != "someone else's" {
		t.Errorf("the file at the key holds %q", b)
	}
	// With the staging file there, a different file at the key is a reused key: both stay.
	s, _ := d.CreateStaging(promoteStaging)
	s.Append(strings.NewReader("mine"), 4)
	s.Close()
	if err := d.Promote(promoteStaging, promoteObject); !errors.Is(err, os.ErrExist) || errors.Is(err, ErrPromotedEarlier) {
		t.Errorf("Promote onto another file: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(d.root, promoteSName)); string(b) != "mine" {
		t.Errorf("the staging file holds %q", b)
	}
}

// After a power loss both names may hold the one file: the staging name goes, durably.
func TestPromoteResolvesBothNames(t *testing.T) {
	d, _ := stagedDisk(t)
	os.MkdirAll(filepath.Join(d.root, "p/1/0b"), 0o700)
	if err := os.Link(filepath.Join(d.root, promoteSName), filepath.Join(d.root, promoteOName)); err != nil {
		t.Fatal(err)
	}
	d.fs = d.fs.(*recFS).inner
	r := record(t, d) // the model: both names durable, as the power loss left them
	if err := d.Promote(promoteStaging, promoteObject); !errors.Is(err, ErrPromotedEarlier) {
		t.Fatalf("Promote with both names: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(d.root, promoteSName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staging name is still there: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(d.root, promoteOName)); string(b) != "the whole file" {
		t.Errorf("the object holds %q", b)
	}
	promoted(t, r, "Promote with both names")
}

// A directory found, not made, gets its parent synced (NewDisk's root), and so does the data
// directory once this process holds the lock (a marker written by a process that stopped
// before it synced the directory).
func TestFoundDirectoriesAreSynced(t *testing.T) {
	var synced []string
	defer func(f func(string) error) { syncPath = f }(syncPath)
	syncPath = func(p string) error { synced = append(synced, p); return nil }

	base := t.TempDir()
	root := filepath.Join(base, "a", "b")
	d, err := NewDisk(root)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if !slices.Contains(synced, base) || !slices.Contains(synced, filepath.Join(base, "a")) {
		t.Errorf("a new data directory: synced %q", synced)
	}
	synced = nil
	d, err = NewDisk(root)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if !slices.Contains(synced, filepath.Join(base, "a")) {
		t.Errorf("an existing data directory: synced %q, want its parent", synced)
	}

	var r *recFS
	defer func(f func(fsys) fsys) { wrapLockFS = f }(wrapLockFS)
	wrapLockFS = func(f fsys) fsys {
		r = &recFS{inner: f, model: newModel(t, root)}
		return r
	}
	l, err := AcquireLock(t.Context(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if got := r.trace(); !slices.Equal(got, []string{"sync ."}) {
		t.Errorf("AcquireLock: %q, want the data directory synced", got)
	}
}

// INSTANCE.tmp, INSTANCE and LOCK are opened through no symlink.
func TestLockFilesFollowNoSymlink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(outside, []byte("outside"), 0o600)
	intact := func(t *testing.T) {
		t.Helper()
		if b, _ := os.ReadFile(outside); string(b) != "outside" {
			t.Errorf("the file outside holds %q", b)
		}
	}

	t.Run("INSTANCE.tmp", func(t *testing.T) {
		root := t.TempDir()
		l, err := AcquireLock(t.Context(), root, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Release()
		for _, target := range []string{outside, "other"} { // out of the root, and in it
			os.WriteFile(filepath.Join(root, "other"), []byte("other"), 0o600)
			os.Remove(filepath.Join(root, instanceTemp))
			os.Remove(filepath.Join(root, instanceName))
			os.Symlink(target, filepath.Join(root, instanceTemp))
			if err := l.SetInstance("6f1e2d3c"); err != nil {
				t.Fatalf("SetInstance with a symlink at INSTANCE.tmp to %s: %v", target, err)
			}
			if fi, err := os.Lstat(filepath.Join(root, instanceName)); err != nil || !fi.Mode().IsRegular() {
				t.Errorf("INSTANCE: %v, %v", fi, err)
			}
			if b, _ := os.ReadFile(filepath.Join(root, "other")); string(b) != "other" {
				t.Errorf("the file inside holds %q", b)
			}
			intact(t)
		}
	})
	t.Run("INSTANCE", func(t *testing.T) {
		root := t.TempDir()
		l, err := AcquireLock(t.Context(), root, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Release()
		os.WriteFile(filepath.Join(root, "other"), []byte("6f1e2d3c\n"), 0o600)
		for _, target := range []string{outside, "other"} {
			os.Remove(filepath.Join(root, instanceName))
			os.Symlink(target, filepath.Join(root, instanceName))
			if id, ok, err := l.Instance(); err == nil {
				t.Errorf("Instance through a symlink to %s: %q, %v", target, id, ok)
			}
			if err := l.SetInstance("7a1e2d3c"); err == nil {
				t.Error("SetInstance over a symlink at INSTANCE")
			}
		}
		intact(t)
	})
	t.Run("LOCK", func(t *testing.T) {
		for _, target := range []string{outside, "other", "missing"} {
			root := t.TempDir()
			os.WriteFile(filepath.Join(root, "other"), nil, 0o600)
			os.Symlink(target, filepath.Join(root, lockName))
			if l, err := AcquireLock(t.Context(), root, nil); err == nil {
				l.Release()
				t.Errorf("AcquireLock with LOCK a symlink to %s", target)
			}
			if _, err := os.Lstat(filepath.Join(root, "missing")); err == nil {
				t.Error("AcquireLock made the target of a dangling symlink")
			}
		}
		root := t.TempDir()
		os.Mkdir(filepath.Join(root, lockName), 0o700)
		if _, err := AcquireLock(t.Context(), root, nil); err == nil {
			t.Error("AcquireLock with LOCK a directory")
		}
		intact(t)
	})
}

// A data directory on a network file system is refused, by name.
func TestNetworkFileSystemsAreRefused(t *testing.T) {
	if name, err := fsName(t.TempDir()); err != nil || networkFS(name) || name == "" {
		t.Errorf("the test's directory: %q, %v", name, err)
	}
	defer func(f func(string) (string, error)) { fsName = f }(fsName)
	for _, name := range []string{"nfs", "cifs", "smb2", "smb", "smbfs", "afpfs", "webdav"} {
		fsName = func(string) (string, error) { return name, nil }
		_, err := NewDisk(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "on "+name+", a network file system") {
			t.Errorf("a data directory on %s: %v", name, err)
		}
	}
	for _, name := range []string{"apfs", "hfs", "0xef53" /* ext4 */, "0x58465342" /* xfs */} {
		fsName = func(string) (string, error) { return name, nil }
		d, err := NewDisk(t.TempDir())
		if err != nil {
			t.Errorf("a data directory on %s: %v", name, err)
			continue
		}
		d.Close()
	}
}
