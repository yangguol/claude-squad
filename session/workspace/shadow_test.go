//go:build darwin || linux

package workspace

import (
	"claude-squad/config"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResetLockContentionPreservesStorageProjectsAndTransactions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var projects []string
	var manifests []string
	for len(projects) < 2 {
		source := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("old"), 0o644))
		value, _, err := NewShadow(source, "contention")
		require.NoError(t, err)
		ws := value.(*shadowWorkspace)
		project := filepath.Dir(ws.data.RepoPath)
		transaction := filepath.Join(project, "transactions", "prepared")
		require.NoError(t, os.MkdirAll(transaction, 0o700))
		manifest := TransactionManifest{Version: CurrentTransactionVersion, TransactionID: "prepared", ProjectID: ws.data.ProjectID, SessionID: ws.data.SessionID, SourcePath: ws.data.SourcePath, State: TransactionPrepared, Entries: []TransactionEntry{}}
		require.NoError(t, WriteManifestDurable(manifest, transaction))
		projects = append(projects, project)
		manifests = append(manifests, filepath.Join(transaction, "manifest.json"))
	}
	sort.Strings(projects)
	statePath := filepath.Join(filepath.Dir(projects[0]), "..", config.StateFileName)
	persisted := []byte(`{"instances":[]}`)
	require.NoError(t, os.WriteFile(statePath, persisted, 0o600))
	lock, err := AcquireProjectLock(projects[1], "concurrent operation")
	require.NoError(t, err)

	entered := false
	err = WithResetPreflight(func() error { entered = true; return nil })
	var busy *OperationInProgressError
	require.ErrorAs(t, err, &busy)
	require.False(t, entered)
	contents, readErr := os.ReadFile(statePath)
	require.NoError(t, readErr)
	require.Equal(t, persisted, contents)
	for _, path := range manifests {
		require.Equal(t, TransactionPrepared, readPersistedTransactionManifest(t, filepath.Dir(path)).State)
	}
	require.NoError(t, lock.Close())
	earlier, err := AcquireProjectLock(projects[0], "after failed reset")
	require.NoError(t, err)
	require.NoError(t, earlier.Close())
}

func TestResetPreservesRecoveryBlockedTransaction(t *testing.T) {
	testResetPreservesBlockedTransaction(t, TransactionRecoveryBlocked)
}

func TestResetPreservesCleanupBlockedTransaction(t *testing.T) {
	testResetPreservesBlockedTransaction(t, TransactionCleanupBlocked)
}

func testResetPreservesBlockedTransaction(t *testing.T, state TransactionState) {
	t.Helper()
	ws := lifecycleWorkspace(t)
	projectPath := filepath.Dir(ws.data.RepoPath)
	transactionDir := filepath.Join(projectPath, "transactions", "blocked")
	require.NoError(t, os.MkdirAll(transactionDir, 0o700))
	manifest := TransactionManifest{Version: CurrentTransactionVersion, TransactionID: "blocked", ProjectID: ws.data.ProjectID, SessionID: ws.data.SessionID, SourcePath: ws.data.SourcePath, State: state, Entries: []TransactionEntry{}, RecoveryIssues: []RecoveryIssue{{TransactionID: "blocked", State: state, Message: "manual recovery required"}}}
	require.NoError(t, WriteManifestDurable(manifest, transactionDir))
	mutated := false

	err := WithResetPreflight(func() error { mutated = true; return nil })
	require.ErrorContains(t, err, "manual recovery required")
	require.False(t, mutated)
	require.DirExists(t, projectPath)
	require.FileExists(t, filepath.Join(transactionDir, "manifest.json"))
}

func TestResetPreservesLegacyIncompleteManifest(t *testing.T) {
	ws := lifecycleWorkspace(t)
	projectPath := filepath.Dir(ws.data.RepoPath)
	transactionDir := filepath.Join(projectPath, "transactions", "legacy")
	require.NoError(t, os.MkdirAll(transactionDir, 0o700))
	manifestPath := filepath.Join(transactionDir, "manifest.json")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`{"version":1}`), 0o600))

	err := WithResetPreflight(func() error { return os.ErrInvalid })
	require.ErrorContains(t, err, "legacy transaction")
	require.NotErrorIs(t, err, os.ErrInvalid)
	require.FileExists(t, manifestPath)
	require.DirExists(t, projectPath)
}

func TestResetRecoversOrdinaryPreCommitTransactionBeforeCleanup(t *testing.T) {
	ws := lifecycleWorkspace(t)
	projectPath := filepath.Dir(ws.data.RepoPath)
	transactionDir := filepath.Join(projectPath, "transactions", "prepared")
	require.NoError(t, os.MkdirAll(transactionDir, 0o700))
	manifest := TransactionManifest{Version: CurrentTransactionVersion, TransactionID: "prepared", ProjectID: ws.data.ProjectID, SessionID: ws.data.SessionID, SourcePath: ws.data.SourcePath, State: TransactionPrepared, Entries: []TransactionEntry{}}
	require.NoError(t, WriteManifestDurable(manifest, transactionDir))

	require.NoError(t, WithResetPreflight(func() error {
		require.Equal(t, TransactionRolledBack, readPersistedTransactionManifest(t, transactionDir).State)
		return nil
	}))
}

func TestResetRejectsProjectInsertedAfterEnumerationWithoutDeletingIt(t *testing.T) {
	ws := lifecycleWorkspace(t)
	root := filepath.Dir(filepath.Dir(ws.data.RepoPath))
	inserted := filepath.Join(root, strings.Repeat("f", 64))
	resetAfterEnumerationHook = func() {
		require.NoError(t, os.MkdirAll(inserted, 0o700))
	}
	t.Cleanup(func() { resetAfterEnumerationHook = nil })
	entered := false

	err := WithResetPreflight(func() error { entered = true; return nil })
	require.ErrorContains(t, err, "changed while locking")
	require.False(t, entered)
	require.DirExists(t, inserted)
}

func TestResetRejectsReferenceInsertedAfterEnumeration(t *testing.T) {
	ws := lifecycleWorkspace(t)
	statePath := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(ws.data.RepoPath))), config.StateFileName)
	persisted := []byte(`{"instances":[]}`)
	require.NoError(t, os.WriteFile(statePath, persisted, 0o600))
	resetAfterEnumerationHook = func() {
		workspaceJSON, err := json.Marshal(ws.Serialize())
		require.NoError(t, err)
		contents := []byte(`{"instances":[{"title":"new","workspace":` + string(workspaceJSON) + `}]}`)
		require.NoError(t, os.WriteFile(statePath, contents, 0o600))
	}
	t.Cleanup(func() { resetAfterEnumerationHook = nil })
	entered := false

	err := WithResetPreflight(func() error { entered = true; return nil })
	require.ErrorContains(t, err, "persisted shadow references changed while locking")
	require.False(t, entered)
	contents, readErr := os.ReadFile(statePath)
	require.NoError(t, readErr)
	require.NotEqual(t, persisted, contents)
}

