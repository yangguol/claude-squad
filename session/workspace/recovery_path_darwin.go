//go:build darwin

package workspace

import (
	"bytes"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

func platformVerifyCanonicalDirectoryPath(directory *os.File, expected string) error {
	buffer := make([]byte, 4096)
	_, _, errno := unix.Syscall(unix.SYS_FCNTL, directory.Fd(), uintptr(unix.F_GETPATH), uintptr(unsafe.Pointer(&buffer[0])))
	if errno != 0 {
		return errno
	}
	end := bytes.IndexByte(buffer, 0)
	if end < 0 {
		return errors.New("descriptor path is unterminated")
	}
	actual := string(buffer[:end])
	if actual != expected {
		return errors.New("descriptor path spelling differs")
	}
	return nil
}
