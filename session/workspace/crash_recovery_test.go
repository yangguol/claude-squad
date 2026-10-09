//go:build darwin || linux

package workspace

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const crashExitCode = 86

type crashFixture struct {
	Project        string   `json:"project"`
	Source         string   `json:"source"`
	TransactionDir string   `json:"transaction_dir"`
	Before         Snapshot `json:"before"`
}

type crashExpectation struct {
	point      DurabilityPoint
	occurrence int
	state      TransactionState
	path       string
	phase      EntryPhase
	cleanup    CleanupPhase
	allPhase   EntryPhase
	allCleanup CleanupPhase
	committed  bool
}

func TestCrashRecoveryInitialManifestPublication(t *testing.T) {
	t.Run("before publication is a removable orphan", func(t *testing.T) {
		fixture := runCrashHelperAt(t, "apply", DurabilityInitialManifestBeforePublication, 1, "")
		_, err := os.Lstat(filepath.Join(fixture.TransactionDir, "manifest.json"))
		require.ErrorIs(t, err, os.ErrNotExist)
		runCrashHelper(t, "discover", "", fixture.TransactionDir)
		_, err = os.Lstat(fixture.TransactionDir)
		require.ErrorIs(t, err, os.ErrNotExist)
		require.Equal(t, fixture.Before, mustSnapshot(t, fixture.Source))
	})
	t.Run("after publication is a prepared transaction", func(t *testing.T) {
		fixture := runCrashHelperAt(t, "apply", DurabilityInitialManifestAfterPublication, 1, "")
		assertCrashManifest(t, fixture, crashExpectation{state: TransactionPrepared, allPhase: EntryPhasePending, allCleanup: CleanupPhasePending})
		runCrashHelper(t, "recover", "", fixture.TransactionDir)
		assertCrashConverged(t, fixture, false)
	})
}

func TestCrashRecoveryForwardMatrix(t *testing.T) {
	// Generic manifest points are occurrence-sensitive. Occurrence one surrounds
	// the applying-state write; the later filesystem points are unambiguous.
	cases := []crashExpectation{
		{point: DurabilityManifestBeforePersistence, occurrence: 1, state: TransactionPrepared, allPhase: EntryPhasePending, allCleanup: CleanupPhasePending},
		{point: DurabilityManifestAfterPersistence, occurrence: 1, state: TransactionApplying, allPhase: EntryPhasePending, allCleanup: CleanupPhasePending},
		{point: durabilityPathPoint(DurabilityForwardCapture, "update"), state: TransactionApplying, path: "update", phase: EntryPhaseCaptureIntent},
		{point: durabilityPathPoint(DurabilityForwardPublish, "update"), state: TransactionApplying, path: "update", phase: EntryPhasePublishIntent},
		{point: DurabilityForwardReadyToCommit, state: TransactionApplying, allCleanup: CleanupPhasePending},
		{point: DurabilityForwardCommit, state: TransactionCommitted, allCleanup: CleanupPhasePending, committed: true},
	}
	for _, test := range cases {
		t.Run(string(test.point)+"-"+strconv.Itoa(test.occurrence), func(t *testing.T) {
			fixture := runCrashHelperAt(t, "apply", test.point, max(test.occurrence, 1), "")
			assertCrashManifest(t, fixture, test)
			runCrashHelper(t, "recover", "", fixture.TransactionDir)
			assertCrashConverged(t, fixture, test.committed)
			runCrashHelper(t, "recover", "", fixture.TransactionDir)
			assertCrashConverged(t, fixture, test.committed)
		})
	}
}

