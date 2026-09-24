//go:build darwin || linux

package workspace

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

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

func projectLockAnchorAttribute() string {
	if runtime.GOOS == "darwin" {
		return "com.claude-squad.lock-anchor"
	}
	return "user.claude-squad-lock-anchor"
}

func platformOpenOrCreateLockDirectoryAt(parent *os.File) (*os.File, error) {
	for {
		directory, err := platformOpenOrCreateDirectoryAt(parent, ".locks", 0o700)
		if err != nil {
			return nil, err
		}
		anchorIdentity, err := directoryIdentity(directory)
		if err != nil {
			_ = directory.Close()
			return nil, err
		}
		trustedIdentity, err := lockAnchorIdentity(parent)
		if isMissingXattr(err) {
			err = unix.Fsetxattr(int(parent.Fd()), projectLockAnchorAttribute(), anchorIdentity, unix.XATTR_CREATE)
			if err == nil || errors.Is(err, unix.EEXIST) {
				trustedIdentity, err = lockAnchorIdentity(parent)
			}
		}
		if err != nil {
			_ = directory.Close()
			return nil, err
		}
		if !bytes.Equal(trustedIdentity, anchorIdentity) {
			_ = directory.Close()
			return nil, errors.New("lock anchor differs from trusted root identity")
		}
		return directory, nil
	}
}

func directoryIdentity(directory *os.File) ([]byte, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &stat); err != nil {
		return nil, err
	}
	identity := make([]byte, 16)
	binary.LittleEndian.PutUint64(identity[:8], uint64(stat.Dev))
	binary.LittleEndian.PutUint64(identity[8:], uint64(stat.Ino))
	return identity, nil
}

func lockAnchorIdentity(directory *os.File) ([]byte, error) {
	size, err := unix.Fgetxattr(int(directory.Fd()), projectLockAnchorAttribute(), nil)
	if err != nil {
		return nil, err
	}
	identity := make([]byte, size)
	_, err = unix.Fgetxattr(int(directory.Fd()), projectLockAnchorAttribute(), identity)
	return identity, err
}

func isMissingXattr(err error) bool {
	if runtime.GOOS == "darwin" {
		return errors.Is(err, unix.Errno(93)) // ENOATTR on Darwin.
	}
	return errors.Is(err, unix.ENODATA)
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

func platformOpenProjectLockAt(shadowDirectory, lockDirectory *os.File, projectID string) (*os.File, error) {
	coordinatorFD, err := unix.Openat(int(shadowDirectory.Fd()), "."+projectID+".lock-coordinator", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Close(coordinatorFD); err != nil {
		return nil, err
	}
	lockFD, err := unix.Openat(int(lockDirectory.Fd()), projectID+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("refuse symlink lock file: %w", err)
		}
		return nil, err
	}
	return os.NewFile(uintptr(lockFD), projectID+".lock"), nil
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
