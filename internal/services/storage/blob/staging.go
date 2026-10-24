package blob

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// appendBuffer is what Append reads into: a request's body streams to the disk through it and
// is never held whole in memory (ADR-022 §6.4).
const appendBuffer = 32 << 10

// CreateStaging implements Store (O_EXCL, at an upload's start only). The empty file's entry
// is durable on return, so every byte a later Sync makes durable is reachable after a power
// loss.
func (d *Disk) CreateStaging(key string) (Staging, error) {
	dir, name, err := keyPath(key, "s")
	if err != nil {
		return nil, err
	}
	if err := d.mkdirDurable(dir); err != nil {
		return nil, err
	}
	f, err := d.fs.OpenFile(name, os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syncDir(d.fs, dir); err != nil {
		f.Close()
		d.fs.Remove(name)
		return nil, fmt.Errorf("blob: create %s: %w", name, err)
	}
	return &staging{f: f}, nil
}

// OpenStaging implements Store; it never creates the file. A missing file is an error that
// wraps fs.ErrNotExist.
func (d *Disk) OpenStaging(key string) (Staging, error) {
	_, name, err := keyPath(key, "s")
	if err != nil {
		return nil, err
	}
	f, err := d.fs.OpenFile(name, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return nil, err
	}
	if _, err := regular(d.fs, f, name); err != nil {
		f.Close()
		return nil, err
	}
	return &staging{f: f}, nil
}

// staging is a session's staging file, opened for appending. It is not safe for concurrent
// use: a session's lock serialises its requests (core/uploads.go).
type staging struct {
	f file
}

// Append copies at most limit bytes of r to the end of the file and returns how many it wrote.
// A read error other than io.EOF is returned with the bytes written before it, which stay in
// the file (a request cut in the middle keeps what arrived, ADR-022 §6.3).
func (s *staging) Append(r io.Reader, limit int64) (int64, error) {
	var written int64
	if limit <= 0 {
		return 0, nil
	}
	buf := make([]byte, min(limit, appendBuffer))
	for written < limit {
		n, rerr := r.Read(buf[:min(int64(len(buf)), limit-written)])
		if n > 0 {
			m, werr := s.f.Write(buf[:n])
			written += int64(m)
			if werr != nil {
				return written, werr
			}
		}
		if errors.Is(rerr, io.EOF) {
			return written, nil
		}
		if rerr != nil {
			return written, rerr
		}
	}
	return written, nil
}

// Truncate cuts the file back to n bytes. Like an append, the new length is durable after Sync.
func (s *staging) Truncate(n int64) error { return s.f.Truncate(n) }

// Sync makes every appended byte, and the file's length, durable.
func (s *staging) Sync() error { return s.f.Sync() }

// Size returns the file's current length.
func (s *staging) Size() (int64, error) {
	fi, err := s.f.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (s *staging) Close() error { return s.f.Close() }
