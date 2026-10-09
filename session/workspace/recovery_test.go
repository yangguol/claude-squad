package workspace

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
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

func recoveryIdentity(path string) *Entry {
	return &Entry{Path: path, Type: EntryRegular, Mode: 0o600, ContentHash: strings.Repeat("a", 64)}
}

func validRecoveryEntry(path string, operation EntryOperation) TransactionEntry {
	entry := TransactionEntry{
		Path: path, QuarantinePath: filepath.ToSlash(filepath.Join("quarantine", path)),
		Operation: operation, Phase: EntryPhasePending, CleanupPhase: CleanupPhasePending,
	}
	switch operation {
	case EntryOperationCreate:
		entry.StagePath = filepath.ToSlash(filepath.Join("stage", path))
		entry.RollbackCaptureName = filepath.ToSlash(filepath.Join(filepath.Dir(path), ".rollback-"+filepath.Base(path)))
		entry.Result = recoveryIdentity(path)
	case EntryOperationUpdate:
		entry.StagePath = filepath.ToSlash(filepath.Join("stage", path))
		entry.CaptureName = filepath.ToSlash(filepath.Join(filepath.Dir(path), ".capture-"+filepath.Base(path)))
		entry.RollbackCaptureName = filepath.ToSlash(filepath.Join(filepath.Dir(path), ".rollback-"+filepath.Base(path)))
		entry.Expected = recoveryIdentity(path)
		entry.Result = recoveryIdentity(path)
	case EntryOperationDelete:
		entry.CaptureName = filepath.ToSlash(filepath.Join(filepath.Dir(path), ".capture-"+filepath.Base(path)))
		entry.Expected = recoveryIdentity(path)
	case EntryOperationCreatedParent:
		entry.StagePath = filepath.ToSlash(filepath.Join("stage-parent", path))
		entry.RollbackCaptureName = filepath.ToSlash(filepath.Join(filepath.Dir(path), ".rollback-"+filepath.Base(path)))
		entry.Result = &Entry{Path: path, Type: EntryType("directory"), Mode: 0o700}
	}
	return entry
}

func validRecoveryManifest(project, source, id string) TransactionManifest {
	return TransactionManifest{
		Version: CurrentTransactionVersion, TransactionID: id, ProjectID: filepath.Base(project),
		SessionID: "session", SourcePath: source, State: TransactionPrepared,
		Entries: []TransactionEntry{validRecoveryEntry("file", EntryOperationUpdate)},
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
	require.Len(t, matches, 1)
	entries, err := os.ReadDir(matches[0])
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, orphanCleanupMarker, entries[0].Name())
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

func TestOrphanCleanupRetainsReplacementAtPrivateQuarantinePath(t *testing.T) {
	project, _ := recoveryProject(t)
	root := filepath.Join(project, "transactions")
	orphan := filepath.Join(root, "orphan")
	require.NoError(t, os.Mkdir(orphan, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(orphan, "owned"), []byte("owned"), 0o600))
	var replacement string

	orphanAfterQuarantineHook = func(quarantine string) {
		quarantine = filepath.Join(root, quarantine)
		retained := quarantine + "-captured"
		require.NoError(t, os.Rename(quarantine, retained))
		require.NoError(t, os.Mkdir(quarantine, 0o700))
		replacement = filepath.Join(quarantine, "replacement")
		require.NoError(t, os.WriteFile(replacement, []byte("replacement"), 0o600))
	}
	t.Cleanup(func() { orphanAfterQuarantineHook = nil })
	err := removeOrphanTransaction(project, orphan)
	require.Error(t, err)
	require.FileExists(t, replacement)
}

func TestOrphanCleanupManifestBeforeCaptureBlocksDeletion(t *testing.T) {
	project, _ := recoveryProject(t)
	orphan := filepath.Join(project, "transactions", "orphan")
	require.NoError(t, os.Mkdir(orphan, 0o700))
	manifest := filepath.Join(orphan, "manifest.json")
	orphanBeforeQuarantineHook = func() {
		require.NoError(t, os.WriteFile(manifest, []byte(`{"version":2}`), 0o600))
	}
	t.Cleanup(func() { orphanBeforeQuarantineHook = nil })
	require.Error(t, removeOrphanTransaction(project, orphan))
	require.FileExists(t, manifest)
	require.DirExists(t, orphan)
}

func TestOrphanCleanupAllowsWritesToCapturedPrivateInode(t *testing.T) {
	project, _ := recoveryProject(t)
	root := filepath.Join(project, "transactions")
	orphan := filepath.Join(root, "orphan")
	require.NoError(t, os.Mkdir(orphan, 0o700))
	orphanAfterQuarantineHook = func(quarantine string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, quarantine, "manifest.json"), []byte("owned"), 0o600))
	}
	t.Cleanup(func() { orphanAfterQuarantineHook = nil })
	require.NoError(t, removeOrphanTransaction(project, orphan))
	require.NoDirExists(t, orphan)
}

func TestValidateManifestRejectsLexicalTrustedRootAliases(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.Error(t, ValidateManifest(project+string(filepath.Separator)+".", directory, validRecoveryManifest(project, source, "tx")))
	require.Error(t, ValidateManifest(project, directory+string(filepath.Separator)+".", validRecoveryManifest(project, source, "tx")))

	metadata := shadowProjectMetadata{Version: CurrentVersion, ProjectID: filepath.Base(project), SourcePath: source + string(filepath.Separator) + "."}
	contents, err := json.Marshal(metadata)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(project, "metadata.json"), contents, 0o600))
	manifest := validRecoveryManifest(project, source, "tx")
	manifest.SourcePath = metadata.SourcePath
	require.Error(t, ValidateManifest(project, directory, manifest))
}

