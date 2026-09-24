//go:build darwin

package workspace

import "golang.org/x/sys/unix"

func platformRenameatNoReplace(directoryFD int, oldName, newName string) error {
	return unix.RenameatxNp(directoryFD, oldName, directoryFD, newName, unix.RENAME_EXCL)
}
