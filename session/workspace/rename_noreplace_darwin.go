//go:build darwin

package workspace

import "golang.org/x/sys/unix"

func platformRenameNoReplace(oldpath, newpath string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_EXCL)
}