func TestResetPreflightAcceptsPublishedUpdateAndCreateRollback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixture := newResetRecoveryFixtureWithCreate(t, "published")
	updateIndex := slices.IndexFunc(fixture.manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })
	createIndex := slices.IndexFunc(fixture.manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "create" })
	require.NotEqual(t, -1, updateIndex)
	require.NotEqual(t, -1, createIndex)
	for _, index := range []int{updateIndex, createIndex} {
		entry := &fixture.manifest.Entries[index]
		if entry.Operation == EntryOperationUpdate {
			require.NoError(t, os.Rename(filepath.Join(fixture.manifest.SourcePath, entry.Path), filepath.Join(fixture.manifest.SourcePath, entry.CaptureName)))
		}
		require.NoError(t, os.Rename(filepath.Join(fixture.transactionDir, entry.StagePath), filepath.Join(fixture.manifest.SourcePath, entry.Path)))
		entry.Phase = EntryPhasePublished
	}
	fixture.manifest.State = TransactionApplying
	require.NoError(t, WriteManifestDurable(fixture.manifest, fixture.transactionDir))
	manifestBefore := mustReadFile(t, filepath.Join(fixture.transactionDir, "manifest.json"))
	sourceBefore := mustSnapshot(t, fixture.manifest.SourcePath)
	transactionBefore := mustSnapshot(t, fixture.transactionDir)

	preflightManifest := fixture.manifest
	report, err := PreflightRecoverTransaction(&preflightManifest, fixture.transactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryUnchanged, report.Disposition)
	require.Empty(t, report.Issues)
	require.Equal(t, manifestBefore, mustReadFile(t, filepath.Join(fixture.transactionDir, "manifest.json")))
	require.Equal(t, sourceBefore, mustSnapshot(t, fixture.manifest.SourcePath))
	require.Equal(t, transactionBefore, mustSnapshot(t, fixture.transactionDir))

	cleanupProceeded := false
	require.NoError(t, WithResetPreflight(func() error {
		cleanupProceeded = true
		persisted := readPersistedTransactionManifest(t, fixture.transactionDir)
		require.Equal(t, TransactionRolledBack, persisted.State)
		require.Equal(t, "before", mustReadFile(t, filepath.Join(fixture.manifest.SourcePath, "update")))
		require.NoFileExists(t, filepath.Join(fixture.manifest.SourcePath, "create"))
		for _, path := range []string{"update", "create"} {
			entry := persisted.Entries[slices.IndexFunc(persisted.Entries, func(entry TransactionEntry) bool { return entry.Path == path })]
			require.FileExists(t, filepath.Join(fixture.transactionDir, entry.StagePath))
			require.NoFileExists(t, filepath.Join(fixture.manifest.SourcePath, entry.RollbackCaptureName))
		}
		return nil
	}))
	require.True(t, cleanupProceeded)
}

func TestResetPreflightsFilesystemRecoveryAcrossAllProjects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixtures := []resetRecoveryFixture{newResetRecoveryFixture(t, "first"), newResetRecoveryFixture(t, "second")}
	sort.Slice(fixtures, func(i, j int) bool { return fixtures[i].project < fixtures[j].project })
	earlier, later := fixtures[0], fixtures[1]
	prepareCapturedUpdate(t, &earlier, false)
	prepareCapturedUpdate(t, &later, true)
	earlierManifestBefore := mustReadFile(t, filepath.Join(earlier.transactionDir, "manifest.json"))
	earlierSourceBefore := mustSnapshot(t, earlier.manifest.SourcePath)

	entered := false
	err := WithResetPreflight(func() error { entered = true; return nil })
	require.ErrorContains(t, err, "restore target is occupied")
	require.ErrorContains(t, err, "paths=[update]")
	require.ErrorContains(t, err, "retained_paths=["+later.entry.CaptureName+" "+later.entry.RollbackCaptureName+"]")
	require.False(t, entered)
	require.Equal(t, earlierManifestBefore, mustReadFile(t, filepath.Join(earlier.transactionDir, "manifest.json")))
	require.Equal(t, earlierSourceBefore, mustSnapshot(t, earlier.manifest.SourcePath))
}

func TestResetPreflightsCommittedCleanupAcrossAllProjects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fixtures := []resetRecoveryFixture{newResetRecoveryFixture(t, "first"), newResetRecoveryFixture(t, "second")}
	sort.Slice(fixtures, func(i, j int) bool { return fixtures[i].project < fixtures[j].project })
	earlier, later := fixtures[0], fixtures[1]
	prepareCapturedUpdate(t, &earlier, false)
	require.NoError(t, ExecuteTransaction(&later.manifest, later.transactionDir))
	later.entry = later.manifest.Entries[slices.IndexFunc(later.manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })]
	require.NoError(t, os.Remove(filepath.Join(later.manifest.SourcePath, later.entry.CaptureName)))
	writeApplyFile(t, later.manifest.SourcePath, later.entry.CaptureName, "external", 0o600)
	earlierManifestBefore := mustReadFile(t, filepath.Join(earlier.transactionDir, "manifest.json"))
	earlierSourceBefore := mustSnapshot(t, earlier.manifest.SourcePath)

	entered := false
	err := WithResetPreflight(func() error { entered = true; return nil })
	require.ErrorContains(t, err, "source temporary is not transaction-owned")
	require.ErrorContains(t, err, "paths=[update]")
	require.ErrorContains(t, err, "retained_paths=["+later.entry.CaptureName+"]")
	require.False(t, entered)
	require.Equal(t, earlierManifestBefore, mustReadFile(t, filepath.Join(earlier.transactionDir, "manifest.json")))
	require.Equal(t, earlierSourceBefore, mustSnapshot(t, earlier.manifest.SourcePath))
}

type resetRecoveryFixture struct {
	project        string
	transactionDir string
	manifest       TransactionManifest
	entry          TransactionEntry
}

func newResetRecoveryFixture(t *testing.T, session string) resetRecoveryFixture {
	t.Helper()
	return newResetRecoveryFixtureWithCreates(t, session, map[string]applyEntry{})
}

func newResetRecoveryFixtureWithCreate(t *testing.T, session string) resetRecoveryFixture {
	t.Helper()
	return newResetRecoveryFixtureWithCreates(t, session, map[string]applyEntry{"create": {Type: EntryRegular, Mode: 0o644, Contents: []byte("created")}})
}

func newResetRecoveryFixtureWithCreates(t *testing.T, session string, creates map[string]applyEntry) resetRecoveryFixture {
	t.Helper()
	source := t.TempDir()
	writeApplyFile(t, source, "update", "before", 0o644)
	value, _, err := NewShadow(source, session)
	require.NoError(t, err)
	ws := value.(*shadowWorkspace)
	transactionRoot := filepath.Join(filepath.Dir(ws.data.RepoPath), "transactions")
	require.NoError(t, os.MkdirAll(transactionRoot, 0o700))
	transactionDir, err := os.MkdirTemp(transactionRoot, session+"-")
	require.NoError(t, err)
	before := mustSnapshot(t, source)
	plan := applyPlan{
		TransactionDir: transactionDir,
		SourcePath:     source,
		MergedTree:     strings.Repeat("a", 40),
		OursSnapshot:   before,
		Updates:        map[string]applyEntry{"update": {Type: EntryRegular, Mode: 0o644, Contents: []byte("after")}},
		Creates:        creates,
	}
	manifest, err := ws.prepareTransaction(plan, time.Unix(300, 0).UTC())
	require.NoError(t, err)
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })]
	return resetRecoveryFixture{project: filepath.Dir(ws.data.RepoPath), transactionDir: transactionDir, manifest: manifest, entry: entry}
}

func prepareCapturedUpdate(t *testing.T, fixture *resetRecoveryFixture, occupyTarget bool) {
	t.Helper()
	require.NoError(t, os.Rename(filepath.Join(fixture.manifest.SourcePath, fixture.entry.Path), filepath.Join(fixture.manifest.SourcePath, fixture.entry.CaptureName)))
	fixture.entry.Phase = EntryPhaseCaptured
	fixture.manifest.State = TransactionApplying
	fixture.manifest.Entries[slices.IndexFunc(fixture.manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == fixture.entry.Path })] = fixture.entry
	if occupyTarget {
		writeApplyFile(t, fixture.manifest.SourcePath, fixture.entry.Path, "external", 0o600)
	}
	require.NoError(t, WriteManifestDurable(fixture.manifest, fixture.transactionDir))
}

