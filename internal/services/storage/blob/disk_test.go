package blob

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const uuidB = "0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f"

func TestNewDisk(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a", "b", "storage")
	d, err := NewDisk(root + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Root() != root {
		t.Errorf("Root = %q", d.Root())
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("the data directory: %v, %v", fi, err)
	}
	// NewDisk writes nothing in it: LOCK and INSTANCE are the lock's, p/ and s/ come with the first file.
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("NewDisk wrote %v", entries)
	}
	again, err := NewDisk(root)
	if err != nil {
		t.Fatalf("an existing directory: %v", err)
	}
	again.Close()

	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	if _, err := NewDisk(file); err == nil {
		t.Error("a file is accepted as the data directory")
	}
	if os.Geteuid() != 0 { // root writes anywhere
		ro := filepath.Join(t.TempDir(), "ro")
		os.Mkdir(ro, 0o500)
		if _, err := NewDisk(ro); err == nil || !strings.Contains(err.Error(), "not writable") {
			t.Errorf("a directory it cannot write to: %v", err)
		}
		if _, err := NewDisk(filepath.Join(ro, "storage")); err == nil {
			t.Error("a directory it cannot create is accepted")
		}
	}
}

func TestCreateCommitOpen(t *testing.T) {
	d := newTestDisk(t)
	key := ObjectKey(482915736204)
	w, err := d.Create(key)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Abort() // nothing after a Commit that succeeded
	w.Write([]byte("hello, "))
	w.Write([]byte("world"))
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("more")); err == nil {
		t.Error("Write after Commit")
	}
	if err := w.Commit(); err == nil {
		t.Error("a second Commit")
	}
	if err := w.Abort(); err != nil {
		t.Error(err)
	}
	r, err := d.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Size() != 12 {
		t.Errorf("Size = %d", r.Size())
	}
	r.Seek(7, io.SeekStart)
	if b, _ := io.ReadAll(r); string(b) != "world" {
		t.Errorf("read %q after Seek", b)
	}
	// On disk the key fans out by the UUID's first two characters.
	u := key[len("p/482915736204/"):]
	if _, err := os.Stat(filepath.Join(d.root, "p", "482915736204", u[:2], u)); err != nil {
		t.Error(err)
	}
	if _, err := d.Open(ObjectKey(482915736204)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open of a missing file: %v", err)
	}
}

// A file deleted while a reader has it open stays readable to that reader (ADR-022 §5).
func TestAnOpenFileOutlivesItsDeletion(t *testing.T) {
	d := newTestDisk(t)
	key := ObjectKey(1)
	writeObject(t, d, key, []byte("old generation"))
	r, err := d.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := d.Delete(key); err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(r); err != nil || string(b) != "old generation" {
		t.Errorf("read %q, %v", b, err)
	}
}

func TestCreateNeverOverwrites(t *testing.T) {
	d := newTestDisk(t)
	key := "p/1/" + uuidA
	writeObject(t, d, key, []byte("first"))
	if _, err := d.Create(key); !errors.Is(err, fs.ErrExist) {
		t.Errorf("Create over an existing file: %v", err)
	}
	// Nor through a symlink at the key, even a dangling one.
	outside := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(outside, []byte("not the store's"), 0o600)
	key2 := "p/1/ab" + uuidA[2:35] + "0"
	os.Symlink(outside, filepath.Join(d.root, "p/1/ab", key2[4:]))
	key3 := "p/1/ab" + uuidA[2:35] + "1"
	os.Symlink(filepath.Join(t.TempDir(), "nothing"), filepath.Join(d.root, "p/1/ab", key3[4:]))
	for _, k := range []string{key2, key3} {
		if _, err := d.Create(k); !errors.Is(err, fs.ErrExist) {
			t.Errorf("Create over a symlink: %v", err)
		}
	}
	if b, _ := os.ReadFile(outside); string(b) != "not the store's" {
		t.Errorf("the symlink's target holds %q", b)
	}
	r, _ := d.Open(key)
	defer r.Close()
	if b, _ := io.ReadAll(r); string(b) != "first" {
		t.Errorf("the file holds %q", b)
	}
}

