package blob

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

// The cost of durability: a chunk appended and synced as an
// upload request does it, an object written and committed, and a staging file promoted. The
// directory under test is TMPDIR's.

func BenchmarkStagingChunk(b *testing.B) {
	for _, size := range []int{256 << 10, 1 << 20, 8 << 20, 16 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			d := benchDisk(b)
			s, err := d.CreateStaging(StagingKey(1))
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			chunk := bytes.Repeat([]byte("x"), size)
			b.SetBytes(int64(size))
			var length int64
			for b.Loop() {
				if length > 64<<20 { // keep the file, and the disk it takes, small
					b.StopTimer()
					s.Truncate(0)
					s.Sync()
					length = 0
					b.StartTimer()
				}
				n, err := s.Append(bytes.NewReader(chunk), int64(size))
				if err != nil {
					b.Fatal(err)
				}
				if err := s.Sync(); err != nil {
					b.Fatal(err)
				}
				length += n
			}
		})
	}
}

func BenchmarkCommit(b *testing.B) {
	for _, size := range []int{0, 4 << 10, 1 << 20} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			d := benchDisk(b)
			writeObjectB(b, d, ObjectKey(1), nil) // the project's directories exist, as in steady use
			data := bytes.Repeat([]byte("x"), size)
			b.SetBytes(int64(size))
			for b.Loop() {
				key := ObjectKey(1)
				writeObjectB(b, d, key, data)
				b.StopTimer()
				d.Delete(key)
				b.StartTimer()
			}
		})
	}
}

func BenchmarkPromote(b *testing.B) {
	d := benchDisk(b)
	writeObjectB(b, d, ObjectKey(1), nil)
	chunk := bytes.Repeat([]byte("x"), 1<<20)
	for b.Loop() {
		b.StopTimer()
		skey, okey := StagingKey(1), ObjectKey(1)
		s, err := d.CreateStaging(skey)
		if err != nil {
			b.Fatal(err)
		}
		s.Append(bytes.NewReader(chunk), int64(len(chunk)))
		s.Sync()
		s.Close()
		b.StartTimer()
		if err := d.Promote(skey, okey); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		d.Delete(okey)
		b.StartTimer()
	}
}

func benchDisk(b *testing.B) *Disk {
	b.Helper()
	d, err := NewDisk(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { d.Close() })
	return d
}

func writeObjectB(b *testing.B, d *Disk, key string, data []byte) {
	b.Helper()
	w, err := d.Create(key)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		b.Fatal(err)
	}
	if err := w.Commit(); err != nil {
		b.Fatal(err)
	}
}

// BenchmarkSync is the sync alone after a write of the size, as (*os.File).Sync does it; on
// darwin bench_darwin_test.go measures fsync(2) beside it.
func BenchmarkSync(b *testing.B) {
	for _, size := range []int{4 << 10, 256 << 10} {
		b.Run(fmt.Sprintf("%dKiB", size>>10), func(b *testing.B) {
			f, err := os.CreateTemp(b.TempDir(), "sync")
			if err != nil {
				b.Fatal(err)
			}
			defer f.Close()
			data := bytes.Repeat([]byte("x"), size)
			for b.Loop() {
				f.WriteAt(data, 0)
				if err := f.Sync(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
