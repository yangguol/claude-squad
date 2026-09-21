package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectModeGitRoot(t *testing.T) {
	repo := initGitRepo(t)

	mode, err := DetectMode(repo)
	require.NoError(t, err)
	require.Equal(t, ModeGit, mode)
}

func TestDetectModeNestedGitDirectory(t *testing.T) {
	repo := initGitRepo(t)
	nested := filepath.Join(repo, "nested", "directory")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	mode, err := DetectMode(nested)
	require.NoError(t, err)
	require.Equal(t, ModeGit, mode)
}

func TestDetectModePlainDirectory(t *testing.T) {
	dir := t.TempDir()

	mode, err := DetectMode(dir)
	require.NoError(t, err)
	require.Equal(t, ModeDirectory, mode)
}

func TestDetectModeMissingPath(t *testing.T) {
	_, err := DetectMode(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}

func TestDetectModeRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(path, []byte("content"), 0o644))

	_, err := DetectMode(path)
	require.Error(t, err)
}

func TestDetectModeResolvesSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.Symlink(dir, link))

	mode, err := DetectMode(link)
	require.NoError(t, err)
	require.Equal(t, ModeDirectory, mode)
}

func TestNewPlainDirectoryReportsUnavailableBackend(t *testing.T) {
	ws, branch, err := New(t.TempDir(), "feature", "")
	require.Nil(t, ws)
	require.Empty(t, branch)
	require.ErrorIs(t, err, ErrDirectoryBackendUnavailable)
}
