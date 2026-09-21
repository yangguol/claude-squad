package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

type EntryType string

const (
	EntryRegular EntryType = "regular"
	EntrySymlink EntryType = "symlink"
)

type Entry struct {
	Path          string      `json:"path"`
	Type          EntryType   `json:"type"`
	Mode          os.FileMode `json:"mode"`
	ContentHash   string      `json:"content_hash,omitempty"`
	SymlinkTarget string      `json:"symlink_target,omitempty"`
}

type Snapshot struct {
	Entries map[string]Entry `json:"entries"`
}

func (s Snapshot) SortedPaths() []string {
	paths := make([]string, 0, len(s.Entries))
	for name := range s.Entries {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return paths
}

func Scan(source string, policy Policy) (Snapshot, error) {
	snapshot := Snapshot{Entries: make(map[string]Entry)}
	err := filepath.WalkDir(source, func(name string, directoryEntry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == source {
			return nil
		}

		relative, err := filepath.Rel(source, name)
		if err != nil {
			return fmt.Errorf("resolve relative path for %q: %w", name, err)
		}
		relative = filepath.ToSlash(relative)
		if policy.Classify(relative) != ClassIncluded {
			if directoryEntry.IsDir() && !policy.mayIncludeDescendant(relative) {
				return filepath.SkipDir
			}
			if !directoryEntry.IsDir() {
				return nil
			}
		}

		info, err := os.Lstat(name)
		if err != nil {
			return fmt.Errorf("lstat %q: %w", relative, err)
		}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			return nil
		case mode.IsRegular():
			hash, err := hashRegularFile(name)
			if err != nil {
				return fmt.Errorf("hash %q: %w", relative, err)
			}
			snapshot.Entries[relative] = Entry{
				Path:        relative,
				Type:        EntryRegular,
				Mode:        mode.Perm(),
				ContentHash: hash,
			}
			return nil
		case mode&os.ModeSymlink != 0:
			target, err := os.Readlink(name)
			if err != nil {
				return fmt.Errorf("read symlink %q: %w", relative, err)
			}
			snapshot.Entries[relative] = Entry{
				Path:          relative,
				Type:          EntrySymlink,
				Mode:          mode.Perm(),
				SymlinkTarget: target,
			}
			return nil
		default:
			return fmt.Errorf("unsupported special file %q (mode %s)", relative, mode.String())
		}
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("scan source: %w", err)
	}
	return snapshot, nil
}

func hashRegularFile(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
