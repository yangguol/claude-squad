//go:build !darwin && !linux

package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"runtime"
)

var platformAtomicRenameNoReplace = func(int, string, string) error {
	return errors.New("atomic apply is unsupported on " + runtime.GOOS)
}

func platformVerifyAtomicApply() error {
	return errors.New("atomic apply is unsupported on " + runtime.GOOS)
}

func platformFilesystemDeviceID(string) (uint64, error) {
	return 0, errors.New("filesystem device identity is unsupported on " + runtime.GOOS)
}

func platformAtomicApplyPreflight(string, string) error {
	return errors.New("atomic apply is unsupported on " + runtime.GOOS)
}

func platformProbeAtomicNoReplace(*os.File) error {
	return errors.New("atomic apply is unsupported on " + runtime.GOOS)
}

func platformRenameatNoReplaceBetween(int, string, int, string) error {
	return errors.New("atomic apply is unsupported on " + runtime.GOOS)
}

func randomProbeName(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
