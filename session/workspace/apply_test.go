package workspace

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

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
	original := applyFS
	applyFS = failingApplyOps{fileOps: original, failRenameTarget: filepath.Join(workspace.data.SourcePath, "c-create")}
	t.Cleanup(func() { applyFS = original })

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "injected apply failure")
	require.Equal(t, before, mustSnapshot(t, source))
	transactions, err := FindIncompleteTransactions(filepath.Dir(workspace.data.RepoPath))
	require.NoError(t, err)
	require.Empty(t, transactions)
}

func TestApplyRollbackFailureRetainsIncompleteTransaction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := t.TempDir()
	writeApplyFile(t, source, "a-update", "before", 0o644)
	workspace := newApplyWorkspace(t, source)
	writeApplyFile(t, workspace.WorkDir(), "a-update", "after", 0o644)
	writeApplyFile(t, workspace.WorkDir(), "b-create", "create", 0o644)
	original := applyFS
	applyFS = failingApplyOps{fileOps: original, failRenameTarget: filepath.Join(workspace.data.SourcePath, "b-create"), failRestorePath: filepath.Join(workspace.data.SourcePath, "a-update")}
	t.Cleanup(func() { applyFS = original })

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "injected apply failure")
	require.ErrorContains(t, err, "injected rollback failure")
	transactions, findErr := FindIncompleteTransactions(filepath.Dir(workspace.data.RepoPath))
	require.NoError(t, findErr)
	require.Len(t, transactions, 1)
	require.Equal(t, transactionApplying, transactions[0].State)
	require.DirExists(t, transactions[0].Directory)
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
	require.Equal(t, "late", mustReadFile(t, filepath.Join(source, "file")))
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
	original := applyFS
	applyFS = failingApplyOps{fileOps: original, failRenameTarget: filepath.Join(workspace.data.SourcePath, "zz-fail")}
	t.Cleanup(func() { applyFS = original })

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
	original := applyFS
	applyFS = failingApplyOps{fileOps: original, failRenameTarget: filepath.Join(workspace.data.SourcePath, "zz-fail")}
	t.Cleanup(func() { applyFS = original })

	_, err := workspace.Apply()
	require.ErrorContains(t, err, "injected apply failure")
	require.Equal(t, before, mustSnapshot(t, source))
	require.Equal(t, "before", mustReadFile(t, filepath.Join(source, "kind", "child")))
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
	require.ErrorContains(t, err, "cleanup committed transaction")
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
	require.ErrorContains(t, err, "cleanup committed transaction")
	transactions, findErr := FindIncompleteTransactions(filepath.Dir(workspace.data.RepoPath))
	require.NoError(t, findErr)
	require.Len(t, transactions, 1)
	require.Equal(t, transactionCommitted, transactions[0].State)
	require.Equal(t, workspace.data.SessionID, transactions[0].SessionID)
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