func TestCrashRecoveryOperationBoundaryMatrix(t *testing.T) {
	// One representative real-exit case covers every applicable operation at
	// capture/publication, including both durable nested parent publications.
	cases := []crashExpectation{
		{point: durabilityPathPoint(DurabilityForwardPublish, "new/deep/file"), state: TransactionApplying, path: "new/deep/file", phase: EntryPhasePublishIntent},
		{point: durabilityPathPoint(DurabilityForwardCapture, "update"), state: TransactionApplying, path: "update", phase: EntryPhaseCaptureIntent},
		{point: durabilityPathPoint(DurabilityForwardCapture, "delete"), state: TransactionApplying, path: "delete", phase: EntryPhaseCaptureIntent},
		{point: durabilityPathPoint(DurabilityForwardPublish, "new"), state: TransactionApplying, path: "new", phase: EntryPhasePublishIntent},
		{point: durabilityPathPoint(DurabilityForwardPublish, "new/deep"), state: TransactionApplying, path: "new/deep", phase: EntryPhasePublishIntent},
	}
	for _, test := range cases {
		t.Run(string(test.point), func(t *testing.T) {
			fixture := runCrashHelperAt(t, "apply", test.point, 1, "")
			assertCrashManifest(t, fixture, test)
			runCrashHelper(t, "recover", "", fixture.TransactionDir)
			assertCrashConverged(t, fixture, false)
		})
	}
}

func TestCrashRecoveryTypeTransitionMatrix(t *testing.T) {
	paths := []string{"regular-to-symlink", "symlink-to-regular", "directory-to-regular", "directory-to-symlink"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			point := durabilityPathPoint(DurabilityForwardPublish, path)
			fixture := runCrashHelperAt(t, "apply", point, 1, "")
			assertCrashManifest(t, fixture, crashExpectation{state: TransactionApplying, path: path, phase: EntryPhasePublishIntent})
			runCrashHelper(t, "recover", "", fixture.TransactionDir)
			assertCrashConverged(t, fixture, false)
		})
	}
}

func TestCrashRecoveryRollbackMatrix(t *testing.T) {
	cases := []crashExpectation{
		{point: durabilityPathPoint(DurabilityRollbackCapture, "new/deep/file"), state: TransactionRollingBack, path: "new/deep/file", phase: EntryPhaseRollbackCaptureIntent},
		{point: durabilityPathPoint(DurabilityRollbackCapture, "update"), state: TransactionRollingBack, path: "update", phase: EntryPhaseRollbackCaptureIntent},
		{point: durabilityPathPoint(DurabilityRollbackRestore, "delete"), state: TransactionRollingBack, path: "delete", phase: EntryPhaseCaptured},
		{point: durabilityPathPoint(DurabilityRollbackRestore, "update"), state: TransactionRollingBack, path: "update", phase: EntryPhaseRollbackCaptured},
		{point: durabilityPathPoint(DurabilityRollbackCapture, "new"), state: TransactionRollingBack, path: "new", phase: EntryPhaseRollbackCaptureIntent},
		{point: DurabilityRollbackComplete, state: TransactionRolledBack, allPhase: EntryPhaseRestored, allCleanup: CleanupPhasePending},
	}
	for _, test := range cases {
		t.Run(string(test.point), func(t *testing.T) {
			fixture := runCrashHelperAt(t, "apply", DurabilityForwardReadyToCommit, 1, "")
			runCrashHelperAt(t, "recover", test.point, 1, fixture.TransactionDir)
			assertCrashManifest(t, fixture, test)
			runCrashHelper(t, "recover", "", fixture.TransactionDir)
			assertCrashConverged(t, fixture, false)
		})
	}
}

func TestCrashRecoveryCommittedCleanupMatrix(t *testing.T) {
	cases := []crashExpectation{
		{point: durabilityPathPoint(DurabilityCleanupQuarantine, "update"), state: TransactionCleaning, path: "update", cleanup: CleanupPhaseQuarantineIntent},
		{point: durabilityPathPoint(DurabilityCleanupRemoval, "update"), state: TransactionCleaning, path: "update", cleanup: CleanupPhaseQuarantined},
		{point: durabilityPathPoint(DurabilityCleanupQuarantine, "delete"), state: TransactionCleaning, path: "delete", cleanup: CleanupPhaseQuarantineIntent},
		{point: durabilityPathPoint(DurabilityCleanupRemoval, "delete"), state: TransactionCleaning, path: "delete", cleanup: CleanupPhaseQuarantined},
		{point: DurabilityCleanupComplete, state: TransactionComplete, allCleanup: CleanupPhaseRemoved},
	}
	for _, test := range cases {
		t.Run(string(test.point), func(t *testing.T) {
			fixture := runCrashHelperAt(t, "apply", DurabilityForwardCommit, 1, "")
			runCrashHelperAt(t, "recover", test.point, 1, fixture.TransactionDir)
			assertCrashManifest(t, fixture, test)
			runCrashHelper(t, "recover", "", fixture.TransactionDir)
			assertCrashConverged(t, fixture, true)
		})
	}
}

