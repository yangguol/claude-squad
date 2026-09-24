//go:build !darwin && !linux

package workspace

import (
	"errors"
	"os"
	"runtime"
)

func platformOpenProjectLock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
}

func platformAcquireProjectLock(_ *os.File) error {
	return errors.New("project locking is unsupported on " + runtime.GOOS)
}

func platformReleaseProjectLock(_ *os.File) error { return nil }

func isProjectLockContention(error) bool { return false }
