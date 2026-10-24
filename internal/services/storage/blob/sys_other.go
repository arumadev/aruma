//go:build !darwin && !linux

package blob

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

// v0.3 runs Storage on darwin and Linux. Elsewhere the store builds, and the lock and the free
// space say plainly that they are not there, so Boot refuses to start Storage.

var errPlatform = fmt.Errorf("blob: %s: %w", runtime.GOOS, errors.ErrUnsupported)

func tryLock(*os.File) error          { return errPlatform }
func unlock(*os.File) error           { return errPlatform }
func freeBytes(string) (int64, error) { return 0, errPlatform }
func writable(string) error           { return nil }
