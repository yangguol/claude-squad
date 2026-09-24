//go:build darwin || linux

package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func platformVerifyProjectLock() error { return nil }

func platformOpenTrustedDirectory(path string) (*os.File, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, fmt.Errorf("trusted directory must be absolute: %q", path)
	}
	fd, err := unix.Open(clean, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), clean), nil
}

func platformOpenOrCreateDirectoryAt(parent *os.File, name string, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		if err = unix.Mkdirat(int(parent.Fd()), name, mode); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
		fd, err = unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return nil, fmt.Errorf("refuse symlink or non-directory entry: %w", err)
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func platformVerifyDirectoryEntry(parent *os.File, name string, directory *os.File) error {
	var entry, opened unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if entry.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("entry is not a real directory")
	}
	if err := unix.Fstat(int(directory.Fd()), &opened); err != nil {
		return err
	}
	if entry.Dev != opened.Dev || entry.Ino != opened.Ino {
		return errors.New("directory entry was substituted")
	}
	return nil
}

func platformOpenProjectLockAt(shadowDirectory, lockDirectory *os.File, projectID string) (*os.File, *os.File, error) {
	coordinatorFD, err := unix.Openat(int(shadowDirectory.Fd()), "."+projectID+".lock-coordinator", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, nil, err
	}
	coordinator := os.NewFile(uintptr(coordinatorFD), ".locks.coordinator")
	lockFD, err := unix.Openat(int(lockDirectory.Fd()), projectID+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		_ = coordinator.Close()
		if errors.Is(err, unix.ELOOP) {
			return nil, nil, fmt.Errorf("refuse symlink lock file: %w", err)
		}
		return nil, nil, err
	}
	return os.NewFile(uintptr(lockFD), projectID+".lock"), coordinator, nil
}

func platformAcquireProjectLock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func platformReleaseProjectLock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func isProjectLockContention(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}
