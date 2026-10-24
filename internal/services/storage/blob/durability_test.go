package blob

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The durability claims, checked on the sequence of calls and on the crash model (crashfs_test.go).

const uuidA = "ab1c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f"

func newTestDisk(t *testing.T) *Disk {
	t.Helper()
	d, err := NewDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// writeObject creates key with data and commits it.
func writeObject(t *testing.T, s Store, key string, data []byte) {
	t.Helper()
	w, err := s.Create(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
}

// The model itself: what is not synced is lost, so a check against it means something.
func TestCrashModelLosesWhatIsNotSynced(t *testing.T) {
	d := newTestDisk(t)
	r := record(t, d)
	if err := r.Mkdir("p", 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := r.OpenFile("p/x", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Write([]byte("hello"))
	f.Sync()
	if ok, _ := r.survives("p/x"); ok {
		t.Error("a file in a directory whose own entry is not synced survives")
	}
	syncDir(r, "p")
	if ok, _ := r.survives("p/x"); ok {
		t.Error("a file in a new directory survives before the root is synced")
	}
	syncDir(r, ".")
	if ok, n := r.survives("p/x"); !ok || n != 5 {
		t.Errorf("survives = %v, %d after every sync", ok, n)
	}
	f.Write([]byte(" world"))
	if _, n := r.survives("p/x"); n != 5 {
		t.Errorf("an unsynced write survives: length %d", n)
	}
}

func TestCommitSyncsTheFileThenItsDirectory(t *testing.T) {
	d := newTestDisk(t)
	r := record(t, d)
	key := "p/1/" + uuidA
	w, err := d.Create(key)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("abc"))
	w.Write([]byte("def"))
	if ok, n := r.survives("p/1/ab/" + uuidA); ok && n != 0 {
		t.Errorf("bytes survive before Commit: %d", n)
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	name := "p/1/ab/" + uuidA
	want := []string{
		"mkdir p", "sync .", // every new directory's parent synced (mkdirDurable)
		"mkdir p/1", "sync p",
		"mkdir p/1/ab", "sync p/1",
		"create " + name, // O_EXCL
		"write " + name, "write " + name,
		"sync " + name, "sync p/1/ab", // Commit: the file, then its directory
	}
	if got := r.trace(); !slices.Equal(got, want) {
		t.Errorf("Create, Write, Commit:\n got %q\nwant %q", got, want)
	}
	if ok, n := r.survives(name); !ok || n != 6 {
		t.Errorf("after Commit a power loss leaves %v, %d bytes", ok, n)
	}

	// The next file in the shard needs no directory made.
	key2 := "p/1/ab" + uuidA[2:35] + "0"
	writeObject(t, d, key2, nil)
	name2 := "p/1/ab/" + key2[4:]
	want = []string{"create " + name2, "sync " + name2, "sync p/1/ab"}
	if got := r.trace(); !slices.Equal(got, want) {
		t.Errorf("a second file:\n got %q\nwant %q", got, want)
	}
}

// A directory found on disk, made by an earlier process that may have stopped before it synced
// the parent, gets that parent synced once.
func TestMkdirDurableSyncsTheParentsOfDirectoriesItFinds(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "p", "1", "ab"), 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := NewDisk(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := record(t, d)
	writeObject(t, d, "p/1/"+uuidA, nil)
	name := "p/1/ab/" + uuidA
	want := []string{"sync .", "sync p", "sync p/1", "create " + name, "sync " + name, "sync p/1/ab"}
	if got := slices.DeleteFunc(r.trace(), func(op string) bool { return strings.HasPrefix(op, "mkdir") }); !slices.Equal(got, want) {
		t.Errorf("first write into found directories:\n got %q\nwant %q", got, want)
	}
	writeObject(t, d, "p/1/ab"+uuidA[2:35]+"0", nil)
	if got := r.trace(); len(got) != 3 {
		t.Errorf("the parents are synced again: %q", got)
	}
}

func TestMkdirDurableRefusesWhatIsNotADirectory(t *testing.T) {
	d := newTestDisk(t)
	if err := os.MkdirAll(filepath.Join(d.root, "p"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.root, "p", "1"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Create("p/1/" + uuidA); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("Create under a file: %v", err)
	}
}

// Writers racing into one new shard: each Commit that returns is durable, though another
// goroutine made the directory.
func TestConcurrentCreatesInANewShardAreDurable(t *testing.T) {
	d := newTestDisk(t)
	r := record(t, d)
	var wg sync.WaitGroup
	keys := make([]string, 16)
	for i := range keys {
		keys[i] = fmt.Sprintf("p/7/ab%s%02d", uuidA[2:34], i)
		wg.Go(func() {
			w, err := d.Create(keys[i])
			if err != nil {
				t.Error(err)
				return
			}
			w.Write([]byte(keys[i]))
			if err := w.Commit(); err != nil {
				t.Error(err)
				return
			}
			if ok, n := r.survives("p/7/ab/" + keys[i][4:]); !ok || n != int64(len(keys[i])) {
				t.Errorf("%s: Commit returned and a power loss leaves %v, %d", keys[i], ok, n)
			}
		})
	}
	wg.Wait()
}

func TestStagingIsDurableAfterSync(t *testing.T) {
	d := newTestDisk(t)
	r := record(t, d)
	key := "s/1/" + uuidA
	name := "s/1/ab/" + uuidA
	s, err := d.CreateStaging(key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := []string{"mkdir s", "sync .", "mkdir s/1", "sync s", "mkdir s/1/ab", "sync s/1", "create " + name, "sync s/1/ab"}
	if got := r.trace(); !slices.Equal(got, want) {
		t.Errorf("CreateStaging:\n got %q\nwant %q", got, want)
	}
	if ok, n := r.survives(name); !ok || n != 0 {
		t.Errorf("a new staging file: survives %v, %d", ok, n)
	}
	s.Append(strings.NewReader("chunk one"), 1<<20)
	if _, n := r.survives(name); n != 0 {
		t.Errorf("bytes not yet synced survive: %d", n)
	}
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, n := r.survives(name); n != 9 {
		t.Errorf("after Sync: %d bytes survive", n)
	}
	// The chunk a crash cut back: the length is durable after Sync too.
	s.Truncate(5)
	s.Sync()
	if _, n := r.survives(name); n != 5 {
		t.Errorf("after Truncate and Sync: %d bytes survive", n)
	}
}

// Promote, cut by a power loss after each of its calls: one of the two names holds the whole
// file every time, and the object's name alone at the end.
func TestPromoteIsDurableAtEveryStep(t *testing.T) {
	d := newTestDisk(t)
	r := record(t, d)
	skey, okey := "s/1/"+uuidA, "p/1/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f"
	sname, oname := "s/1/ab/"+uuidA, "p/1/0b/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f"
	s, err := d.CreateStaging(skey)
	if err != nil {
		t.Fatal(err)
	}
	s.Append(strings.NewReader("the whole file"), 1<<20)
	s.Sync()
	s.Close()
	r.trace()
	steps := 0
	r.onOp = func(op string) {
		steps++
		sOK, sN := r.survives(sname)
		oOK, oN := r.survives(oname)
		if !(sOK && sN == 14) && !(oOK && oN == 14) {
			t.Errorf("a power loss after %q leaves staging %v/%d and object %v/%d", op, sOK, sN, oOK, oN)
		}
	}
	if err := d.Promote(skey, okey); err != nil {
		t.Fatal(err)
	}
	r.onOp = nil
	want := []string{
		"sync " + sname, // the staging file's bytes first
		"mkdir p", "sync .", "mkdir p/1", "sync p", "mkdir p/1/0b", "sync p/1",
		"rename " + sname + " " + oname,
		"sync p/1/0b", "sync s/1/ab", // both directories
	}
	if got := r.trace(); !slices.Equal(got, want) {
		t.Errorf("Promote:\n got %q\nwant %q", got, want)
	}
	if steps != len(want) {
		t.Errorf("checked %d steps", steps)
	}
	if ok, n := r.survives(oname); !ok || n != 14 {
		t.Errorf("after Promote the object survives %v, %d", ok, n)
	}
	if ok, _ := r.survives(sname); ok {
		t.Error("after Promote the staging name survives a power loss")
	}
}

// SetInstance, cut after each call: the marker is absent or whole, never empty or cut.
func TestSetInstanceIsAtomicAndDurable(t *testing.T) {
	root := t.TempDir()
	l, err := AcquireLock(t.Context(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	dl := l.(*dirLock)
	r := &recFS{inner: dl.fs, model: newModel(t, root)}
	dl.fs = r
	id := "6f1e2d3c-4b5a-4987-8a6b-5c4d3e2f1a0b"
	r.onOp = func(op string) {
		if ok, n := r.survives(instanceName); ok && n != int64(len(id)+1) {
			t.Errorf("a power loss after %q leaves INSTANCE with %d bytes", op, n)
		}
	}
	if err := l.SetInstance(id); err != nil {
		t.Fatal(err)
	}
	want := []string{"create INSTANCE.tmp", "write INSTANCE.tmp", "sync INSTANCE.tmp", "rename INSTANCE.tmp INSTANCE", "sync ."}
	if got := r.trace(); !slices.Equal(got, want) {
		t.Errorf("SetInstance:\n got %q\nwant %q", got, want)
	}
	if ok, n := r.survives(instanceName); !ok || n != int64(len(id)+1) {
		t.Errorf("after SetInstance the marker survives %v, %d", ok, n)
	}
}

func TestDeletePrefixIsDurable(t *testing.T) {
	d := newTestDisk(t)
	writeObject(t, d, "p/3/"+uuidA, []byte("x"))
	r := record(t, d)
	if err := d.DeletePrefix(ProjectPrefix(3)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.survives("p/3"); ok {
		t.Error("the project's directory survives a power loss after DeletePrefix")
	}
}

// Every failing call makes the operation fail: no error is dropped on the way to a durability
// claim. failAt runs through every call of the operation, until one run fails none.
func TestEveryFailureIsReported(t *testing.T) {
	cases := []struct {
		name string
		prep func(t *testing.T, d *Disk)
		run  func(d *Disk) error
		// after checks the disk once the operation failed.
		after func(t *testing.T, d *Disk)
	}{
		{"Create, Write, Commit", nil, func(d *Disk) error {
			w, err := d.Create("p/1/" + uuidA)
			if err != nil {
				return err
			}
			if _, err := w.Write(bytes.Repeat([]byte("x"), 100)); err != nil {
				w.Abort()
				return err
			}
			if err := w.Commit(); err != nil {
				w.Abort()
				return err
			}
			return nil
		}, func(t *testing.T, d *Disk) {
			if _, err := os.Lstat(filepath.Join(d.root, "p/1/ab", uuidA)); err == nil {
				t.Error("Abort after a failure left the file")
			}
		}},
		{"CreateStaging, Append, Sync", nil, func(d *Disk) error {
			s, err := d.CreateStaging("s/1/" + uuidA)
			if err != nil {
				return err
			}
			defer s.Close()
			if _, err := s.Append(strings.NewReader("abc"), 3); err != nil {
				return err
			}
			return s.Sync()
		}, nil},
		{"Promote", func(t *testing.T, d *Disk) {
			s, err := d.CreateStaging("s/1/" + uuidA)
			if err != nil {
				t.Fatal(err)
			}
			s.Append(strings.NewReader("abc"), 3)
			s.Sync()
			s.Close()
		}, func(d *Disk) error {
			return d.Promote("s/1/"+uuidA, "p/1/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f")
		}, nil},
		{"DeletePrefix", func(t *testing.T, d *Disk) {
			writeObject(t, d, "p/1/"+uuidA, nil)
			writeObject(t, d, "p/1/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f", nil)
		}, func(d *Disk) error { return d.DeletePrefix("p/1/") }, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for k := 1; ; k++ {
				d := newTestDisk(t)
				if c.prep != nil {
					c.prep(t, d)
				}
				r := record(t, d)
				r.failAt = k
				err := c.run(d)
				if !r.failed {
					if err != nil {
						t.Fatalf("with no failure: %v", err)
					}
					if k < 3 {
						t.Fatalf("only %d calls", k-1)
					}
					break
				}
				if err == nil {
					t.Errorf("call %d failed (%q) and the operation succeeded", k, r.ops)
				} else if c.after != nil {
					c.after(t, d)
				}
			}
		})
	}
}
