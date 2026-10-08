//go:build darwin

package workspace

import "golang.org/x/sys/unix"

func platformRenameatNoReplace(directoryFD int, oldName, newName string) error {
	return platformRenameatNoReplaceBetween(directoryFD, oldName, directoryFD, newName)
}

func platformRenameatNoReplaceBetween(oldDirectoryFD int, oldName string, newDirectoryFD int, newName string) error {
	return unix.RenameatxNp(oldDirectoryFD, oldName, newDirectoryFD, newName, unix.RENAME_EXCL)
}
