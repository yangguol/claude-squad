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
	source, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	projectID := shadowProjectID(source)
	project := filepath.Join(t.TempDir(), projectID)
	require.NoError(t, os.MkdirAll(filepath.Join(project, "transactions"), 0o700))
	project, err = filepath.EvalSymlinks(project)
	require.NoError(t, err)
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

func TestReadManifestStrictRejectsDuplicateMembersRecursively(t *testing.T) {
	project, source := recoveryProject(t)
	tests := map[string]string{
		"top-level version":   `{"version":2,"version":2,"transaction_id":"tx","project_id":` + mustJSON(t, filepath.Base(project)) + `,"session_id":"session","source_path":` + mustJSON(t, source) + `,"state":"prepared","entries":[]}`,
		"nested entry path":   `{"version":2,"transaction_id":"tx","project_id":` + mustJSON(t, filepath.Base(project)) + `,"session_id":"session","source_path":` + mustJSON(t, source) + `,"state":"prepared","entries":[{"path":"file","path":"other","operation":"update","phase":"pending","cleanup_phase":"pending"}]}`,
		"expected tree entry": `{"version":2,"transaction_id":"tx","project_id":` + mustJSON(t, filepath.Base(project)) + `,"session_id":"session","source_path":` + mustJSON(t, source) + `,"state":"prepared","entries":[{"path":"dir","stage_path":"stage/dir","capture_name":".capture-dir","rollback_capture_name":".rollback-dir","quarantine_path":".quarantine-dir","operation":"update","phase":"pending","cleanup_phase":"pending","expected_tree":{"child":{"path":"child","path":"other","type":"regular","mode":420}}}]}`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(project, "transactions", "tx")
			require.NoError(t, os.Mkdir(directory, 0o700))
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
			require.NoError(t, os.WriteFile(filepath.Join(directory, "manifest.json"), []byte(contents), 0o600))
			_, err := ReadManifestStrict(project, directory)
			require.ErrorContains(t, err, "duplicate JSON object member")
		})
	}
}

func TestValidateManifestUsesCanonicalTrustedRootsAndMetadataIdentity(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))

	t.Run("project symlink alias", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), filepath.Base(project))
		require.NoError(t, os.Symlink(project, alias))
		require.Error(t, ValidateManifest(alias, filepath.Join(alias, "transactions", "tx"), validRecoveryManifest(project, source, "tx")))
	})
	t.Run("source symlink in trusted metadata", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "source")
		require.NoError(t, os.Symlink(source, alias))
		id := shadowProjectID(alias)
		badProject := filepath.Join(t.TempDir(), id)
		require.NoError(t, os.MkdirAll(filepath.Join(badProject, "transactions", "tx"), 0o700))
		metadata, err := json.Marshal(shadowProjectMetadata{Version: CurrentVersion, ProjectID: id, SourcePath: alias})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(badProject, "metadata.json"), metadata, 0o600))
		require.Error(t, ValidateManifest(badProject, filepath.Join(badProject, "transactions", "tx"), validRecoveryManifest(badProject, alias, "tx")))
	})
	t.Run("metadata project ID hash mismatch", func(t *testing.T) {
		metadata, err := json.Marshal(shadowProjectMetadata{Version: CurrentVersion, ProjectID: filepath.Base(project), SourcePath: t.TempDir()})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(project, "metadata.json"), metadata, 0o600))
		require.Error(t, ValidateManifest(project, directory, validRecoveryManifest(project, source, "tx")))
	})
}

func TestValidateManifestSeparatesNamespacesAndRejectsActualCollisions(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))

	allowed := validRecoveryManifest(project, source, "tx")
	allowed.Entries[0].StagePath = ".capture-file"
	require.NoError(t, ValidateManifest(project, directory, allowed), "same relative string in source and transaction roots is not a collision")

	for name, mutate := range map[string]func(*TransactionManifest){
		"source target and capture":        func(m *TransactionManifest) { m.Entries[0].CaptureName = m.Entries[0].Path },
		"transaction stage and quarantine": func(m *TransactionManifest) { m.Entries[0].QuarantinePath = m.Entries[0].StagePath },
	} {
		t.Run(name, func(t *testing.T) {
			manifest := validRecoveryManifest(project, source, "tx")
			mutate(&manifest)
			require.Error(t, ValidateManifest(project, directory, manifest))
		})
	}
}

