package blob

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The tests of the contract commit (S2-0): keys. S2-A tests the disk.

func TestKeys(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		for _, k := range []string{ObjectKey(482915736204), StagingKey(482915736204)} {
			if !ValidKey(k) || seen[k] {
				t.Fatalf("key %q is invalid or repeated", k)
			}
			seen[k] = true
		}
	}
	o, s := ObjectKey(482915736204), StagingKey(482915736204)
	if !strings.HasPrefix(o, ProjectPrefix(482915736204)) || !strings.HasPrefix(s, StagingPrefix(482915736204)) {
		t.Errorf("keys %q, %q are not under their project's prefixes", o, s)
	}
	// A version 4 UUID: 8-4-4-4-12 lower-case hex, version nibble 4, variant 10xx.
	u := o[len("p/482915736204/"):]
	if len(u) != 36 || u[8] != '-' || u[13] != '-' || u[14] != '4' || u[18] != '-' || u[23] != '-' ||
		!strings.ContainsRune("89ab", rune(u[19])) || strings.ToLower(u) != u {
		t.Errorf("%q is not a version 4 UUID", u)
	}
}

func TestValidKeyRefusesForeignKeys(t *testing.T) {
	good := "p/1/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f"
	if !ValidKey(good) {
		t.Fatal("a well-formed key is refused")
	}
	for _, k := range []string{
		"", "p/", "p/1/", "x/1/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f",
		"p/1/0B7C3F4E-8A1D-4C2B-9E5F-6A7B8C9D0E1F", // case
		"p/-1/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f",
		"p/1/../0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1", "p/1/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f/x",
		"/p/1/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f", "p/1/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f\n",
		"p/1/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1\x00", "p//0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f",
		"p/1/ab/0b7c3f4e-8a1d-4c2b-9e5f-6a7b8c9d0e1f", // the on-disk shard is not part of the key
	} {
		if ValidKey(k) {
			t.Errorf("ValidKey(%q) = true", k)
		}
	}
}

func TestNewDiskNeedsAnAbsoluteRoot(t *testing.T) {
	if _, err := NewDisk("data/storage"); err == nil {
		t.Error("a relative data directory is accepted")
	}
	d, err := NewDisk("/var/lib/aruma/storage/")
	if err != nil || d.Root() != "/var/lib/aruma/storage" {
		t.Errorf("NewDisk = %v, %v", d, err)
	}
}

var _ func(context.Context, string, func(time.Duration)) (Lock, error) = AcquireLock

// The interfaces of the contract, method by method: S2-A implements them, core calls them.
func TestInterfacesArePinned(t *testing.T) {
	for _, c := range []struct {
		typ  reflect.Type
		want string
	}{
		{reflect.TypeFor[Store](), "Create func(string) (blob.Writer, error); CreateStaging func(string) (blob.Staging, error); " +
			"Delete func(string) error; DeletePrefix func(string) error; FreeBytes func() (int64, error); " +
			"Open func(string) (blob.Reader, error); OpenStaging func(string) (blob.Staging, error); Promote func(string, string) error; " +
			"Walk func(string, func(string, time.Time) error) error"},
		{reflect.TypeFor[Writer](), "Abort func() error; Commit func() error; Write func([]uint8) (int, error)"},
		{reflect.TypeFor[Reader](), "Close func() error; Read func([]uint8) (int, error); Seek func(int64, int) (int64, error); Size func() int64"},
		{reflect.TypeFor[Staging](), "Append func(io.Reader, int64) (int64, error); Close func() error; Size func() (int64, error); " +
			"Sync func() error; Truncate func(int64) error"},
		{reflect.TypeFor[Lock](), "Instance func() (string, bool, error); Release func() error; SetInstance func(string) error"},
	} {
		var methods []string
		for i := range c.typ.NumMethod() {
			m := c.typ.Method(i)
			methods = append(methods, m.Name+" "+m.Type.String())
		}
		if got := strings.Join(methods, "; "); got != c.want {
			t.Errorf("%v:\n got %s\nwant %s", c.typ, got, c.want)
		}
	}
}