func TestCrashRecoveryExternalRestoreBlocker(t *testing.T) {
	fixture := runCrashHelperAt(t, "apply", durabilityPathPoint(DurabilityForwardPublish, "update"), 1, "")
	writeApplyFile(t, fixture.Source, "update", "external", 0o600)
	runCrashHelper(t, "recover", "", fixture.TransactionDir)
	first := readCrashManifest(t, fixture)
	require.Equal(t, TransactionRecoveryBlocked, first.State)
	require.Len(t, first.RecoveryIssues, 1)
	entry := crashEntry(t, first, "update")
	require.Equal(t, []string{"update"}, first.RecoveryIssues[0].Paths)
	require.Equal(t, []string{entry.CaptureName, entry.RollbackCaptureName}, first.RecoveryIssues[0].RetainedPaths)
	require.Equal(t, "external", mustReadFile(t, filepath.Join(fixture.Source, "update")))
	require.FileExists(t, filepath.Join(fixture.Source, entry.CaptureName))
	require.NoFileExists(t, filepath.Join(fixture.Source, entry.RollbackCaptureName))

	runCrashHelper(t, "recover", "", fixture.TransactionDir)
	second := readCrashManifest(t, fixture)
	require.Equal(t, first, second)
	require.Equal(t, "external", mustReadFile(t, filepath.Join(fixture.Source, "update")))
	require.FileExists(t, filepath.Join(fixture.Source, entry.CaptureName))
	require.NoFileExists(t, filepath.Join(fixture.Source, entry.RollbackCaptureName))
}

func TestCrashRecoverySourceTemporaryReplacement(t *testing.T) {
	fixture := runCrashHelperAt(t, "apply", DurabilityForwardCommit, 1, "")
	manifest := readCrashManifest(t, fixture)
	entry := crashEntry(t, manifest, "update")
	require.NoError(t, os.Remove(filepath.Join(fixture.Source, entry.CaptureName)))
	writeApplyFile(t, fixture.Source, entry.CaptureName, "external temporary", 0o600)
	runCrashHelper(t, "recover", "", fixture.TransactionDir)
	first := readCrashManifest(t, fixture)
	require.Equal(t, TransactionCleanupBlocked, first.State)
	require.Len(t, first.RecoveryIssues, 1)
	require.Equal(t, []string{"update"}, first.RecoveryIssues[0].Paths)
	require.Equal(t, []string{entry.CaptureName}, first.RecoveryIssues[0].RetainedPaths)
	require.Equal(t, "external temporary", mustReadFile(t, filepath.Join(fixture.Source, entry.CaptureName)))

	runCrashHelper(t, "recover", "", fixture.TransactionDir)
	second := readCrashManifest(t, fixture)
	require.Equal(t, first, second)
	require.Equal(t, "external temporary", mustReadFile(t, filepath.Join(fixture.Source, entry.CaptureName)))
}

func TestCrashRecoveryConvergesAfterRepeatedRecoveryCrashes(t *testing.T) {
	fixture := runCrashHelperAt(t, "apply", DurabilityForwardReadyToCommit, 1, "")
	for _, test := range []crashExpectation{
		{point: durabilityPathPoint(DurabilityRollbackCapture, "new/deep/file"), state: TransactionRollingBack, path: "new/deep/file", phase: EntryPhaseRollbackCaptureIntent},
		{point: durabilityPathPoint(DurabilityRollbackCapture, "link"), state: TransactionRollingBack, path: "link", phase: EntryPhaseRollbackCaptureIntent},
	} {
		runCrashHelperAt(t, "recover", test.point, 1, fixture.TransactionDir)
		assertCrashManifest(t, fixture, test)
	}
	runCrashHelper(t, "recover", "", fixture.TransactionDir)
	assertCrashConverged(t, fixture, false)
}

