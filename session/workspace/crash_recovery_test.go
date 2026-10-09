//go:build darwin || linux

package workspace

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

func TestCrashRecoveryForwardMatrix(t *testing.T) {
	points := []DurabilityPoint{
		DurabilityManifestBeforePersistence,
		DurabilityManifestAfterPersistence,
		durabilityPathPoint(DurabilityForwardCapture, "update"),
		durabilityPathPoint(DurabilityForwardPublish, "update"),
		DurabilityForwardReadyToCommit,
		DurabilityForwardCommit,
	}
	for _, point := range points {
		t.Run(string(point), func(t *testing.T) {
			fixture := runCrashHelper(t, "apply", point, "")
			persisted := readCrashManifest(t, fixture)
			if point == DurabilityForwardCommit {
				require.Equal(t, TransactionCommitted, persisted.State)
			} else {
				require.NotEqual(t, TransactionCommitted, persisted.State)
			}
			runCrashHelper(t, "recover", "", fixture.TransactionDir)
			assertCrashConverged(t, fixture, point == DurabilityForwardCommit)
			runCrashHelper(t, "recover", "", fixture.TransactionDir)
			assertCrashConverged(t, fixture, point == DurabilityForwardCommit)
		})
	}
}

func TestCrashRecoveryRollbackMatrix(t *testing.T) {
	points := []DurabilityPoint{
		durabilityPathPoint(DurabilityRollbackCapture, "update"),
		durabilityPathPoint(DurabilityRollbackRestore, "update"),
		DurabilityRollbackComplete,
	}
	for _, point := range points {
		t.Run(string(point), func(t *testing.T) {
			fixture := runCrashHelper(t, "apply", DurabilityForwardReadyToCommit, "")
			runCrashHelper(t, "recover", point, fixture.TransactionDir)
			runCrashHelper(t, "recover", "", fixture.TransactionDir)
			assertCrashConverged(t, fixture, false)
			runCrashHelper(t, "recover", "", fixture.TransactionDir)
			assertCrashConverged(t, fixture, false)
		})
	}
}

func TestCrashRecoveryCommittedCleanupMatrix(t *testing.T) {
	points := []DurabilityPoint{
		durabilityPathPoint(DurabilityCleanupQuarantine, "update"),
		durabilityPathPoint(DurabilityCleanupRemoval, "update"),
		DurabilityCleanupComplete,
	}
	for _, point := range points {
		t.Run(string(point), func(t *testing.T) {
			fixture := runCrashHelper(t, "apply", DurabilityForwardCommit, "")
			runCrashHelper(t, "recover", point, fixture.TransactionDir)
			runCrashHelper(t, "recover", "", fixture.TransactionDir)
			assertCrashConverged(t, fixture, true)
			runCrashHelper(t, "recover", "", fixture.TransactionDir)
			assertCrashConverged(t, fixture, true)
		})
	}
}

func TestCrashRecoveryTypeTransitionMatrix(t *testing.T) {
	// The shared fixture includes regular update/delete, symlink creation, nested
	// creation, and two durable created-parent directory publications.
	for _, point := range []DurabilityPoint{
		durabilityPathPoint(DurabilityForwardPublish, "link"),
		durabilityPathPoint(DurabilityForwardPublish, "new"),
		durabilityPathPoint(DurabilityForwardPublish, "new/deep"),
	} {
		t.Run(string(point), func(t *testing.T) {
			fixture := runCrashHelper(t, "apply", point, "")
			runCrashHelper(t, "recover", "", fixture.TransactionDir)
			assertCrashConverged(t, fixture, false)
		})
	}
}

func TestCrashRecoveryExternalRestoreBlocker(t *testing.T) {
	fixture := runCrashHelper(t, "apply", durabilityPathPoint(DurabilityForwardPublish, "update"), "")
	writeApplyFile(t, fixture.Source, "update", "external", 0o600)
	runCrashHelper(t, "recover", "", fixture.TransactionDir)
	manifest := readCrashManifest(t, fixture)
	require.Equal(t, TransactionRecoveryBlocked, manifest.State)
	require.Equal(t, "external", mustReadFile(t, filepath.Join(fixture.Source, "update")))
	require.Equal(t, []string{"update"}, manifest.RecoveryIssues[0].Paths)
	require.NotEmpty(t, manifest.RecoveryIssues[0].RetainedPaths)
}

