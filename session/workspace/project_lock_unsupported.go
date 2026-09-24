//go:build !darwin && !linux

package workspace

import (
	"errors"
	"os"
	"runtime"
)

func platformVerifyProjectLock() error {
	return errors.New("project locking is unsupported on " + runtime.GOOS)
}

func platformOpenTrustedDirectory(string) (*os.File, error) {
	return nil, errors.New("trusted directory traversal is unsupported on " + runtime.GOOS)
}

func platformOpenOrCreateDirectoryAt(*os.File, string, uint32) (*os.File, error) {
	return nil, errors.New("descriptor-relative directory creation is unsupported on " + runtime.GOOS)
}

func platformVerifyDirectoryEntry(*os.File, string, *os.File) error {
	return errors.New("descriptor-relative directory verification is unsupported on " + runtime.GOOS)
}

func platformOpenProjectLockAt(*os.File, *os.File, string) (*os.File, *os.File, error) {
	return nil, nil, errors.New("project locking is unsupported on " + runtime.GOOS)
}

func platformAcquireProjectLock(_ *os.File) error {
	return errors.New("project locking is unsupported on " + runtime.GOOS)
}

func platformReleaseProjectLock(_ *os.File) error { return nil }

func isProjectLockContention(error) bool { return false }
