package blob

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzKey: no input makes the store panic, a key it refuses makes no call on the disk, and a key
// it accepts maps to a file inside the root, in its kind's tree and its own shard.
func FuzzKey(f *testing.F) {
	for _, s := range []string{
		"p/1/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f", "s/482915736204/ab1c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f",
		"p/1/../0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1", "/p/1/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f",
		"p/1/------------------------------------", "p/0/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f\x00",
		"p/1/0B7C3F4E-8A1D-4C2B-9E5F-6A7B8C9D0E1F", "", "p/", "s/1/ab/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f",
	} {
		f.Add(s)
	}
	d, err := NewDisk(f.TempDir())
	if err != nil {
		f.Fatal(err)
	}
	r := &recFS{inner: d.fs, model: &crashModel{root: &node{dir: true, kids: map[string]*node{}}}}
	d.fs = r
	f.Fuzz(func(t *testing.T, key string) {
		dir, name, err := keyPath(key, "ps")
		if ValidKey(key) != (err == nil) {
			t.Fatalf("keyPath(%q) = %v, ValidKey %v", key, err, ValidKey(key))
		}
		if err != nil {
			r.mu.Lock()
			r.calls = 0
			r.mu.Unlock()
			d.Open(key)
			d.Delete(key)
			d.OpenStaging(key)
			d.Promote(key, ObjectKey(1))
			if w, err := d.Create(key); err == nil {
				w.Abort()
			}
			if s, err := d.CreateStaging(key); err == nil {
				s.Close()
			}
			if r.calls != 0 {
				t.Fatalf("the refused key %q made %d calls on the disk", key, r.calls)
			}
			return
		}
		full := filepath.Join(d.root, filepath.FromSlash(name))
		if rel, err := filepath.Rel(d.root, full); err != nil || !filepath.IsLocal(rel) {
			t.Fatalf("%q maps outside the root: %s", key, full)
		}
		u := key[strings.LastIndexByte(key, '/')+1:]
		if !strings.HasPrefix(name, key[:2]) || dir != key[:len(key)-len(u)]+u[:2] || name != dir+"/"+u {
			t.Fatalf("%q maps to %s in %s", key, name, dir)
		}
		if strings.Contains("/"+name+"/", "/../") {
			t.Fatalf("%q maps to %s", key, name)
		}
	})
}
