//go:build darwin || linux

package workspace

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScanRejectsSpecialFilesWithRelativePath(t *testing.T) {
	source := t.TempDir()
	pipe := filepath.Join(source, "unsafe.pipe")
	require.NoError(t, syscall.Mkfifo(pipe, 0o600))

	_, err := Scan(source, Policy{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsafe.pipe")
	require.NotContains(t, err.Error(), source)

	require.NoError(t, os.Remove(pipe))
}
