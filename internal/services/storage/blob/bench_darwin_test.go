package blob

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"testing"
)

// BenchmarkFsyncWithoutFullSync is BenchmarkSync with fsync(2) in place of Go's Sync, which on
// darwin is fcntl(F_FULLFSYNC): the difference is the drive's cache flush the store pays for,
// which fsync(2) on darwin does not ask ("the drive itself may not physically write the data
// to the platters for quite some time", fsync(2) on macOS).
func BenchmarkFsyncWithoutFullSync(b *testing.B) {
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
				if err := syscall.Fsync(int(f.Fd())); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