func TestAbortRemovesTheFile(t *testing.T) {
	d := newTestDisk(t)
	key := ObjectKey(1)
	w, err := d.Create(key)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("partial"))
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
	if err := w.Abort(); err != nil {
		t.Errorf("a second Abort: %v", err)
	}
	if _, err := d.Open(key); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the file is still there: %v", err)
	}
	if err := w.Commit(); err == nil {
		t.Error("Commit after Abort")
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	d := newTestDisk(t)
	o, s := ObjectKey(1), StagingKey(1)
	writeObject(t, d, o, nil)
	st, err := d.CreateStaging(s)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	for _, k := range []string{o, s} {
		for range 2 {
			if err := d.Delete(k); err != nil {
				t.Errorf("Delete(%s): %v", k, err)
			}
		}
	}
	if err := d.Delete(ObjectKey(2)); err != nil {
		t.Errorf("Delete of a key never written: %v", err)
	}
}

func TestPromote(t *testing.T) {
	d := newTestDisk(t)
	skey, okey := StagingKey(5), ObjectKey(5)
	s, err := d.CreateStaging(skey)
	if err != nil {
		t.Fatal(err)
	}
	s.Append(strings.NewReader("uploaded bytes"), 1<<20)
	s.Sync()
	s.Close()
	if err := d.Promote(skey, okey); err != nil {
		t.Fatal(err)
	}
	r, err := d.Open(okey)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(r); string(b) != "uploaded bytes" {
		t.Errorf("the object holds %q", b)
	}
	r.Close()
	if _, err := d.OpenStaging(skey); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the staging file is still there: %v", err)
	}
	// A second Promote, as a finalize after a crash would make it: the staging file is gone.
	if err := d.Promote(skey, ObjectKey(5)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Promote of a missing staging file: %v", err)
	}

	// Never over an existing object file.
	skey2 := StagingKey(5)
	s, _ = d.CreateStaging(skey2)
	s.Append(strings.NewReader("other"), 5)
	s.Close()
	if err := d.Promote(skey2, okey); !errors.Is(err, fs.ErrExist) {
		t.Errorf("Promote over an object: %v", err)
	}
	r, _ = d.Open(okey)
	if b, _ := io.ReadAll(r); string(b) != "uploaded bytes" {
		t.Errorf("the object holds %q", b)
	}
	r.Close()
	if s, err := d.OpenStaging(skey2); err != nil {
		t.Errorf("the staging file is gone: %v", err)
	} else {
		s.Close()
	}

	for _, c := range [][2]string{
		{okey, ObjectKey(5)},   // from an object
		{skey2, StagingKey(5)}, // to a staging key
		{skey2, ObjectKey(6)},  // to another project
		{"s/5/../" + uuidA, ObjectKey(5)},
	} {
		if err := d.Promote(c[0], c[1]); err == nil {
			t.Errorf("Promote(%s, %s) succeeded", c[0], c[1])
		}
	}
}

