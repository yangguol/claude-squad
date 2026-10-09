package workspace

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestApplyTransactionCleanMerge(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "both.txt", "one\ntwo\nthree\n", 0o644)
	writeApplyFile(t, source, "agent-delete.txt", "delete\n", 0o644)
	writeApplyFile(t, source, "source-only.txt", "base\n", 0o644)
	writeApplyFile(t, source, "rename-old.txt", "rename\n", 0o644)
	writeApplyFile(t, source, "script", "run\n", 0o644)

	workspace := newApplyWorkspace(t, source)

	writeApplyFile(t, workspace.WorkDir(), "both.txt", "agent\ntwo\nthree\n", 0o644)
	require.NoError(t, os.Remove(filepath.Join(workspace.WorkDir(), "agent-delete.txt")))
	writeApplyFile(t, workspace.WorkDir(), "agent-add.txt", "agent\n", 0o644)
	require.NoError(t, os.Rename(filepath.Join(workspace.WorkDir(), "rename-old.txt"), filepath.Join(workspace.WorkDir(), "rename-new.txt")))
	require.NoError(t, os.Chmod(filepath.Join(workspace.WorkDir(), "script"), 0o755))

	writeApplyFile(t, source, "both.txt", "one\ntwo\nsource\n", 0o644)
	writeApplyFile(t, source, "source-only.txt", "source\n", 0o644)
	writeApplyFile(t, source, "source-add.txt", "source add\n", 0o644)
	before := mustSnapshot(t, source)

	plan, err := workspace.planApply()
	require.NoError(t, err)
	require.NotEmpty(t, plan.TransactionDir)
	require.DirExists(t, plan.TransactionDir)
	require.Equal(t, []string{"agent-add.txt", "agent-delete.txt", "both.txt", "rename-new.txt", "rename-old.txt", "script"}, plan.ChangedPaths())
	require.Equal(t, "agent\ntwo\nsource\n", string(plan.Updates["both.txt"].Contents))
	require.Equal(t, os.FileMode(0o755), plan.Updates["script"].Mode)
	require.Equal(t, before, mustSnapshot(t, source), "planning must not mutate source")

	result, err := workspace.Apply()
	require.NoError(t, err)
	require.Equal(t, plan.ChangedPaths(), result.Changed)
	require.NotEqual(t, before, mustSnapshot(t, source))
	require.Equal(t, "agent\ntwo\nsource\n", mustReadFile(t, filepath.Join(source, "both.txt")))
	require.Equal(t, "source\n", mustReadFile(t, filepath.Join(source, "source-only.txt")))
	require.Equal(t, "source add\n", mustReadFile(t, filepath.Join(source, "source-add.txt")))
	require.Equal(t, "agent\n", mustReadFile(t, filepath.Join(source, "agent-add.txt")))
	require.NoFileExists(t, filepath.Join(source, "agent-delete.txt"))
	require.NoFileExists(t, filepath.Join(source, "rename-old.txt"))
	require.Equal(t, "rename\n", mustReadFile(t, filepath.Join(source, "rename-new.txt")))
	info, err := os.Stat(filepath.Join(source, "script"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	require.Equal(t, plan.MergedTree, workspace.data.LastAppliedTree)
	require.NotEmpty(t, workspace.data.LastAppliedAt)
}

func TestApplyTransactionRevalidatesTouchedPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "base", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "agent", 0o644)
	applyBeforeRevalidateHook = func() { writeApplyFile(t, source, "file", "late", 0o644) }
	t.Cleanup(func() { applyBeforeRevalidateHook = nil })

	_, err := workspace.Apply()
	var conflict *ConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, []string{"file"}, conflict.Paths)
	require.Equal(t, "late", mustReadFile(t, filepath.Join(source, "file")))
}

func TestApplyTransactionSymlinkAndIgnoredPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, policyFileName, "ignored/\n[generated]\ndist/\n", 0o644)
	writeApplyFile(t, source, "target", "target", 0o644)
	writeApplyFile(t, source, "ignored/value", "source", 0o644)
	workspace := newApplyWorkspace(t, source)
	require.NoError(t, os.Symlink("target", filepath.Join(workspace.WorkDir(), "link")))
	writeApplyFile(t, workspace.WorkDir(), "ignored/value", "agent", 0o644)
	writeApplyFile(t, workspace.WorkDir(), "dist/output", "generated", 0o644)

	result, err := workspace.Apply()
	require.NoError(t, err)
	require.Equal(t, []string{"link"}, result.Changed)
	target, err := os.Readlink(filepath.Join(source, "link"))
	require.NoError(t, err)
	require.Equal(t, "target", target)
	require.Equal(t, "source", mustReadFile(t, filepath.Join(source, "ignored/value")))
	require.NoFileExists(t, filepath.Join(source, "dist/output"))
}

func TestPlanApplyUnchangedReapplyIsNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "same.txt", "same\n", 0o644)
	workspace := newApplyWorkspace(t, source)

	plan, err := workspace.planApply()
	require.NoError(t, err)
	require.Empty(t, plan.ChangedPaths())
	result, err := workspace.Apply()
	require.NoError(t, err)
	require.Empty(t, result.Changed)
}

func TestPlanApplyIgnoresPersistedNonIncludedPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, policyFileName, "ignored/\n[generated]\ndist/\n", 0o644)
	writeApplyFile(t, source, "keep", "base", 0o644)
	require.NoError(t, os.Mkdir(filepath.Join(source, "ignored"), 0o755))
	writeApplyFile(t, source, "ignored/value", "base", 0o644)
	workspace := newApplyWorkspace(t, source)

	writeApplyFile(t, workspace.WorkDir(), "keep", "agent", 0o644)
	writeApplyFile(t, source, "ignored/value", "source", 0o644)
	writeApplyFile(t, workspace.WorkDir(), "dist/output", "generated", 0o644)

	plan, err := workspace.planApply()
	require.NoError(t, err)
	require.Equal(t, []string{"keep"}, plan.ChangedPaths())
}

type failingApplyOps struct {
	fileOps
	failRenameTarget string
	failRestorePath  string
}

func (f failingApplyOps) RenameNoReplace(oldpath, newpath string) error {
	if filepath.Clean(newpath) == filepath.Clean(f.failRenameTarget) {
		return errors.New("injected apply failure")
	}
	if filepath.Clean(newpath) == filepath.Clean(f.failRestorePath) && (strings.HasPrefix(filepath.Base(oldpath), ".claude-squad-rollback-") || strings.HasPrefix(filepath.Base(oldpath), ".claude-squad-displaced-")) {
		return errors.New("injected rollback failure")
	}
	return f.fileOps.RenameNoReplace(oldpath, newpath)
}

func (f failingApplyOps) Rename(oldpath, newpath string) error {
	if filepath.Clean(newpath) == filepath.Clean(f.failRestorePath) && (strings.HasPrefix(filepath.Base(oldpath), ".claude-squad-rollback-") || strings.HasPrefix(filepath.Base(oldpath), ".claude-squad-displaced-")) {
		return errors.New("injected rollback failure")
	}
	return f.fileOps.Rename(oldpath, newpath)
}

func TestApplyRollbackRestoresUpdatesAndDeletions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "a-update", "before", 0o755)
	writeApplyFile(t, source, "b-delete", "delete", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "a-update", "after", 0o644)
	require.NoError(t, os.Remove(filepath.Join(workspace.WorkDir(), "b-delete")))
	writeApplyFile(t, workspace.WorkDir(), "c-create", "create", 0o644)
	before := mustSnapshot(t, source)
	transactionBeforeCommitHook = func() error { return errors.New("injected apply failure") }
	t.Cleanup(func() { transactionBeforeCommitHook = nil })

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "injected apply failure")
	require.Equal(t, before, mustSnapshot(t, source))
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", "*", "manifest.json"))
	require.NoError(t, globErr)
	require.Len(t, matches, 1)
	require.Equal(t, TransactionRolledBack, readPersistedTransactionManifest(t, filepath.Dir(matches[0])).State)
}

func TestApplyRollbackFailureRetainsIncompleteTransaction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "a-update", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "a-update", "after", 0o644)
	writeApplyFile(t, workspace.WorkDir(), "b-create", "create", 0o644)
	transactionBeforeCommitHook = func() error { return errors.New("injected apply failure") }
	t.Cleanup(func() { transactionBeforeCommitHook = nil })

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "injected apply failure")
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", "*", "manifest.json"))
	require.NoError(t, globErr)
	require.Len(t, matches, 1)
	require.Equal(t, TransactionRolledBack, readPersistedTransactionManifest(t, filepath.Dir(matches[0])).State)
	require.DirExists(t, filepath.Dir(matches[0]))
}

func TestApplyPreservesPredictableSiblingCollision(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	collision := filepath.Join(source, ".claude-squad-apply-file-tmp")
	writeApplyFile(t, source, filepath.Base(collision), "unplanned", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)

	_, err := workspace.Apply()
	require.NoError(t, err)
	require.Equal(t, "unplanned", mustReadFile(t, collision))
}

