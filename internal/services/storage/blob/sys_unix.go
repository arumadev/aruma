//go:build darwin || linux

package blob

import (
	"errors"
	"math"
	"os"
	"syscall"
)

// tryLock takes flock(LOCK_EX|LOCK_NB) on f, or returns errLocked while another open file
// holds it.
func tryLock(f *os.File) error {
	return flock(f, syscall.LOCK_EX|syscall.LOCK_NB)
}

// unlock gives the lock on f up. Closing f gives it up as well.
func unlock(f *os.File) error { return flock(f, syscall.LOCK_UN) }

func flock(f *os.File, how int) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = syscall.Flock(int(fd), how) }); err != nil {
		return err
	}
	if errors.Is(ferr, syscall.EWOULDBLOCK) || errors.Is(ferr, syscall.EINTR) {
		return errLocked
	}
	return ferr
}

// freeBytes returns statfs's f_bavail × f_bsize for the file system of path.
func freeBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	avail, bsize := uint64(st.Bavail), uint64(st.Bsize)
	if bsize != 0 && avail > math.MaxInt64/bsize {
		return math.MaxInt64, nil
	}
	return int64(avail * bsize), nil
}

// writable checks with access(2) that this process may create files in dir: write and search
// permission, on a file system mounted read-write (EROFS otherwise).
func writable(dir string) error {
	const wOK, xOK = 0x2, 0x1 // W_OK, X_OK in <unistd.h> on darwin and Linux
	return syscall.Access(dir, wOK|xOK)
}