func TestStaging(t *testing.T) {
	d := newTestDisk(t)
	key := StagingKey(9)
	s, err := d.CreateStaging(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateStaging(key); !errors.Is(err, fs.ErrExist) {
		t.Errorf("CreateStaging over an existing file: %v", err)
	}
	if n, err := s.Append(strings.NewReader("0123456789"), 4); n != 4 || err != nil {
		t.Errorf("Append with a limit: %d, %v", n, err)
	}
	if n, err := s.Append(strings.NewReader("abc"), 100); n != 3 || err != nil {
		t.Errorf("Append to EOF: %d, %v", n, err)
	}
	if n, err := s.Append(strings.NewReader("abc"), 0); n != 0 || err != nil {
		t.Errorf("Append(0): %d, %v", n, err)
	}
	// A request cut in the middle: what arrived stays.
	cut := io.MultiReader(strings.NewReader("xyz"), errReader{io.ErrUnexpectedEOF})
	if n, err := s.Append(cut, 100); n != 3 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Append of a cut body: %d, %v", n, err)
	}
	// More than one buffer.
	big := strings.Repeat("b", 3*appendBuffer+17)
	if n, err := s.Append(strings.NewReader(big), int64(len(big))); n != int64(len(big)) || err != nil {
		t.Errorf("Append of %d bytes: %d, %v", len(big), n, err)
	}
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Size(); n != int64(10+len(big)) {
		t.Errorf("Size = %d", n)
	}
	if err := s.Truncate(7); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// The next chunk's request opens it and appends after what is durable.
	s, err = d.OpenStaging(key)
	if err != nil {
		t.Fatal(err)
	}
	s.Append(strings.NewReader("!"), 1)
	s.Close()
	r, _ := d.Open(key)
	if b, _ := io.ReadAll(r); string(b) != "0123abc!" {
		t.Errorf("the staging file holds %q", b)
	}
	r.Close()
}

