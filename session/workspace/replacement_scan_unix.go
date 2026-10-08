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

func platformRequirePathAbsent(root, relative string) error {
	typeName, err := platformPreparedPathType(root, relative)
	if err != nil {
		return err
	}
	if typeName != "" {
		return os.ErrExist
	}
	return nil
}

type openedDirectoryBinding struct {
	parent   *os.File
	name     string
	identity unix.Stat_t
	child    *os.File
}

func closeOpenedDirectoryBindings(root *os.File, bindings []openedDirectoryBinding) error {
	var closeErr error
	for index := len(bindings) - 1; index >= 0; index-- {
		closeErr = errors.Join(closeErr, bindings[index].child.Close())
	}
	return errors.Join(closeErr, root.Close())
}

func verifyOpenedDirectoryBindings(bindings []openedDirectoryBinding) error {
	for index := len(bindings) - 1; index >= 0; index-- {
		binding := bindings[index]
		if err := verifyOrphanDirectoryEntry(binding.parent, binding.name, binding.identity); err != nil {
			return fmt.Errorf("directory component %q changed binding: %w", binding.name, err)
		}
		if err := verifyOpenedReplacementDirectory(binding.child, binding.identity); err != nil {
			return err
		}
	}
	return nil
}

func platformPreparedPathType(root, relative string) (result EntryType, returnErr error) {
	rootDirectory, err := platformOpenTrustedDirectory(root)
	if err != nil {
		return "", err
	}
	bindings := make([]openedDirectoryBinding, 0)
	defer func() { returnErr = errors.Join(returnErr, closeOpenedDirectoryBindings(rootDirectory, bindings)) }()
	parent := rootDirectory
	parts := strings.Split(filepath.ToSlash(relative), "/")
	for index, component := range parts {
		stat, err := orphanEntryInfoAt(parent, component)
		if index == len(parts)-1 && preparedPathAfterLeafStatHook != nil {
			preparedPathAfterLeafStatHook(strings.Join(parts[:index+1], "/"))
		}
		if errors.Is(err, unix.ENOENT) {
			if err := verifyOpenedDirectoryBindings(bindings); err != nil {
				return "", err
			}
			absent, err := orphanEntryAbsentAt(parent, component)
			if err != nil || !absent {
				return "", errors.Join(err, errors.New("prepared path leaf changed after absence check"))
			}
			return "", nil
		}
		if err != nil {
			return "", err
		}
		fileType := stat.Mode & unix.S_IFMT
		if index == len(parts)-1 {
			leaf, err := platformOpenPreparedLeafAt(parent, component)
			if err != nil {
				return "", fmt.Errorf("open prepared path leaf: %w", err)
			}
			opened, statErr := orphanOpenedInfo(leaf)
			verifyErr := verifyOpenedDirectoryBindings(bindings)
			binding, bindingErr := orphanEntryInfoAt(parent, component)
			closeErr := leaf.Close()
			if statErr != nil || bindingErr != nil || !sameOrphanIdentity(stat, opened) || !sameOrphanIdentity(opened, binding) {
				bindingErr = errors.Join(bindingErr, errors.New("prepared path leaf changed binding"))
			}
			if err := errors.Join(statErr, verifyErr, bindingErr, closeErr); err != nil {
				return "", err
			}
			switch fileType {
			case unix.S_IFDIR:
				return EntryType("directory"), nil
			case unix.S_IFREG:
				return EntryRegular, nil
			case unix.S_IFLNK:
				return EntrySymlink, nil
			default:
				return EntryType("special"), nil
			}
		}
		if fileType != unix.S_IFDIR {
			return "", verifyOpenedDirectoryBindings(bindings)
		}
		child, identity, err := openOrphanDirectoryAt(parent, component)
		if err != nil {
			return "", err
		}
		bindings = append(bindings, openedDirectoryBinding{parent: parent, name: component, identity: identity, child: child})
		if preparedPathAfterOpenHook != nil {
			preparedPathAfterOpenHook(strings.Join(parts[:index+1], "/"))
		}
		if err := verifyOrphanDirectoryEntry(parent, component, identity); err != nil {
			return "", err
		}
		parent = child
	}
	return EntryType("directory"), verifyOpenedDirectoryBindings(bindings)
}

func openBoundAbsoluteDirectory(path string) (*os.File, []openedDirectoryBinding, *os.File, error) {
	clean, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return nil, nil, nil, err
	}
	if !filepath.IsAbs(clean) {
		return nil, nil, nil, fmt.Errorf("bound directory must be absolute: %q", path)
	}
	anchor, err := platformOpenTrustedDirectory(string(filepath.Separator))
	if err != nil {
		return nil, nil, nil, err
	}
	bindings := make([]openedDirectoryBinding, 0)
	parent := anchor
	trimmed := strings.TrimPrefix(filepath.ToSlash(clean), "/")
	if trimmed == "" {
		return anchor, bindings, anchor, nil
	}
	for _, component := range strings.Split(trimmed, "/") {
		child, identity, err := openOrphanDirectoryAt(parent, component)
		if err != nil {
			_ = closeOpenedDirectoryBindings(anchor, bindings)
			return nil, nil, nil, err
		}
		bindings = append(bindings, openedDirectoryBinding{parent: parent, name: component, identity: identity, child: child})
		if err := verifyOrphanDirectoryEntry(parent, component, identity); err != nil {
			_ = closeOpenedDirectoryBindings(anchor, bindings)
			return nil, nil, nil, err
		}
		parent = child
	}
	return anchor, bindings, parent, nil
}

