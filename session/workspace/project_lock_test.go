package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectLockIsNonBlockingAndReleasedByClose(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), shadowProjectsDirectory, "project")
	first, err := AcquireProjectLock(projectPath, "apply")
	require.NoError(t, err)

	_, err = AcquireProjectLock(projectPath, "reset")
	var inProgress *OperationInProgressError
	require.ErrorAs(t, err, &inProgress)
	require.Equal(t, "project", inProgress.ProjectID)
	require.Equal(t, "reset", inProgress.Operation)

	require.NoError(t, first.Close())
	second, err := AcquireProjectLock(projectPath, "reset")
	require.NoError(t, err)
	require.NoError(t, second.Close())
}

func TestProjectLockSerializesSessionsForOneProject(t *testing.T) {
	shadowProjects := filepath.Join(t.TempDir(), shadowProjectsDirectory)
	firstProjectPath := filepath.Join(shadowProjects, "same-project")
	secondSessionProjectPath := filepath.Join(shadowProjects, "same-project")

	lock, err := AcquireProjectLock(firstProjectPath, "apply")
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })

	_, err = AcquireProjectLock(secondSessionProjectPath, "recover")
	var inProgress *OperationInProgressError
	require.ErrorAs(t, err, &inProgress)
	require.Equal(t, "same-project", inProgress.ProjectID)
}

func TestProjectLockNeverTreatsContentsAsStale(t *testing.T) {
	projectPath := filepath.Join(t.TempDir(), shadowProjectsDirectory, "project")
	lockPath := filepath.Join(filepath.Dir(projectPath), ".locks", "project.lock")
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0o700))
	require.NoError(t, os.WriteFile(lockPath, []byte("stale-looking owner data"), 0o600))

	lock, err := AcquireProjectLock(projectPath, "apply")
	require.NoError(t, err)
	defer func() { require.NoError(t, lock.Close()) }()
	require.Equal(t, "stale-looking owner data", mustReadFile(t, lockPath))
}

func TestApplyRejectsDifferentFilesystemsBeforeSourceMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	before := mustSnapshot(t, source)

	original := filesystemDeviceID
	filesystemDeviceID = func(path string) (uint64, error) {
		if filepath.Clean(path) == filepath.Clean(workspace.data.SourcePath) {
			return 1, nil
		}
		return 2, nil
	}
	t.Cleanup(func() { filesystemDeviceID = original })

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "same filesystem")
	require.Equal(t, before, mustSnapshot(t, source))
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", "*", "manifest.json"))
	require.NoError(t, globErr)
	require.Empty(t, matches)
}

func TestAtomicNoReplacePreflightRunsBeforeManifestAndSourceMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	before := mustSnapshot(t, source)

	original := applyRenameNoReplace
	applyRenameNoReplace = func(_, _ string) error { return errors.New("injected no-replace unavailable") }
	t.Cleanup(func() { applyRenameNoReplace = original })

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "injected no-replace unavailable")
	require.Equal(t, before, mustSnapshot(t, source))
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", "*", "manifest.json"))
	require.NoError(t, globErr)
	require.Empty(t, matches)
}
