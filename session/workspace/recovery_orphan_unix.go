//go:build darwin || linux

package workspace

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

func orphanEntryInfoAt(directory *os.File, name string) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unix.Stat_t{}, err
	}
	return stat, nil
}

func orphanOpenedInfo(directory *os.File) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &stat); err != nil {
		return unix.Stat_t{}, err
	}
	return stat, nil
}

func sameOrphanIdentity(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino
}

type manifestFileIdentity struct {
	device    uint64
	inode     uint64
	directory bool
}

func platformLookupManifestComponent(directory *os.File, requested string) (string, *manifestFileIdentity, error) {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return "", nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var matched string
	for _, entry := range entries {
		if entry.Name() == requested {
			matched = entry.Name()
			break
		}
	}
	if matched == "" {
		var requestedStat unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), requested, &requestedStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				return "", nil, os.ErrNotExist
			}
			return "", nil, err
		}
		for _, entry := range entries {
			var candidateStat unix.Stat_t
			if err := unix.Fstatat(int(directory.Fd()), entry.Name(), &candidateStat, unix.AT_SYMLINK_NOFOLLOW); err == nil && sameOrphanIdentity(requestedStat, candidateStat) {
				matched = entry.Name()
				break
			}
		}
		if matched == "" {
			return "", nil, errors.New("namespace component identity was not found in parent")
		}
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), matched, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return "", nil, err
	}
	return matched, &manifestFileIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino), directory: stat.Mode&unix.S_IFMT == unix.S_IFDIR}, nil
}

func openOrphanDirectoryAt(parent *os.File, name string) (*os.File, unix.Stat_t, error) {
	entry, err := orphanEntryInfoAt(parent, name)
	if err != nil {
		return nil, unix.Stat_t{}, err
	}
	if entry.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, unix.Stat_t{}, errors.New("orphan transaction is not a real directory")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, unix.Stat_t{}, err
	}
	directory := os.NewFile(uintptr(fd), name)
	opened, err := orphanOpenedInfo(directory)
	if err != nil || !sameOrphanIdentity(entry, opened) {
		_ = directory.Close()
		return nil, unix.Stat_t{}, errors.Join(err, errors.New("orphan transaction changed while opening"))
	}
	return directory, opened, nil
}

func orphanEntryAbsentAt(directory *os.File, name string) (bool, error) {
	_, err := orphanEntryInfoAt(directory, name)
	if errors.Is(err, unix.ENOENT) {
		return true, nil
	}
	return false, err
}

func verifyOrphanDirectoryEntry(parent *os.File, name string, expected unix.Stat_t) error {
	entry, err := orphanEntryInfoAt(parent, name)
	if err != nil {
		return err
	}
	if entry.Mode&unix.S_IFMT != unix.S_IFDIR || !sameOrphanIdentity(entry, expected) {
		return errors.New("orphan directory entry changed identity")
	}
	return nil
}

const (
	orphanQuarantinePrefix = ".orphan-"
	orphanCleanupMarker    = ".cleanup-complete"
	orphanCleanupContents  = "claude-squad orphan cleanup v1\n"
)

func retainedOrphanHasValidMarkerAt(root *os.File, name string) (bool, error) {
	if len(name) != len(orphanQuarantinePrefix)+32 || !strings.HasPrefix(name, orphanQuarantinePrefix) {
		return false, nil
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(name, orphanQuarantinePrefix)); err != nil {
		return false, nil
	}
	directory, _, err := openOrphanDirectoryAt(root, name)
	if err != nil {
		return false, fmt.Errorf("inspect retained orphan: %w", err)
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return false, err
	}
	if len(entries) != 1 || entries[0].Name() != orphanCleanupMarker {
		return false, errors.New("retained orphan marker directory has unexpected contents")
	}
	fd, err := unix.Openat(int(directory.Fd()), orphanCleanupMarker, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	marker := os.NewFile(uintptr(fd), orphanCleanupMarker)
	contents, readErr := io.ReadAll(marker)
	closeErr := marker.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return false, err
	}
	if string(contents) != orphanCleanupContents {
		return false, errors.New("retained orphan marker has invalid contents")
	}
	return true, nil
}

func writeOrphanCleanupMarker(directory *os.File) error {
	fd, err := unix.Openat(int(directory.Fd()), orphanCleanupMarker, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	marker := os.NewFile(uintptr(fd), orphanCleanupMarker)
	_, writeErr := marker.WriteString(orphanCleanupContents)
	syncErr := marker.Sync()
	closeErr := marker.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return directory.Sync()
}

func removeOrphanDirectoryContents(directory *os.File) error {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		stat, err := orphanEntryInfoAt(directory, name)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			child, opened, err := openOrphanDirectoryAt(directory, name)
			if err != nil {
				return err
			}
			if err := removeOrphanDirectoryContents(child); err != nil {
				_ = child.Close()
				return err
			}
			if err := child.Close(); err != nil {
				return err
			}
			if err := verifyOrphanDirectoryEntry(directory, name, opened); err != nil {
				return err
			}
			if err := unix.Unlinkat(int(directory.Fd()), name, unix.AT_REMOVEDIR); err != nil {
				return err
			}
			continue
		}
		if err := unix.Unlinkat(int(directory.Fd()), name, 0); err != nil {
			return err
		}
	}
	return nil
}

func removeOrphanAt(root *os.File, originalName, quarantineName string) error {
	orphan, identity, err := openOrphanDirectoryAt(root, originalName)
	if err != nil {
		return err
	}
	defer orphan.Close()
	absent, err := orphanEntryAbsentAt(orphan, "manifest.json")
	if err != nil || !absent {
		return errors.Join(err, errors.New("orphan transaction acquired a manifest"))
	}
	if err := platformAtomicRenameNoReplace(int(root.Fd()), originalName, quarantineName); err != nil {
		return fmt.Errorf("quarantine orphan transaction: %w", err)
	}
	if err := verifyOrphanDirectoryEntry(root, quarantineName, identity); err != nil {
		return fmt.Errorf("quarantined orphan transaction changed identity: %w", err)
	}
	// This check follows the atomic capture and precedes the test seam: a
	// manifest installed in the source entry before capture must retain the
	// captured directory. Once captured, the private inode and new writes into
	// it are transaction-owned cleanup material.
	absent, err = orphanEntryAbsentAt(orphan, "manifest.json")
	if err != nil || !absent {
		return errors.Join(err, errors.New("orphan transaction acquired a manifest before capture"))
	}
	if orphanAfterQuarantineHook != nil {
		orphanAfterQuarantineHook(quarantineName)
	}
	if err := removeOrphanDirectoryContents(orphan); err != nil {
		return fmt.Errorf("remove quarantined orphan contents: %w", err)
	}
	if err := writeOrphanCleanupMarker(orphan); err != nil {
		return fmt.Errorf("mark quarantined orphan cleanup complete: %w", err)
	}
	// The recursive removal is descriptor-bound, but unlinkat of the directory
	// itself would resolve quarantineName again and could remove a replacement
	// installed after an identity check. Retain the now-empty captured directory;
	// the untrusted source name has already been removed atomically.
	if err := verifyOrphanDirectoryEntry(root, quarantineName, identity); err != nil {
		return fmt.Errorf("retain replaced quarantine pathname: %w", err)
	}
	return nil
}