func platformSnapshotDirectoryTree(sourceRoot, name string) (result map[string]Entry, returnErr error) {
	rootDirectory, bindings, parent, err := openBoundAbsoluteDirectory(sourceRoot)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, closeOpenedDirectoryBindings(rootDirectory, bindings)) }()
	if replacementScanAfterSourceRootOpenHook != nil {
		replacementScanAfterSourceRootOpenHook()
	}
	parts := strings.Split(filepath.ToSlash(name), "/")
	for index, component := range parts {
		if replacementScanBeforeOpenHook != nil {
			replacementScanBeforeOpenHook(strings.Join(parts[:index+1], "/"))
		}
		child, identity, err := openOrphanDirectoryAt(parent, component)
		if err != nil {
			return nil, fmt.Errorf("open replacement directory %q: %w", strings.Join(parts[:index+1], "/"), err)
		}
		bindings = append(bindings, openedDirectoryBinding{parent: parent, name: component, identity: identity, child: child})
		if err := verifyOrphanDirectoryEntry(parent, component, identity); err != nil {
			return nil, fmt.Errorf("bind replacement directory %q: %w", strings.Join(parts[:index+1], "/"), err)
		}
		parent = child
		if index == len(parts)-1 {
			if replacementScanAfterRootOpenHook != nil {
				replacementScanAfterRootOpenHook()
			}
			result = make(map[string]Entry)
			scanErr := scanReplacementDirectory(parent, "", result)
			verifyErr := verifyOpenedDirectoryBindings(bindings)
			if err := errors.Join(scanErr, verifyErr); err != nil {
				return result, err
			}
			return result, nil
		}
	}
	return nil, errors.New("replacement source is not a real directory")
}

func verifyOpenedReplacementDirectory(directory *os.File, expected unix.Stat_t) error {
	actual, err := orphanOpenedInfo(directory)
	if err != nil || !sameOrphanIdentity(actual, expected) {
		return errors.Join(err, errors.New("replacement directory changed while scanning"))
	}
	return nil
}

func scanReplacementDirectory(directory *os.File, prefix string, result map[string]Entry) error {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, directoryEntry := range entries {
		name := directoryEntry.Name()
		relative := name
		if prefix != "" {
			relative = prefix + "/" + name
		}
		stat, err := orphanEntryInfoAt(directory, name)
		if err != nil {
			return err
		}
		mode := os.FileMode(stat.Mode & 0o777)
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			result[relative] = Entry{Path: relative, Type: EntryType("directory"), Mode: mode}
			if replacementScanBeforeOpenHook != nil {
				replacementScanBeforeOpenHook(relative)
			}
			child, opened, err := openOrphanDirectoryAt(directory, name)
			if err != nil {
				return fmt.Errorf("open replacement descendant %q: %w", relative, err)
			}
			scanErr := scanReplacementDirectory(child, relative, result)
			closeErr := child.Close()
			verifyErr := verifyOrphanDirectoryEntry(directory, name, opened)
			if err := errors.Join(scanErr, closeErr, verifyErr); err != nil {
				return fmt.Errorf("scan replacement descendant %q: %w", relative, err)
			}
		case unix.S_IFREG:
			entry, err := snapshotReplacementRegularAt(directory, name, relative, stat)
			if err != nil {
				return err
			}
			result[relative] = entry
		case unix.S_IFLNK:
			target, err := readReplacementSymlinkAt(directory, name, stat)
			if err != nil {
				return fmt.Errorf("read replacement symlink %q: %w", relative, err)
			}
			result[relative] = Entry{Path: relative, Type: EntrySymlink, Mode: mode, SymlinkTarget: target}
		default:
			return fmt.Errorf("replacement entry %q has unsupported file type", relative)
		}
	}
	return nil
}

func snapshotReplacementRegularAt(directory *os.File, name, relative string, expected unix.Stat_t) (Entry, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Entry{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	opened, statErr := orphanOpenedInfo(file)
	if statErr != nil || opened.Mode&unix.S_IFMT != unix.S_IFREG || !sameOrphanIdentity(expected, opened) {
		_ = file.Close()
		return Entry{}, errors.Join(statErr, errors.New("replacement regular file changed while opening"))
	}
	hash := sha256.New()
	_, readErr := io.Copy(hash, file)
	closeErr := file.Close()
	final, finalErr := orphanEntryInfoAt(directory, name)
	if finalErr != nil || !sameOrphanIdentity(expected, final) {
		finalErr = errors.Join(finalErr, errors.New("replacement regular file changed while reading"))
	}
	if err := errors.Join(readErr, closeErr, finalErr); err != nil {
		return Entry{}, err
	}
	return Entry{Path: relative, Type: EntryRegular, Mode: os.FileMode(expected.Mode & 0o777), ContentHash: hex.EncodeToString(hash.Sum(nil))}, nil
}

func readReplacementSymlinkAt(directory *os.File, name string, expected unix.Stat_t) (string, error) {
	buffer := make([]byte, 256)
	for {
		count, err := unix.Readlinkat(int(directory.Fd()), name, buffer)
		if err != nil {
			return "", err
		}
		if count < len(buffer) {
			final, err := orphanEntryInfoAt(directory, name)
			if err != nil || !sameOrphanIdentity(expected, final) || final.Mode&unix.S_IFMT != unix.S_IFLNK {
				return "", errors.Join(err, errors.New("replacement symlink changed while reading"))
			}
			return string(buffer[:count]), nil
		}
		buffer = make([]byte, len(buffer)*2)
	}
}