func TestApplyRejectsSourceChangeBetweenValidationAndMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "agent", 0o644)
	applyAfterMutationValidationHook = func(string) {
		applyAfterMutationValidationHook = nil
		require.NoError(t, os.Remove(filepath.Join(source, "file")))
		writeApplyFile(t, source, "file", "late", 0o644)
	}
	t.Cleanup(func() { applyAfterMutationValidationHook = nil })

	_, err := workspace.Apply()
	require.Error(t, err)
	matches, globErr := filepath.Glob(filepath.Join(source, ".claude-squad-displaced-*"))
	require.NoError(t, globErr)
	require.Len(t, matches, 1)
	require.Equal(t, "late", mustReadFile(t, matches[0]))
}

func TestApplyPreservesReplacementAfterValidatedCapture(t *testing.T) {
	tests := map[string]struct {
		prepare func(*testing.T, string, *shadowWorkspace)
		late    func(*testing.T, string)
		assert  func(*testing.T, string)
	}{
		"regular-file-update": {
			prepare: func(t *testing.T, source string, workspace *shadowWorkspace) {
				writeApplyFile(t, source, "file", "before", 0o644)
				writeApplyFile(t, workspace.WorkDir(), "file", "agent", 0o644)
			},
			late: func(t *testing.T, source string) {
				writeApplyFile(t, source, "file", "late", 0o644)
			},
			assert: func(t *testing.T, source string) {
				require.Equal(t, "late", mustReadFile(t, filepath.Join(source, "file")))
			},
		},
		"directory-to-file": {
			prepare: func(t *testing.T, source string, workspace *shadowWorkspace) {
				writeApplyFile(t, source, "kind/child", "before", 0o644)
				require.NoError(t, os.RemoveAll(filepath.Join(workspace.WorkDir(), "kind")))
				writeApplyFile(t, workspace.WorkDir(), "kind", "agent", 0o644)
			},
			late: func(t *testing.T, source string) {
				writeApplyFile(t, source, "kind/late-child", "late", 0o644)
			},
			assert: func(t *testing.T, source string) {
				require.Equal(t, "late", mustReadFile(t, filepath.Join(source, "kind", "late-child")))
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			source := t.TempDir()
			if name == "regular-file-update" {
				writeApplyFile(t, source, "file", "before", 0o644)
			} else {
				writeApplyFile(t, source, "kind/child", "before", 0o644)
			}
			workspace := newApplyWorkspace(t, source)
			if name == "regular-file-update" {
				writeApplyFile(t, workspace.WorkDir(), "file", "agent", 0o644)
			} else {
				require.NoError(t, os.RemoveAll(filepath.Join(workspace.WorkDir(), "kind")))
				writeApplyFile(t, workspace.WorkDir(), "kind", "agent", 0o644)
			}
			applyAfterCaptureValidationHook = func(string) {
				applyAfterCaptureValidationHook = nil
				test.late(t, source)
			}
			t.Cleanup(func() { applyAfterCaptureValidationHook = nil })

			_, err := workspace.Apply()
			require.Error(t, err)
			test.assert(t, source)
		})
	}
}

func TestApplyNoReplacePreflightFailsBeforeSourceMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "agent", 0o644)
	original := platformAtomicRenameNoReplace
	platformAtomicRenameNoReplace = func(int, string, string) error { return errors.New("unsupported no-replace backend") }
	t.Cleanup(func() { platformAtomicRenameNoReplace = original })

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "atomic no-replace rename is unavailable")
	require.Equal(t, "before", mustReadFile(t, filepath.Join(source, "file")))
}

func TestRenameNoReplaceRefusesExistingDestination(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	destination := filepath.Join(parent, "destination")
	require.NoError(t, os.WriteFile(source, []byte("source"), 0o600))
	require.NoError(t, os.WriteFile(destination, []byte("destination"), 0o600))

	err := platformRenameNoReplace(source, destination)
	require.Error(t, err)
	require.Equal(t, "source", mustReadFile(t, source))
	require.Equal(t, "destination", mustReadFile(t, destination))
}

func TestApplyRejectsPostBackupCreateTargetDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "base", "base", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "created", "agent", 0o644)
	applyAfterBackupHook = func(string) {
		require.NoError(t, os.Mkdir(filepath.Join(source, "created"), 0o755))
	}
	t.Cleanup(func() { applyAfterBackupHook = nil })

	_, err := workspace.Apply()
	var conflict *ConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, []string{"created"}, conflict.Paths)
	require.DirExists(t, filepath.Join(source, "created"))
}

func TestApplyRejectsPostBackupSourceAndAncestorChanges(t *testing.T) {
	tests := map[string]func(*testing.T, string){
		"source": func(t *testing.T, source string) {
			writeApplyFile(t, source, "dir/file", "late", 0o644)
		},
		"ancestor": func(t *testing.T, source string) {
			require.NoError(t, os.Remove(filepath.Join(source, "dir", "file")))
			require.NoError(t, os.Remove(filepath.Join(source, "dir")))
			require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(source, "dir")))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			source := t.TempDir()
			writeApplyFile(t, source, "dir/file", "before", 0o644)
			workspace := newApplyWorkspace(t, source)
			writeApplyFile(t, workspace.WorkDir(), "dir/file", "after", 0o644)
			applyAfterBackupHook = func(string) { mutate(t, source) }
			t.Cleanup(func() { applyAfterBackupHook = nil })

			_, err := workspace.Apply()
			require.Error(t, err)
			if name == "source" {
				require.Equal(t, "late", mustReadFile(t, filepath.Join(source, "dir/file")))
			} else {
				info, statErr := os.Lstat(filepath.Join(source, "dir"))
				require.NoError(t, statErr)
				require.NotZero(t, info.Mode()&os.ModeSymlink)
			}
		})
	}
}

func TestApplyRollbackRemovesCreatedParents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "old", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "new/child", "created", 0o644)
	writeApplyFile(t, workspace.WorkDir(), "zz-fail", "created", 0o644)
	transactionBeforeCommitHook = func() error { return errors.New("injected apply failure") }
	t.Cleanup(func() { transactionBeforeCommitHook = nil })

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "injected apply failure")
	require.NoDirExists(t, filepath.Join(source, "new"))
}

func TestApplyDirectoryToFileRollbackRestoresTree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "kind/child", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	require.NoError(t, os.RemoveAll(filepath.Join(workspace.WorkDir(), "kind")))
	writeApplyFile(t, workspace.WorkDir(), "kind", "after", 0o644)
	writeApplyFile(t, workspace.WorkDir(), "zz-fail", "fail", 0o644)
	before := mustSnapshot(t, source)
	transactionBeforeCommitHook = func() error { return errors.New("injected apply failure") }
	t.Cleanup(func() { transactionBeforeCommitHook = nil })

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "injected apply failure")
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", "*", "manifest.json"))
	require.NoError(t, globErr)
	require.Len(t, matches, 1)
	persisted := readPersistedTransactionManifest(t, filepath.Dir(matches[0]))
	require.Equal(t, TransactionRolledBack, persisted.State)
	require.Equal(t, before, mustSnapshot(t, source))
}

func TestApplyDirectoryToFileAndFileToTree(t *testing.T) {
	tests := []struct{ name, sourcePath, worktreePath string }{
		{name: "directory-to-file", sourcePath: "kind/child", worktreePath: "kind"},
		{name: "file-to-tree", sourcePath: "kind", worktreePath: "kind/child"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			source := t.TempDir()
			writeApplyFile(t, source, test.sourcePath, "before", 0o644)
			workspace := newApplyWorkspace(t, source)
			require.NoError(t, os.RemoveAll(filepath.Join(workspace.WorkDir(), "kind")))
			writeApplyFile(t, workspace.WorkDir(), test.worktreePath, "after", 0o644)

			_, err := workspace.Apply()
			require.NoError(t, err)
			require.Equal(t, "after", mustReadFile(t, filepath.Join(source, filepath.FromSlash(test.worktreePath))))
		})
	}
}

type cleanupFailOps struct {
	fileOps
	removeManifest bool
}

func (f cleanupFailOps) RemoveAll(path string) error {
	if filepath.Base(filepath.Dir(path)) == "transactions" || filepath.Base(path) != "transactions" {
		if f.removeManifest {
			_ = f.fileOps.Remove(filepath.Join(path, "manifest.json"))
		}
		return errors.New("injected cleanup failure")
	}
	return f.fileOps.RemoveAll(path)
}

func TestApplyCleanupBlockedReturnsSuccessWithTypedWarning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	transactionAfterCommitHook = func() {
		matches, err := filepath.Glob(filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", "*", "manifest.json"))
		require.NoError(t, err)
		manifest := readPersistedTransactionManifest(t, filepath.Dir(matches[0]))
		entry := manifest.Entries[0]
		require.NoError(t, os.Remove(filepath.Join(source, entry.CaptureName)))
		writeApplyFile(t, source, entry.CaptureName, "external", 0o600)
	}
	t.Cleanup(func() { transactionAfterCommitHook = nil })

	result, err := workspace.Apply()
	require.NoError(t, err)
	require.NotNil(t, result.Warning)
	require.NotEmpty(t, result.TransactionID)
	require.Equal(t, "after", mustReadFile(t, filepath.Join(source, "file")))

	transactionDir := filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", result.TransactionID)
	persisted := readPersistedTransactionManifest(t, transactionDir)
	entry := persisted.Entries[slices.IndexFunc(persisted.Entries, func(entry TransactionEntry) bool { return entry.Path == "file" })]
	require.Equal(t, []string{"file"}, result.Warning.Paths)
	require.Equal(t, []string{entry.CaptureName}, result.Warning.RetainedPaths)
	require.Equal(t, TransactionCleanupBlocked, persisted.State)
	require.Len(t, persisted.RecoveryIssues, 1)
	require.Equal(t, result.Warning.Paths, persisted.RecoveryIssues[0].Paths)
	require.Equal(t, result.Warning.RetainedPaths, persisted.RecoveryIssues[0].RetainedPaths)
}