func TestValidateManifestRejectsFilesystemAliasesWhereSupported(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "existing"), []byte("x"), 0o600))
	alias := filepath.Join(source, "EXISTING")
	first, err := os.Stat(filepath.Join(source, "existing"))
	require.NoError(t, err)
	second, err := os.Stat(alias)
	if err != nil || !os.SameFile(first, second) {
		t.Skip("filesystem is case-sensitive")
	}
	manifest := validRecoveryManifest(project, source, "tx")
	manifest.Entries[0] = validRecoveryEntry("existing", EntryOperationUpdate)
	manifest.Entries = append(manifest.Entries, validRecoveryEntry("EXISTING", EntryOperationUpdate))
	require.Error(t, ValidateManifest(project, directory, manifest))
}

func TestValidateManifestRejectsAncestorNamespaceOverlaps(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	for name, mutate := range map[string]func(*TransactionManifest){
		"source targets": func(m *TransactionManifest) {
			m.Entries = append(m.Entries, validRecoveryEntry("file/child", EntryOperationCreate))
		},
		"transaction paths": func(m *TransactionManifest) {
			m.Entries[0].QuarantinePath = "stage/file/child"
		},
	} {
		t.Run(name, func(t *testing.T) {
			manifest := validRecoveryManifest(project, source, "tx")
			mutate(&manifest)
			require.Error(t, ValidateManifest(project, directory, manifest))
		})
	}
}

func TestValidateManifestDoesNotExemptDescendantsBelowMalformedAncestor(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	writeApplyFile(t, source, "ancestor", "file", 0o600)

	for _, operation := range []EntryOperation{EntryOperationDelete, EntryOperationCreate} {
		t.Run(string(operation), func(t *testing.T) {
			manifest := validRecoveryManifest(project, source, "tx")
			ancestor := validRecoveryEntry("ancestor", operation)
			if operation == EntryOperationCreate {
				ancestor.Expected = nil
				ancestor.CaptureName = ""
				ancestor.QuarantinePath = ""
			}
			manifest.Entries = []TransactionEntry{ancestor, validRecoveryEntry("ancestor/child", EntryOperationCreate)}
			require.Error(t, ValidateManifest(project, directory, manifest))
		})
	}
}

func TestManifestNamespaceComparisonUsesGoverningParentCaseBehavior(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(root, "mount"), 0o700))
	originalLookup := lookupManifestComponent
	lookupManifestComponent = func(directory *os.File, requested string) (manifestComponentLookup, error) {
		lookup, err := originalLookup(directory, requested)
		if requested == "mount" {
			lookup.caseInsensitive = false
		}
		if requested == "Name" {
			lookup.caseInsensitive = false
		}
		return lookup, err
	}
	t.Cleanup(func() { lookupManifestComponent = originalLookup })

	resolved, err := resolveManifestNamespacePath(filepath.Join(root, "mount", "Name"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(resolved.components), 3)
	components := resolved.components[len(resolved.components)-3:]
	require.False(t, components[1].caseInsensitive)
	require.False(t, components[2].caseInsensitive, "the unresolved leaf must use the newly entered directory filesystem's behavior")
}

func TestValidateManifestRequiresOperationSpecificRecoveryFields(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	tests := []struct {
		name      string
		operation EntryOperation
		clear     func(*TransactionEntry)
	}{
		{"create stage", EntryOperationCreate, func(e *TransactionEntry) { e.StagePath = "" }},
		{"create rollback capture", EntryOperationCreate, func(e *TransactionEntry) { e.RollbackCaptureName = "" }},
		{"create result", EntryOperationCreate, func(e *TransactionEntry) { e.Result = nil }},
		{"update stage", EntryOperationUpdate, func(e *TransactionEntry) { e.StagePath = "" }},
		{"update capture", EntryOperationUpdate, func(e *TransactionEntry) { e.CaptureName = "" }},
		{"update rollback capture", EntryOperationUpdate, func(e *TransactionEntry) { e.RollbackCaptureName = "" }},
		{"update quarantine", EntryOperationUpdate, func(e *TransactionEntry) { e.QuarantinePath = "" }},
		{"update expected", EntryOperationUpdate, func(e *TransactionEntry) { e.Expected = nil }},
		{"update result", EntryOperationUpdate, func(e *TransactionEntry) { e.Result = nil }},
		{"delete capture", EntryOperationDelete, func(e *TransactionEntry) { e.CaptureName = "" }},
		{"delete quarantine", EntryOperationDelete, func(e *TransactionEntry) { e.QuarantinePath = "" }},
		{"delete expected", EntryOperationDelete, func(e *TransactionEntry) { e.Expected = nil }},
		{"created parent stage", EntryOperationCreatedParent, func(e *TransactionEntry) { e.StagePath = "" }},
		{"created parent rollback capture", EntryOperationCreatedParent, func(e *TransactionEntry) { e.RollbackCaptureName = "" }},
		{"created parent result", EntryOperationCreatedParent, func(e *TransactionEntry) { e.Result = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validRecoveryManifest(project, source, "tx")
			manifest.Entries[0] = validRecoveryEntry("file", test.operation)
			test.clear(&manifest.Entries[0])
			require.Error(t, ValidateManifest(project, directory, manifest))
		})
	}
}