func TestResetMixedRecoverableAndBlockedProjectsMakesNoRecoveryMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	makeTransaction := func(state TransactionState, id string) (string, string) {
		source := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("old"), 0o644))
		value, _, err := NewShadow(source, id)
		require.NoError(t, err)
		ws := value.(*shadowWorkspace)
		directory := filepath.Join(filepath.Dir(ws.data.RepoPath), "transactions", id)
		require.NoError(t, os.MkdirAll(directory, 0o700))
		manifest := TransactionManifest{Version: CurrentTransactionVersion, TransactionID: id, ProjectID: ws.data.ProjectID, SessionID: ws.data.SessionID, SourcePath: ws.data.SourcePath, State: state, Entries: []TransactionEntry{}}
		if state == TransactionRecoveryBlocked {
			manifest.RecoveryIssues = []RecoveryIssue{{TransactionID: id, State: state, Message: "manual recovery required"}}
		}
		require.NoError(t, WriteManifestDurable(manifest, directory))
		return directory, filepath.Dir(ws.data.RepoPath)
	}
	recoverable, _ := makeTransaction(TransactionPrepared, "recoverable")
	blocked, _ := makeTransaction(TransactionRecoveryBlocked, "blocked")
	before, err := os.ReadFile(filepath.Join(recoverable, "manifest.json"))
	require.NoError(t, err)

	err = WithResetPreflight(func() error { return os.ErrInvalid })
	require.ErrorContains(t, err, "manual recovery required")
	after, readErr := os.ReadFile(filepath.Join(recoverable, "manifest.json"))
	require.NoError(t, readErr)
	require.Equal(t, before, after)
	require.Equal(t, TransactionRecoveryBlocked, readPersistedTransactionManifest(t, blocked).State)
}

func TestResetDoesNotCleanupProjectAppearingAfterFinalValidation(t *testing.T) {
	ws := lifecycleWorkspace(t)
	root := filepath.Dir(filepath.Dir(ws.data.RepoPath))
	inserted := filepath.Join(root, strings.Repeat("e", 64))
	resetAfterValidationHook = func() {
		require.NoError(t, os.MkdirAll(inserted, 0o700))
	}
	t.Cleanup(func() { resetAfterValidationHook = nil })

	err := WithReset(func(cleanup func() error) error { return cleanup() })
	require.ErrorContains(t, err, "changed after validation")
	require.DirExists(t, inserted)
}

func TestResetAcquiresMultipleProjectLocksInSortedOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, source := range []string{t.TempDir(), t.TempDir(), t.TempDir()} {
		require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("content"), 0o644))
		_, _, err := NewShadow(source, "reset-order")
		require.NoError(t, err)
	}
	var acquired []string
	resetProjectLockedHook = func(path string) { acquired = append(acquired, path) }
	t.Cleanup(func() { resetProjectLockedHook = nil })

	require.NoError(t, WithResetPreflight(func() error { return nil }))
	require.True(t, sort.StringsAreSorted(acquired))
	require.Len(t, acquired, 3)
}

func TestNewShadowHoldsProjectLockAcrossBaselineScan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("content"), 0o644))

	var contention error
	scanAfterLstatHook = func(string) {
		if contention != nil {
			return
		}
		canonical, err := filepath.EvalSymlinks(source)
		require.NoError(t, err)
		root, err := shadowRoot()
		require.NoError(t, err)
		_, contention = AcquireProjectLock(filepath.Join(root, shadowProjectID(canonical)), "probe")
	}
	t.Cleanup(func() { scanAfterLstatHook = nil })

	_, _, err := NewShadow(source, "locked-scan")
	require.NoError(t, err)
	var busy *OperationInProgressError
	require.ErrorAs(t, contention, &busy)
}

func lifecycleWorkspace(t *testing.T) *shadowWorkspace {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("old"), 0o644))
	return newApplyWorkspace(t, source)
}

func TestNewSessionRejectedWhileProjectRecoveryBlocked(t *testing.T) {
	ws := lifecycleWorkspace(t)
	projectPath := filepath.Dir(ws.data.RepoPath)
	transactionDir := filepath.Join(projectPath, "transactions", "blocked")
	require.NoError(t, os.MkdirAll(transactionDir, 0o700))
	manifest := TransactionManifest{Version: CurrentTransactionVersion, TransactionID: "blocked", ProjectID: ws.data.ProjectID, SessionID: ws.data.SessionID, SourcePath: ws.data.SourcePath, State: TransactionRecoveryBlocked, Entries: []TransactionEntry{}, RecoveryIssues: []RecoveryIssue{{TransactionID: "blocked", State: TransactionRecoveryBlocked, Message: "manual recovery required"}}}
	require.NoError(t, WriteManifestDurable(manifest, transactionDir))

	_, _, err := NewShadow(ws.data.SourcePath, "rejected")
	require.ErrorContains(t, err, "manual recovery required")
}

func TestRestoreRecoveryBlockedSessionReadOnly(t *testing.T) {
	ws := lifecycleWorkspace(t)
	transactionDir := filepath.Join(filepath.Dir(ws.data.RepoPath), "transactions", "blocked")
	require.NoError(t, os.MkdirAll(transactionDir, 0o700))
	manifest := TransactionManifest{Version: CurrentTransactionVersion, TransactionID: "blocked", ProjectID: ws.data.ProjectID, SessionID: ws.data.SessionID, SourcePath: ws.data.SourcePath, State: TransactionRecoveryBlocked, Entries: []TransactionEntry{}, RecoveryIssues: []RecoveryIssue{{TransactionID: "blocked", State: TransactionRecoveryBlocked, Message: "manual recovery required", Paths: []string{"file"}, RetainedPaths: []string{"retained"}}}}
	require.NoError(t, WriteManifestDurable(manifest, transactionDir))

	restored, err := RestoreShadow(ws.Serialize())
	require.NoError(t, err)
	require.True(t, restored.AccessState().ReadOnly)
	require.Equal(t, RecoveryBlocked, restored.AccessState().Disposition)
	require.Equal(t, []string{"file"}, restored.AccessState().Paths)
	require.Equal(t, []string{"retained"}, restored.AccessState().RetainedPaths)
	require.False(t, restored.Capabilities().Apply)
	require.False(t, restored.Capabilities().Resume)
}

func TestRestoreCleanupBlockedSessionReadOnly(t *testing.T) {
	ws := lifecycleWorkspace(t)
	transactionDir := filepath.Join(filepath.Dir(ws.data.RepoPath), "transactions", "cleanup")
	require.NoError(t, os.MkdirAll(transactionDir, 0o700))
	manifest := TransactionManifest{Version: CurrentTransactionVersion, TransactionID: "cleanup", ProjectID: ws.data.ProjectID, SessionID: ws.data.SessionID, SourcePath: ws.data.SourcePath, State: TransactionCleanupBlocked, Entries: []TransactionEntry{}, RecoveryIssues: []RecoveryIssue{{TransactionID: "cleanup", State: TransactionCleanupBlocked, Message: "cleanup required", Paths: []string{"file"}, RetainedPaths: []string{"capture", "quarantine"}}}}
	require.NoError(t, WriteManifestDurable(manifest, transactionDir))

	restored, err := RestoreShadow(ws.Serialize())
	require.NoError(t, err)
	require.True(t, restored.AccessState().ReadOnly)
	require.Equal(t, RecoveryUnchanged, restored.AccessState().Disposition)
	require.Equal(t, []string{"file"}, restored.AccessState().Paths)
	require.Equal(t, []string{"capture", "quarantine"}, restored.AccessState().RetainedPaths)
	require.False(t, restored.Capabilities().Apply)
}

func TestRestoreLegacyIncompleteSessionReadOnly(t *testing.T) {
	ws := lifecycleWorkspace(t)
	transactionDir := filepath.Join(filepath.Dir(ws.data.RepoPath), "transactions", "legacy")
	require.NoError(t, os.MkdirAll(transactionDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(transactionDir, "manifest.json"), []byte(`{"version":1}`), 0o600))

	restored, err := RestoreShadow(ws.Serialize())
	require.NoError(t, err)
	require.True(t, restored.AccessState().ReadOnly)
	require.Contains(t, restored.AccessState().Message, "legacy")
}

