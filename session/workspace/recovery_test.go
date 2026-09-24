package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func recoveryProject(t *testing.T) (string, string) {
	t.Helper()
	source := t.TempDir()
	projectID := shadowProjectID(source)
	project := filepath.Join(t.TempDir(), projectID)
	require.NoError(t, os.MkdirAll(filepath.Join(project, "transactions"), 0o700))
	contents, err := json.Marshal(shadowProjectMetadata{Version: CurrentVersion, ProjectID: projectID, SourcePath: source})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(project, "metadata.json"), contents, 0o600))
	return project, source
}

func validRecoveryManifest(project, source, id string) TransactionManifest {
	return TransactionManifest{
		Version: CurrentTransactionVersion, TransactionID: id, ProjectID: filepath.Base(project),
		SessionID: "session", SourcePath: source, State: TransactionPrepared,
		Entries: []TransactionEntry{{Path: "file", StagePath: "stage/file", CaptureName: ".capture-file", RollbackCaptureName: ".rollback-file", QuarantinePath: ".quarantine-file", Operation: EntryOperationUpdate, Phase: EntryPhasePending, CleanupPhase: CleanupPhasePending}},
	}
}

func writeRecoveryManifest(t *testing.T, directory string, manifest TransactionManifest) []byte {
	t.Helper()
	contents, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "manifest.json"), contents, 0o600))
	return contents
}

func TestReadManifestStrictRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	project, source := recoveryProject(t)
	for name, suffix := range map[string]string{"unknown": `,"surprise":true}`, "trailing": `} {}`} {
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(project, "transactions", name)
			require.NoError(t, os.Mkdir(directory, 0o700))
			contents, err := json.Marshal(validRecoveryManifest(project, source, name))
			require.NoError(t, err)
			contents = append(contents[:len(contents)-1], []byte(suffix)...)
			require.NoError(t, os.WriteFile(filepath.Join(directory, "manifest.json"), contents, 0o600))
			_, err = ReadManifestStrict(project, directory)
			require.Error(t, err)
		})
	}
}

func TestValidateManifestRejectsUnknownVersionStateOperationAndPhase(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	base := validRecoveryManifest(project, source, "tx")
	tests := map[string]func(*TransactionManifest){
		"version":       func(m *TransactionManifest) { m.Version = 99 },
		"state":         func(m *TransactionManifest) { m.State = "surprise" },
		"operation":     func(m *TransactionManifest) { m.Entries[0].Operation = "surprise" },
		"phase":         func(m *TransactionManifest) { m.Entries[0].Phase = "surprise" },
		"cleanup phase": func(m *TransactionManifest) { m.Entries[0].CleanupPhase = "surprise" },
		"invalid operation phase": func(m *TransactionManifest) {
			m.Entries[0].Operation, m.Entries[0].Phase = EntryOperationCreate, EntryPhaseCaptured
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			m := base
			m.Entries = append([]TransactionEntry(nil), base.Entries...)
			mutate(&m)
			require.Error(t, ValidateManifest(project, directory, m))
		})
	}
}

func TestValidateManifestRejectsDuplicateTargetAndTemporaryPaths(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	fields := []struct {
		name      string
		duplicate func(*TransactionEntry, TransactionEntry)
	}{
		{"target", func(e *TransactionEntry, first TransactionEntry) { e.Path = first.Path }},
		{"stage", func(e *TransactionEntry, first TransactionEntry) { e.StagePath = first.StagePath }},
		{"capture", func(e *TransactionEntry, first TransactionEntry) { e.CaptureName = first.CaptureName }},
		{"rollback", func(e *TransactionEntry, first TransactionEntry) { e.RollbackCaptureName = first.RollbackCaptureName }},
		{"quarantine", func(e *TransactionEntry, first TransactionEntry) { e.QuarantinePath = first.QuarantinePath }},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			m := validRecoveryManifest(project, source, "tx")
			second := TransactionEntry{Path: "other", StagePath: "stage/other", CaptureName: ".capture-other", RollbackCaptureName: ".rollback-other", QuarantinePath: ".quarantine-other", Operation: EntryOperationUpdate, Phase: EntryPhasePending, CleanupPhase: CleanupPhasePending}
			field.duplicate(&second, m.Entries[0])
			m.Entries = append(m.Entries, second)
			require.Error(t, ValidateManifest(project, directory, m))
		})
	}
	t.Run("cross-kind temporary", func(t *testing.T) {
		m := validRecoveryManifest(project, source, "tx")
		m.Entries[0].QuarantinePath = m.Entries[0].StagePath
		require.Error(t, ValidateManifest(project, directory, m))
	})
}

func TestValidateManifestRejectsAbsoluteTraversalAndOutOfRootPaths(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	tests := map[string]func(*TransactionManifest){
		"absolute target":      func(m *TransactionManifest) { m.Entries[0].Path = filepath.Join(source, "file") },
		"target traversal":     func(m *TransactionManifest) { m.Entries[0].Path = "../file" },
		"unclean stage":        func(m *TransactionManifest) { m.Entries[0].StagePath = "stage/../file" },
		"absolute capture":     func(m *TransactionManifest) { m.Entries[0].CaptureName = filepath.Join(source, ".capture") },
		"nested temporary":     func(m *TransactionManifest) { m.Entries[0].RollbackCaptureName = "dir/name" },
		"project mismatch":     func(m *TransactionManifest) { m.ProjectID = "other" },
		"source mismatch":      func(m *TransactionManifest) { m.SourcePath = t.TempDir() },
		"transaction mismatch": func(m *TransactionManifest) { m.TransactionID = "other" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			m := validRecoveryManifest(project, source, "tx")
			mutate(&m)
			require.Error(t, ValidateManifest(project, directory, m))
		})
	}
}