func TestValidateManifestRejectsMalformedEntryIdentities(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	tests := map[string]func(*Entry){
		"unknown type":             func(e *Entry) { e.Type = "unknown" },
		"regular missing hash":     func(e *Entry) { e.ContentHash = "" },
		"regular malformed hash":   func(e *Entry) { e.ContentHash = "not-a-sha256" },
		"regular symlink target":   func(e *Entry) { e.SymlinkTarget = "target" },
		"symlink missing target":   func(e *Entry) { e.Type, e.ContentHash = EntrySymlink, "" },
		"symlink content hash":     func(e *Entry) { e.Type, e.SymlinkTarget = EntrySymlink, "target" },
		"type bits in mode":        func(e *Entry) { e.Mode = os.ModeDir | 0o700 },
		"permissions out of range": func(e *Entry) { e.Mode = 0o1000 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := validRecoveryManifest(project, source, "tx")
			mutate(manifest.Entries[0].Expected)
			require.Error(t, ValidateManifest(project, directory, manifest))
		})
	}

	manifest := validRecoveryManifest(project, source, "tx")
	manifest.Entries[0].ExpectedTree = map[string]Entry{
		"child": {Path: "child", Type: EntryRegular, Mode: 0o600, ContentHash: "bad"},
	}
	require.Error(t, ValidateManifest(project, directory, manifest))
}

func TestValidateManifestValidatesAppliedEntriesAndCreatedOwnership(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	valid := validRecoveryManifest(project, source, "tx")
	valid.AppliedEntries = map[string]Entry{"file": *recoveryIdentity("file")}
	require.NoError(t, ValidateManifest(project, directory, valid), "prepared manifests need no created ownership")
	createdParentIntent := valid
	createdParentIntent.AppliedEntries = maps.Clone(valid.AppliedEntries)
	createdParentIntent.Entries = []TransactionEntry{validRecoveryEntry("parent", EntryOperationCreatedParent)}
	createdParentIntent.Entries[0].Phase = EntryPhasePublishIntent
	createdParentIntent.Entries[0].CreatedDevice, createdParentIntent.Entries[0].CreatedInode = 1, 2
	require.NoError(t, ValidateManifest(project, directory, createdParentIntent), "durable ownership may identify the private staged inode before publication")

	tests := map[string]func(*TransactionManifest){
		"unsafe applied key": func(m *TransactionManifest) {
			m.AppliedEntries["../escape"] = *recoveryIdentity("../escape")
		},
		"applied path mismatch": func(m *TransactionManifest) {
			m.AppliedEntries["other"] = *recoveryIdentity("different")
		},
		"malformed applied identity": func(m *TransactionManifest) {
			m.AppliedEntries["bad"] = Entry{Path: "bad", Type: EntryRegular, Mode: 0o600, ContentHash: "bad"}
		},
		"applied descendant below file": func(m *TransactionManifest) {
			m.AppliedEntries["file/child"] = *recoveryIdentity("file/child")
		},
		"partial ownership": func(m *TransactionManifest) {
			m.Entries[0].CreatedDevice = 1
		},
		"ownership on update": func(m *TransactionManifest) {
			m.Entries[0].CreatedDevice, m.Entries[0].CreatedInode = 1, 2
		},
		"ownership in created-parent pending": func(m *TransactionManifest) {
			m.Entries[0] = validRecoveryEntry("parent", EntryOperationCreatedParent)
			m.Entries[0].CreatedDevice, m.Entries[0].CreatedInode = 1, 2
		},
		"missing ownership in created-parent publish intent": func(m *TransactionManifest) {
			m.Entries[0] = validRecoveryEntry("parent", EntryOperationCreatedParent)
			m.Entries[0].Phase = EntryPhasePublishIntent
		},
		"missing ownership in created-parent published": func(m *TransactionManifest) {
			m.Entries[0] = validRecoveryEntry("parent", EntryOperationCreatedParent)
			m.Entries[0].Phase = EntryPhasePublished
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := valid
			manifest.AppliedEntries = maps.Clone(valid.AppliedEntries)
			manifest.Entries = slices.Clone(valid.Entries)
			mutate(&manifest)
			require.Error(t, ValidateManifest(project, directory, manifest))
		})
	}
}

func TestDiscoverySkipsOnlyValidatedRetainedOrphanTombstones(t *testing.T) {
	project, _ := recoveryProject(t)
	root := filepath.Join(project, "transactions")
	orphan := filepath.Join(root, "orphan")
	require.NoError(t, os.Mkdir(orphan, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(orphan, "owned"), []byte("owned"), 0o600))

	first, err := DiscoverTransactions(project)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.True(t, first[0].Orphan)

	second, err := DiscoverTransactions(project)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.True(t, second[0].Orphan)
	require.Error(t, second[0].Invalid)
	matches, err := filepath.Glob(filepath.Join(root, ".orphan-*"))
	require.NoError(t, err)
	require.Len(t, matches, 1)

	fake := filepath.Join(root, ".orphan-user-created")
	require.NoError(t, os.Mkdir(fake, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(fake, ".cleanup-complete"), []byte("not valid"), 0o600))
	third, err := DiscoverTransactions(project)
	require.NoError(t, err)
	require.Len(t, third, 2)
	for _, transaction := range third {
		require.True(t, transaction.Orphan)
		require.Error(t, transaction.Invalid)
	}
	require.DirExists(t, fake)
}