func TestHealthyRecoveredSessionRetainsNormalCapabilities(t *testing.T) {
	ws := lifecycleWorkspace(t)
	restored, err := RestoreShadow(ws.Serialize())
	require.NoError(t, err)
	require.False(t, restored.AccessState().ReadOnly)
	require.True(t, restored.Capabilities().Apply)
	require.True(t, restored.Capabilities().Resume)
}

func TestApplyRefreshesRecoveryAndPublishesAccessUnderContinuousLock(t *testing.T) {
	ws := lifecycleWorkspace(t)
	require.NoError(t, os.WriteFile(filepath.Join(ws.data.WorktreePath, "file"), []byte("agent"), 0o644))
	transactionRoot := filepath.Join(filepath.Dir(ws.data.RepoPath), "transactions")
	require.NoError(t, os.MkdirAll(transactionRoot, 0o700))
	transactionDir, err := os.MkdirTemp(transactionRoot, ws.data.SessionID+"-interrupted-")
	require.NoError(t, err)
	plan := applyPlan{
		TransactionDir: transactionDir,
		SourcePath:     ws.data.SourcePath,
		MergedTree:     strings.Repeat("a", 40),
		OursSnapshot:   mustSnapshot(t, ws.data.SourcePath),
		Updates: map[string]applyEntry{
			"file": {Type: EntryRegular, Mode: 0o644, Contents: []byte("interrupted")},
		},
	}
	manifest, err := ws.prepareTransaction(plan, time.Unix(100, 0).UTC())
	require.NoError(t, err)
	entry := &manifest.Entries[0]
	require.Equal(t, EntryOperationUpdate, entry.Operation)
	require.NoError(t, os.Rename(filepath.Join(ws.data.SourcePath, entry.Path), filepath.Join(ws.data.SourcePath, entry.CaptureName)))
	require.NoError(t, os.Rename(filepath.Join(transactionDir, filepath.FromSlash(entry.StagePath)), filepath.Join(ws.data.SourcePath, entry.Path)))
	entry.Phase = EntryPhasePublished
	manifest.State = TransactionApplying
	require.NoError(t, WriteManifestDurable(manifest, transactionDir))
	require.Equal(t, "interrupted", mustReadFile(t, filepath.Join(ws.data.SourcePath, "file")))
	require.Equal(t, "old", mustReadFile(t, filepath.Join(ws.data.SourcePath, entry.CaptureName)))

	var probes int
	probe := func() {
		_, err := AcquireProjectLock(filepath.Dir(ws.data.RepoPath), "recovery probe")
		var busy *OperationInProgressError
		require.ErrorAs(t, err, &busy)
		probes++
	}
	transactionManifestWriteHook = func(state TransactionState) error {
		if state == TransactionRollingBack || state == TransactionRolledBack {
			probe()
		}
		return nil
	}
	lifecycleAccessStatePublishedHook = func() {
		probe()
		require.Equal(t, "old", mustReadFile(t, filepath.Join(ws.data.SourcePath, "file")), "rollback must restore source before writable access is published")
	}
	t.Cleanup(func() {
		transactionManifestWriteHook = nil
		lifecycleAccessStatePublishedHook = nil
	})

	result, err := ws.Apply()
	require.NoError(t, err)
	require.Equal(t, []string{"file"}, result.Changed)
	require.Equal(t, "agent", mustReadFile(t, filepath.Join(ws.data.SourcePath, "file")))
	require.Equal(t, TransactionRolledBack, readPersistedTransactionManifest(t, transactionDir).State)
	require.False(t, ws.AccessState().ReadOnly)
	require.True(t, ws.Capabilities().Apply)
	require.GreaterOrEqual(t, probes, 3)
}

func TestApplyHoldsProjectLockAcrossCommittedRecoveryCleanupAndPublication(t *testing.T) {
	ws := lifecycleWorkspace(t)
	transactionRoot := filepath.Join(filepath.Dir(ws.data.RepoPath), "transactions")
	require.NoError(t, os.MkdirAll(transactionRoot, 0o700))
	transactionDir, err := os.MkdirTemp(transactionRoot, ws.data.SessionID+"-committed-")
	require.NoError(t, err)
	plan := applyPlan{
		TransactionDir: transactionDir,
		SourcePath:     ws.data.SourcePath,
		MergedTree:     strings.Repeat("b", 40),
		OursSnapshot:   mustSnapshot(t, ws.data.SourcePath),
		Updates: map[string]applyEntry{
			"file": {Type: EntryRegular, Mode: 0o644, Contents: []byte("committed")},
		},
	}
	manifest, err := ws.prepareTransaction(plan, time.Unix(200, 0).UTC())
	require.NoError(t, err)
	require.NoError(t, ExecuteTransaction(&manifest, transactionDir))
	require.Equal(t, TransactionCommitted, readPersistedTransactionManifest(t, transactionDir).State)
	require.Equal(t, "committed", mustReadFile(t, filepath.Join(ws.data.SourcePath, "file")))

	var discovery, cleanupMutation, cleanupState, publication int
	probe := func(stage string) {
		_, err := AcquireProjectLock(filepath.Dir(ws.data.RepoPath), "committed cleanup "+stage+" probe")
		var busy *OperationInProgressError
		require.ErrorAs(t, err, &busy)
		switch stage {
		case "discovery":
			discovery++
		case "cleanup mutation":
			cleanupMutation++
		case "cleanup state":
			cleanupState++
		case "publication":
			publication++
		}
	}
	manifestComponentAfterOpenHook = func(string) { probe("discovery") }
	cleanupAfterSourceQuarantineHook = func(string) { probe("cleanup mutation") }
	transactionManifestWriteHook = func(state TransactionState) error {
		if state == TransactionCleaning || state == TransactionComplete {
			probe("cleanup state")
		}
		return nil
	}
	lifecycleAccessStatePublishedHook = func() { probe("publication") }
	t.Cleanup(func() {
		manifestComponentAfterOpenHook = nil
		cleanupAfterSourceQuarantineHook = nil
		transactionManifestWriteHook = nil
		lifecycleAccessStatePublishedHook = nil
	})

	result, err := ws.Apply()
	require.NoError(t, err)
	require.Empty(t, result.Changed)
	require.Equal(t, "committed", mustReadFile(t, filepath.Join(ws.data.SourcePath, "file")))
	require.Equal(t, TransactionComplete, readPersistedTransactionManifest(t, transactionDir).State)
	require.Positive(t, discovery)
	require.Positive(t, cleanupMutation)
	require.GreaterOrEqual(t, cleanupState, 2)
	require.Equal(t, 1, publication)
}

