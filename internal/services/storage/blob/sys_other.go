//go:build !darwin && !linux

package blob

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

// v0.3 runs Storage on darwin and Linux alone (ADR-022 §5): elsewhere NewDisk refuses at boot,
// before anything relies on flock, statfs or POSIX unlink.

var errPlatform = fmt.Errorf("blob: Storage runs on darwin and Linux in v0.3, not on %s: %w", runtime.GOOS, errors.ErrUnsupported)

func supportedPlatform() error          { return errPlatform }
func tryLock(*os.File) error            { return errPlatform }
func unlock(*os.File) error             { return errPlatform }
func freeBytes(string) (int64, error)   { return 0, errPlatform }
func statfsName(string) (string, error) { return "", errPlatform }
func writable(string) error             { return errPlatform }