func TestValidateManifestRejectsNonexistentCaseEquivalentLeavesWhereSupported(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	probe := filepath.Join(source, "CaseProbe")
	require.NoError(t, os.Mkdir(probe, 0o700))
	_, err := os.Stat(filepath.Join(source, "caseprobe"))
	if err != nil {
		t.Skip("filesystem is case-sensitive")
	}

	manifest := validRecoveryManifest(project, source, "tx")
	manifest.Entries[0] = validRecoveryEntry("CaseProbe/First", EntryOperationCreate)
	manifest.Entries = append(manifest.Entries, validRecoveryEntry("caseprobe/first", EntryOperationCreate))
	require.Error(t, ValidateManifest(project, directory, manifest))
}

func TestValidateManifestRejectsTwoUnresolvedCaseEquivalentNamesWhereSupported(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	parent := filepath.Join(source, "CaseProbe")
	require.NoError(t, os.Mkdir(parent, 0o700))
	if _, err := os.Stat(filepath.Join(source, "caseprobe")); err != nil {
		t.Skip("filesystem is case-sensitive")
	}

	manifest := validRecoveryManifest(project, source, "tx")
	manifest.Entries[0] = validRecoveryEntry("CaseProbe/Unresolved", EntryOperationCreate)
	manifest.Entries = append(manifest.Entries, validRecoveryEntry("CaseProbe/unresolved", EntryOperationCreate))
	require.Error(t, ValidateManifest(project, directory, manifest))
}

func TestValidateManifestRejectsFinalFIFOWithoutBlocking(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, makeFIFO(filepath.Join(source, "fifo")))
	manifest := validRecoveryManifest(project, source, "tx")
	manifest.Entries[0] = validRecoveryEntry("fifo", EntryOperationUpdate)

	done := make(chan error, 1)
	go func() { done <- ValidateManifest(project, directory, manifest) }()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("manifest validation blocked opening a FIFO leaf")
	}
}

func TestValidateManifestAppliesStoredSpellingToAllTrustedDirectories(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin F_GETPATH spelling contract")
	}
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	manifest := validRecoveryManifest(project, source, "tx")

	for name, canonical := range map[string]string{
		"project":               project,
		"transaction root":      filepath.Join(project, "transactions"),
		"transaction directory": directory,
		"metadata source":       source,
	} {
		t.Run(name, func(t *testing.T) {
			alias := swapASCIIBaseCase(canonical)
			if alias == canonical {
				t.Skip("path has no ASCII letter")
			}
			info, err := os.Stat(alias)
			if err != nil {
				t.Skip("fixture filesystem is case-sensitive")
			}
			canonicalInfo, err := os.Stat(canonical)
			require.NoError(t, err)
			require.True(t, os.SameFile(canonicalInfo, info))

			switch name {
			case "project":
				require.Error(t, ValidateManifest(alias, filepath.Join(alias, "transactions", "tx"), manifest))
			case "transaction root":
				aliasedDirectory := filepath.Join(project, filepath.Base(alias), "tx")
				require.Error(t, ValidateManifest(project, aliasedDirectory, manifest))
			case "transaction directory":
				require.Error(t, ValidateManifest(project, alias, manifest))
			case "metadata source":
				mutated := manifest
				mutated.SourcePath = alias
				require.Error(t, ValidateManifest(project, directory, mutated))
			}
		})
	}
}

func TestValidateManifestBindsNamespaceIdentityToOpenedComponent(t *testing.T) {
	project, source := recoveryProject(t)
	directory := filepath.Join(project, "transactions", "tx")
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(source, "parent"), 0o700))
	manifest := validRecoveryManifest(project, source, "tx")
	manifest.Entries[0] = validRecoveryEntry("parent/one", EntryOperationCreate)
	manifest.Entries = append(manifest.Entries, validRecoveryEntry("parent/two", EntryOperationCreate))

	original := manifestComponentAfterOpenHook
	manifestComponentAfterOpenHook = func(name string) {
		if name != "parent" {
			return
		}
		manifestComponentAfterOpenHook = nil
		require.NoError(t, os.Rename(filepath.Join(source, "parent"), filepath.Join(source, "old-parent")))
		require.NoError(t, os.Mkdir(filepath.Join(source, "parent"), 0o700))
	}
	t.Cleanup(func() { manifestComponentAfterOpenHook = original })
	require.Error(t, ValidateManifest(project, directory, manifest))
}

func TestDiscoveryClassifiesForgedRetainedOrphansAndFIFOWithoutBlocking(t *testing.T) {
	project, _ := recoveryProject(t)
	root := filepath.Join(project, "transactions")
	forgedNames := []string{
		".orphan-0123456789abcdef0123456789abcdef",
		".orphan-ABCDEF0123456789ABCDEF0123456789",
	}
	for _, name := range forgedNames {
		directory := filepath.Join(root, name)
		require.NoError(t, os.Mkdir(directory, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(directory, ".cleanup-complete"), []byte(orphanCleanupContents), 0o600))
	}
	fifoDirectory := filepath.Join(root, ".orphan-fedcba9876543210fedcba9876543210")
	require.NoError(t, os.Mkdir(fifoDirectory, 0o700))
	require.NoError(t, makeFIFO(filepath.Join(fifoDirectory, ".cleanup-complete")))

	done := make(chan struct{})
	var discovered []DiscoveredTransaction
	var err error
	go func() {
		discovered, err = DiscoverTransactions(project)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("discovery blocked reading a FIFO marker")
	}
	require.NoError(t, err)
	require.Len(t, discovered, 3)
	for _, transaction := range discovered {
		require.True(t, transaction.Orphan)
		require.Error(t, transaction.Invalid, "retained entries must be explicitly classified")
	}
}

