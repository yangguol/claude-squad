//go:build darwin

package workspace

import (
	"os"

	"golang.org/x/sys/unix"
)

func platformOpenPreparedLeafAt(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_SYMLINK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
