package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScanRecordsFilesModesAndSymlinksWithoutTraversal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior differs on Windows")
	}
	source := t.TempDir()
	external := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "plain.txt"), []byte("plain\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(source, "empty"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(source, "real-dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "real-dir", "inside.txt"), []byte("inside"), 0o644))
	require.NoError(t, os.Symlink("plain.txt", filepath.Join(source, "file-link")))
	require.NoError(t, os.Symlink("real-dir", filepath.Join(source, "dir-link")))
	require.NoError(t, os.WriteFile(filepath.Join(external, "secret.txt"), []byte("secret"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(external, "secret.txt"), filepath.Join(source, "external-link")))

	snapshot, err := Scan(source, Policy{})
	require.NoError(t, err)

	require.Equal(t, []string{"dir-link", "external-link", "file-link", "plain.txt", "real-dir/inside.txt", "run.sh"}, snapshot.SortedPaths())
	require.NotContains(t, snapshot.Entries, "empty")
	require.NotContains(t, snapshot.Entries, "dir-link/inside.txt", "directory symlinks must not be traversed")

	plain := snapshot.Entries["plain.txt"]
	require.Equal(t, "plain.txt", plain.Path)
	require.Equal(t, EntryRegular, plain.Type)
	require.Equal(t, os.FileMode(0o644), plain.Mode)
	hash := sha256.Sum256([]byte("plain\n"))
	require.Equal(t, hex.EncodeToString(hash[:]), plain.ContentHash)

	require.Equal(t, os.FileMode(0o755), snapshot.Entries["run.sh"].Mode)
	require.Equal(t, EntrySymlink, snapshot.Entries["file-link"].Type)
	require.Equal(t, "plain.txt", snapshot.Entries["file-link"].SymlinkTarget)
	require.Equal(t, "real-dir", snapshot.Entries["dir-link"].SymlinkTarget)
	require.Equal(t, filepath.Join(external, "secret.txt"), snapshot.Entries["external-link"].SymlinkTarget)
	require.Empty(t, snapshot.Entries["external-link"].ContentHash)
}

func TestScanExcludesAllNonIncludedClassifications(t *testing.T) {
	source := t.TempDir()
	for _, directory := range []string{"ignored", "shared", "generated"} {
		require.NoError(t, os.Mkdir(filepath.Join(source, directory), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(source, directory, "hidden.txt"), []byte("hidden"), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(source, "keep.txt"), []byte("keep"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, policyFileName), []byte("ignored/\n[shared]\nshared/\n[generated]\ngenerated/\n"), 0o644))
	policy, err := LoadPolicy(source)
	require.NoError(t, err)

	snapshot, err := Scan(source, policy)
	require.NoError(t, err)
	require.Equal(t, []string{policyFileName, "keep.txt"}, snapshot.SortedPaths())
}

func TestScanCanExplicitlyExcludePolicyFile(t *testing.T) {
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, policyFileName), []byte(policyFileName+"\n"), 0o644))
	policy, err := LoadPolicy(source)
	require.NoError(t, err)

	snapshot, err := Scan(source, policy)
	require.NoError(t, err)
	require.Empty(t, snapshot.Entries)
}

func TestScanRejectsSpecialFilesWithRelativePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes differ on Windows")
	}
	source := t.TempDir()
	pipe := filepath.Join(source, "unsafe.pipe")
	require.NoError(t, syscall.Mkfifo(pipe, 0o600))

	_, err := Scan(source, Policy{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsafe.pipe")
	require.NotContains(t, err.Error(), source)
}
