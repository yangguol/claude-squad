//go:build !darwin && !linux

package workspace

import (
	"errors"
	"runtime"
)

func platformRequirePathAbsent(string, string) error {
	return errors.New("descriptor-relative path checks are unsupported on " + runtime.GOOS)
}

func platformPreparedPathType(string, string) (EntryType, error) {
	return "", errors.New("descriptor-relative path checks are unsupported on " + runtime.GOOS)
}

func platformSnapshotDirectoryTree(string, string) (map[string]Entry, error) {
	return nil, errors.New("descriptor-relative replacement scan is unsupported on " + runtime.GOOS)
}
