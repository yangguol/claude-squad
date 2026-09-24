//go:build !darwin && !linux

package workspace

import (
	"errors"
	"os"
	"runtime"
)

func platformAcquireProjectLock(_ *os.File) error {
	return errors.New("project locking is unsupported on " + runtime.GOOS)
}

func platformReleaseProjectLock(_ *os.File) error { return nil }

func isProjectLockContention(error) bool { return false }
