//go:build darwin || linux

package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

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