func TestAcknowledgeApplyRemovesOnlyCompleteTransaction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	result, err := workspace.Apply()
	require.NoError(t, err)
	require.NoError(t, workspace.AcknowledgeApply(result.TransactionID))
	require.NoDirExists(t, filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", result.TransactionID))
	require.Error(t, workspace.AcknowledgeApply("not-complete"))
}

func TestAcknowledgeApplyPreservesReplacementAfterValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	result, err := workspace.Apply()
	require.NoError(t, err)
	transactionDir := filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", result.TransactionID)
	retained := transactionDir + "-validated"
	acknowledgeAfterValidationHook = func() {
		acknowledgeAfterValidationHook = nil
		require.NoError(t, os.Rename(transactionDir, retained))
		require.NoError(t, os.Mkdir(transactionDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(transactionDir, "replacement"), []byte("keep"), 0o600))
	}
	t.Cleanup(func() { acknowledgeAfterValidationHook = nil })
	require.Error(t, workspace.AcknowledgeApply(result.TransactionID))
	require.FileExists(t, filepath.Join(transactionDir, "replacement"))
	require.FileExists(t, filepath.Join(retained, "manifest.json"))
}

func TestAcknowledgeApplyRejectsExistingNonCompleteTransaction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	workspace := newApplyWorkspace(t, source)
	projectPath := filepath.Dir(workspace.data.RepoPath)
	transactionDir := filepath.Join(projectPath, "transactions", "pending")
	require.NoError(t, os.MkdirAll(transactionDir, 0o700))
	manifest := TransactionManifest{Version: CurrentTransactionVersion, TransactionID: "pending", ProjectID: workspace.data.ProjectID, SessionID: workspace.data.SessionID, SourcePath: source, State: TransactionPrepared, Entries: []TransactionEntry{}}
	require.NoError(t, WriteManifestDurable(manifest, transactionDir))
	require.Error(t, workspace.AcknowledgeApply("pending"))
	require.FileExists(t, filepath.Join(transactionDir, "manifest.json"))
}

func TestApplyReportsCommittedCleanupFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	original := applyFS
	applyFS = cleanupFailOps{fileOps: original}
	t.Cleanup(func() { applyFS = original })

	_, err := workspace.Apply()
	require.NoError(t, err, "Task 7 owns committed cleanup")
	require.Equal(t, "after", mustReadFile(t, filepath.Join(source, "file")))
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", "*", "manifest.json"))
	require.NoError(t, globErr)
	require.NotEmpty(t, matches)
}

func TestApplyCleanupFailureRemainsDiscoverableAfterManifestDeletion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	original := applyFS
	applyFS = cleanupFailOps{fileOps: original, removeManifest: true}
	t.Cleanup(func() { applyFS = original })

	_, err := workspace.Apply()
	require.NoError(t, err, "Task 7 owns committed cleanup")
	manifestMatches, globErr := filepath.Glob(filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", "*", "manifest.json"))
	require.NoError(t, globErr)
	require.Len(t, manifestMatches, 1)
	require.Equal(t, TransactionComplete, readPersistedTransactionManifest(t, filepath.Dir(manifestMatches[0])).State)
}

func TestIncompleteTransactionDiscoveryDoesNotDelete(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "transactions", "session-nonce")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	transaction := Transaction{Directory: directory, Version: 1, SessionID: "session", SourcePath: "/source", State: transactionPrepared}
	require.NoError(t, writeTransactionManifest(transaction))

	transactions, err := FindIncompleteTransactions(root)
	require.NoError(t, err)
	require.Len(t, transactions, 1)
	require.Equal(t, directory, transactions[0].Directory)
	require.FileExists(t, filepath.Join(directory, "manifest.json"))
}

func TestApplyConflictTypesAreSortedAndLeaveSourceUntouched(t *testing.T) {
	tests := map[string]func(*testing.T, string, *shadowWorkspace){
		"text": func(t *testing.T, source string, w *shadowWorkspace) {
			writeApplyFile(t, source, "text", "source\n", 0o644)
			writeApplyFile(t, w.WorkDir(), "text", "agent\n", 0o644)
		},
		"add-add": func(t *testing.T, source string, w *shadowWorkspace) {
			writeApplyFile(t, source, "added", "source\n", 0o644)
			writeApplyFile(t, w.WorkDir(), "added", "agent\n", 0o644)
		},
		"modify-delete": func(t *testing.T, source string, w *shadowWorkspace) {
			writeApplyFile(t, source, "victim", "source\n", 0o644)
			require.NoError(t, os.Remove(filepath.Join(w.WorkDir(), "victim")))
		},
		"binary": func(t *testing.T, source string, w *shadowWorkspace) {
			writeApplyBytes(t, source, "binary", []byte{0, 1, 2}, 0o644)
			writeApplyBytes(t, w.WorkDir(), "binary", []byte{0, 3, 4}, 0o644)
		},
		"file-directory": func(t *testing.T, source string, w *shadowWorkspace) {
			require.NoError(t, os.Remove(filepath.Join(source, "kind")))
			writeApplyFile(t, source, "kind/child", "source", 0o644)
			writeApplyFile(t, w.WorkDir(), "kind", "agent", 0o644)
		},
		"file-symlink": func(t *testing.T, source string, w *shadowWorkspace) {
			require.NoError(t, os.Remove(filepath.Join(source, "kind")))
			require.NoError(t, os.Symlink("source-target", filepath.Join(source, "kind")))
			writeApplyFile(t, w.WorkDir(), "kind", "agent", 0o644)
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			source := t.TempDir()
			writeApplyFile(t, source, "text", "base\n", 0o644)
			writeApplyFile(t, source, "victim", "base\n", 0o644)
			writeApplyBytes(t, source, "binary", []byte{0, 9}, 0o644)
			writeApplyFile(t, source, "kind", "base", 0o644)
			workspace := newApplyWorkspace(t, source)
			mutate(t, source, workspace)
			before := mustSnapshot(t, source)

			_, err := workspace.Apply()
			var conflict *ConflictError
			require.True(t, errors.As(err, &conflict), "%v", err)
			require.NotEmpty(t, conflict.Paths)
			require.True(t, sort.StringsAreSorted(conflict.Paths))
			for _, path := range conflict.Paths {
				require.False(t, filepath.IsAbs(path))
				require.NotContains(t, path, "..")
			}
			require.Equal(t, before, mustSnapshot(t, source))
		})
	}
}

func TestApplyConflictPreservesUnusualPathExactly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	name := "odd\tquoted\"line\nname-é"
	writeApplyFile(t, source, name, "base\n", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, source, name, "source\n", 0o644)
	writeApplyFile(t, workspace.WorkDir(), name, "agent\n", 0o644)

	_, err := workspace.Apply()
	var conflict *ConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, []string{name}, conflict.Paths)
}

func TestParseMergeTreeOutputFailsClosed(t *testing.T) {
	oid := "0123456789012345678901234567890123456789"
	valid := []byte(oid + "\x00100644 " + oid + " 1\tpath\x00100644 " + oid + " 2\tpath\x00100644 " + oid + " 3\tpath\x00\x00")
	tests := map[string][]byte{
		"truncated":           valid[:len(valid)-1],
		"unrecognized record": append(append([]byte(nil), valid[:len(valid)-1]...), []byte("unexpected\x00")...),
		"incomplete stages":   []byte(oid + "\x00100644 " + oid + " 1\tpath"),
		"malformed stage":     []byte(oid + "\x00100644 " + oid + " 4\tpath\x00\x00"),
	}
	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := parseMergeTreeOutput(output)
			require.Error(t, err)
		})
	}
}

func TestPlanApplyRejectsCompleteSnapshotRaces(t *testing.T) {
	tests := map[string]func(string, string){
		"source addition":   func(source, _ string) { writeApplyFile(t, source, "late", "late", 0o644) },
		"worktree mutation": func(_, worktree string) { writeApplyFile(t, worktree, "tracked", "late", 0o644) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			source := t.TempDir()
			writeApplyFile(t, source, "tracked", "base", 0o644)
			workspace := newApplyWorkspace(t, source)
			applyPlanAfterImportsHook = func() { mutate(source, workspace.WorkDir()) }
			t.Cleanup(func() { applyPlanAfterImportsHook = nil })

			_, err := workspace.planApply()
			require.ErrorContains(t, err, "changed while planning")
		})
	}
}