func TestOpenStagingNeverCreates(t *testing.T) {
	d := newTestDisk(t)
	key := StagingKey(9)
	if _, err := d.OpenStaging(key); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("OpenStaging of a missing file: %v", err)
	}
	// A cancelled session: its file is gone, and a late chunk does not bring it back.
	s, _ := d.CreateStaging(key)
	s.Close()
	d.Delete(key)
	if _, err := d.OpenStaging(key); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("OpenStaging after the session's file was removed: %v", err)
	}
	u := key[len("s/9/"):]
	if _, err := os.Lstat(filepath.Join(d.root, "s", "9", u[:2], u)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the file is back: %v", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// Keys the store refuses: refused before a single call reaches the disk.
func TestForeignKeysNeverReachTheDisk(t *testing.T) {
	d := newTestDisk(t)
	r := record(t, d)
	bad := []string{
		"", "p/", "p/1/", "../" + uuidA, "p/1/../" + uuidA[3:], "p/1/" + uuidA + "/x", "/p/1/" + uuidA,
		"p/1/" + strings.ToUpper(uuidA), "p/1/" + uuidA[:35] + "\x00", "p/1/" + uuidA + "\n",
		"x/1/" + uuidA, "p//" + uuidA, "p/1/ab/" + uuidA, "p/1/" + uuidA[:35] + "/", "p\\1\\" + uuidA,
	}
	for _, k := range bad {
		var errs []error
		_, err := d.Create(k)
		errs = append(errs, err)
		_, err = d.Open(k)
		errs = append(errs, err)
		errs = append(errs, d.Delete(k))
		_, err = d.CreateStaging(k)
		errs = append(errs, err)
		_, err = d.OpenStaging(k)
		errs = append(errs, err)
		errs = append(errs, d.Promote(k, ObjectKey(1)), d.Promote(StagingKey(1), k))
		for i, err := range errs {
			if !errors.Is(err, errInvalidKey) {
				t.Errorf("call %d with %q: %v", i, k, err)
			}
		}
	}
	// A valid key of the wrong kind.
	for _, err := range []error{
		func() error { _, err := d.Create(StagingKey(1)); return err }(),
		func() error { _, err := d.CreateStaging(ObjectKey(1)); return err }(),
		func() error { _, err := d.OpenStaging(ObjectKey(1)); return err }(),
	} {
		if !errors.Is(err, errInvalidKey) {
			t.Errorf("a key of the wrong kind: %v", err)
		}
	}
	if r.calls != 0 {
		t.Errorf("refused keys made %d calls on the disk: %q", r.calls, r.ops)
	}
}

// No key and no symlink inside the data directory reaches a file outside it.
func TestSymlinksInsideTheRootLeadNowhere(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, uuidA)
	os.WriteFile(victim, []byte("outside"), 0o600)
	check := func(t *testing.T) {
		t.Helper()
		if b, err := os.ReadFile(victim); err != nil || string(b) != "outside" {
			t.Errorf("the file outside: %q, %v", b, err)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 1 {
			t.Errorf("the directory outside holds %v", entries)
		}
	}
	walked := func(t *testing.T, d *Disk, prefix string) []string {
		t.Helper()
		var keys []string
		if err := d.Walk(prefix, func(k string, _ time.Time) error { keys = append(keys, k); return nil }); err != nil {
			t.Fatal(err)
		}
		return keys
	}

	t.Run("file", func(t *testing.T) {
		d := newTestDisk(t)
		writeObject(t, d, "p/1/"+uuidB, nil) // makes p/1/0b
		os.MkdirAll(filepath.Join(d.root, "p/1/ab"), 0o700)
		os.Symlink(victim, filepath.Join(d.root, "p/1/ab", uuidA))
		if _, err := d.Open("p/1/" + uuidA); err == nil {
			t.Error("Open follows a symlink out of the root")
		}
		if _, err := d.Create("p/1/" + uuidA); !errors.Is(err, fs.ErrExist) {
			t.Errorf("Create: %v", err)
		}
		if keys := walked(t, d, "p/"); len(keys) != 1 {
			t.Errorf("Walk reports %q", keys)
		}
		if err := d.Delete("p/1/" + uuidA); err != nil {
			t.Error(err)
		}
		check(t)
	})
	t.Run("file inside", func(t *testing.T) {
		// A symlink to another of the store's files is not followed either.
		d := newTestDisk(t)
		writeObject(t, d, "p/1/"+uuidB, []byte("b"))
		os.MkdirAll(filepath.Join(d.root, "p/1/ab"), 0o700)
		os.Symlink("../0b/"+uuidB, filepath.Join(d.root, "p/1/ab", uuidA))
		if _, err := d.Open("p/1/" + uuidA); !errors.Is(err, errNotRegular) {
			t.Errorf("Open of a symlink inside the root: %v", err)
		}
		s := "s/1/" + uuidA
		os.MkdirAll(filepath.Join(d.root, "s/1/ab"), 0o700)
		os.Symlink("../../../p/1/0b/"+uuidB, filepath.Join(d.root, "s/1/ab", uuidA))
		if _, err := d.OpenStaging(s); !errors.Is(err, errNotRegular) {
			t.Errorf("OpenStaging of a symlink: %v", err)
		}
		if err := d.Promote(s, ObjectKey(1)); !errors.Is(err, errNotRegular) {
			t.Errorf("Promote of a symlink: %v", err)
		}
	})
	for _, dir := range []string{"p", "p/1", "p/1/ab"} {
		t.Run("directory "+dir, func(t *testing.T) {
			d := newTestDisk(t)
			os.MkdirAll(filepath.Join(d.root, filepath.Dir(dir)), 0o700)
			os.Symlink(outside, filepath.Join(d.root, dir))
			if _, err := d.Create("p/1/" + uuidA); err == nil {
				t.Error("Create through a symlinked directory")
			}
			if _, err := d.Open("p/1/" + uuidA); err == nil {
				t.Error("Open through a symlinked directory")
			}
			if keys := walked(t, d, "p/"); len(keys) != 0 {
				t.Errorf("Walk reports %q", keys)
			}
			if keys := walked(t, d, "p/1/"); len(keys) != 0 {
				t.Errorf("Walk reports %q", keys)
			}
			d.Delete("p/1/" + uuidA) // os.Root refuses the path: check below sees the file outside intact
			if err := d.DeletePrefix("p/1/"); err != nil {
				t.Error(err)
			}
			check(t)
		})
	}
}

func TestWalk(t *testing.T) {
	d := newTestDisk(t)
	var want []string
	for _, p := range []int64{12, 3, 400} { // byte order on disk: 12 < 3 < 400
		for range 700 {
			k := ObjectKey(p)
			u := k[strings.LastIndexByte(k, '/')+1:]
			dir := filepath.Join(d.root, "p", k[2:strings.LastIndexByte(k, '/')], u[:2])
			os.MkdirAll(dir, 0o700)
			if err := os.WriteFile(filepath.Join(dir, u), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			want = append(want, k)
		}
	}
	sortByDisk(want)
	writeObject(t, d, "p/12/"+uuidA, nil) // through the store as well
	want = insertSorted(want, "p/12/"+uuidA)
	st, _ := d.CreateStaging(StagingKey(12))
	st.Close()

	// What is not the store's: none of it is reported.
	junk := []string{"p/12/stray", "p/abc/0b/" + uuidB, "p/12/0b/notakey", "p/12/0b/" + strings.ToUpper(uuidB),
		"p/12/zz/" + uuidB /* wrong shard */, "p/12/0bx/" + uuidB, "p/stray"}
	for _, j := range junk {
		os.MkdirAll(filepath.Join(d.root, filepath.Dir(j)), 0o700)
		os.WriteFile(filepath.Join(d.root, j), nil, 0o600)
	}
	os.Symlink(filepath.Join(d.root, "p/12/ab", uuidA), filepath.Join(d.root, "p/12/ab", "ab"+uuidA[2:35]+"9"))
	os.MkdirAll(filepath.Join(d.root, "p/12/0b", uuidB[:35]+"9"), 0o700) // a directory where a file goes

	var got []string
	walk := func(prefix string) []string {
		var keys []string
		err := d.Walk(prefix, func(k string, mod time.Time) error {
			if mod.IsZero() || time.Since(mod) > time.Hour {
				t.Errorf("%s: modTime %v", k, mod)
			}
			keys = append(keys, k)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return keys
	}
	got = walk("p/")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Walk(p/): %d keys, want %d", len(got), len(want))
		for i := range min(len(got), len(want)) {
			if got[i] != want[i] {
				t.Errorf("first difference at %d: %s, want %s", i, got[i], want[i])
				break
			}
		}
	}
	if again := walk("p/"); strings.Join(again, "\n") != strings.Join(got, "\n") {
		t.Error("a second walk gives another order")
	}
	if got := walk("p/3/"); len(got) != 700 || !strings.HasPrefix(got[0], "p/3/") {
		t.Errorf("Walk(p/3/): %d keys", len(got))
	}
	if got := walk("s/"); len(got) != 1 {
		t.Errorf("Walk(s/): %q", got)
	}
	if got := walk("p/99/"); len(got) != 0 {
		t.Errorf("Walk of a project with no files: %q", got)
	}
	stop := errors.New("stop")
	n := 0
	if err := d.Walk("p/", func(string, time.Time) error { n++; return stop }); err != stop || n != 1 {
		t.Errorf("fn's error: %v after %d keys", err, n)
	}
	// fn may delete what it is given (the boot sweep).
	if err := d.Walk("p/400/", func(k string, _ time.Time) error { return d.Delete(k) }); err != nil {
		t.Error(err)
	}
	if got := walk("p/400/"); len(got) != 0 {
		t.Errorf("after deleting during the walk: %d keys", len(got))
	}
	empty := newTestDisk(t)
	if err := empty.Walk("p/", func(string, time.Time) error { return stop }); err != nil {
		t.Errorf("Walk of an empty store: %v", err)
	}
}

// sortByDisk sorts keys as Walk returns them: by project, shard and UUID, each in byte order.
func sortByDisk(keys []string) {
	path := func(k string) string {
		i := strings.LastIndexByte(k, '/')
		return k[:i] + "/\x00" + k[i+1:i+3] + "/\x00" + k[i+1:]
	}
	for i := 1; i < len(keys); i++ { // insertion sort keeps this free of sort's ordering rules
		for j := i; j > 0 && path(keys[j]) < path(keys[j-1]); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
}

func insertSorted(keys []string, k string) []string {
	keys = append(keys, k)
	sortByDisk(keys)
	return keys
}

func TestBadPrefixes(t *testing.T) {
	d := newTestDisk(t)
	r := record(t, d)
	for _, p := range []string{"", "/", "p", "s", "x/", "../", "p/../", "p/1", "p/1/ab/", "/p/", "p//", "p/-1/", "p/1/../"} {
		if err := d.Walk(p, func(string, time.Time) error { return nil }); err == nil {
			t.Errorf("Walk(%q) is accepted", p)
		}
		if err := d.DeletePrefix(p); err == nil {
			t.Errorf("DeletePrefix(%q) is accepted", p)
		}
	}
	// A prefix of every project is refused to DeletePrefix.
	for _, p := range []string{"p/", "s/"} {
		if err := d.DeletePrefix(p); err == nil {
			t.Errorf("DeletePrefix(%q) is accepted", p)
		}
	}
	if r.calls != 0 {
		t.Errorf("refused prefixes made %d calls on the disk", r.calls)
	}
}

func TestDeletePrefix(t *testing.T) {
	d := newTestDisk(t)
	for range 50 {
		writeObject(t, d, ObjectKey(8), []byte("x"))
		writeObject(t, d, ObjectKey(9), []byte("y"))
		s, _ := d.CreateStaging(StagingKey(8))
		s.Close()
	}
	count := func(prefix string) int {
		n := 0
		d.Walk(prefix, func(string, time.Time) error { n++; return nil })
		return n
	}
	if err := d.DeletePrefix(ProjectPrefix(8)); err != nil {
		t.Fatal(err)
	}
	if err := d.DeletePrefix(StagingPrefix(8)); err != nil {
		t.Fatal(err)
	}
	if count("p/8/") != 0 || count("s/8/") != 0 || count("p/9/") != 50 {
		t.Errorf("after DeletePrefix: p/8 %d, s/8 %d, p/9 %d", count("p/8/"), count("s/8/"), count("p/9/"))
	}
	for _, dir := range []string{"p/8", "s/8"} {
		if _, err := os.Lstat(filepath.Join(d.root, dir)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: %v", dir, err)
		}
	}
	if err := d.DeletePrefix(ProjectPrefix(8)); err != nil {
		t.Errorf("a second DeletePrefix: %v", err)
	}
	// The store makes the directories again for a later file (its memory of them is dropped).
	writeObject(t, d, ObjectKey(8), nil)
}

// A DeletePrefix cut at any of its calls finishes on the next call.
func TestDeletePrefixResumes(t *testing.T) {
	setup := func(t *testing.T) *Disk {
		d := newTestDisk(t)
		for _, k := range []string{"p/4/" + uuidA, "p/4/" + uuidB, "p/4/ab" + uuidA[2:35] + "0", "p/5/" + uuidA} {
			writeObject(t, d, k, []byte(k))
		}
		os.MkdirAll(filepath.Join(d.root, "p/4/0b/nested/deeper"), 0o700)
		os.WriteFile(filepath.Join(d.root, "p/4/0b/nested/deeper/f"), nil, 0o600)
		os.Symlink(d.root, filepath.Join(d.root, "p/4/loop")) // removed as a name, never followed
		return d
	}
	for k := 1; ; k++ {
		d := setup(t)
		r := record(t, d)
		r.failAt = k
		err := d.DeletePrefix("p/4/")
		if !r.failed {
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if err == nil {
			t.Errorf("call %d failed and DeletePrefix succeeded", k)
		}
		if err := d.DeletePrefix("p/4/"); err != nil {
			t.Errorf("resuming after call %d: %v", k, err)
		}
		if _, err := os.Lstat(filepath.Join(d.root, "p/4")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("resuming after call %d left p/4: %v", k, err)
		}
		if _, err := d.Open("p/5/" + uuidA); err != nil {
			t.Errorf("another project's file: %v", err)
		}
		if _, err := os.Stat(d.root); err != nil {
			t.Fatal("the data directory is gone")
		}
	}
}