func TestValidateManifestRequiresRecoveryPathsAndValidExpectedTree(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))

	for name, clear := range map[string]func(*TransactionEntry){
		"stage":            func(e *TransactionEntry) { e.StagePath = "" },
		"capture":          func(e *TransactionEntry) { e.CaptureName = "" },
		"rollback capture": func(e *TransactionEntry) { e.RollbackCaptureName = "" },
		"quarantine":       func(e *TransactionEntry) { e.QuarantinePath = "" },
	} {
		t.Run("missing "+name, func(t *testing.T) {
			manifest := validRecoveryManifest(project, source, "tx")
			clear(&manifest.Entries[0])
			require.Error(t, ValidateManifest(project, directory, manifest))
		})
	}

	for name, tree := range map[string]map[string]Entry{
		"empty key":         {"": {Path: "", Type: EntryRegular}},
		"path mismatch":     {"child": {Path: "other", Type: EntryRegular}},
		"target escape":     {"../outside": {Path: "../outside", Type: EntryRegular}},
		"overlapping paths": {"parent": {Path: "parent", Type: EntryRegular}, "parent/child": {Path: "parent/child", Type: EntryRegular}},
	} {
		t.Run("expected tree "+name, func(t *testing.T) {
			manifest := validRecoveryManifest(project, source, "tx")
			manifest.Entries[0].Path = "directory"
			manifest.Entries[0].ExpectedTree = tree
			require.Error(t, ValidateManifest(project, directory, manifest))
		})
	}

	require.NoError(t, os.Mkdir(filepath.Join(source, "directory"), 0o700))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(source, "directory", "linked")))
	manifest := validRecoveryManifest(project, source, "tx")
	manifest.Entries[0].Path = "directory"
	manifest.Entries[0].ExpectedTree = map[string]Entry{"linked/child": {Path: "linked/child", Type: EntryRegular}}
	require.Error(t, ValidateManifest(project, directory, manifest))
}

func TestDiscoveryValidatesCleanupMarkerTypeAndContents(t *testing.T) {
	for name, setup := range map[string]func(*testing.T, string){
		"directory":              func(t *testing.T, path string) { require.NoError(t, os.Mkdir(path, 0o700)) },
		"symlink":                func(t *testing.T, path string) { require.NoError(t, os.Symlink(t.TempDir(), path)) },
		"malformed regular file": func(t *testing.T, path string) { require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o600)) },
	} {
		t.Run(name, func(t *testing.T) {
			project, _ := recoveryProject(t)
			setup(t, filepath.Join(project, "transactions", "tx.cleanup.json"))
			discovered, err := DiscoverTransactions(project)
			require.NoError(t, err)
			require.Len(t, discovered, 1)
			require.Error(t, discovered[0].Invalid)
		})
	}
}

func TestOrphanCleanupBindsRootAcrossSubstitution(t *testing.T) {
	project, _ := recoveryProject(t)
	root := filepath.Join(project, "transactions")
	orphan := filepath.Join(root, "orphan")
	require.NoError(t, os.Mkdir(orphan, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(orphan, "owned"), []byte("owned"), 0o600))
	movedRoot := filepath.Join(filepath.Dir(root), "transactions-moved")
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "unowned"), []byte("unowned"), 0o600))

	orphanBeforeQuarantineHook = func() {
		require.NoError(t, os.Rename(root, movedRoot))
		require.NoError(t, os.Symlink(outside, root))
	}
	t.Cleanup(func() { orphanBeforeQuarantineHook = nil })
	err := removeOrphanTransaction(project, orphan)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(outside, "unowned"))
	require.NoFileExists(t, filepath.Join(movedRoot, "orphan", "owned"))
}

func TestOrphanCleanupRetainsOwnedAndUnownedContentAfterQuarantineSubstitution(t *testing.T) {
	project, _ := recoveryProject(t)
	root := filepath.Join(project, "transactions")
	orphan := filepath.Join(root, "orphan")
	require.NoError(t, os.Mkdir(orphan, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(orphan, "owned"), []byte("owned"), 0o600))
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "unowned"), []byte("unowned"), 0o600))
	var retained string

	orphanAfterQuarantineHook = func(quarantine string) {
		quarantine = filepath.Join(root, quarantine)
		retained = quarantine + "-retained"
		require.NoError(t, os.Rename(quarantine, retained))
		require.NoError(t, os.Symlink(outside, quarantine))
	}
	t.Cleanup(func() { orphanAfterQuarantineHook = nil })
	err := removeOrphanTransaction(project, orphan)
	require.Error(t, err)
	require.FileExists(t, filepath.Join(outside, "unowned"))
	require.FileExists(t, filepath.Join(retained, "owned"))
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