func TestMergeApplyCommitsDoesNotAcceptFailedCommandWithStages(t *testing.T) {
	original := mergeTreeCommand
	mergeTreeCommand = func(string, ...string) ([]byte, error) {
		oid := "0123456789012345678901234567890123456789"
		return []byte(oid + "\x00100644 " + oid + " 1\tpath\x00100644 " + oid + " 2\tpath\x00100644 " + oid + " 3\tpath\x00\x00"), &exec.ExitError{}
	}
	t.Cleanup(func() { mergeTreeCommand = original })
	workspace := &shadowWorkspace{data: ShadowData{RepoPath: t.TempDir(), SourcePath: t.TempDir()}}
	oid := "0123456789012345678901234567890123456789"
	_, _, err := workspace.mergeApplyCommits(oid, oid)
	require.Error(t, err)
	require.Contains(t, fmt.Sprint(err), "failed")
}

func preparationPlan(t *testing.T) (*shadowWorkspace, applyPlan, Snapshot) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "update", "before", 0o644)
	writeApplyFile(t, source, "delete", "remove", 0o600)
	workspace := newApplyWorkspace(t, source)
	before := mustSnapshot(t, source)
	transactionRoot := filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions")
	require.NoError(t, os.MkdirAll(transactionRoot, 0o700))
	transactionDir, err := os.MkdirTemp(transactionRoot, workspace.data.SessionID+"-")
	require.NoError(t, err)
	plan := applyPlan{
		TransactionDir: transactionDir,
		SourcePath:     workspace.data.SourcePath,
		MergedTree:     strings.Repeat("a", 40),
		OursSnapshot:   before,
		Creates: map[string]applyEntry{
			"new/deep/file": {Type: EntryRegular, Mode: 0o640, Contents: []byte("new")},
			"link":          {Type: EntrySymlink, SymlinkTarget: "update"},
		},
		Updates: map[string]applyEntry{
			"update": {Type: EntryRegular, Mode: 0o755, Contents: []byte("after")},
		},
		Deletes: []string{"delete"},
	}
	return workspace, plan, before
}

func TestPrepareTransactionPreallocatesEveryOperationAndCreatedParent(t *testing.T) {
	workspace, plan, _ := preparationPlan(t)
	manifest, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.NoError(t, err)
	require.Equal(t, CurrentTransactionVersion, manifest.Version)
	require.Equal(t, TransactionPrepared, manifest.State)

	byPath := make(map[string]TransactionEntry)
	for _, entry := range manifest.Entries {
		byPath[entry.Path] = entry
	}
	require.Equal(t, EntryOperationCreate, byPath["new/deep/file"].Operation)
	require.Equal(t, EntryOperationCreate, byPath["link"].Operation)
	require.Equal(t, EntryOperationUpdate, byPath["update"].Operation)
	require.Equal(t, EntryOperationDelete, byPath["delete"].Operation)
	require.Equal(t, EntryOperationCreatedParent, byPath["new"].Operation)
	require.Equal(t, EntryOperationCreatedParent, byPath["new/deep"].Operation)
	require.Less(t, slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "new" }), slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Path == "new/deep" }))

	for _, entry := range manifest.Entries {
		require.Equal(t, EntryPhasePending, entry.Phase)
		require.Equal(t, CleanupPhasePending, entry.CleanupPhase)
		switch entry.Operation {
		case EntryOperationCreate:
			require.NotEmpty(t, entry.StagePath)
			require.NotEmpty(t, entry.RollbackCaptureName)
			require.NotNil(t, entry.Result)
		case EntryOperationUpdate:
			require.NotEmpty(t, entry.StagePath)
			require.NotEmpty(t, entry.CaptureName)
			require.NotEmpty(t, entry.RollbackCaptureName)
			require.NotEmpty(t, entry.QuarantinePath)
			require.NotNil(t, entry.Expected)
			require.NotNil(t, entry.Result)
		case EntryOperationDelete:
			require.NotEmpty(t, entry.CaptureName)
			require.NotEmpty(t, entry.QuarantinePath)
			require.NotNil(t, entry.Expected)
		case EntryOperationCreatedParent:
			require.NotEmpty(t, entry.RollbackCaptureName)
			require.Equal(t, EntryType("directory"), entry.Result.Type)
		}
	}
}

func TestPrepareTransactionPreallocatesUniqueSourceAndPrivatePaths(t *testing.T) {
	workspace, plan, _ := preparationPlan(t)
	manifest, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.NoError(t, err)

	sourceNames, privateNames := map[string]bool{}, map[string]bool{}
	for _, entry := range manifest.Entries {
		for _, name := range []string{entry.Path, entry.CaptureName, entry.RollbackCaptureName} {
			if name == "" {
				continue
			}
			require.False(t, sourceNames[name], name)
			sourceNames[name] = true
		}
		for _, name := range []string{entry.StagePath, entry.QuarantinePath} {
			if name == "" {
				continue
			}
			require.False(t, privateNames[name], name)
			privateNames[name] = true
			require.NoError(t, validateContained(plan.TransactionDir, filepath.Join(plan.TransactionDir, filepath.FromSlash(name)), "private path"))
		}
	}
	project := filepath.Dir(workspace.data.RepoPath)
	require.NoError(t, ValidateManifest(project, plan.TransactionDir, manifest))
}

func TestPrepareTransactionDurablyStagesRegularFilesAndSymlinks(t *testing.T) {
	workspace, plan, _ := preparationPlan(t)
	manifest, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.NoError(t, err)
	byPath := make(map[string]TransactionEntry)
	for _, entry := range manifest.Entries {
		byPath[entry.Path] = entry
	}
	require.Equal(t, "new", mustReadFile(t, filepath.Join(plan.TransactionDir, filepath.FromSlash(byPath["new/deep/file"].StagePath))))
	target, err := os.Readlink(filepath.Join(plan.TransactionDir, filepath.FromSlash(byPath["link"].StagePath)))
	require.NoError(t, err)
	require.Equal(t, "update", target)
	require.FileExists(t, filepath.Join(plan.TransactionDir, "manifest.json"))
}

func TestPreparedManifestContainsCompleteRollbackInformation(t *testing.T) {
	workspace, plan, _ := preparationPlan(t)
	now := time.Unix(100, 200).UTC()
	manifest, err := workspace.prepareTransaction(plan, now)
	require.NoError(t, err)
	require.Equal(t, plan.MergedTree, manifest.AppliedTree)
	require.Equal(t, now.Format(time.RFC3339Nano), manifest.AppliedAt)
	persisted, err := ReadManifestStrict(filepath.Dir(workspace.data.RepoPath), plan.TransactionDir)
	require.NoError(t, err)
	contents, err := json.Marshal(manifest)
	require.NoError(t, err)
	var publicManifest TransactionManifest
	require.NoError(t, json.Unmarshal(contents, &publicManifest))
	require.Equal(t, publicManifest, persisted)
}

func TestPrepareTransactionDoesNotMutateSource(t *testing.T) {
	workspace, plan, before := preparationPlan(t)
	_, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.NoError(t, err)
	require.Equal(t, before, mustSnapshot(t, workspace.data.SourcePath))
	matches, err := filepath.Glob(filepath.Join(workspace.data.SourcePath, ".claude-squad-*"))
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestApplyKeepsCompleteV2ManifestAuthoritativeDuringLegacyExecution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	applyAfterBackupHook = func(directory string) {
		manifest, err := ReadManifestStrict(filepath.Dir(workspace.data.RepoPath), directory)
		require.NoError(t, err)
		require.Equal(t, CurrentTransactionVersion, manifest.Version)
		require.NotEmpty(t, manifest.Entries)
	}
	t.Cleanup(func() { applyAfterBackupHook = nil })

	_, err := workspace.Apply()
	require.NoError(t, err)
}

func TestPrepareTransactionValidatesBeforePublishingManifest(t *testing.T) {
	workspace, plan, before := preparationPlan(t)
	workspace.data.SessionID = ""

	_, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.ErrorContains(t, err, "validate prepared transaction manifest")
	require.Equal(t, before, mustSnapshot(t, workspace.data.SourcePath))
	require.NoFileExists(t, filepath.Join(plan.TransactionDir, "manifest.json"))
	require.NoDirExists(t, plan.TransactionDir)
}

