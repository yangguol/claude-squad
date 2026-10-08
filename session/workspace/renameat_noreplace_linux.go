//go:build linux

package workspace

import "golang.org/x/sys/unix"

func platformRenameatNoReplace(directoryFD int, oldName, newName string) error {
	return platformRenameatNoReplaceBetween(directoryFD, oldName, directoryFD, newName)
}

func platformRenameatNoReplaceBetween(oldDirectoryFD int, oldName string, newDirectoryFD int, newName string) error {
	return unix.Renameat2(oldDirectoryFD, oldName, newDirectoryFD, newName, unix.RENAME_NOREPLACE)
}
