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

func TestProjectLockHandlesLockDirectorySubstitutionWithoutAliasEscape(t *testing.T) {
	shadowProjects := filepath.Join(t.TempDir(), shadowProjectsDirectory)
	lockRoot := filepath.Join(shadowProjects, ".locks")
	parked := filepath.Join(shadowProjects, ".locks-parked")
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(lockRoot, 0o700))

	original := projectLockAfterDirectoryVerificationHook
	projectLockAfterDirectoryVerificationHook = func() {
		projectLockAfterDirectoryVerificationHook = nil
		require.NoError(t, os.Rename(lockRoot, parked))
		require.NoError(t, os.Symlink(outside, lockRoot))
	}
	t.Cleanup(func() { projectLockAfterDirectoryVerificationHook = original })

	lock, err := AcquireProjectLock(filepath.Join(shadowProjects, "project"), "apply")
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(outside, "project.lock"))
	require.FileExists(t, filepath.Join(parked, "project.lock"))

	require.NoError(t, os.Remove(lockRoot))
	require.NoError(t, os.Mkdir(lockRoot, 0o700))
	_, err = AcquireProjectLock(filepath.Join(shadowProjects, "project"), "reset")
	var inProgress *OperationInProgressError
	require.ErrorAs(t, err, &inProgress, "the stable project coordinator must prevent a replacement directory from escaping serialization")
	require.NoError(t, lock.Close())
	second, err := AcquireProjectLock(filepath.Join(shadowProjects, "project"), "reset")
	require.NoError(t, err)
	require.NoError(t, second.Close())
}

func TestProbeAtomicNoReplaceExercisesCollisionAndPreservesDestination(t *testing.T) {
	transactionRoot := t.TempDir()
	original := platformAtomicRenameNoReplace
	calls := 0
	platformAtomicRenameNoReplace = func(directoryFD int, source, destination string) error {
		calls++
		return original(directoryFD, source, destination)
	}
	t.Cleanup(func() { platformAtomicRenameNoReplace = original })

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
	original := platformAtomicRenameNoReplace
	probeAttempted := false
	platformAtomicRenameNoReplace = func(directoryFD int, source, destination string) error {
		probeAttempted = true
		return original(directoryFD, source, destination)
	}
	t.Cleanup(func() { platformAtomicRenameNoReplace = original })

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "symlink")
	require.False(t, probeAttempted, "preflight must reject the symlink before creating probe artifacts")
	require.Equal(t, before, mustSnapshot(t, source))
	entries, readErr := os.ReadDir(outside)
	require.NoError(t, readErr)
	require.Empty(t, entries)
}

func TestApplyRejectsTransactionRootSubstitutionBeforeProbe(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	before := mustSnapshot(t, source)

	projectPath := filepath.Dir(workspace.data.RepoPath)
	transactionRoot := filepath.Join(projectPath, "transactions")
	parked := filepath.Join(projectPath, "transactions-parked")
	outside := t.TempDir()
	original := atomicApplyAfterTransactionRootVerificationHook
	atomicApplyAfterTransactionRootVerificationHook = func() {
		require.NoError(t, os.Rename(transactionRoot, parked))
		require.NoError(t, os.Symlink(outside, transactionRoot))
	}
	t.Cleanup(func() { atomicApplyAfterTransactionRootVerificationHook = original })

	_, err := workspace.Apply()
	require.Error(t, err)
	require.Equal(t, before, mustSnapshot(t, source))
	outsideEntries, readErr := os.ReadDir(outside)
	require.NoError(t, readErr)
	require.Empty(t, outsideEntries)
	parkedEntries, readErr := os.ReadDir(parked)
	require.NoError(t, readErr)
	require.Empty(t, parkedEntries)
}

func TestAtomicNoReplacePreflightRunsBeforeManifestAndSourceMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	before := mustSnapshot(t, source)

	original := platformAtomicRenameNoReplace
	platformAtomicRenameNoReplace = func(int, string, string) error { return errors.New("injected no-replace unavailable") }
	t.Cleanup(func() { platformAtomicRenameNoReplace = original })

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "injected no-replace unavailable")
	require.Equal(t, before, mustSnapshot(t, source))
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", "*", "manifest.json"))
	require.NoError(t, globErr)
	require.Empty(t, matches)
}