func TestPreallocationRetriesOccupiedSourceAndPrivateNames(t *testing.T) {
	workspace, plan, _ := preparationPlan(t)
	plan.Creates = map[string]applyEntry{"new": {Type: EntryRegular, Mode: 0o600, Contents: []byte("new")}}
	plan.Updates = nil
	plan.Deletes = nil
	occupiedNonce := bytes.Repeat([]byte{0x11}, 12)
	availableNonce := bytes.Repeat([]byte{0x22}, 12)
	require.NoError(t, os.WriteFile(filepath.Join(workspace.data.SourcePath, ".claude-squad-rollback-"+hex.EncodeToString(occupiedNonce)), []byte("occupied"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(plan.TransactionDir, "stage"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(plan.TransactionDir, "stage", hex.EncodeToString(occupiedNonce)), []byte("occupied"), 0o600))

	originalRead := applyRandomRead
	calls := 0
	applyRandomRead = func(buffer []byte) (int, error) {
		calls++
		value := availableNonce
		if calls == 1 || calls == 3 {
			value = occupiedNonce
		}
		return copy(buffer, value), nil
	}
	t.Cleanup(func() { applyRandomRead = originalRead })

	manifest, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.NoError(t, err)
	require.Len(t, manifest.Entries, 1)
	require.Equal(t, "stage/"+hex.EncodeToString(availableNonce), manifest.Entries[0].StagePath)
	require.Equal(t, ".claude-squad-rollback-"+hex.EncodeToString(availableNonce), manifest.Entries[0].RollbackCaptureName)
	require.FileExists(t, filepath.Join(workspace.data.SourcePath, ".claude-squad-rollback-"+hex.EncodeToString(occupiedNonce)))
}

func TestExclusiveTransactionDirectoryRetriesCollision(t *testing.T) {
	root := t.TempDir()
	first := bytes.Repeat([]byte{0x33}, 16)
	second := bytes.Repeat([]byte{0x44}, 16)
	collision := filepath.Join(root, "session-"+hex.EncodeToString(first))
	require.NoError(t, os.Mkdir(collision, 0o700))
	originalRead := applyRandomRead
	calls := 0
	applyRandomRead = func(buffer []byte) (int, error) {
		calls++
		value := second
		if calls == 1 {
			value = first
		}
		return copy(buffer, value), nil
	}
	t.Cleanup(func() { applyRandomRead = originalRead })

	created, err := createExclusiveTransactionDirectory(root, "session")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "session-"+hex.EncodeToString(second)), created)
	require.DirExists(t, collision)
	require.DirExists(t, created)
}

func TestPrepareTransactionDoesNotRecordExistingParentsAsCreated(t *testing.T) {
	workspace, plan, _ := preparationPlan(t)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace.data.SourcePath, "existing", "empty"), 0o751))
	plan.Creates = map[string]applyEntry{"existing/empty/file": {Type: EntryRegular, Mode: 0o600, Contents: []byte("new")}}
	plan.Updates = nil
	plan.Deletes = nil

	manifest, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.NoError(t, err)
	for _, entry := range manifest.Entries {
		require.NotEqual(t, EntryOperationCreatedParent, entry.Operation, entry.Path)
	}
}

func TestPreparedReplacementDirectoryTreeIncludesDirectoriesModesAndEmptyDirectories(t *testing.T) {
	workspace, plan, _ := preparationPlan(t)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace.data.SourcePath, "tree", "empty"), 0o751))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace.data.SourcePath, "tree", "nested"), 0o750))
	writeApplyFile(t, workspace.data.SourcePath, "tree/nested/file", "old", 0o640)
	plan.OursSnapshot = mustSnapshot(t, workspace.data.SourcePath)
	plan.Creates = map[string]applyEntry{"tree": {Type: EntryRegular, Mode: 0o600, Contents: []byte("replacement")}}
	plan.Updates = nil
	plan.Deletes = nil

	manifest, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.NoError(t, err)
	require.Len(t, manifest.Entries, 1)
	tree := manifest.Entries[0].ExpectedTree
	require.Equal(t, EntryType("directory"), tree["empty"].Type)
	require.Equal(t, os.FileMode(0o751), tree["empty"].Mode)
	require.Equal(t, EntryType("directory"), tree["nested"].Type)
	require.Equal(t, os.FileMode(0o750), tree["nested"].Mode)
	require.Equal(t, EntryRegular, tree["nested/file"].Type)
}

func TestPrepareTransactionFailureLeavesSourceAndRemovesOwnedArtifacts(t *testing.T) {
	workspace, plan, before := preparationPlan(t)
	require.NoError(t, os.Mkdir(filepath.Join(plan.TransactionDir, "stage"), 0o700))
	for index := range 100 {
		name := fmt.Sprintf("collision-%d", index)
		plan.Creates[name] = applyEntry{Type: EntryRegular, Mode: 0o600, Contents: []byte(name)}
	}

	original := applyFS
	applyFS = failingStageOps{fileOps: original}
	t.Cleanup(func() { applyFS = original })
	_, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.ErrorContains(t, err, "injected stage failure")
	require.Equal(t, before, mustSnapshot(t, workspace.data.SourcePath))
	require.NoDirExists(t, plan.TransactionDir)
}

func TestPreparedCaptureOccupantIsPreservedAndApplyFailsSafely(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	var occupant string
	applyBeforeRevalidateHook = func() {
		matches, err := filepath.Glob(filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", "*", "manifest.json"))
		require.NoError(t, err)
		require.Len(t, matches, 1)
		manifest, err := ReadManifestStrict(filepath.Dir(workspace.data.RepoPath), filepath.Dir(matches[0]))
		require.NoError(t, err)
		occupant = filepath.Join(source, filepath.FromSlash(manifest.Entries[0].CaptureName))
		require.NoError(t, os.WriteFile(occupant, []byte("occupant"), 0o600))
	}
	t.Cleanup(func() { applyBeforeRevalidateHook = nil })

	_, err := workspace.Apply()
	require.Error(t, err)
	require.Equal(t, "before", mustReadFile(t, filepath.Join(source, "file")))
	require.Equal(t, "occupant", mustReadFile(t, occupant))
}

func TestPreparedRollbackCaptureOccupantIsPreservedAndRollbackFailsSafely(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	writeApplyFile(t, workspace.WorkDir(), "zz-fail", "fail", 0o644)
	var occupant string
	applyBeforeRevalidateHook = func() {
		matches, err := filepath.Glob(filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", "*", "manifest.json"))
		require.NoError(t, err)
		manifest, err := ReadManifestStrict(filepath.Dir(workspace.data.RepoPath), filepath.Dir(matches[0]))
		require.NoError(t, err)
		for _, entry := range manifest.Entries {
			if entry.Path == "file" {
				occupant = filepath.Join(source, filepath.FromSlash(entry.RollbackCaptureName))
			}
		}
		require.NotEmpty(t, occupant)
		require.NoError(t, os.WriteFile(occupant, []byte("occupant"), 0o600))
	}
	applyBeforeMutationHook = func(path string) {
		if path == "zz-fail" {
			writeApplyFile(t, source, "zz-fail", "late", 0o600)
		}
	}
	t.Cleanup(func() {
		applyBeforeRevalidateHook = nil
		applyBeforeMutationHook = nil
	})

	_, err := workspace.Apply()
	require.Error(t, err)
	require.Equal(t, "occupant", mustReadFile(t, occupant))
	require.Equal(t, "after", mustReadFile(t, filepath.Join(source, "file")))
}

func TestPreparedQuarantineOccupantIsPreservedAndApplyFailsSafely(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "file", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "file", "after", 0o644)
	var occupant string
	applyBeforeRevalidateHook = func() {
		matches, err := filepath.Glob(filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", "*", "manifest.json"))
		require.NoError(t, err)
		require.Len(t, matches, 1)
		manifest, err := ReadManifestStrict(filepath.Dir(workspace.data.RepoPath), filepath.Dir(matches[0]))
		require.NoError(t, err)
		occupant = filepath.Join(filepath.Dir(matches[0]), filepath.FromSlash(manifest.Entries[0].QuarantinePath))
		require.NoError(t, os.MkdirAll(filepath.Dir(occupant), 0o700))
		require.NoError(t, os.WriteFile(occupant, []byte("occupant"), 0o600))
	}
	t.Cleanup(func() { applyBeforeRevalidateHook = nil })

	_, err := workspace.Apply()
	require.NoError(t, err, "Task 7 owns quarantine cleanup")
	require.Equal(t, "after", mustReadFile(t, filepath.Join(source, "file")))
	require.Equal(t, "occupant", mustReadFile(t, occupant))
}

func TestPrepareTransactionRevalidatesParentClassificationBeforePublish(t *testing.T) {
	for name, setup := range map[string]func(*testing.T, *shadowWorkspace, *applyPlan){
		"appeared": func(t *testing.T, workspace *shadowWorkspace, plan *applyPlan) {
			applyAfterParentClassificationHook = func() {
				require.NoError(t, os.Mkdir(filepath.Join(workspace.data.SourcePath, "parent"), 0o700))
			}
		},
		"disappeared": func(t *testing.T, workspace *shadowWorkspace, plan *applyPlan) {
			require.NoError(t, os.Mkdir(filepath.Join(workspace.data.SourcePath, "parent"), 0o700))
			applyAfterParentClassificationHook = func() {
				require.NoError(t, os.Remove(filepath.Join(workspace.data.SourcePath, "parent")))
			}
		},
		"type changed": func(t *testing.T, workspace *shadowWorkspace, plan *applyPlan) {
			require.NoError(t, os.Mkdir(filepath.Join(workspace.data.SourcePath, "parent"), 0o700))
			applyAfterParentClassificationHook = func() {
				require.NoError(t, os.Remove(filepath.Join(workspace.data.SourcePath, "parent")))
				writeApplyFile(t, workspace.data.SourcePath, "parent", "replacement", 0o600)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			workspace, plan, _ := preparationPlan(t)
			plan.Creates = map[string]applyEntry{"parent/child": {Type: EntryRegular, Mode: 0o600, Contents: []byte("new")}}
			plan.Updates = nil
			plan.Deletes = nil
			setup(t, workspace, &plan)
			t.Cleanup(func() { applyAfterParentClassificationHook = nil })

			_, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
			require.ErrorContains(t, err, "parent classification changed")
			require.NoFileExists(t, filepath.Join(plan.TransactionDir, "manifest.json"))
		})
	}
}

func TestPrepareTransactionCleanupSyncsRootAfterRemoval(t *testing.T) {
	workspace, plan, _ := preparationPlan(t)
	workspace.data.SessionID = ""
	root := filepath.Dir(plan.TransactionDir)
	originalSync := applySyncDirectory
	var events []string
	applySyncDirectory = func(path string) error {
		if path == root {
			events = append(events, "sync")
			require.NoDirExists(t, plan.TransactionDir)
		}
		return nil
	}
	t.Cleanup(func() { applySyncDirectory = originalSync })

	_, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.Error(t, err)
	require.Equal(t, []string{"sync"}, events)
}

func TestPrepareTransactionCleanupFailurePreservesAndReportsTransaction(t *testing.T) {
	workspace, plan, _ := preparationPlan(t)
	workspace.data.SessionID = ""
	original := applyFS
	applyFS = cleanupFailOps{fileOps: original}
	t.Cleanup(func() { applyFS = original })

	_, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.ErrorContains(t, err, "injected cleanup failure")
	require.DirExists(t, plan.TransactionDir)
}

func TestPrepareTransactionRejectsSwappedFinalPreparedParent(t *testing.T) {
	workspace, plan, _ := preparationPlan(t)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace.data.SourcePath, "parent"), 0o755))
	plan.Creates = map[string]applyEntry{"parent/child": {Type: EntryRegular, Mode: 0o600, Contents: []byte("new")}}
	plan.Updates = nil
	plan.Deletes = nil
	preparedPathAfterOpenHook = func(path string) {
		if path != "parent" {
			return
		}
		preparedPathAfterOpenHook = nil
		require.NoError(t, os.Rename(filepath.Join(workspace.data.SourcePath, "parent"), filepath.Join(workspace.data.SourcePath, "parked")))
		require.NoError(t, os.Mkdir(filepath.Join(workspace.data.SourcePath, "parent"), 0o755))
	}
	t.Cleanup(func() { preparedPathAfterOpenHook = nil })

	_, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.Error(t, err)
	require.DirExists(t, filepath.Join(workspace.data.SourcePath, "parent"))
	require.DirExists(t, filepath.Join(workspace.data.SourcePath, "parked"))
	require.NoFileExists(t, filepath.Join(workspace.data.SourcePath, "parent", "child"))
	require.NoFileExists(t, filepath.Join(plan.TransactionDir, "manifest.json"))
}

