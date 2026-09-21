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
