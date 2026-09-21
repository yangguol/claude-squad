package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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
	require.Equal(t, filepath.Join(home, ".claude-squad", "shadow-projects", wantID, "repo"), data.RepoPath)
	require.Equal(t, filepath.Join(home, ".claude-squad", "shadow-projects", wantID, "worktrees", data.SessionID), data.WorktreePath)
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
	require.Equal(t, Capabilities{BranchPicker: false, Push: false, Apply: true}, ws.Capabilities())
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
	_, err = ws.Apply()
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "apply", unsupported.Operation)

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
	project := filepath.Join(home, ".claude-squad", "shadow-projects", id)
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

func mustReadFile(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(name)
	require.NoError(t, err)
	return string(contents)
}