func makeFIFO(path string) error {
	return unix.Mkfifo(path, 0o600)
}

func swapASCIIBaseCase(path string) string {
	directory, base := filepath.Split(path)
	for index, character := range base {
		if character >= 'a' && character <= 'z' {
			return filepath.Join(directory, base[:index]+string(character-'a'+'A')+base[index+1:])
		}
		if character >= 'A' && character <= 'Z' {
			return filepath.Join(directory, base[:index]+string(character-'A'+'a')+base[index+1:])
		}
	}
	return path
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

func failedRecoveryTransaction(t *testing.T) (applyPlan, TransactionManifest, Snapshot) {
	t.Helper()
	_, plan, manifest := preparedForwardTransaction(t)
	before := mustSnapshot(t, manifest.SourcePath)
	transactionBeforeCommitHook = func() error { return errors.New("stop for recovery") }
	rollingWrites := 0
	transactionManifestWriteHook = func(state TransactionState) error {
		if state == TransactionRollingBack {
			rollingWrites++
			if rollingWrites == 2 {
				return errors.New("simulate crash before automatic rollback")
			}
		}
		return nil
	}
	t.Cleanup(func() { transactionBeforeCommitHook = nil; transactionManifestWriteHook = nil })
	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	transactionManifestWriteHook = nil
	persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
	require.Equal(t, TransactionRollingBack, persisted.State)
	return plan, persisted, before
}

func TestCleanupCommittedTransactionNeverRollsBack(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	before := mustSnapshot(t, manifest.SourcePath)
	for path := range before.Entries {
		if strings.Contains(path, ".claude-squad-displaced-") {
			delete(before.Entries, path)
		}
	}
	warning, err := CleanCommittedTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Nil(t, warning)
	require.Equal(t, before, mustSnapshot(t, manifest.SourcePath))
	require.Equal(t, TransactionComplete, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestCleanupCapturesSourceTemporaryBeforeRecursiveRemoval(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	index := slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })
	entry := manifest.Entries[index]
	sourceTemporary := filepath.Join(manifest.SourcePath, entry.CaptureName)
	quarantine := filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.QuarantinePath))
	require.FileExists(t, sourceTemporary)
	transactionManifestWriteHook = func(state TransactionState) error {
		if manifest.Entries[index].CleanupPhase == CleanupPhaseQuarantined {
			transactionManifestWriteHook = nil
			return errors.New("stop before private removal")
		}
		return nil
	}
	t.Cleanup(func() { transactionManifestWriteHook = nil })
	require.Error(t, QuarantineSourceTemporary(&manifest, index, plan.TransactionDir))
	require.NoFileExists(t, sourceTemporary)
	require.FileExists(t, quarantine)
}

type sourceRemoveAllTrackingOps struct {
	fileOps
	source string
	called bool
}

func (f *sourceRemoveAllTrackingOps) RemoveAll(path string) error {
	if path == f.source || strings.HasPrefix(path, f.source+string(filepath.Separator)) {
		f.called = true
	}
	return f.fileOps.RemoveAll(path)
}

func TestCleanupNeverRemoveAllsSourceNamespacePath(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	original := applyFS
	tracking := &sourceRemoveAllTrackingOps{fileOps: original, source: manifest.SourcePath}
	applyFS = tracking
	t.Cleanup(func() { applyFS = original })
	_, err := CleanCommittedTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.False(t, tracking.called)
}