func TestValidateManifestRejectsSymlinkedTransactionComponents(t *testing.T) {
	project, source := recoveryProject(t)
	outside := t.TempDir()
	symlinkDirectory := filepath.Join(project, "transactions", "linked")
	require.NoError(t, os.Symlink(outside, symlinkDirectory))
	require.Error(t, ValidateManifest(project, symlinkDirectory, validRecoveryManifest(project, source, "linked")))

	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "manifest.json"), []byte(`{}`), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(outside, "manifest.json"), filepath.Join(directory, "manifest.json")))
	_, err := ReadManifestStrict(project, directory)
	require.Error(t, err)
}

func TestInvalidManifestIsReportedWithoutRewritingBytes(t *testing.T) {
	project, _ := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "bad")
	require.NoError(t, os.Mkdir(directory, 0o700))
	original := []byte(`{"version":2,"state":"unknown"}`)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "manifest.json"), original, 0o600))
	discovered, err := DiscoverTransactions(project)
	require.NoError(t, err)
	require.Len(t, discovered, 1)
	require.Error(t, discovered[0].Invalid)
	require.False(t, discovered[0].Legacy)
	require.Equal(t, original, mustReadBytes(t, filepath.Join(directory, "manifest.json")))
}

func TestLegacyIncompleteManifestIsRetainedForManualRecovery(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "legacy")
	require.NoError(t, os.Mkdir(directory, 0o700))
	original := []byte(`{"version":1,"session_id":"old","source_path":` + mustJSON(t, source) + `,"state":"applying","entries":[]}`)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "manifest.json"), original, 0o600))
	discovered, err := DiscoverTransactions(project)
	require.NoError(t, err)
	require.Len(t, discovered, 1)
	require.True(t, discovered[0].Legacy)
	require.Nil(t, discovered[0].Manifest)
	require.NoError(t, discovered[0].Invalid)
	require.Equal(t, original, mustReadBytes(t, filepath.Join(directory, "manifest.json")))
}

func TestOrphanPreManifestDirectoryIsQuarantinedAndRemoved(t *testing.T) {
	project, _ := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "orphan")
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "staged"), []byte("safe"), 0o600))
	discovered, err := DiscoverTransactions(project)
	require.NoError(t, err)
	require.Len(t, discovered, 1)
	require.True(t, discovered[0].Orphan)
	require.NoDirExists(t, directory)
	matches, err := filepath.Glob(filepath.Join(project, "transactions", ".orphan-*"))
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestOrphanCleanupRejectsSymlinkAndNonDirectChild(t *testing.T) {
	project, _ := recoveryProject(t)
	outside := t.TempDir()
	linked := filepath.Join(project, "transactions", "linked")
	require.NoError(t, os.Symlink(outside, linked))
	require.Error(t, removeOrphanTransaction(project, linked))
	require.DirExists(t, outside)
	nested := filepath.Join(project, "transactions", "parent", "nested")
	require.NoError(t, os.MkdirAll(nested, 0o700))
	require.Error(t, removeOrphanTransaction(project, nested))
	require.DirExists(t, nested)
}

func TestDiscoverySortsTransactionsAndIssuesDeterministically(t *testing.T) {
	project, source := recoveryProject(t)
	for _, name := range []string{"z-invalid", "a-valid", "m-orphan", "b-legacy"} {
		directory := filepath.Join(project, "transactions", name)
		require.NoError(t, os.Mkdir(directory, 0o700))
		switch name {
		case "a-valid":
			writeRecoveryManifest(t, directory, validRecoveryManifest(project, source, name))
		case "b-legacy":
			require.NoError(t, os.WriteFile(filepath.Join(directory, "manifest.json"), []byte(`{"version":1}`), 0o600))
		case "z-invalid":
			require.NoError(t, os.WriteFile(filepath.Join(directory, "manifest.json"), []byte(`{"version":2,"unknown":true}`), 0o600))
		}
	}
	discovered, err := DiscoverTransactions(project)
	require.NoError(t, err)
	require.Equal(t, []string{"a-valid", "b-legacy", "m-orphan", "z-invalid"}, []string{filepath.Base(discovered[0].Directory), filepath.Base(discovered[1].Directory), filepath.Base(discovered[2].Directory), filepath.Base(discovered[3].Directory)})
	require.NoError(t, discovered[0].Invalid)
	require.NotNil(t, discovered[0].Manifest)
	require.True(t, discovered[1].Legacy)
	require.True(t, discovered[2].Orphan)
	require.Error(t, discovered[3].Invalid)
}

func mustReadBytes(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return contents
}
func mustJSON(t *testing.T, value string) string {
	t.Helper()
	contents, err := json.Marshal(value)
	require.NoError(t, err)
	return string(contents)
}
