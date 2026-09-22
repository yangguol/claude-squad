//go:build !darwin && !linux

package workspace

import (
	"errors"
	"runtime"
)

func platformRenameNoReplace(_, _ string) error {
	return errors.New("atomic no-replace rename is unsupported on " + runtime.GOOS)
}