func TestApplyRefreshesNewBlockedStateBeforeMutation(t *testing.T) {
	ws := lifecycleWorkspace(t)
	transactionDir := filepath.Join(filepath.Dir(ws.data.RepoPath), "transactions", "invalid")
	require.NoError(t, os.MkdirAll(transactionDir, 0o700))
	manifestPath := filepath.Join(transactionDir, "manifest.json")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`{"version":2,"unexpected":true}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(ws.data.WorktreePath, "file"), []byte("agent"), 0o644))

	_, err := ws.Apply()
	require.ErrorContains(t, err, "read-only")
	require.Equal(t, "old", mustReadFile(t, filepath.Join(ws.data.SourcePath, "file")))
	state := ws.AccessState()
	require.True(t, state.ReadOnly)
	require.Equal(t, []string{transactionDir}, state.RetainedPaths)
	require.False(t, ws.Capabilities().Apply)
}

func TestRestoreRetainedOrphanReadOnlyWithExactDeduplicatedPath(t *testing.T) {
	ws := lifecycleWorkspace(t)
	path := filepath.Join(filepath.Dir(ws.data.RepoPath), "transactions", ".orphan-0123456789abcdef0123456789abcdef")
	require.NoError(t, os.MkdirAll(path, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(path, orphanCleanupMarker), []byte(orphanCleanupContents), 0o600))

	restored, err := RestoreShadow(ws.Serialize())
	require.NoError(t, err)
	require.True(t, restored.AccessState().ReadOnly)
	require.Equal(t, RecoveryBlocked, restored.AccessState().Disposition)
	require.Equal(t, []string{path}, restored.AccessState().RetainedPaths)
	require.False(t, restored.Capabilities().Apply)
	require.False(t, restored.Capabilities().Resume)
}

func TestApplyAndResumeContendOnSameProjectLock(t *testing.T) {
	ws := lifecycleWorkspace(t)
	lock, err := AcquireProjectLock(filepath.Dir(ws.data.RepoPath), "holder")
	require.NoError(t, err)
	defer lock.Close()
	_, err = ws.Apply()
	var busy *OperationInProgressError
	require.ErrorAs(t, err, &busy)
	require.ErrorAs(t, ws.Setup(), &busy)
}

func TestShadowLayoutStableIdentityAndCanonicalSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	require.NoError(t, os.Mkdir(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "hello.txt"), []byte("hello\n"), 0o644))
	alias := filepath.Join(parent, "alias")
	require.NoError(t, os.Symlink(source, alias))

	ws, branch, err := NewShadow(alias, "unsafe/../title")
	require.NoError(t, err)
	require.NoError(t, ws.Setup())
	t.Cleanup(func() { require.NoError(t, ws.Cleanup()) })

	canonical, err := filepath.EvalSymlinks(source)
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(canonical))
	wantID := hex.EncodeToString(sum[:])
	data := ws.Serialize().Shadow
	require.NotNil(t, data)
	require.Equal(t, wantID, data.ProjectID)
	require.Equal(t, canonical, data.SourcePath)
	require.Equal(t, "session/"+data.SessionID, branch)
	require.Equal(t, branch, data.BranchName)
	canonicalHome, err := filepath.EvalSymlinks(home)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(canonicalHome, ".claude-squad", "shadow-projects", wantID, "repo"), data.RepoPath)
	require.Equal(t, filepath.Join(canonicalHome, ".claude-squad", "shadow-projects", wantID, "worktrees", data.SessionID), data.WorktreePath)
	require.NotContains(t, data.WorktreePath, "unsafe")
	require.NotEmpty(t, data.BaselineCommit)
	_, err = os.Lstat(filepath.Join(source, ".git"))
	require.ErrorIs(t, err, os.ErrNotExist)

	metadataBytes, err := os.ReadFile(filepath.Join(filepath.Dir(data.RepoPath), "metadata.json"))
	require.NoError(t, err)
	var metadata shadowProjectMetadata
	require.NoError(t, json.Unmarshal(metadataBytes, &metadata))
	require.Equal(t, shadowProjectMetadata{Version: CurrentVersion, ProjectID: wantID, SourcePath: canonical}, metadata)
}

func TestShadowSessionsHaveFreshIndependentBaselines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	file := filepath.Join(source, "state.txt")
	require.NoError(t, os.WriteFile(file, []byte("first\n"), 0o644))

	first, _, err := NewShadow(source, "same title")
	require.NoError(t, err)
	require.NoError(t, first.Setup())
	t.Cleanup(func() { require.NoError(t, first.Cleanup()) })

	require.NoError(t, os.WriteFile(file, []byte("second\n"), 0o644))
	second, _, err := NewShadow(source, "same title")
	require.NoError(t, err)
	require.NoError(t, second.Setup())
	t.Cleanup(func() { require.NoError(t, second.Cleanup()) })

	firstData := first.Serialize().Shadow
	secondData := second.Serialize().Shadow
	require.Equal(t, firstData.ProjectID, secondData.ProjectID)
	require.Equal(t, firstData.RepoPath, secondData.RepoPath)
	require.NotEqual(t, firstData.SessionID, secondData.SessionID)
	require.NotEqual(t, firstData.BranchName, secondData.BranchName)
	require.NotEqual(t, firstData.WorktreePath, secondData.WorktreePath)
	require.NotEqual(t, firstData.BaselineCommit, secondData.BaselineCommit)
	require.Equal(t, "first\n", mustReadFile(t, filepath.Join(first.WorkDir(), "state.txt")))
	require.Equal(t, "second\n", mustReadFile(t, filepath.Join(second.WorkDir(), "state.txt")))

	require.NoError(t, os.WriteFile(filepath.Join(first.WorkDir(), "state.txt"), []byte("agent\n"), 0o644))
	require.Equal(t, "second\n", mustReadFile(t, filepath.Join(second.WorkDir(), "state.txt")))
	require.Equal(t, "second\n", mustReadFile(t, file))
}

func TestShadowSharedLinksAndLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(source, "node_modules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "node_modules", "dep"), []byte("shared"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(source, "vendor"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, ".claude-squad-ignore"), []byte("[shared]\nvendor/\n"), 0o644))

	ws, _, err := NewShadow(source, "links")
	require.NoError(t, err)
	require.NoError(t, ws.Setup())

	data := ws.Serialize()
	require.Equal(t, ModeDirectory, ws.Mode())
	require.Equal(t, Capabilities{BranchPicker: false, Push: false, Apply: true, Resume: true}, ws.Capabilities())
	require.Equal(t, filepath.Base(source), ws.DisplayName())
	require.ElementsMatch(t, []string{"node_modules", "vendor"}, data.Shadow.SharedLinks)
	for _, name := range data.Shadow.SharedLinks {
		target, err := os.Readlink(filepath.Join(ws.WorkDir(), filepath.FromSlash(name)))
		require.NoError(t, err)
		require.Equal(t, data.Shadow.SourcePath, filepath.Dir(target))
		require.Equal(t, filepath.Base(name), filepath.Base(target))
	}

	restored, err := RestoreShadow(data)
	require.NoError(t, err)
	require.Equal(t, data, restored.Serialize())
	valid, err := restored.IsValid()
	require.NoError(t, err)
	require.True(t, valid)
	dirty, err := restored.IsDirty()
	require.NoError(t, err)
	require.False(t, dirty)

	require.NoError(t, os.WriteFile(filepath.Join(ws.WorkDir(), "main.go"), []byte("package changed\n"), 0o644))
	diff := ws.Diff()
	require.NoError(t, diff.Error)
	require.Contains(t, diff.Content, "package changed")
	require.Equal(t, 1, ws.DiffNumstat().Added)
	require.NoError(t, ws.CommitChanges("change"))

	var unsupported *ErrUnsupported
	_, err = ws.IsBranchCheckedOut()
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "branch checkout check", unsupported.Operation)
	err = ws.PushChanges("push", false)
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "push", unsupported.Operation)

	require.NoError(t, ws.Remove())
	valid, err = ws.IsValid()
	require.NoError(t, err)
	require.False(t, valid)
	require.NoError(t, ws.Setup())
	require.NoError(t, ws.Cleanup())
	require.NoDirExists(t, data.Shadow.WorktreePath)
}

func TestShadowRejectsMetadataCollisionAndUnsafeRestore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	source := t.TempDir()
	canonical, err := filepath.EvalSymlinks(source)
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(canonical))
	id := hex.EncodeToString(sum[:])
	canonicalHome, err := filepath.EvalSymlinks(home)
	require.NoError(t, err)
	project := filepath.Join(canonicalHome, ".claude-squad", "shadow-projects", id)
	require.NoError(t, os.MkdirAll(project, 0o755))
	bad, err := json.Marshal(shadowProjectMetadata{Version: CurrentVersion, ProjectID: id, SourcePath: t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(project, "metadata.json"), bad, 0o600))

	_, _, err = NewShadow(source, "collision")
	require.ErrorContains(t, err, "metadata")

	outside := filepath.Join(t.TempDir(), "outside")
	data := Data{Version: CurrentVersion, Mode: ModeDirectory, Shadow: &ShadowData{
		ProjectID: id, SourcePath: canonical, RepoPath: filepath.Join(project, "repo"),
		WorktreePath: outside, SessionID: strings.Repeat("a", 32), BranchName: "session/" + strings.Repeat("a", 32),
		BaselineCommit: strings.Repeat("b", 40),
	}}
	_, err = RestoreShadow(data)
	require.ErrorContains(t, err, "worktree path")
}

func TestShadowRejectsBaselineInjectionAndWrongIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("safe"), 0o644))
	ws, _, err := NewShadow(source, "identity")
	require.NoError(t, err)
	require.NoError(t, ws.Setup())
	t.Cleanup(func() { _ = ws.Cleanup() })
	data := ws.Serialize()

	injected := data
	injected.Shadow = copyShadowData(data.Shadow)
	injected.Shadow.BaselineCommit = "--output=/tmp/injected"
	_, err = RestoreShadow(injected)
	require.ErrorContains(t, err, "baseline commit is invalid")

	wrongBaseline := data
	wrongBaseline.Shadow = copyShadowData(data.Shadow)
	wrongBaseline.Shadow.BaselineCommit = strings.Repeat("f", 40)
	_, err = RestoreShadow(wrongBaseline)
	require.ErrorContains(t, err, "baseline does not resolve to a commit")

	other := t.TempDir()
	require.NoError(t, runGitOnly(other, "init"))
	wrongRepo := data
	wrongRepo.Shadow = copyShadowData(data.Shadow)
	require.NoError(t, os.RemoveAll(data.Shadow.RepoPath))
	require.NoError(t, os.Rename(other, data.Shadow.RepoPath))
	_, err = RestoreShadow(wrongRepo)
	require.Error(t, err)
}

func TestShadowRejectsSymlinkedInternalPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("safe"), 0o644))
	ws, _, err := NewShadow(source, "symlinks")
	require.NoError(t, err)
	data := ws.Serialize().Shadow
	outside := t.TempDir()
	require.NoError(t, os.RemoveAll(filepath.Dir(data.WorktreePath)))
	require.NoError(t, os.Symlink(outside, filepath.Dir(data.WorktreePath)))
	require.ErrorContains(t, ws.Setup(), "symlink")
	require.NoDirExists(t, filepath.Join(outside, data.SessionID))

	require.NoError(t, os.Remove(filepath.Dir(data.WorktreePath)))
	require.NoError(t, os.Mkdir(filepath.Dir(data.WorktreePath), 0o700))
	require.NoError(t, os.RemoveAll(data.RepoPath))
	require.NoError(t, os.Symlink(outside, data.RepoPath))
	_, err = RestoreShadow(ws.Serialize())
	require.ErrorContains(t, err, "symlink")
}

func TestShadowSetupPrunesStaleRegistrationAndCleanupPreservesLiveBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("safe"), 0o644))
	ws, _, err := NewShadow(source, "cleanup")
	require.NoError(t, err)
	require.NoError(t, ws.Setup())
	data := ws.Serialize().Shadow
	require.NoError(t, os.RemoveAll(data.WorktreePath))
	require.NoError(t, ws.Setup())
	require.DirExists(t, data.WorktreePath)

	outside := t.TempDir()
	require.NoError(t, os.RemoveAll(data.WorktreePath))
	require.NoError(t, os.Symlink(outside, data.WorktreePath))
	require.Error(t, ws.Cleanup())
	_, err = runGit(data.RepoPath, nil, "show-ref", "--verify", "refs/heads/"+data.BranchName)
	require.NoError(t, err, "cleanup must retain the branch when worktree removal is unsafe")
}

func TestImportSnapshotRejectsMutationAndSymlinkSwap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	path := filepath.Join(source, "file")
	require.NoError(t, os.WriteFile(path, []byte("before"), 0o644))
	snapshot, err := Scan(source, Policy{})
	require.NoError(t, err)
	repo := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.Mkdir(repo, 0o755))
	require.NoError(t, runGitOnly(repo, "init"))
	require.NoError(t, os.WriteFile(path, []byte("after"), 0o644))
	_, err = importSnapshot(repo, source, snapshot)
	require.ErrorContains(t, err, "changed after scan")

	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "outside"), path))
	_, err = importSnapshot(repo, source, snapshot)
	require.Error(t, err)
}

func TestShadowRejectsSharedSourceAncestorSymlinkSwap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(source, "deps", "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, ".claude-squad-ignore"), []byte("[shared]\ndeps/pkg/\n"), 0o644))
	ws, _, err := NewShadow(source, "shared-swap")
	require.NoError(t, err)
	outside := t.TempDir()
	require.NoError(t, os.RemoveAll(filepath.Join(source, "deps")))
	require.NoError(t, os.Symlink(outside, filepath.Join(source, "deps")))
	require.ErrorContains(t, ws.Setup(), "shared")
	require.NoFileExists(t, filepath.Join(ws.WorkDir(), "deps", "pkg"))
}

func TestShadowRepairsPartialRepositoryInitialization(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("safe"), 0o644))
	canonical, err := filepath.EvalSymlinks(source)
	require.NoError(t, err)
	id := shadowProjectID(canonical)
	root, err := shadowRoot()
	require.NoError(t, err)
	project := filepath.Join(root, id)
	repo := filepath.Join(project, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	metadata, err := json.Marshal(shadowProjectMetadata{Version: CurrentVersion, ProjectID: id, SourcePath: canonical})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(project, "metadata.json"), metadata, 0o600))
	require.NoError(t, runGitOnly(repo, "init"))

	ws, _, err := NewShadow(source, "repair")
	require.NoError(t, err)
	data := ws.Serialize().Shadow
	name, err := runGit(data.RepoPath, nil, "config", "--local", "--get", "user.name")
	require.NoError(t, err)
	require.Equal(t, "Claude Squad", strings.TrimSpace(name))
	require.NoError(t, ws.Setup())
	require.NoError(t, ws.Cleanup())
}

func TestShadowRejectsLinkedOrExternalGitMetadata(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("safe"), 0o644))
	ws, _, err := NewShadow(source, "git-metadata")
	require.NoError(t, err)
	data := ws.Serialize()

	outside := t.TempDir()
	require.NoError(t, runGitOnly(outside, "init"))
	require.NoError(t, os.RemoveAll(filepath.Join(data.Shadow.RepoPath, ".git")))
	require.NoError(t, os.Symlink(filepath.Join(outside, ".git"), filepath.Join(data.Shadow.RepoPath, ".git")))
	_, err = RestoreShadow(data)
	require.ErrorContains(t, err, "git metadata")

	require.NoError(t, os.Remove(filepath.Join(data.Shadow.RepoPath, ".git")))
	require.NoError(t, os.WriteFile(filepath.Join(data.Shadow.RepoPath, ".git"), []byte("gitdir: "+filepath.Join(outside, ".git")+"\n"), 0o600))
	_, err = RestoreShadow(data)
	require.ErrorContains(t, err, "git metadata")
}

func TestIncompleteTransactionReportedOnRestoreAndPreserved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "safe", 0o644)
	workspace := newApplyWorkspace(t, source)
	data := workspace.Serialize()
	directory := filepath.Join(filepath.Dir(data.Shadow.RepoPath), "transactions", data.Shadow.SessionID+"-incomplete")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, writeTransactionManifest(Transaction{Directory: directory, Version: 1, SessionID: data.Shadow.SessionID, SourcePath: source, State: transactionApplying}))

	restored, err := RestoreShadow(data)
	require.NoError(t, err)
	require.True(t, restored.AccessState().ReadOnly)
	require.Contains(t, restored.AccessState().Message, "legacy")
	require.FileExists(t, filepath.Join(directory, "manifest.json"))
}

func TestCommittedCleanupFailureReportedOnRestore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "safe", 0o644)
	workspace := newApplyWorkspace(t, source)
	data := workspace.Serialize()
	directory := filepath.Join(filepath.Dir(data.Shadow.RepoPath), "transactions", data.Shadow.SessionID+"-cleanup")
	transaction := Transaction{Directory: directory, Version: 1, SessionID: data.Shadow.SessionID, SourcePath: source, State: transactionCommitted}
	contents, err := json.MarshalIndent(transaction, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(directory), 0o700))
	require.NoError(t, os.WriteFile(directory+".cleanup.json", contents, 0o600))

	restored, err := RestoreShadow(data)
	require.NoError(t, err)
	require.True(t, restored.AccessState().ReadOnly)
	require.FileExists(t, directory+".cleanup.json")
}

func TestRestoreShadowRejectsUnrelatedWorktreeButAllowsAbsentPausedWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("safe"), 0o644))
	ws, _, err := NewShadow(source, "restore-ownership")
	require.NoError(t, err)
	require.NoError(t, ws.Setup())
	data := ws.Serialize()
	require.NoError(t, ws.Remove())

	paused, err := RestoreShadow(data)
	require.NoError(t, err)
	require.Equal(t, data, paused.Serialize())

	require.NoError(t, os.MkdirAll(data.Shadow.WorktreePath, 0o700))
	require.NoError(t, runGitOnly(data.Shadow.WorktreePath, "init"))
	_, err = RestoreShadow(data)
	require.ErrorContains(t, err, "worktree identity")
	require.NoError(t, os.RemoveAll(data.Shadow.WorktreePath))
	require.NoError(t, ws.Cleanup())
}

func TestRestoreShadowRejectsNestedDirectoryInsideOwnedWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("safe"), 0o644))
	ws, _, err := NewShadow(source, "nested-restore")
	require.NoError(t, err)
	require.NoError(t, ws.Setup())
	data := ws.Serialize()
	require.NoError(t, ws.Remove())

	parentWorktree := filepath.Dir(data.Shadow.WorktreePath)
	require.NoError(t, os.Remove(parentWorktree))
	_, err = runGit(data.Shadow.RepoPath, nil, "worktree", "add", "--", parentWorktree, data.Shadow.BranchName)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = runGit(data.Shadow.RepoPath, nil, "worktree", "remove", "--force", "--", parentWorktree)
		_ = ws.Cleanup()
	})
	require.NoError(t, os.Mkdir(data.Shadow.WorktreePath, 0o700))

	_, err = RestoreShadow(data)
	require.ErrorContains(t, err, "worktree identity")
}

func TestShadowPruneRejectsLinkedOrExternalGitMetadata(t *testing.T) {
	for _, kind := range []string{"symlink", "gitfile"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			source := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("safe"), 0o644))
			ws, _, err := NewShadow(source, "prune-metadata")
			require.NoError(t, err)
			data := ws.Serialize().Shadow

			outside := t.TempDir()
			require.NoError(t, runGitOnly(outside, "init"))
			gitMetadata := filepath.Join(data.RepoPath, ".git")
			require.NoError(t, os.RemoveAll(gitMetadata))
			if kind == "symlink" {
				require.NoError(t, os.Symlink(filepath.Join(outside, ".git"), gitMetadata))
			} else {
				require.NoError(t, os.WriteFile(gitMetadata, []byte("gitdir: "+filepath.Join(outside, ".git")+"\n"), 0o600))
			}

			require.ErrorContains(t, ws.Prune(), "git metadata")
		})
	}
}

func TestImportSnapshotRejectsModeAndSymlinkMutations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("regular mode", func(t *testing.T) {
		source := t.TempDir()
		path := filepath.Join(source, "script")
		require.NoError(t, os.WriteFile(path, []byte("same"), 0o644))
		snapshot, err := Scan(source, Policy{})
		require.NoError(t, err)
		require.NoError(t, os.Chmod(path, 0o755))
		repo := filepath.Join(t.TempDir(), "repo")
		require.NoError(t, os.Mkdir(repo, 0o755))
		require.NoError(t, runGitOnly(repo, "init"))
		_, err = importSnapshot(repo, source, snapshot)
		require.ErrorContains(t, err, "mode changed")
	})

	t.Run("symlink target and type", func(t *testing.T) {
		for _, mutation := range []string{"target", "type"} {
			t.Run(mutation, func(t *testing.T) {
				source := t.TempDir()
				path := filepath.Join(source, "link")
				require.NoError(t, os.Symlink("before", path))
				snapshot, err := Scan(source, Policy{})
				require.NoError(t, err)
				require.NoError(t, os.Remove(path))
				if mutation == "target" {
					require.NoError(t, os.Symlink("after", path))
				} else {
					require.NoError(t, os.WriteFile(path, []byte("before"), 0o644))
				}
				repo := filepath.Join(t.TempDir(), "repo")
				require.NoError(t, os.Mkdir(repo, 0o755))
				require.NoError(t, runGitOnly(repo, "init"))
				_, err = importSnapshot(repo, source, snapshot)
				require.Error(t, err)
			})
		}
	})
}

func TestShadowInitializesExactRepoWithoutModifyingEnclosingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, runGitOnly(home, "init"))
	require.NoError(t, runGitOnly(home, "config", "--local", "user.name", "Enclosing Owner"))
	require.NoError(t, runGitOnly(home, "config", "--local", "user.email", "enclosing@example.com"))

	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("safe"), 0o644))
	canonical, err := filepath.EvalSymlinks(source)
	require.NoError(t, err)
	id := shadowProjectID(canonical)
	root, err := shadowRoot()
	require.NoError(t, err)
	project := filepath.Join(root, id)
	repo := filepath.Join(project, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	metadata, err := json.Marshal(shadowProjectMetadata{Version: CurrentVersion, ProjectID: id, SourcePath: canonical})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(project, "metadata.json"), metadata, 0o600))

	ws, _, err := NewShadow(source, "nested-init")
	require.NoError(t, err)
	name, err := runGit(home, nil, "config", "--local", "--get", "user.name")
	require.NoError(t, err)
	require.Equal(t, "Enclosing Owner", strings.TrimSpace(name))
	shadowName, err := runGit(repo, nil, "config", "--local", "--get", "user.name")
	require.NoError(t, err)
	require.Equal(t, "Claude Squad", strings.TrimSpace(shadowName))
	require.DirExists(t, filepath.Join(repo, ".git"))
	require.NoError(t, ws.Setup())
	require.NoError(t, ws.Cleanup())
}

func TestCleanupShadowProjectsRemovesOnlyValidatedUnreferencedProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root, err := shadowRoot()
	require.NoError(t, err)

	source := t.TempDir()
	canonicalSource, err := filepath.EvalSymlinks(source)
	require.NoError(t, err)
	projectID := shadowProjectID(canonicalSource)
	project := filepath.Join(root, projectID)
	require.NoError(t, writeShadowProjectMetadataForTest(project, shadowProjectMetadata{
		Version: CurrentVersion, ProjectID: projectID, SourcePath: canonicalSource,
	}))
	require.NoError(t, os.WriteFile(filepath.Join(project, "owned"), []byte("remove"), 0o600))

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "keep"), []byte("safe"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, strings.Repeat("a", 64))))

	maliciousID := strings.Repeat("b", 64)
	maliciousProject := filepath.Join(root, maliciousID)
	require.NoError(t, writeShadowProjectMetadataForTest(maliciousProject, shadowProjectMetadata{
		Version: CurrentVersion, ProjectID: maliciousID, SourcePath: outside,
	}))

	err = CleanupShadowProjects()
	require.Error(t, err)
	require.ErrorContains(t, err, strings.Repeat("a", 64))
	require.ErrorContains(t, err, maliciousID)
	require.NoDirExists(t, project)
	require.DirExists(t, maliciousProject)
	require.FileExists(t, filepath.Join(outside, "keep"))
	require.DirExists(t, canonicalSource)
}

func TestCleanupShadowProjectsKeepsStoredReferencesAndRejectsMalformedPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("safe"), 0o644))
	ws, _, err := NewShadow(source, "stored")
	require.NoError(t, err)
	data := ws.Serialize()
	project := filepath.Dir(data.Shadow.RepoPath)

	state := map[string]any{
		"instances": []any{map[string]any{
			"title":     "stored",
			"workspace": data,
		}},
	}
	contents, err := json.Marshal(state)
	require.NoError(t, err)
	configDirectory := filepath.Join(home, ".claude-squad")
	require.NoError(t, os.WriteFile(filepath.Join(configDirectory, "state.json"), contents, 0o600))

	require.NoError(t, CleanupShadowProjects())
	require.DirExists(t, project)

	outside := filepath.Join(t.TempDir(), "outside")
	malformed := data
	malformed.Shadow = copyShadowData(data.Shadow)
	malformed.Shadow.RepoPath = outside
	state["instances"] = []any{map[string]any{"title": "bad", "workspace": malformed}}
	contents, err = json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(configDirectory, "state.json"), contents, 0o600))

	err = CleanupShadowProjects()
	require.ErrorContains(t, err, "repository path")
	require.DirExists(t, project)
}

func TestCleanupShadowProjectsMalformedStateFailsClosedButReportsProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root, err := shadowRoot()
	require.NoError(t, err)
	source := t.TempDir()
	canonicalSource, err := filepath.EvalSymlinks(source)
	require.NoError(t, err)
	projectID := shadowProjectID(canonicalSource)
	project := filepath.Join(root, projectID)
	require.NoError(t, writeShadowProjectMetadataForTest(project, shadowProjectMetadata{
		Version: CurrentVersion, ProjectID: projectID, SourcePath: canonicalSource,
	}))
	malformed := filepath.Join(root, "malformed")
	require.NoError(t, os.Mkdir(malformed, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude-squad", "state.json"), []byte("{"), 0o600))

	err = CleanupShadowProjects()
	require.ErrorContains(t, err, "decode persisted session state")
	require.ErrorContains(t, err, malformed)
	require.DirExists(t, project)
	require.DirExists(t, malformed)
}

func TestStoredShadowProjectReferencesRejectsMalformedStateShapes(t *testing.T) {
	tests := []struct {
		name  string
		state string
	}{
		{"missing instances", `{}`},
		{"null instances", `{"instances":null}`},
		{"malformed instance", `{"instances":[null]}`},
		{"malformed workspace", `{"instances":[{"workspace":7}]}`},
		{"directory missing shadow", `{"instances":[{"workspace":{"version":1,"mode":"directory"}}]}`},
		{"directory null shadow", `{"instances":[{"workspace":{"version":1,"mode":"directory","shadow":null}}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			root, err := shadowRoot()
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(home, ".claude-squad", "state.json"), []byte(test.state), 0o600))
			_, err = storedShadowProjectReferences(root)
			require.Error(t, err)
		})
	}
}

