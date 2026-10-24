package blob

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"
)

// prefixPattern is the prefixes Walk takes: a kind ("p/", "s/"), or a kind and a project.
var prefixPattern = regexp.MustCompile(`^(p|s)/([0-9]+/)?$`)

// Walk implements Store. The keys come in the order of their paths on disk (project, shard,
// UUID, each in byte order), so a second walk over the same files gives the same order. It
// reports regular files alone, at a key ValidKey accepts and in that key's shard: anything
// else in the tree (a symlink, a stray file) is not the store's, and is neither reported nor
// followed. A file removed during the walk is skipped.
func (d *Disk) Walk(prefix string, fn func(key string, modTime time.Time) error) error {
	if !prefixPattern.MatchString(prefix) {
		return fmt.Errorf("blob: walk %q: not a valid prefix", prefix)
	}
	kind, project, _ := strings.Cut(prefix, "/")
	project = strings.TrimSuffix(project, "/")
	var projects []string
	if project != "" {
		projects = []string{project}
	} else {
		entries, err := d.readDir(kind)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() && isDigits(e.Name()) {
				projects = append(projects, e.Name())
			}
		}
	}
	for _, p := range projects {
		pdir := kind + "/" + p
		shards, err := d.readDir(pdir)
		if err != nil {
			return err
		}
		for _, sh := range shards {
			if !sh.IsDir() || len(sh.Name()) != 2 {
				continue
			}
			sdir := pdir + "/" + sh.Name()
			files, err := d.readDir(sdir)
			if err != nil {
				return err
			}
			for _, f := range files {
				key := pdir + "/" + f.Name()
				if !f.Type().IsRegular() || !ValidKey(key) || !strings.HasPrefix(f.Name(), sh.Name()) {
					continue
				}
				fi, err := f.Info()
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				if err != nil {
					return err
				}
				if err := fn(key, fi.ModTime()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// readDir lists dir, a directory of the tree. A directory that is missing, or is not one or is
// reached through something that is not one (a symlink), has no entries.
func (d *Disk) readDir(dir string) ([]fs.DirEntry, error) {
	if ok, err := d.realDir(dir); !ok {
		return nil, err
	}
	entries, err := d.fs.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return entries, err
}

// realDir reports whether dir and each directory above it is a directory, not a symlink.
func (d *Disk) realDir(dir string) (bool, error) {
	for i := 0; i <= len(dir); i++ {
		if i < len(dir) && dir[i] != '/' {
			continue
		}
		fi, err := d.fs.Lstat(dir[:i])
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !fi.IsDir() {
			return false, nil
		}
	}
	return true, nil
}

// projectPrefix is the prefixes DeletePrefix takes: one project's object or staging files.
var projectPrefix = regexp.MustCompile(`^(p|s)/[0-9]+/$`)

// maxTreeDepth bounds how deep DeletePrefix descends below a project's directory: the store
// makes two levels (shard, file), and a deeper tree is not its own.
const maxTreeDepth = 8

// DeletePrefix implements Store, for one project's prefix (ProjectPrefix, StagingPrefix): a
// prefix of every project is refused, so no mistake empties the whole store. It removes the
// project's directory and everything below it without following a symlink, then syncs the
// kind's directory, so the removal is durable on return. A call after a failure, or after a
// crash, continues on what is left; a missing directory is not an error.
func (d *Disk) DeletePrefix(prefix string) error {
	if !projectPrefix.MatchString(prefix) {
		return fmt.Errorf("blob: delete %q: not a project's prefix", prefix)
	}
	defer d.forgetDirs()
	kind, dir := prefix[:1], strings.TrimSuffix(prefix, "/")
	if ok, err := d.realDir(kind); !ok {
		return err // the project has no directory in the tree
	}
	fi, err := d.fs.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		err = d.removeTree(dir, 0)
	} else {
		err = d.remove(dir) // a symlink or a file: the name goes, nothing it points to
	}
	if err != nil {
		return fmt.Errorf("blob: delete %s: %w", prefix, err)
	}
	if err := syncDir(d.fs, kind); err != nil {
		return fmt.Errorf("blob: delete %s: %w", prefix, err)
	}
	return nil
}

// removeTree removes dir, a directory, and everything below it. The entries' types come from
// the directory listing, which does not follow a symlink, so a symlink is removed as a name.
func (d *Disk) removeTree(dir string, depth int) error {
	if depth > maxTreeDepth {
		return fmt.Errorf("%s: deeper than the store's tree", dir)
	}
	entries, err := d.fs.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := dir + "/" + e.Name()
		if e.IsDir() {
			err = d.removeTree(name, depth+1)
		} else {
			err = d.remove(name)
		}
		if err != nil {
			return err
		}
	}
	return d.remove(dir)
}

func (d *Disk) remove(name string) error {
	if err := d.fs.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// FreeBytes implements Store: the bytes an unprivileged process may still write on the data
// directory's file system (statfs f_bavail × f_bsize). Where there is no statfs it returns an
// error that wraps errors.ErrUnsupported.
func (d *Disk) FreeBytes() (int64, error) {
	n, err := freeBytes(d.root)
	if err != nil {
		return 0, fmt.Errorf("blob: free space of %s: %w", d.root, err)
	}
	return n, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