func TestCrashRecoverySourceTemporaryReplacement(t *testing.T) {
	fixture := runCrashHelper(t, "apply", DurabilityForwardCommit, "")
	manifest := readCrashManifest(t, fixture)
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })]
	require.NoError(t, os.Remove(filepath.Join(fixture.Source, entry.CaptureName)))
	writeApplyFile(t, fixture.Source, entry.CaptureName, "external temporary", 0o600)
	runCrashHelper(t, "recover", "", fixture.TransactionDir)
	manifest = readCrashManifest(t, fixture)
	require.Equal(t, TransactionCleanupBlocked, manifest.State)
	require.Equal(t, "external temporary", mustReadFile(t, filepath.Join(fixture.Source, entry.CaptureName)))
	require.Equal(t, []string{entry.CaptureName}, manifest.RecoveryIssues[0].RetainedPaths)
	runCrashHelper(t, "recover", "", fixture.TransactionDir)
	require.Equal(t, "external temporary", mustReadFile(t, filepath.Join(fixture.Source, entry.CaptureName)))
}

func TestCrashRecoveryConvergesAfterRepeatedRecoveryCrashes(t *testing.T) {
	fixture := runCrashHelper(t, "apply", DurabilityForwardReadyToCommit, "")
	for _, point := range []DurabilityPoint{
		durabilityPathPoint(DurabilityRollbackCapture, "new/deep/file"),
		durabilityPathPoint(DurabilityRollbackCapture, "link"),
	} {
		runCrashHelper(t, "recover", point, fixture.TransactionDir)
	}
	runCrashHelper(t, "recover", "", fixture.TransactionDir)
	assertCrashConverged(t, fixture, false)
}

func TestCrashHelperProcess(t *testing.T) {
	if os.Getenv("CS_TEST_CRASH_HELPER") != "1" {
		return
	}
	point := DurabilityPoint(os.Getenv("CS_TEST_CRASH_AT"))
	if point != "" {
		durabilityCheckpoint = func(observed DurabilityPoint) {
			if observed == point {
				os.Exit(crashExitCode)
			}
		}
	}
	switch os.Getenv("CS_TEST_CRASH_MODE") {
	case "apply":
		workspace, plan, before := preparationPlan(t)
		manifest, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
		require.NoError(t, err)
		fixture := crashFixture{
			Project: filepath.Dir(filepath.Dir(plan.TransactionDir)), Source: manifest.SourcePath,
			TransactionDir: plan.TransactionDir, Before: before,
		}
		contents, err := json.Marshal(fixture)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(os.Getenv("CS_TEST_CRASH_FIXTURE"), contents, 0o600))
		require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	case "recover":
		directory := os.Getenv("CS_TEST_TRANSACTION_DIR")
		project := filepath.Dir(filepath.Dir(directory))
		manifest, err := ReadManifestStrict(project, directory)
		require.NoError(t, err)
		_, err = RecoverTransaction(&manifest, directory)
		require.NoError(t, err)
	default:
		t.Fatalf("unknown crash helper mode %q", os.Getenv("CS_TEST_CRASH_MODE"))
	}
}

func runCrashHelper(t *testing.T, mode string, point DurabilityPoint, transactionDir string) crashFixture {
	t.Helper()
	fixturePath := filepath.Join(t.TempDir(), "fixture.json")
	command := exec.Command(os.Args[0], "-test.run=^TestCrashHelperProcess$")
	command.Env = append(os.Environ(),
		"CS_TEST_CRASH_HELPER=1",
		"CS_TEST_CRASH_MODE="+mode,
		"CS_TEST_CRASH_AT="+string(point),
		"CS_TEST_CRASH_FIXTURE="+fixturePath,
		"CS_TEST_TRANSACTION_DIR="+transactionDir,
	)
	err := command.Run()
	if point != "" {
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		require.Equal(t, crashExitCode, exit.ExitCode())
	} else {
		require.NoError(t, err)
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

func assertCrashConverged(t *testing.T, fixture crashFixture, committed bool) {
	t.Helper()
	manifest := readCrashManifest(t, fixture)
	if committed {
		require.Equal(t, TransactionComplete, manifest.State)
		actual := mustSnapshot(t, fixture.Source)
		require.Equal(t, slices.Sorted(maps.Keys(manifest.AppliedEntries)), slices.Sorted(maps.Keys(actual.Entries)))
		for path, expected := range manifest.AppliedEntries {
			observed := actual.Entries[path]
			if expected.Type == EntrySymlink {
				expected.Mode = observed.Mode
			}
			require.Equal(t, expected, observed, path)
		}
	} else {
		require.Equal(t, TransactionRolledBack, manifest.State)
		require.Equal(t, fixture.Before, mustSnapshot(t, fixture.Source))
	}
	for path := range mustSnapshot(t, fixture.Source).Entries {
		require.False(t, strings.Contains(path, ".claude-squad-"), "source temporary retained at %s", path)
	}
}