func TestCleanupReplacementBecomesCleanupBlocked(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })]
	require.NoError(t, os.Remove(filepath.Join(manifest.SourcePath, entry.CaptureName)))
	writeApplyFile(t, manifest.SourcePath, entry.CaptureName, "external", 0o600)
	warning, err := CleanCommittedTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.NotNil(t, warning)
	require.Contains(t, warning.RetainedPaths, entry.CaptureName)
	require.Equal(t, "external", mustReadFile(t, filepath.Join(manifest.SourcePath, entry.CaptureName)))
	require.Equal(t, TransactionCleanupBlocked, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestCleaningRecoveryResumesIdempotently(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	manifest.State = TransactionCleaning
	require.NoError(t, WriteManifestDurable(manifest, plan.TransactionDir))
	first, err := CleanCommittedTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Nil(t, first)
	second, err := CleanCommittedTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Nil(t, second)
}

func TestCompleteTransactionRetainsMetadataUntilAcknowledged(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	warning, err := CleanCommittedTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Nil(t, warning)
	require.FileExists(t, filepath.Join(plan.TransactionDir, "manifest.json"))
	require.Equal(t, TransactionComplete, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestRecoveryPreparedTransactionRollsBack(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	before := mustSnapshot(t, manifest.SourcePath)
	report, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryRolledBack, report.Disposition)
	require.Equal(t, before, mustSnapshot(t, manifest.SourcePath))
	require.Equal(t, TransactionRolledBack, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestRecoveryApplyingTransactionRollsBackInReverseOrder(t *testing.T) {
	plan, manifest, before := failedRecoveryTransaction(t)
	var operations []string
	transactionAfterFilesystemHook = func(action, path string) {
		if action == "rollback-capture" || action == "restore" {
			operations = append(operations, action+":"+path)
		}
	}
	t.Cleanup(func() { transactionAfterFilesystemHook = nil })
	report, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryRolledBack, report.Disposition)
	require.Equal(t, before, mustSnapshot(t, manifest.SourcePath))

	var expected []string
	for index := len(manifest.Entries) - 1; index >= 0; index-- {
		entry := manifest.Entries[index]
		switch entry.Operation {
		case EntryOperationCreate, EntryOperationCreatedParent:
			expected = append(expected, "rollback-capture:"+entry.Path)
		case EntryOperationUpdate:
			expected = append(expected, "rollback-capture:"+entry.Path, "restore:"+entry.Path)
		case EntryOperationDelete:
			expected = append(expected, "restore:"+entry.Path)
		}
	}
	require.Equal(t, expected, operations)
}

func TestRecoveryRollingBackTransactionResumesIdempotently(t *testing.T) {
	plan, manifest, before := failedRecoveryTransaction(t)
	transactionManifestWriteHook = func(state TransactionState) error {
		if state == TransactionRollingBack {
			transactionManifestWriteHook = nil
			return errors.New("interrupt rollback")
		}
		return nil
	}
	t.Cleanup(func() { transactionManifestWriteHook = nil })
	_, err := RollbackTransaction(&manifest, plan.TransactionDir)
	require.Error(t, err)
	persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
	report, err := RecoverTransaction(&persisted, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryRolledBack, report.Disposition)
	require.Equal(t, before, mustSnapshot(t, manifest.SourcePath))
}

func TestRecoveryInfersCaptureCompletedBeforePhaseWrite(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	index := slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })
	entry := &manifest.Entries[index]
	require.NoError(t, os.Rename(filepath.Join(manifest.SourcePath, entry.Path), filepath.Join(manifest.SourcePath, entry.CaptureName)))
	entry.Phase = EntryPhaseCaptureIntent
	manifest.State = TransactionApplying
	require.NoError(t, WriteManifestDurable(manifest, plan.TransactionDir))
	report, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryRolledBack, report.Disposition)
	require.Equal(t, "before", mustReadFile(t, filepath.Join(manifest.SourcePath, "update")))
}

func TestRecoveryInfersPublicationCompletedBeforePhaseWrite(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	index := slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "link" })
	entry := &manifest.Entries[index]
	require.NoError(t, os.Rename(filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.StagePath)), filepath.Join(manifest.SourcePath, entry.Path)))
	entry.Phase = EntryPhasePublishIntent
	manifest.State = TransactionApplying
	require.NoError(t, WriteManifestDurable(manifest, plan.TransactionDir))
	report, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryRolledBack, report.Disposition)
	require.NoFileExists(t, filepath.Join(manifest.SourcePath, "link"))
}

func TestRecoveryInfersRollbackCaptureCompletedBeforePhaseWrite(t *testing.T) {
	plan, manifest, before := failedRecoveryTransaction(t)
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })]
	crashed := false
	transactionAfterFilesystemHook = func(action, path string) {
		if !crashed && action == "rollback-capture" && path == entry.Path {
			crashed = true
			transactionManifestWriteHook = func(state TransactionState) error {
				transactionManifestWriteHook = nil
				return errors.New("crash after rollback capture before phase persistence")
			}
		}
	}
	t.Cleanup(func() { transactionAfterFilesystemHook = nil; transactionManifestWriteHook = nil })
	_, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.Error(t, err)
	require.True(t, crashed)
	persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
	require.Equal(t, EntryPhaseRollbackCaptureIntent, persisted.Entries[slices.IndexFunc(persisted.Entries, func(e TransactionEntry) bool { return e.Path == entry.Path })].Phase)

	transactionAfterFilesystemHook = nil
	report, err := RecoverTransaction(&persisted, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryRolledBack, report.Disposition)
	require.Equal(t, before, mustSnapshot(t, manifest.SourcePath))
	repeated, err := RecoverTransaction(&persisted, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryRolledBack, repeated.Disposition)
}

func TestRecoveryInfersCreatedParentRollbackCaptureBeforePhaseWrite(t *testing.T) {
	plan, manifest, before := failedRecoveryTransaction(t)
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "new/deep" })]
	crashed := false
	transactionAfterFilesystemHook = func(action, path string) {
		if !crashed && action == "rollback-capture" && path == entry.Path {
			crashed = true
			transactionManifestWriteHook = func(state TransactionState) error {
				transactionManifestWriteHook = nil
				return errors.New("crash after created-parent rollback capture before phase persistence")
			}
		}
	}
	t.Cleanup(func() { transactionAfterFilesystemHook = nil; transactionManifestWriteHook = nil })
	_, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.Error(t, err)
	require.True(t, crashed)
	persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
	persistedEntry := persisted.Entries[slices.IndexFunc(persisted.Entries, func(e TransactionEntry) bool { return e.Path == entry.Path })]
	require.Equal(t, EntryPhaseRollbackCaptureIntent, persistedEntry.Phase)
	require.NoDirExists(t, filepath.Join(manifest.SourcePath, entry.Path))
	require.DirExists(t, filepath.Join(manifest.SourcePath, entry.RollbackCaptureName))

	transactionAfterFilesystemHook = nil
	report, err := RecoverTransaction(&persisted, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryRolledBack, report.Disposition)
	require.Equal(t, before, mustSnapshot(t, manifest.SourcePath))
	repeated, err := RecoverTransaction(&persisted, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryRolledBack, repeated.Disposition)
}

