//go:build darwin || linux

package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type forwardRoots struct {
	sourceAnchor   *os.File
	sourceBindings []openedDirectoryBinding
	source         *os.File
	txAnchor       *os.File
	txBindings     []openedDirectoryBinding
	tx             *os.File
}

func openForwardRoots(sourcePath, transactionDir string) (*forwardRoots, error) {
	sourceAnchor, sourceBindings, source, err := openBoundAbsoluteDirectory(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("bind source root: %w", err)
	}
	txAnchor, txBindings, tx, err := openBoundAbsoluteDirectory(transactionDir)
	if err != nil {
		_ = closeOpenedDirectoryBindings(sourceAnchor, sourceBindings)
		return nil, fmt.Errorf("bind transaction directory: %w", err)
	}
	return &forwardRoots{sourceAnchor: sourceAnchor, sourceBindings: sourceBindings, source: source, txAnchor: txAnchor, txBindings: txBindings, tx: tx}, nil
}

func (r *forwardRoots) close() error {
	return errors.Join(closeOpenedDirectoryBindings(r.sourceAnchor, r.sourceBindings), closeOpenedDirectoryBindings(r.txAnchor, r.txBindings))
}

func (r *forwardRoots) verify() error {
	return errors.Join(verifyOpenedDirectoryBindings(r.sourceBindings), verifyOpenedDirectoryBindings(r.txBindings))
}

func openRelativeParent(root *os.File, relative string) (*os.File, string, error) {
	clean := filepath.ToSlash(relative)
	parts := strings.Split(clean, "/")
	if clean == "" || filepath.IsAbs(relative) || strings.Contains(relative, "\\") || filepath.ToSlash(filepath.Clean(filepath.FromSlash(clean))) != clean {
		return nil, "", errors.New("unsafe relative path")
	}
	for _, component := range parts {
		if component == "" || component == "." || component == ".." {
			return nil, "", errors.New("unsafe relative path component")
		}
	}
	fd, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, "", err
	}
	parent := os.NewFile(uintptr(fd), relative)
	for _, component := range parts[:len(parts)-1] {
		var before unix.Stat_t
		if err := unix.Fstatat(int(parent.Fd()), component, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			_ = parent.Close()
			return nil, "", err
		}
		childFD, openErr := unix.Openat(int(parent.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			_ = parent.Close()
			return nil, "", openErr
		}
		child := os.NewFile(uintptr(childFD), component)
		var opened, rebound unix.Stat_t
		statErr := unix.Fstat(childFD, &opened)
		rebindErr := unix.Fstatat(int(parent.Fd()), component, &rebound, unix.AT_SYMLINK_NOFOLLOW)
		if statErr != nil || rebindErr != nil || before.Mode&unix.S_IFMT != unix.S_IFDIR || !sameOrphanIdentity(before, opened) || !sameOrphanIdentity(opened, rebound) {
			_ = child.Close()
			_ = parent.Close()
			return nil, "", errors.Join(statErr, rebindErr, errors.New("relative directory component changed binding"))
		}
		if err := parent.Close(); err != nil {
			_ = child.Close()
			return nil, "", err
		}
		parent = child
	}
	return parent, parts[len(parts)-1], nil
}

func entryStatAt(root *os.File, relative string) (unix.Stat_t, error) {
	parent, leaf, err := openRelativeParent(root, relative)
	if err != nil {
		return unix.Stat_t{}, err
	}
	defer parent.Close()
	var stat unix.Stat_t
	err = unix.Fstatat(int(parent.Fd()), leaf, &stat, unix.AT_SYMLINK_NOFOLLOW)
	return stat, err
}

func absentAt(root *os.File, relative string) (bool, error) {
	_, err := entryStatAt(root, relative)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
		return true, nil
	}
	return false, err
}

