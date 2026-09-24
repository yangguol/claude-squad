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

func TestProjectLockCloseIsNilSafeAndIdempotent(t *testing.T) {
	var nilLock *ProjectLock
	require.NoError(t, nilLock.Close())

	projectPath := filepath.Join(t.TempDir(), shadowProjectsDirectory, "project")
	lock, err := AcquireProjectLock(projectPath, "apply")
	require.NoError(t, err)
	require.NoError(t, lock.Close())
	require.NoError(t, lock.Close())
}

func TestProjectLockRejectsSymlinkedLockDirectory(t *testing.T) {
	shadowProjects := filepath.Join(t.TempDir(), shadowProjectsDirectory)
	require.NoError(t, os.MkdirAll(shadowProjects, 0o700))
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(shadowProjects, ".locks")))

	_, err := AcquireProjectLock(filepath.Join(shadowProjects, "project"), "apply")
	require.ErrorContains(t, err, "symlink")
	require.NoFileExists(t, filepath.Join(outside, "project.lock"))
}

func TestProjectLockRejectsSymlinkedLockFile(t *testing.T) {
	shadowProjects := filepath.Join(t.TempDir(), shadowProjectsDirectory)
	lockRoot := filepath.Join(shadowProjects, ".locks")
	require.NoError(t, os.MkdirAll(lockRoot, 0o700))
	outside := filepath.Join(t.TempDir(), "outside.lock")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(lockRoot, "project.lock")))

	_, err := AcquireProjectLock(filepath.Join(shadowProjects, "project"), "apply")
	require.ErrorContains(t, err, "symlink")
	require.Equal(t, "outside", mustReadFile(t, outside))
}

func TestProbeAtomicNoReplaceExercisesCollisionAndPreservesDestination(t *testing.T) {
	transactionRoot := t.TempDir()
	original := applyRenameNoReplace
	calls := 0
	applyRenameNoReplace = func(source, destination string) error {
		calls++
		if calls == 1 {
			_, err := os.Lstat(destination)
			require.ErrorIs(t, err, os.ErrNotExist)
			return original(source, destination)
		}
		require.Equal(t, "probe-owned destination", mustReadFile(t, destination))
		err := original(source, destination)
		require.Error(t, err)
		require.Equal(t, "probe-owned destination", mustReadFile(t, destination))
		return err
	}
	t.Cleanup(func() { applyRenameNoReplace = original })

	require.NoError(t, ProbeAtomicNoReplace(transactionRoot))
	require.Equal(t, 2, calls)
	entries, err := os.ReadDir(transactionRoot)
	require.NoError(t, err)
	require.Empty(t, entries)
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

func TestApplyRejectsSymlinkedTransactionRootBeforeOutsideMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	before := mustSnapshot(t, source)

	projectPath := filepath.Dir(workspace.data.RepoPath)
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(projectPath, "transactions")))
	original := applyRenameNoReplace
	probeAttempted := false
	applyRenameNoReplace = func(source, destination string) error {
		probeAttempted = true
		return original(source, destination)
	}
	t.Cleanup(func() { applyRenameNoReplace = original })

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "symlink")
	require.False(t, probeAttempted, "preflight must reject the symlink before creating probe artifacts")
	require.Equal(t, before, mustSnapshot(t, source))
	entries, readErr := os.ReadDir(outside)
	require.NoError(t, readErr)
	require.Empty(t, entries)
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