func TestPreparedPathClassificationRejectsSwappedFinalComponent(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, root, path string)
		swap  func(t *testing.T, root, path string)
	}{
		{
			name: "present leaf",
			setup: func(t *testing.T, root, path string) {
				writeApplyFile(t, root, path, "original", 0o600)
			},
			swap: func(t *testing.T, root, path string) {
				require.NoError(t, os.Rename(filepath.Join(root, path), filepath.Join(root, "parent", "parked")))
				writeApplyFile(t, root, path, "substitute", 0o600)
			},
		},
		{
			name: "absent leaf",
			setup: func(t *testing.T, root, path string) {
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755))
			},
			swap: func(t *testing.T, root, path string) {
				writeApplyFile(t, root, path, "appeared", 0o600)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := "parent/leaf"
			test.setup(t, root, path)
			preparedPathAfterLeafStatHook = func(got string) {
				if got != path {
					return
				}
				preparedPathAfterLeafStatHook = nil
				test.swap(t, root, path)
			}
			t.Cleanup(func() { preparedPathAfterLeafStatHook = nil })

			_, err := platformPreparedPathType(root, path)
			require.Error(t, err)
		})
	}
}

func TestPreparedReplacementDirectoryScanRejectsSwappedSourceRoot(t *testing.T) {
	container, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	source := filepath.Join(container, "source")
	parked := filepath.Join(container, "parked")
	require.NoError(t, os.Mkdir(source, 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(source, "tree"), 0o755))
	writeApplyFile(t, source, "tree/file", "original", 0o600)
	replacementScanAfterSourceRootOpenHook = func() {
		replacementScanAfterSourceRootOpenHook = nil
		require.NoError(t, os.Rename(source, parked))
		require.NoError(t, os.Mkdir(source, 0o755))
		require.NoError(t, os.Mkdir(filepath.Join(source, "tree"), 0o755))
		writeApplyFile(t, source, "tree/file", "substitute", 0o600)
	}
	t.Cleanup(func() { replacementScanAfterSourceRootOpenHook = nil })

	_, err = snapshotDirectoryTree(source, "tree")
	require.Error(t, err)
	require.Equal(t, "original", mustReadFile(t, filepath.Join(parked, "tree", "file")))
	require.Equal(t, "substitute", mustReadFile(t, filepath.Join(source, "tree", "file")))
}

func TestPreparedReplacementDirectoryScanRejectsSwappedRoot(t *testing.T) {
	workspace, plan, _ := preparationPlan(t)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace.data.SourcePath, "tree"), 0o755))
	writeApplyFile(t, workspace.data.SourcePath, "tree/file", "inside", 0o600)
	plan.OursSnapshot = mustSnapshot(t, workspace.data.SourcePath)
	plan.Creates = map[string]applyEntry{"tree": {Type: EntryRegular, Mode: 0o600, Contents: []byte("replacement")}}
	plan.Updates = nil
	plan.Deletes = nil
	replacementScanAfterRootOpenHook = func() {
		replacementScanAfterRootOpenHook = nil
		require.NoError(t, os.Rename(filepath.Join(workspace.data.SourcePath, "tree"), filepath.Join(workspace.data.SourcePath, "parked")))
		require.NoError(t, os.Mkdir(filepath.Join(workspace.data.SourcePath, "tree"), 0o755))
		writeApplyFile(t, workspace.data.SourcePath, "tree/file", "substitute", 0o600)
	}
	t.Cleanup(func() { replacementScanAfterRootOpenHook = nil })

	_, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.Error(t, err)
	require.Equal(t, "inside", mustReadFile(t, filepath.Join(workspace.data.SourcePath, "parked", "file")))
	require.Equal(t, "substitute", mustReadFile(t, filepath.Join(workspace.data.SourcePath, "tree", "file")))
	require.NoFileExists(t, filepath.Join(plan.TransactionDir, "manifest.json"))
}

func TestPrepareTransactionRevalidatesParentAfterManifestValidation(t *testing.T) {
	workspace, plan, before := preparationPlan(t)
	plan.Creates = map[string]applyEntry{"parent/child": {Type: EntryRegular, Mode: 0o600, Contents: []byte("new")}}
	plan.Updates = nil
	plan.Deletes = nil
	applyAfterManifestValidationHook = func() {
		applyAfterManifestValidationHook = nil
		require.NoError(t, os.Mkdir(filepath.Join(workspace.data.SourcePath, "parent"), 0o755))
	}
	t.Cleanup(func() { applyAfterManifestValidationHook = nil })

	_, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.ErrorContains(t, err, "parent classification changed")
	require.Equal(t, before, mustSnapshot(t, workspace.data.SourcePath))
	require.NoFileExists(t, filepath.Join(plan.TransactionDir, "manifest.json"))
}

func TestPreparedReplacementDirectoryScanRejectsSwappedDescendant(t *testing.T) {
	workspace, plan, _ := preparationPlan(t)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace.data.SourcePath, "tree", "child"), 0o755))
	writeApplyFile(t, workspace.data.SourcePath, "tree/child/file", "inside", 0o600)
	outside := t.TempDir()
	writeApplyFile(t, outside, "file", "outside", 0o600)
	plan.OursSnapshot = mustSnapshot(t, workspace.data.SourcePath)
	plan.Creates = map[string]applyEntry{"tree": {Type: EntryRegular, Mode: 0o600, Contents: []byte("replacement")}}
	plan.Updates = nil
	plan.Deletes = nil
	replacementScanBeforeOpenHook = func(path string) {
		if path != "child" {
			return
		}
		replacementScanBeforeOpenHook = nil
		require.NoError(t, os.Rename(filepath.Join(workspace.data.SourcePath, "tree", "child"), filepath.Join(workspace.data.SourcePath, "tree", "parked")))
		require.NoError(t, os.Symlink(outside, filepath.Join(workspace.data.SourcePath, "tree", "child")))
	}
	t.Cleanup(func() { replacementScanBeforeOpenHook = nil })

	_, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.Error(t, err)
	require.Equal(t, "outside", mustReadFile(t, filepath.Join(outside, "file")))
	require.NoFileExists(t, filepath.Join(plan.TransactionDir, "manifest.json"))
}

type failingStageOps struct{ fileOps }

func (f failingStageOps) OpenFile(name string, flag int, mode os.FileMode) (fileHandle, error) {
	if filepath.Base(filepath.Dir(name)) == "stage" {
		return nil, errors.New("injected stage failure")
	}
	return f.fileOps.OpenFile(name, flag, mode)
}