func TestStoredShadowProjectReferencesAllowsLegacyGitRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root, err := shadowRoot()
	require.NoError(t, err)
	state := `{"instances":[{"title":"legacy","worktree":{"repo_path":"/repo","worktree_path":"/tree","session_name":"legacy","branch_name":"branch"}}]}`
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude-squad", "state.json"), []byte(state), 0o600))
	references, err := storedShadowProjectReferences(root)
	require.NoError(t, err)
	require.Empty(t, references)
}

func TestCleanupShadowProjectsReportsTransactionDirectoryWithoutManifest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	canonicalSource, err := filepath.EvalSymlinks(source)
	require.NoError(t, err)
	root, err := shadowRoot()
	require.NoError(t, err)
	projectID := shadowProjectID(canonicalSource)
	project := filepath.Join(root, projectID)
	require.NoError(t, writeShadowProjectMetadataForTest(project, shadowProjectMetadata{Version: CurrentVersion, ProjectID: projectID, SourcePath: canonicalSource}))
	incomplete := filepath.Join(project, "transactions", "interrupted")
	require.NoError(t, os.MkdirAll(incomplete, 0o700))

	err = CleanupShadowProjects()
	require.ErrorContains(t, err, "missing manifest.json")
	require.DirExists(t, project)
	require.DirExists(t, incomplete)
}

