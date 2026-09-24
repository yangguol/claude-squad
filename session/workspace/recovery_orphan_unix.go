//go:build darwin || linux

package workspace

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

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

type manifestComponentLookup struct {
	actualName      string
	aliasDetected   bool
	caseInsensitive bool
}

func platformLookupManifestComponent(directory *os.File, requested string) (manifestComponentLookup, error) {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return manifestComponentLookup{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	lookup := manifestComponentLookup{}
	for _, entry := range entries {
		if entry.Name() == requested {
			lookup.actualName = entry.Name()
		}
		alternate := alternateASCIIName(entry.Name())
		if alternate == entry.Name() {
			continue
		}
		var original, alias unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), entry.Name(), &original, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return manifestComponentLookup{}, err
		}
		if err := unix.Fstatat(int(directory.Fd()), alternate, &alias, unix.AT_SYMLINK_NOFOLLOW); err == nil && sameOrphanIdentity(original, alias) {
			lookup.caseInsensitive = true
		} else if err != nil && !errors.Is(err, unix.ENOENT) {
			return manifestComponentLookup{}, err
		}
	}
	if lookup.actualName != "" {
		return lookup, nil
	}

	var requestedStat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), requested, &requestedStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return lookup, os.ErrNotExist
		}
		return manifestComponentLookup{}, err
	}
	for _, entry := range entries {
		var candidateStat unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), entry.Name(), &candidateStat, unix.AT_SYMLINK_NOFOLLOW); err == nil && sameOrphanIdentity(requestedStat, candidateStat) {
			lookup.actualName = entry.Name()
			lookup.aliasDetected = true
			lookup.caseInsensitive = true
			return lookup, nil
		}
	}
	return manifestComponentLookup{}, errors.New("namespace component identity was not found in parent")
}

func alternateASCIIName(name string) string {
	for index, character := range name {
		if character >= 'a' && character <= 'z' {
			return name[:index] + string(character-'a'+'A') + name[index+1:]
		}
		if character >= 'A' && character <= 'Z' {
			return name[:index] + string(character-'A'+'a') + name[index+1:]
		}
	}
	return name
}

func openManifestComponentAt(parent *os.File, name string) (*os.File, *manifestFileIdentity, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ELOOP) {
		var binding unix.Stat_t
		if statErr := unix.Fstatat(int(parent.Fd()), name, &binding, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
			return nil, nil, statErr
		}
		if binding.Mode&unix.S_IFMT == unix.S_IFLNK {
			return nil, &manifestFileIdentity{device: uint64(binding.Dev), inode: uint64(binding.Ino)}, nil
		}
	}
	if err != nil {
		return nil, nil, err
	}
	component := os.NewFile(uintptr(fd), name)
	if manifestComponentAfterOpenHook != nil {
		manifestComponentAfterOpenHook(name)
	}
	stat, err := orphanOpenedInfo(component)
	if err != nil {
		_ = component.Close()
		return nil, nil, err
	}
	binding, err := orphanEntryInfoAt(parent, name)
	if err != nil || !sameOrphanIdentity(binding, stat) {
		_ = component.Close()
		return nil, nil, errors.Join(err, errors.New("namespace component changed while opening"))
	}
	fileType := stat.Mode & unix.S_IFMT
	if fileType != unix.S_IFDIR && fileType != unix.S_IFREG {
		_ = component.Close()
		return nil, nil, errors.New("namespace component has unsupported file type")
	}
	return component, &manifestFileIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino), directory: fileType == unix.S_IFDIR}, nil
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

func isRetainedOrphanName(name string) bool {
	if len(name) != len(orphanQuarantinePrefix)+32 || name[:len(orphanQuarantinePrefix)] != orphanQuarantinePrefix {
		return false
	}
	suffix := name[len(orphanQuarantinePrefix):]
	if _, err := hex.DecodeString(suffix); err != nil {
		return false
	}
	for _, character := range suffix {
		if character >= 'A' && character <= 'F' {
			return false
		}
	}
	return true
}

func validateRetainedOrphanMarkerAt(root *os.File, name string) error {
	directory, _, err := openOrphanDirectoryAt(root, name)
	if err != nil {
		return fmt.Errorf("inspect retained orphan: %w", err)
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Name() != orphanCleanupMarker {
		return errors.New("retained orphan marker directory has unexpected contents")
	}
	fd, err := unix.Openat(int(directory.Fd()), orphanCleanupMarker, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	marker := os.NewFile(uintptr(fd), orphanCleanupMarker)
	stat, statErr := orphanOpenedInfo(marker)
	if statErr != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = marker.Close()
		return errors.Join(statErr, errors.New("retained orphan marker is not a regular file"))
	}
	contents, readErr := io.ReadAll(marker)
	closeErr := marker.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	if string(contents) != orphanCleanupContents {
		return errors.New("retained orphan marker has invalid contents")
	}
	return nil
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