func newApplyWorkspace(t *testing.T, source string) *shadowWorkspace {
	t.Helper()
	value, _, err := NewShadow(source, "apply")
	require.NoError(t, err)
	workspace := value.(*shadowWorkspace)
	require.NoError(t, workspace.Setup())
	t.Cleanup(func() { _ = workspace.Cleanup() })
	return workspace
}

func writeApplyFile(t *testing.T, root, relative, contents string, mode os.FileMode) {
	t.Helper()
	writeApplyBytes(t, root, relative, []byte(contents), mode)
}

func writeApplyBytes(t *testing.T, root, relative string, contents []byte, mode os.FileMode) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(relative))
	require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o755))
	require.NoError(t, os.WriteFile(name, contents, mode))
}

func mustSnapshot(t *testing.T, root string) Snapshot {
	t.Helper()
	snapshot, err := Scan(root, Policy{})
	require.NoError(t, err)
	return snapshot
}

func readPersistedTransactionManifest(t *testing.T, transactionDir string) TransactionManifest {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(transactionDir, "manifest.json"))
	require.NoError(t, err)
	var manifest TransactionManifest
	require.NoError(t, json.Unmarshal(contents, &manifest))
	return manifest
}

func preparedForwardTransaction(t *testing.T) (*shadowWorkspace, applyPlan, TransactionManifest) {
	t.Helper()
	workspace, plan, _ := preparationPlan(t)
	manifest, err := workspace.prepareTransaction(plan, time.Unix(100, 200).UTC())
	require.NoError(t, err)
	return workspace, plan, manifest
}

func TestForwardCreatePersistsIntentBeforePublication(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	transactionBeforeFilesystemHook = func(action, path string) {
		if action == "publish" && path == "link" {
			persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
			require.Equal(t, TransactionApplying, persisted.State)
			require.Equal(t, EntryPhasePublishIntent, persisted.Entries[slices.IndexFunc(persisted.Entries, func(e TransactionEntry) bool { return e.Path == path })].Phase)
		}
	}
	t.Cleanup(func() { transactionBeforeFilesystemHook = nil })
	require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
}

func TestForwardUpdatePersistsCaptureAndPublishPhases(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	var phases []EntryPhase
	transactionBeforeFilesystemHook = func(action, path string) {
		if path == "update" && (action == "capture" || action == "publish") {
			persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
			phases = append(phases, persisted.Entries[slices.IndexFunc(persisted.Entries, func(e TransactionEntry) bool { return e.Path == path })].Phase)
		}
	}
	t.Cleanup(func() { transactionBeforeFilesystemHook = nil })
	require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.Equal(t, []EntryPhase{EntryPhaseCaptureIntent, EntryPhasePublishIntent}, phases)
}

func TestForwardDeletePersistsCaptureBeforeTargetAbsence(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	transactionBeforeFilesystemHook = func(action, path string) {
		if action == "capture" && path == "delete" {
			persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
			require.Equal(t, EntryPhaseCaptureIntent, persisted.Entries[slices.IndexFunc(persisted.Entries, func(e TransactionEntry) bool { return e.Path == path })].Phase)
			require.FileExists(t, filepath.Join(manifest.SourcePath, path))
		}
	}
	t.Cleanup(func() { transactionBeforeFilesystemHook = nil })
	require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.NoFileExists(t, filepath.Join(manifest.SourcePath, "delete"))
}

func TestForwardCreatedParentUsesWALState(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	checked := false
	transactionBeforeFilesystemHook = func(action, path string) {
		if action != "create-parent" || path != "new" {
			return
		}
		persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
		entry := persisted.Entries[slices.IndexFunc(persisted.Entries, func(e TransactionEntry) bool { return e.Path == path })]
		require.Equal(t, EntryPhasePublishIntent, entry.Phase)
		require.NotZero(t, entry.CreatedDevice)
		require.NotZero(t, entry.CreatedInode)
		require.DirExists(t, filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.StagePath)))
		require.NoDirExists(t, filepath.Join(manifest.SourcePath, path))
		checked = true
	}
	t.Cleanup(func() { transactionBeforeFilesystemHook = nil })
	require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.True(t, checked)
	require.DirExists(t, filepath.Join(manifest.SourcePath, "new"))
}

func TestCreatedParentIdentityWriteFailurePreventsPublication(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	opened := false
	transactionAfterCreatedParentOpenHook = func(path string) {
		if path == "new" {
			opened = true
		}
	}
	transactionManifestWriteHook = func(state TransactionState) error {
		if state == TransactionApplying && opened {
			opened = false
			return errors.New("injected ownership identity write failure")
		}
		return nil
	}
	t.Cleanup(func() {
		transactionAfterCreatedParentOpenHook = nil
		transactionManifestWriteHook = nil
	})

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
	index := slices.IndexFunc(persisted.Entries, func(entry TransactionEntry) bool { return entry.Path == "new" })
	require.NotEqual(t, TransactionRollingBack, persisted.State)
	require.NotZero(t, persisted.Entries[index].CreatedDevice)
	require.NotZero(t, persisted.Entries[index].CreatedInode)
	require.NoDirExists(t, filepath.Join(manifest.SourcePath, "new"))
	require.DirExists(t, filepath.Join(plan.TransactionDir, filepath.FromSlash(persisted.Entries[index].StagePath)))
}

func TestForwardCaptureValidationFailureRollsBack(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	transactionAfterFilesystemHook = func(action, path string) {
		if action == "capture" && path == "update" {
			writeApplyFile(t, manifest.SourcePath, manifest.Entries[slices.IndexFunc(manifest.Entries, func(e TransactionEntry) bool { return e.Path == path })].CaptureName, "changed", 0o644)
		}
	}
	t.Cleanup(func() { transactionAfterFilesystemHook = nil })
	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.NotEqual(t, TransactionRollingBack, readPersistedTransactionManifest(t, plan.TransactionDir).State)
	require.FileExists(t, filepath.Join(manifest.SourcePath, manifest.Entries[slices.IndexFunc(manifest.Entries, func(e TransactionEntry) bool { return e.Path == "update" })].CaptureName))
}