func TestRecoveryInfersRestorationCompletedBeforePhaseWrite(t *testing.T) {
	plan, manifest, before := failedRecoveryTransaction(t)
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })]
	crashed := false
	transactionAfterFilesystemHook = func(action, path string) {
		if !crashed && action == "restore" && path == entry.Path {
			crashed = true
			transactionManifestWriteHook = func(state TransactionState) error {
				transactionManifestWriteHook = nil
				return errors.New("crash after restoration before phase persistence")
			}
		}
	}
	t.Cleanup(func() { transactionAfterFilesystemHook = nil; transactionManifestWriteHook = nil })
	_, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.Error(t, err)
	require.True(t, crashed)
	persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
	persistedEntry := persisted.Entries[slices.IndexFunc(persisted.Entries, func(e TransactionEntry) bool { return e.Path == entry.Path })]
	require.Equal(t, EntryPhaseRollbackCaptured, persistedEntry.Phase)
	require.NoFileExists(t, filepath.Join(manifest.SourcePath, persistedEntry.CaptureName))
	require.Equal(t, "before", mustReadFile(t, filepath.Join(manifest.SourcePath, entry.Path)))

	transactionAfterFilesystemHook = nil
	report, err := RecoverTransaction(&persisted, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryRolledBack, report.Disposition)
	require.Equal(t, before, mustSnapshot(t, manifest.SourcePath))
	repeated, err := RecoverTransaction(&persisted, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryRolledBack, repeated.Disposition)
}

func TestRecoveryCreateUpdateDeleteAndTypeTransitions(t *testing.T) {
	plan, manifest, before := failedRecoveryTransaction(t)
	report, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryRolledBack, report.Disposition)
	require.Equal(t, before, mustSnapshot(t, manifest.SourcePath))
}

func TestRecoveryRepeatedInvocationIsIdempotent(t *testing.T) {
	plan, manifest, before := failedRecoveryTransaction(t)
	first, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	second, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, first.Disposition, second.Disposition)
	require.Equal(t, before, mustSnapshot(t, manifest.SourcePath))
}

func TestRecoveryExternalRestoreTargetBecomesRecoveryBlocked(t *testing.T) {
	plan, manifest, _ := failedRecoveryTransaction(t)
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })]
	require.NoError(t, os.Remove(filepath.Join(manifest.SourcePath, entry.Path)))
	writeApplyFile(t, manifest.SourcePath, entry.Path, "external", 0o600)
	report, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryBlocked, report.Disposition)
	require.Equal(t, "external", mustReadFile(t, filepath.Join(manifest.SourcePath, entry.Path)))
	require.Equal(t, TransactionRecoveryBlocked, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestRecoveryBlockedPreservesExternalAndRetainedContent(t *testing.T) {
	plan, manifest, _ := failedRecoveryTransaction(t)
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })]
	writeApplyFile(t, manifest.SourcePath, entry.Path, "external", 0o600)
	report, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryBlocked, report.Disposition)
	require.NotEmpty(t, report.Issues)
	require.Contains(t, report.Issues[0].RetainedPaths, entry.CaptureName)
	require.Equal(t, "external", mustReadFile(t, filepath.Join(manifest.SourcePath, entry.Path)))
	require.FileExists(t, filepath.Join(manifest.SourcePath, entry.CaptureName))
}

func TestRollbackNeverRemovesSourceTemporaryByPathname(t *testing.T) {
	plan, manifest, _ := failedRecoveryTransaction(t)
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })]
	require.NoError(t, os.Remove(filepath.Join(manifest.SourcePath, entry.Path)))
	writeApplyFile(t, manifest.SourcePath, entry.Path, "external", 0o600)
	writeApplyFile(t, manifest.SourcePath, entry.RollbackCaptureName, "temporary occupant", 0o600)
	_, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, "temporary occupant", mustReadFile(t, filepath.Join(manifest.SourcePath, entry.RollbackCaptureName)))
}

func TestRecoveryPublishIntentDoesNotClaimEqualExternalTargetWhileStageRemains(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	index := slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "link" })
	entry := &manifest.Entries[index]
	entry.Phase, manifest.State = EntryPhasePublishIntent, TransactionApplying
	require.NoError(t, os.Symlink(entry.Result.SymlinkTarget, filepath.Join(manifest.SourcePath, entry.Path)))
	require.NoError(t, WriteManifestDurable(manifest, plan.TransactionDir))

	report, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryBlocked, report.Disposition)
	require.Equal(t, []string{entry.Path}, report.Issues[0].Paths)
	require.Equal(t, []string{entry.Path, entry.StagePath}, report.Issues[0].RetainedPaths)
	require.FileExists(t, filepath.Join(manifest.SourcePath, entry.Path))
	require.FileExists(t, filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.StagePath)))
	require.Equal(t, TransactionRecoveryBlocked, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestRecoveryPublishIntentBlocksWhenStageMismatches(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	index := slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "link" })
	entry := &manifest.Entries[index]
	entry.Phase, manifest.State = EntryPhasePublishIntent, TransactionApplying
	require.NoError(t, os.Remove(filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.StagePath))))
	writeApplyFile(t, plan.TransactionDir, entry.StagePath, "external stage", 0o600)
	require.NoError(t, WriteManifestDurable(manifest, plan.TransactionDir))

	report, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryBlocked, report.Disposition)
	require.Equal(t, []string{entry.Path}, report.Issues[0].Paths)
	require.Equal(t, []string{entry.StagePath}, report.Issues[0].RetainedPaths)
	require.NoFileExists(t, filepath.Join(manifest.SourcePath, entry.Path))
	require.Equal(t, "external stage", mustReadFile(t, filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.StagePath))))
}