func TestCleanupShadowProjectsRejectsProjectReplacementBeforeQuarantine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	canonicalSource, err := filepath.EvalSymlinks(source)
	require.NoError(t, err)
	root, err := shadowRoot()
	require.NoError(t, err)
	projectID := shadowProjectID(canonicalSource)
	project := filepath.Join(root, projectID)
	require.NoError(t, writeShadowProjectMetadataForTest(project, shadowProjectMetadata{Version: CurrentVersion, ProjectID: projectID, SourcePath: canonicalSource}))
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "keep"), []byte("safe"), 0o600))
	originalHook := cleanupBeforeQuarantine
	cleanupBeforeQuarantine = func(path string) {
		require.Equal(t, project, path)
		require.NoError(t, os.RemoveAll(path))
		require.NoError(t, os.Symlink(outside, path))
	}
	t.Cleanup(func() { cleanupBeforeQuarantine = originalHook })

	err = CleanupShadowProjects()
	require.ErrorContains(t, err, "changed before quarantine")
	require.FileExists(t, filepath.Join(outside, "keep"))
	info, statErr := os.Lstat(project)
	require.NoError(t, statErr)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
}

func TestCleanupShadowProjectsKeepsActiveSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("safe"), 0o644))
	ws, _, err := NewShadow(source, "active")
	require.NoError(t, err)
	require.NoError(t, ws.Setup())
	data := ws.Serialize().Shadow
	t.Cleanup(func() { _ = ws.Cleanup() })

	require.NoError(t, CleanupShadowProjects())
	require.DirExists(t, filepath.Dir(data.RepoPath))
	require.DirExists(t, data.WorktreePath)
}

