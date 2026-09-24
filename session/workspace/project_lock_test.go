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

func TestProjectDirectoryReplacementCannotSplitSerialization(t *testing.T) {
	shadowProjects := filepath.Join(t.TempDir(), shadowProjectsDirectory)
	projectPath := filepath.Join(shadowProjects, "project")
	first, err := AcquireProjectLock(projectPath, "apply")
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })

	require.NoError(t, os.Mkdir(projectPath, 0o700))
	parked := projectPath + ".parked"
	require.NoError(t, os.Rename(projectPath, parked))
	require.NoError(t, os.Mkdir(projectPath, 0o700))

	_, err = AcquireProjectLock(projectPath, "resume")
	var inProgress *OperationInProgressError
	require.ErrorAs(t, err, &inProgress, "replacing the canonical project directory must not create a second lock domain")
	require.Equal(t, "project", inProgress.ProjectID)
}

func TestProjectLockDoesNotRequireXattrSupport(t *testing.T) {
	shadowProjects := filepath.Join(t.TempDir(), shadowProjectsDirectory)
	original := projectLockXattrProbe
	projectLockXattrProbe = func(string) error { return errors.New("xattrs unsupported") }
	t.Cleanup(func() { projectLockXattrProbe = original })

	lock, err := AcquireProjectLock(filepath.Join(shadowProjects, "project"), "apply")
	require.NoError(t, err)
	require.NoError(t, lock.Close())
}

func TestProjectLockCreatesOnlyAuthoritativeLockArtifact(t *testing.T) {
	shadowProjects := filepath.Join(t.TempDir(), shadowProjectsDirectory)
	lock, err := AcquireProjectLock(filepath.Join(shadowProjects, "project"), "apply")
	require.NoError(t, err)
	require.NoError(t, lock.Close())

	entries, err := os.ReadDir(shadowProjects)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, ".locks", entries[0].Name())
	require.FileExists(t, filepath.Join(shadowProjects, ".locks", "project.lock"))
}

func TestProjectLockRejectsInvalidProjectIDBeforePathUse(t *testing.T) {
	shadowProjects := filepath.Join(t.TempDir(), shadowProjectsDirectory)
	_, err := AcquireProjectLock(filepath.Join(shadowProjects, ".locks"), "apply")
	require.ErrorContains(t, err, "invalid project ID")
	require.NoDirExists(t, shadowProjects)
}

func TestProjectLocksForDistinctProjectsRemainIndependent(t *testing.T) {
	shadowProjects := filepath.Join(t.TempDir(), shadowProjectsDirectory)
	first, err := AcquireProjectLock(filepath.Join(shadowProjects, "first"), "apply")
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })

	second, err := AcquireProjectLock(filepath.Join(shadowProjects, "second"), "apply")
	require.NoError(t, err)
	require.NoError(t, second.Close())
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

	_, err := AcquireProjectLock(filepath.Join(shadowProjects, "project"), "apply")
	require.Error(t, err, "substitution during acquisition must fail closed")
	require.NoFileExists(t, filepath.Join(outside, "project.lock"))
	require.FileExists(t, filepath.Join(parked, "project.lock"))

	require.NoError(t, os.Remove(lockRoot))
	require.NoError(t, os.Rename(parked, lockRoot))
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

func TestAtomicApplyPreflightRejectsTransactionRootSubstitutionAfterProbe(t *testing.T) {
	source := t.TempDir()
	projectPath := t.TempDir()
	transactionRoot := filepath.Join(projectPath, "transactions")
	parked := filepath.Join(projectPath, "transactions-parked")
	outside := t.TempDir()

	originalHook := atomicApplyAfterTransactionRootVerificationHook
	originalRename := platformAtomicRenameNoReplace
	probeCalls := 0
	atomicApplyAfterTransactionRootVerificationHook = func() {
		atomicApplyAfterTransactionRootVerificationHook = nil
		require.NoError(t, os.Rename(transactionRoot, parked))
		require.NoError(t, os.Symlink(outside, transactionRoot))
	}
	platformAtomicRenameNoReplace = func(directoryFD int, source, destination string) error {
		probeCalls++
		return originalRename(directoryFD, source, destination)
	}
	t.Cleanup(func() {
		atomicApplyAfterTransactionRootVerificationHook = originalHook
		platformAtomicRenameNoReplace = originalRename
	})

	err := atomicApplyPreflight(source, transactionRoot)
	require.ErrorContains(t, err, "transaction root changed")
	require.Equal(t, 2, probeCalls, "substitution must be detected after all descriptor-relative probes")
	outsideEntries, readErr := os.ReadDir(outside)
	require.NoError(t, readErr)
	require.Empty(t, outsideEntries)
	parkedEntries, readErr := os.ReadDir(parked)
	require.NoError(t, readErr)
	require.Empty(t, parkedEntries)
}

func TestApplyRejectsTransactionRootSubstitutionBeforePlanning(t *testing.T) {
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
	originalRootHook := atomicApplyAfterTransactionRootVerificationHook
	originalPlanHook := applyPlanStartHook
	planReached := false
	atomicApplyAfterTransactionRootVerificationHook = func() {
		atomicApplyAfterTransactionRootVerificationHook = nil
		require.NoError(t, os.Rename(transactionRoot, parked))
		require.NoError(t, os.Symlink(outside, transactionRoot))
	}
	applyPlanStartHook = func() { planReached = true }
	t.Cleanup(func() {
		atomicApplyAfterTransactionRootVerificationHook = originalRootHook
		applyPlanStartHook = originalPlanHook
	})

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "transaction root changed")
	require.False(t, planReached, "planApply must not be reached after failed preflight")
	require.Equal(t, before, mustSnapshot(t, source))
	outsideEntries, readErr := os.ReadDir(outside)
	require.NoError(t, readErr)
	require.Empty(t, outsideEntries)
	parkedEntries, readErr := os.ReadDir(parked)
	require.NoError(t, readErr)
	require.Empty(t, parkedEntries)
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
