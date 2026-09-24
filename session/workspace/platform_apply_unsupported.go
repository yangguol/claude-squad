//go:build !darwin && !linux

package workspace

import (
	"errors"
	"runtime"
)

func platformVerifyAtomicApply() error {
	return errors.New("atomic apply is unsupported on " + runtime.GOOS)
}

func platformFilesystemDeviceID(string) (uint64, error) {
	return 0, errors.New("filesystem device identity is unsupported on " + runtime.GOOS)
}