func TestCrashHelperProcess(t *testing.T) {
	if os.Getenv("CS_TEST_CRASH_HELPER") != "1" {
		return
	}
	point := DurabilityPoint(os.Getenv("CS_TEST_CRASH_AT"))
	occurrence, err := strconv.Atoi(os.Getenv("CS_TEST_CRASH_OCCURRENCE"))
	require.NoError(t, err)
	seen := 0
	if point != "" {
		durabilityCheckpoint = func(observed DurabilityPoint) {
			if observed == point {
				seen++
				if seen == occurrence {
					os.Exit(crashExitCode)
				}
			}
		}
	}
	switch os.Getenv("CS_TEST_CRASH_MODE") {
	case "apply":
		workspace, plan, before := crashPreparationPlan(t)
		fixture := crashFixture{Project: filepath.Dir(filepath.Dir(plan.TransactionDir)), Source: plan.SourcePath, TransactionDir: plan.TransactionDir, Before: before}
		writeCrashFixture(t, fixture)
		manifest, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
		require.NoError(t, err)
		require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	case "recover":
		directory := os.Getenv("CS_TEST_TRANSACTION_DIR")
		project := filepath.Dir(filepath.Dir(directory))
		manifest, err := ReadManifestStrict(project, directory)
		require.NoError(t, err)
		_, err = RecoverTransaction(&manifest, directory)
		require.NoError(t, err)
	case "discover":
		directory := os.Getenv("CS_TEST_TRANSACTION_DIR")
		_, err := DiscoverTransactions(filepath.Dir(filepath.Dir(directory)))
		require.NoError(t, err)
	default:
		t.Fatalf("unknown crash helper mode %q", os.Getenv("CS_TEST_CRASH_MODE"))
	}
}

func crashPreparationPlan(t *testing.T) (*shadowWorkspace, applyPlan, Snapshot) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "update", "before", 0o644)
	writeApplyFile(t, source, "delete", "remove", 0o600)
	writeApplyFile(t, source, "regular-to-symlink", "regular", 0o644)
	require.NoError(t, os.Symlink("update", filepath.Join(source, "symlink-to-regular")))
	require.NoError(t, os.Mkdir(filepath.Join(source, "directory-to-regular"), 0o750))
	writeApplyFile(t, source, "directory-to-regular/child", "old", 0o600)
	require.NoError(t, os.Mkdir(filepath.Join(source, "directory-to-symlink"), 0o751))
	writeApplyFile(t, source, "directory-to-symlink/child", "old", 0o600)
	workspace := newApplyWorkspace(t, source)
	before := mustSnapshot(t, source)
	transactionRoot := filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions")
	require.NoError(t, os.MkdirAll(transactionRoot, 0o700))
	transactionDir, err := os.MkdirTemp(transactionRoot, workspace.data.SessionID+"-")
	require.NoError(t, err)
	plan := applyPlan{
		TransactionDir: transactionDir, SourcePath: workspace.data.SourcePath,
		MergedTree: strings.Repeat("a", 40), OursSnapshot: before,
		Creates: map[string]applyEntry{
			"new/deep/file":        {Type: EntryRegular, Mode: 0o640, Contents: []byte("new")},
			"link":                 {Type: EntrySymlink, SymlinkTarget: "update"},
			"directory-to-regular": {Type: EntryRegular, Mode: 0o600, Contents: []byte("replacement")},
			"directory-to-symlink": {Type: EntrySymlink, SymlinkTarget: "update"},
		},
		Updates: map[string]applyEntry{
			"update":             {Type: EntryRegular, Mode: 0o755, Contents: []byte("after")},
			"regular-to-symlink": {Type: EntrySymlink, SymlinkTarget: "update"},
			"symlink-to-regular": {Type: EntryRegular, Mode: 0o600, Contents: []byte("regular")},
		},
		Deletes: []string{"delete"},
	}
	return workspace, plan, before
}

func writeCrashFixture(t *testing.T, fixture crashFixture) {
	t.Helper()
	contents, err := json.Marshal(fixture)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv("CS_TEST_CRASH_FIXTURE"), contents, 0o600))
}

func runCrashHelper(t *testing.T, mode string, point DurabilityPoint, transactionDir string) crashFixture {
	t.Helper()
	return runCrashHelperAt(t, mode, point, 1, transactionDir)
}