func renameRelativeNoReplace(fromRoot *os.File, fromRelative string, toRoot *os.File, toRelative string) error {
	fromParent, fromLeaf, err := openRelativeParent(fromRoot, fromRelative)
	if err != nil {
		return err
	}
	defer fromParent.Close()
	toParent, toLeaf, err := openRelativeParent(toRoot, toRelative)
	if err != nil {
		return err
	}
	defer toParent.Close()
	if err := platformRenameatNoReplaceBetween(int(fromParent.Fd()), fromLeaf, int(toParent.Fd()), toLeaf); err != nil {
		return err
	}
	return errors.Join(fromParent.Sync(), toParent.Sync())
}

func mkdirRelative(root *os.File, relative string, mode os.FileMode) (unix.Stat_t, error) {
	parent, leaf, err := openRelativeParent(root, relative)
	if err != nil {
		return unix.Stat_t{}, err
	}
	defer parent.Close()
	if err := unix.Mkdirat(int(parent.Fd()), leaf, uint32(mode.Perm())); err != nil {
		return unix.Stat_t{}, err
	}
	if err := parent.Sync(); err != nil {
		return unix.Stat_t{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), leaf, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unix.Stat_t{}, err
	}
	return stat, nil
}

func snapshotEntryAt(root *os.File, relative string) (Entry, bool, error) {
	parent, leaf, err := openRelativeParent(root, relative)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return Entry{}, false, nil
		}
		return Entry{}, false, err
	}
	defer parent.Close()
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), leaf, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return Entry{}, false, nil
		}
		return Entry{}, false, err
	}
	entry := Entry{Path: relative, Mode: os.FileMode(stat.Mode & 0o777)}
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		entry.Type = EntryType("directory")
	case unix.S_IFREG:
		fd, err := unix.Openat(int(parent.Fd()), leaf, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return Entry{}, false, err
		}
		file := os.NewFile(uintptr(fd), leaf)
		opened, statErr := orphanOpenedInfo(file)
		hash := sha256.New()
		_, readErr := io.Copy(hash, file)
		closeErr := file.Close()
		final, finalErr := orphanEntryInfoAt(parent, leaf)
		if statErr != nil || !sameOrphanIdentity(stat, opened) || finalErr != nil || !sameOrphanIdentity(stat, final) {
			return Entry{}, false, errors.Join(statErr, readErr, closeErr, finalErr, errors.New("regular file changed while validating"))
		}
		if err := errors.Join(readErr, closeErr); err != nil {
			return Entry{}, false, err
		}
		entry.Type = EntryRegular
		entry.ContentHash = hex.EncodeToString(hash.Sum(nil))
	case unix.S_IFLNK:
		target, err := readReplacementSymlinkAt(parent, leaf, stat)
		if err != nil {
			return Entry{}, false, err
		}
		entry.Type = EntrySymlink
		entry.SymlinkTarget = target
	default:
		return Entry{}, false, errors.New("unsupported file type")
	}
	return entry, true, nil
}

func openExclusiveAt(directory *os.File, name string, mode os.FileMode) (int, error) {
	return unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
}

func renameReplaceAt(directory *os.File, from, to string) error {
	return unix.Renameat(int(directory.Fd()), from, int(directory.Fd()), to)
}

func unlinkAt(directory *os.File, name string) error {
	return unix.Unlinkat(int(directory.Fd()), name, 0)
}

func readFileAt(directory *os.File, name string) ([]byte, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	contents, readErr := io.ReadAll(file)
	return contents, errors.Join(readErr, file.Close())
}

func snapshotSubtreeAt(root *os.File, relative string) (map[string]Entry, error) {
	parent, leaf, err := openRelativeParent(root, relative)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	child, _, err := openOrphanDirectoryAt(parent, leaf)
	if err != nil {
		return nil, err
	}
	defer child.Close()
	result := make(map[string]Entry)
	if err := scanReplacementDirectory(child, "", result); err != nil {
		return nil, err
	}
	return result, nil
}
