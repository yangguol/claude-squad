//go:build darwin || linux

package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRenameNoReplaceContractMovesIntoAbsentDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	require.NoError(t, os.WriteFile(source, []byte("source"), 0o600))

	require.NoError(t, platformRenameNoReplace(source, destination))
	require.NoFileExists(t, source)
	require.Equal(t, "source", mustReadFile(t, destination))
}

func TestRenameNoReplaceContractPreservesExistingDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	require.NoError(t, os.WriteFile(source, []byte("source"), 0o600))
	require.NoError(t, os.WriteFile(destination, []byte("destination"), 0o600))

	require.Error(t, platformRenameNoReplace(source, destination))
	require.Equal(t, "source", mustReadFile(t, source))
	require.Equal(t, "destination", mustReadFile(t, destination))
}

func TestRenameNoReplaceContractSupportsFileDirectoryAndSymlink(t *testing.T) {
	tests := []struct {
		name   string
		create func(string) error
		assert func(*testing.T, string)
	}{
		{
			name:   "file",
			create: func(path string) error { return os.WriteFile(path, []byte("content"), 0o600) },
			assert: func(t *testing.T, path string) { require.Equal(t, "content", mustReadFile(t, path)) },
		},
		{
			name:   "directory",
			create: func(path string) error { return os.Mkdir(path, 0o700) },
			assert: func(t *testing.T, path string) { require.DirExists(t, path) },
		},
		{
			name:   "symlink",
			create: func(path string) error { return os.Symlink("target", path) },
			assert: func(t *testing.T, path string) {
				target, err := os.Readlink(path)
				require.NoError(t, err)
				require.Equal(t, "target", target)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			destination := filepath.Join(root, "destination")
			require.NoError(t, test.create(source))

			require.NoError(t, platformRenameNoReplace(source, destination))
			require.NoFileExists(t, source)
			test.assert(t, destination)
		})
	}
}

func TestAtomicApplyPlatformContract(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	transactionRoot := filepath.Join(root, "transactions")
	require.NoError(t, os.Mkdir(source, 0o700))
	require.NoError(t, os.Mkdir(transactionRoot, 0o700))

	require.NoError(t, VerifyAtomicApplyPlatform())
	require.NoError(t, VerifySameFilesystem(source, transactionRoot))
	require.NoError(t, ProbeAtomicNoReplace(transactionRoot))
	require.NoError(t, atomicApplyPreflight(source, transactionRoot))
}

func TestPreparationTraversalClosesAllDescriptors(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "one", "two", "tree"), 0o755))
	writeApplyFile(t, root, "one/two/tree/file", "content", 0o600)
	fd, err := unix.Dup(1)
	require.NoError(t, err)
	require.NoError(t, unix.Close(fd))
	for range 100 {
		require.NoError(t, platformRequirePathAbsent(root, "one/two/missing"))
		_, err := snapshotDirectoryTree(root, "one/two/tree")
		require.NoError(t, err)
	}
	reused, err := unix.Dup(1)
	require.NoError(t, err)
	t.Cleanup(func() { _ = unix.Close(reused) })
	require.Equal(t, fd, reused)
}