func runCrashHelperAt(t *testing.T, mode string, point DurabilityPoint, occurrence int, transactionDir string) crashFixture {
	t.Helper()
	fixturePath := filepath.Join(t.TempDir(), "fixture.json")
	command := exec.Command(os.Args[0], "-test.run=^TestCrashHelperProcess$")
	command.Env = append(os.Environ(),
		"CS_TEST_CRASH_HELPER=1", "CS_TEST_CRASH_MODE="+mode,
		"CS_TEST_CRASH_AT="+string(point), "CS_TEST_CRASH_OCCURRENCE="+strconv.Itoa(occurrence),
		"CS_TEST_CRASH_FIXTURE="+fixturePath, "CS_TEST_TRANSACTION_DIR="+transactionDir,
	)
	output, err := command.CombinedOutput()
	if point != "" {
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit, string(output))
		require.Equal(t, crashExitCode, exit.ExitCode(), string(output))
	} else {
		require.NoError(t, err, string(output))
	}
	if mode != "apply" {
		return crashFixture{TransactionDir: transactionDir}
	}
	contents, readErr := os.ReadFile(fixturePath)
	require.NoError(t, readErr)
	var fixture crashFixture
	require.NoError(t, json.Unmarshal(contents, &fixture))
	return fixture
}

func readCrashManifest(t *testing.T, fixture crashFixture) TransactionManifest {
	t.Helper()
	manifest, err := ReadManifestStrict(fixture.Project, fixture.TransactionDir)
	require.NoError(t, err)
	return manifest
}

func crashEntry(t *testing.T, manifest TransactionManifest, path string) TransactionEntry {
	t.Helper()
	index := slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == path })
	require.NotEqual(t, -1, index, path)
	return manifest.Entries[index]
}

func assertCrashManifest(t *testing.T, fixture crashFixture, expected crashExpectation) {
	t.Helper()
	manifest := readCrashManifest(t, fixture)
	require.Equal(t, expected.state, manifest.State)
	if expected.path != "" {
		entry := crashEntry(t, manifest, expected.path)
		if expected.phase != "" {
			require.Equal(t, expected.phase, entry.Phase, expected.path)
		}
		if expected.cleanup != "" {
			require.Equal(t, expected.cleanup, entry.CleanupPhase, expected.path)
		}
	}
	for _, entry := range manifest.Entries {
		if expected.allPhase != "" {
			require.Equal(t, expected.allPhase, entry.Phase, entry.Path)
		}
		if expected.allCleanup != "" {
			if expected.allCleanup == CleanupPhaseRemoved && entry.CaptureName == "" {
				continue
			}
			require.Equal(t, expected.allCleanup, entry.CleanupPhase, entry.Path)
		}
	}
	if expected.point == DurabilityForwardReadyToCommit || expected.point == DurabilityForwardCommit {
		for _, entry := range manifest.Entries {
			want := EntryPhasePublished
			if entry.Operation == EntryOperationDelete {
				want = EntryPhaseCaptured
			}
			require.Equal(t, want, entry.Phase, entry.Path)
		}
	}
}

func assertCrashConverged(t *testing.T, fixture crashFixture, committed bool) {
	t.Helper()
	manifest := readCrashManifest(t, fixture)
	if committed {
		require.Equal(t, TransactionComplete, manifest.State)
		actual := mustSnapshot(t, fixture.Source)
		expected := normalizeExpectedSnapshot(t, Snapshot{Entries: maps.Clone(manifest.AppliedEntries)})
		require.Equal(t, expected, actual)
	} else {
		require.Equal(t, TransactionRolledBack, manifest.State, manifest.RecoveryIssues)
		require.Equal(t, fixture.Before, mustSnapshot(t, fixture.Source))
	}
	for path := range mustSnapshot(t, fixture.Source).Entries {
		require.False(t, strings.Contains(path, ".claude-squad-"), "source temporary retained at %s", path)
	}
}

func normalizeExpectedSnapshot(t *testing.T, snapshot Snapshot) Snapshot {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink("target", probe))
	info, err := os.Lstat(probe)
	require.NoError(t, err)
	for path, entry := range snapshot.Entries {
		if entry.Type == EntrySymlink {
			entry.Mode = info.Mode().Perm()
			snapshot.Entries[path] = entry
		}
	}
	return snapshot
}