func TestForwardPublicationRefusesConcurrentTarget(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	transactionBeforeFilesystemHook = func(action, path string) {
		if action == "publish" && path == "link" {
			writeApplyFile(t, manifest.SourcePath, path, "other", 0o600)
		}
	}
	t.Cleanup(func() { transactionBeforeFilesystemHook = nil })
	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.Equal(t, "other", mustReadFile(t, filepath.Join(manifest.SourcePath, "link")))
	require.NotEqual(t, TransactionRollingBack, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestFinalValidationDetectsOpenDescriptorWriteBeforeCommit(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(e TransactionEntry) bool { return e.Path == "update" })]
	file, err := os.OpenFile(filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.StagePath)), os.O_WRONLY, 0)
	require.NoError(t, err)
	transactionBeforeFinalValidationHook = func() {
		_, err := file.WriteAt([]byte("xxxxx"), 0)
		require.NoError(t, err)
		require.NoError(t, file.Sync())
	}
	t.Cleanup(func() { transactionBeforeFinalValidationHook = nil; _ = file.Close() })
	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.NotEqual(t, TransactionRollingBack, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestOpenDescriptorWriteAfterDurableCommitIsExternal(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	entry := manifest.Entries[slices.IndexFunc(manifest.Entries, func(e TransactionEntry) bool { return e.Path == "update" })]
	file, err := os.OpenFile(filepath.Join(plan.TransactionDir, filepath.FromSlash(entry.StagePath)), os.O_WRONLY, 0)
	require.NoError(t, err)
	transactionAfterCommitHook = func() {
		_, err := file.WriteAt([]byte("xxxxx"), 0)
		require.NoError(t, err)
		require.NoError(t, file.Sync())
	}
	t.Cleanup(func() { transactionAfterCommitHook = nil; _ = file.Close() })
	require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.Equal(t, TransactionCommitted, readPersistedTransactionManifest(t, plan.TransactionDir).State)
	require.Equal(t, "xxxxx", mustReadFile(t, filepath.Join(manifest.SourcePath, "update")))
}

func TestCommittedManifestContainsAppliedTreeAndTime(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
	require.Equal(t, TransactionCommitted, persisted.State)
	require.NotEmpty(t, persisted.AppliedTree)
	require.NotEmpty(t, persisted.AppliedAt)
}

func TestCommittedIsTheOnlySuccessfulApplyCommitPoint(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	transactionBeforeCommitHook = func() error { return errors.New("stop before commit") }
	t.Cleanup(func() { transactionBeforeCommitHook = nil })
	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.NotEqual(t, TransactionRollingBack, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestFinalValidationRunsAfterBeforeCommitHook(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	transactionBeforeCommitHook = func() error {
		writeApplyFile(t, manifest.SourcePath, "update", "mutated by hook", 0o644)
		return nil
	}
	t.Cleanup(func() { transactionBeforeCommitHook = nil })

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.NotEqual(t, TransactionRollingBack, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestPreparedRevalidationFailureDurablyEntersRollingBack(t *testing.T) {
	workspace, _, _ := preparationPlan(t)
	writeApplyFile(t, workspace.WorkDir(), "update", "agent", 0o644)
	applyBeforeRevalidateHook = func() { writeApplyFile(t, workspace.data.SourcePath, "update", "concurrent", 0o644) }
	t.Cleanup(func() { applyBeforeRevalidateHook = nil })

	_, err := workspace.Apply()
	require.Error(t, err)
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(workspace.data.RepoPath), "transactions", "*", "manifest.json"))
	require.NoError(t, globErr)
	require.Len(t, matches, 1)
	manifest := readPersistedTransactionManifest(t, filepath.Dir(matches[0]))
	require.NotEqual(t, TransactionRollingBack, manifest.State)
}

func TestFinalValidationRejectsUnexpectedCreatedParentChild(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	transactionBeforeCommitHook = func() error {
		writeApplyFile(t, manifest.SourcePath, "new/unexpected", "unexpected", 0o600)
		return nil
	}
	t.Cleanup(func() { transactionBeforeCommitHook = nil })

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.NotEqual(t, TransactionRollingBack, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestCreatedParentPublishIntentWithoutIdentityIsAmbiguous(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	index := slices.IndexFunc(manifest.Entries, func(entry TransactionEntry) bool { return entry.Operation == EntryOperationCreatedParent })
	require.NotEqual(t, -1, index)
	manifest.State = TransactionApplying
	manifest.Entries[index].Phase = EntryPhasePublishIntent
	require.NoError(t, os.MkdirAll(filepath.Join(manifest.SourcePath, filepath.FromSlash(manifest.Entries[index].Path)), 0o755))
	require.NoError(t, WriteManifestDurable(manifest, plan.TransactionDir))

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.NotEqual(t, TransactionRollingBack, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestForwardExecutionDoesNotFollowSubstitutedSourceRoot(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	outside := t.TempDir()
	parked := manifest.SourcePath + ".parked"
	applyAfterBackupHook = func(string) {
		applyAfterBackupHook = nil
		require.NoError(t, os.Rename(manifest.SourcePath, parked))
		require.NoError(t, os.Symlink(outside, manifest.SourcePath))
	}
	t.Cleanup(func() { applyAfterBackupHook = nil })

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.Empty(t, mustSnapshot(t, outside).Entries)
}

func TestForwardExecutionDoesNotFollowSubstitutedTransactionDirectory(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	outside := t.TempDir()
	parked := plan.TransactionDir + ".parked"
	applyAfterBackupHook = func(string) {
		applyAfterBackupHook = nil
		require.NoError(t, os.Rename(plan.TransactionDir, parked))
		require.NoError(t, os.Symlink(outside, plan.TransactionDir))
	}
	t.Cleanup(func() { applyAfterBackupHook = nil })

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.NoFileExists(t, filepath.Join(outside, "manifest.json"))
}

func TestCommitWriteFailureBeforeRenameDurablyRollsBack(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	failed := false
	transactionManifestWriteHook = func(state TransactionState) error {
		if state == TransactionCommitted && !failed {
			failed = true
			return errors.New("injected commit write failure")
		}
		return nil
	}
	t.Cleanup(func() { transactionManifestWriteHook = nil })

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.NotEqual(t, TransactionRollingBack, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestCommitDirectorySyncFailureAfterRenameUsesDurableReadback(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	transactionAfterManifestRenameHook = func(state TransactionState) error {
		if state == TransactionCommitted {
			return errors.New("injected post-rename sync failure")
		}
		return nil
	}
	t.Cleanup(func() { transactionAfterManifestRenameHook = nil })

	require.NoError(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.Equal(t, TransactionCommitted, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestCreatedParentConcurrentCreatorIsNotClaimed(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	transactionBeforeFilesystemHook = func(action, path string) {
		if action == "create-parent" && path == "new" {
			require.NoError(t, os.Mkdir(filepath.Join(manifest.SourcePath, "new"), 0o755))
		}
	}
	t.Cleanup(func() { transactionBeforeFilesystemHook = nil })

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
	require.NotEqual(t, TransactionRollingBack, persisted.State)
	index := slices.IndexFunc(persisted.Entries, func(entry TransactionEntry) bool { return entry.Path == "new" })
	require.NotZero(t, persisted.Entries[index].CreatedDevice)
	require.NotZero(t, persisted.Entries[index].CreatedInode)
	require.DirExists(t, filepath.Join(manifest.SourcePath, "new"))
}

func TestFinalValidationRejectsUnexpectedChildInExistingAppliedDirectory(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	require.NoError(t, os.Mkdir(filepath.Join(manifest.SourcePath, "existing"), 0o755))
	manifest.AppliedEntries["existing"] = Entry{Path: "existing", Type: EntryType("directory"), Mode: 0o755}
	transactionBeforeCommitHook = func() error {
		writeApplyFile(t, manifest.SourcePath, "existing/unexpected", "unexpected", 0o600)
		return nil
	}
	t.Cleanup(func() { transactionBeforeCommitHook = nil })

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.NotEqual(t, TransactionRollingBack, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestFinalDirectoryScanRejectsReboundTopLevelChild(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	old := filepath.Join(manifest.SourcePath, "new.old")
	replacementScanAfterRootReadHook = func() {
		replacementScanAfterRootReadHook = nil
		require.NoError(t, os.Rename(filepath.Join(manifest.SourcePath, "new"), old))
		require.NoError(t, os.Mkdir(filepath.Join(manifest.SourcePath, "new"), 0o755))
	}
	t.Cleanup(func() { replacementScanAfterRootReadHook = nil })

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.NotEqual(t, TransactionRollingBack, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestCreatedParentReplacementBeforeOwnershipOpenIsNotClaimed(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	transactionAfterCreatedParentMkdirHook = func(path string) {
		if path != "new" {
			return
		}
		transactionAfterCreatedParentMkdirHook = nil
		require.NoError(t, os.Mkdir(filepath.Join(manifest.SourcePath, path), 0o755))
		writeApplyFile(t, manifest.SourcePath, path+"/external", "external", 0o600)
	}
	t.Cleanup(func() { transactionAfterCreatedParentMkdirHook = nil })

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
	require.NotEqual(t, TransactionRollingBack, persisted.State)
	index := slices.IndexFunc(persisted.Entries, func(entry TransactionEntry) bool { return entry.Path == "new" })
	require.NotZero(t, persisted.Entries[index].CreatedDevice)
	require.NotZero(t, persisted.Entries[index].CreatedInode)
	require.Equal(t, "external", mustReadFile(t, filepath.Join(manifest.SourcePath, "new/external")))
}

func TestCreatedParentReplacementAfterOwnershipOpenIsNotClaimed(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	transactionAfterCreatedParentOpenHook = func(path string) {
		if path != "new" {
			return
		}
		transactionAfterCreatedParentOpenHook = nil
		require.NoError(t, os.Mkdir(filepath.Join(manifest.SourcePath, path), 0o755))
	}
	t.Cleanup(func() { transactionAfterCreatedParentOpenHook = nil })

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
	require.NotEqual(t, TransactionRollingBack, persisted.State)
	index := slices.IndexFunc(persisted.Entries, func(entry TransactionEntry) bool { return entry.Path == "new" })
	require.NotZero(t, persisted.Entries[index].CreatedDevice)
	require.NotZero(t, persisted.Entries[index].CreatedInode)
	require.DirExists(t, filepath.Join(plan.TransactionDir, filepath.FromSlash(persisted.Entries[index].StagePath)))
	require.DirExists(t, filepath.Join(manifest.SourcePath, "new"))
}

func TestCommitUncertainReadbackRequiresExactCanonicalManifest(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	transactionAfterManifestRenameHook = func(state TransactionState) error {
		if state != TransactionCommitted {
			return nil
		}
		persisted := readPersistedTransactionManifest(t, plan.TransactionDir)
		persisted.AppliedTree = "forged"
		contents, err := json.Marshal(persisted)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(plan.TransactionDir, "manifest.json"), contents, 0o600))
		return errors.New("injected uncertain commit")
	}
	t.Cleanup(func() { transactionAfterManifestRenameHook = nil })

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.NotEqual(t, TransactionRollingBack, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}

func TestCommitUncertainReadbackRejectsUnknownManifestField(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	transactionAfterManifestRenameHook = func(state TransactionState) error {
		if state != TransactionCommitted {
			return nil
		}
		name := filepath.Join(plan.TransactionDir, "manifest.json")
		contents, err := os.ReadFile(name)
		require.NoError(t, err)
		contents = append(contents[:len(contents)-2], []byte(",\n  \"unknown\": true\n}")...)
		require.NoError(t, os.WriteFile(name, contents, 0o600))
		return errors.New("injected uncertain commit")
	}
	t.Cleanup(func() { transactionAfterManifestRenameHook = nil })

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
}

func TestPreparedSourceRootOpenFailureDurablyEntersRollingBack(t *testing.T) {
	_, plan, manifest := preparedForwardTransaction(t)
	require.NoError(t, os.Rename(manifest.SourcePath, manifest.SourcePath+".moved"))

	require.Error(t, ExecuteTransaction(&manifest, plan.TransactionDir))
	require.Equal(t, TransactionRollingBack, readPersistedTransactionManifest(t, plan.TransactionDir).State)
}
