package workspace

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanApplyCleanMergesAndLeavesSourceUntouched(t *testing.T) {
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
	require.Equal(t, before, mustSnapshot(t, source), "Task 6 Apply only plans")
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