func TestRecoveryPendingCreateDoesNotClaimEqualExternalTarget(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	index := slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "link" })
	entry := manifest.Entries[index]
	require.NoError(t, os.Symlink(entry.Result.SymlinkTarget, filepath.Join(manifest.SourcePath, entry.Path)))
	report, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryBlocked, report.Disposition)
	require.Equal(t, []string{entry.Path}, report.Issues[0].Paths)
	require.Equal(t, []string{entry.Path, entry.StagePath}, report.Issues[0].RetainedPaths)
	require.FileExists(t, filepath.Join(manifest.SourcePath, entry.Path))
}

func TestRecoveryBlockedReportSurvivesRestartExactly(t *testing.T) {
	plan, manifest, _ := failedRecoveryTransaction(t)
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })]
	writeApplyFile(t, manifest.SourcePath, entry.Path, "external", 0o600)
	first, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryBlocked, first.Disposition)
	persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
	second, err := RecoverTransaction(&persisted, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestRecoveryRenameConflictBecomesDurablyBlocked(t *testing.T) {
	plan, manifest, _ := failedRecoveryTransaction(t)
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })]
	transactionBeforeFilesystemHook = func(action, path string) {
		if action == "rollback-capture" && path == entry.Path {
			writeApplyFile(t, manifest.SourcePath, entry.RollbackCaptureName, "external", 0o600)
		}
	}
	t.Cleanup(func() { transactionBeforeFilesystemHook = nil })
	report, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryBlocked, report.Disposition)
	require.Equal(t, TransactionRecoveryBlocked, readPersistedTransactionManifest(t, plan.TransactionDir).State)
	require.Equal(t, "external", mustReadFile(t, filepath.Join(manifest.SourcePath, entry.RollbackCaptureName)))
}

func TestRecoveryRollbackCaptureSourceReplacementIsPreserved(t *testing.T) {
	plan, manifest, _ := failedRecoveryTransaction(t)
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "update" })]
	var displaced string
	transactionBeforeFilesystemHook = func(action, path string) {
		if action != "rollback-capture" || path != entry.Path {
			return
		}
		displaced = filepath.Join(manifest.SourcePath, "displaced-result")
		require.NoError(t, os.Rename(filepath.Join(manifest.SourcePath, entry.Path), displaced))
		writeApplyFile(t, manifest.SourcePath, entry.Path, "external", 0o600)
	}
	t.Cleanup(func() { transactionBeforeFilesystemHook = nil })
	report, err := RecoverTransaction(&manifest, plan.TransactionDir)
	require.NoError(t, err)
	require.Equal(t, RecoveryBlocked, report.Disposition)
	require.Equal(t, "external", mustReadFile(t, filepath.Join(manifest.SourcePath, entry.Path)))
	require.NoFileExists(t, filepath.Join(manifest.SourcePath, entry.RollbackCaptureName))
	require.FileExists(t, displaced)
}

func TestRecoveryCreatedParentPublishIntentClassifiesStageOwnership(t *testing.T) {
	for _, test := range []struct {
		name         string
		replaceStage bool
		publish      bool
		wantBlocked  bool
	}{
		{name: "owned at stage"},
		{name: "owned at source", publish: true},
		{name: "replacement at stage", replaceStage: true, wantBlocked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, plan, manifest := preparedForwardTransaction(t)
			index := slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Operation == EntryOperationCreatedParent })
			entry := &manifest.Entries[index]
			require.NoError(t, os.Mkdir(filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.StagePath)), 0o755))
			var stat unix.Stat_t
			require.NoError(t, unix.Lstat(filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.StagePath)), &stat))
			entry.CreatedDevice, entry.CreatedInode = uint64(stat.Dev), uint64(stat.Ino)
			entry.Phase, manifest.State = EntryPhasePublishIntent, TransactionApplying
			if test.publish {
				require.NoError(t, os.Rename(filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.StagePath)), filepath.Join(manifest.SourcePath, entry.Path)))
			}
			if test.replaceStage {
				require.NoError(t, os.Remove(filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.StagePath))))
				require.NoError(t, os.Mkdir(filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.StagePath)), 0o755))
			}
			require.NoError(t, WriteManifestDurable(manifest, plan.TransactionDir))
			report, err := RecoverTransaction(&manifest, plan.TransactionDir)
			require.NoError(t, err)
			require.Equal(t, test.wantBlocked, report.Disposition == RecoveryBlocked)
			if test.wantBlocked {
				require.DirExists(t, filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.StagePath)))
			}
		})
	}
}
