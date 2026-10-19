package migrations

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "append missing migrations to released.lock")

const lockFile = "released.lock"

const lockHeader = `# Released migrations: <schema>/<file> sha256:<hex of the file's bytes>.
# A released migration is never edited or deleted; a schema change is a new numbered file.
# Add new files with: go test ./internal/store/migrations -run TestReleasedMigrations -update
`

// TestReleasedMigrations keeps released migrations frozen. A released migration has already
// run on someone's database: editing it would leave that database different from a fresh one,
// with no version number to tell them apart, and deleting it breaks the numbering. So every
// migration file is listed in released.lock with its sha256, and this test fails when a listed
// file changed or is gone, or when a file is not listed.
//
// After adding a migration, record it:
//
//	go test ./internal/store/migrations -run TestReleasedMigrations -update
//
// -update only appends lines for files the lock lacks; it never rewrites or removes a line. A
// migration still being written on a branch is not released yet: to change it, delete its line
// and run -update again. Squashing released migrations into a baseline is a deliberate commit
// that rewrites the lock by hand, with an ADR (ADR-015).
func TestReleasedMigrations(t *testing.T) {
	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := appendMissing(files); err != nil {
			t.Fatal(err)
		}
	}
	locked, err := readLock()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range slices.Sorted(maps.Keys(locked)) {
		sum, ok := files[name]
		switch {
		case !ok:
			t.Errorf("%s is in %s but no longer exists. Released migrations are never deleted: restore it.", name, lockFile)
		case sum != locked[name]:
			t.Errorf("%s changed after release (locked %s, now %s). Released migrations are never edited: "+
				"restore the file and make the change in a new numbered migration, then add that with -update.", name, locked[name], sum)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if _, ok := locked[name]; !ok {
			t.Errorf("%s is not in %s. If it is a new migration, add it with "+
				"`go test ./internal/store/migrations -run TestReleasedMigrations -update`; never edit a released one.", name, lockFile)
		}
	}
}

// migrationFiles hashes every migration on disk, in any schema directory, so a file the embed
// pattern misses is still caught.
func migrationFiles() (map[string]string, error) {
	names, err := fs.Glob(os.DirFS("."), "*/*.sql")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(names))
	for _, name := range names {
		b, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out[name] = "sha256:" + hex.EncodeToString(sum[:])
	}
	return out, nil
}

func readLock() (map[string]string, error) {
	b, err := os.ReadFile(lockFile)
	if err != nil {
		return nil, fmt.Errorf("%w (create it with -update)", err)
	}
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, sum, ok := strings.Cut(line, " ")
		if !ok || !strings.HasPrefix(sum, "sha256:") || strings.Contains(sum, " ") {
			return nil, fmt.Errorf("%s:%d: want <schema>/<file> sha256:<hex>, got %q", lockFile, n, line)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("%s:%d: %s is listed twice", lockFile, n, name)
		}
		out[name] = sum
	}
	return out, sc.Err()
}

// appendMissing adds a line for each file the lock lacks, creating the lock if needed. Lines
// already present are left exactly as they are, right or wrong.
func appendMissing(files map[string]string) error {
	locked, err := readLock()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		locked = map[string]string{}
		if err := os.WriteFile(lockFile, []byte(lockHeader), 0o644); err != nil {
			return err
		}
	case err != nil:
		return err
	}
	var add strings.Builder
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if _, ok := locked[name]; !ok {
			fmt.Fprintf(&add, "%s %s\n", name, files[name])
		}
	}
	if add.Len() == 0 {
		return nil
	}
	b, err := os.ReadFile(lockFile)
	if err != nil {
		return err
	}
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		b = append(b, '\n')
	}
	return os.WriteFile(lockFile, append(b, add.String()...), 0o644)
}