func TestCleanupShadowProjectsReportsAndPreservesIncompleteTransactions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "file"), []byte("safe"), 0o644))
	ws, _, err := NewShadow(source, "transaction")
	require.NoError(t, err)
	data := ws.Serialize().Shadow
	project := filepath.Dir(data.RepoPath)
	transactionDirectory := filepath.Join(project, "transactions", data.SessionID+"-incomplete")
	require.NoError(t, os.MkdirAll(transactionDirectory, 0o700))
	require.NoError(t, writeTransactionManifest(Transaction{
		Directory: transactionDirectory, Version: 1, SessionID: data.SessionID,
		SourcePath: source, State: transactionApplying,
	}))

	err = CleanupShadowProjects()
	require.ErrorContains(t, err, "incomplete apply transactions")
	require.DirExists(t, project)
	require.FileExists(t, filepath.Join(transactionDirectory, "manifest.json"))
	require.FileExists(t, filepath.Join(source, "file"))
}

func writeShadowProjectMetadataForTest(project string, metadata shadowProjectMetadata) error {
	if err := os.MkdirAll(project, 0o700); err != nil {
		return err
	}
	contents, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(project, "metadata.json"), contents, 0o600)
}

func copyShadowData(data *ShadowData) *ShadowData {
	copy := *data
	return &copy
}

func runGitOnly(dir string, args ...string) error {
	_, err := runGit(dir, nil, args...)
	return err
}

func mustReadFile(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(name)
	require.NoError(t, err)
	return string(contents)
}
