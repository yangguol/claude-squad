package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitWorkspaceCapabilities(t *testing.T) {
	repo := initGitRepo(t)
	ws, branch, err := NewGit(repo, "feature", "")
	require.NoError(t, err)
	require.NotEmpty(t, branch)
	require.Equal(t, ModeGit, ws.Mode())
	require.Equal(t, Capabilities{BranchPicker: true, Push: true, Apply: false}, ws.Capabilities())
	require.Equal(t, filepath.Base(repo), ws.DisplayName())
	require.Equal(t, branch, ws.BranchName())
	require.Equal(t, ws.WorkDir(), ws.Serialize().Git.WorktreePath)
	var unsupported *ErrUnsupported
	require.ErrorAs(t, mustApply(t, ws), &unsupported)
	require.Equal(t, "apply", unsupported.Operation)
}

func TestGitWorkspaceExistingBranch(t *testing.T) {
	repo := initGitRepo(t)
	mustRunGit(t, repo, "branch", "existing")

	ws, branch, err := NewGit(repo, "feature", "existing")
	require.NoError(t, err)
	require.Equal(t, "existing", branch)
	require.Equal(t, "existing", ws.BranchName())
	require.True(t, ws.Serialize().Git.IsExistingBranch)
}

func TestGitWorkspaceSerializationRoundTrip(t *testing.T) {
	repo := initGitRepo(t)
	ws, _, err := NewGit(repo, "feature", "")
	require.NoError(t, err)

	data := ws.Serialize()
	restored, err := RestoreGit(data)
	require.NoError(t, err)
	require.Equal(t, data, restored.Serialize())
	require.Equal(t, ws.WorkDir(), restored.WorkDir())
	require.Equal(t, ws.DisplayName(), restored.DisplayName())
	require.Equal(t, ws.BranchName(), restored.BranchName())
}

func TestGitWorkspaceRejectsMalformedData(t *testing.T) {
	validGit := GitData{
		RepoPath:      "/repo",
		WorktreePath:  "/worktree",
		SessionName:   "feature",
		BranchName:    "cs-feature",
		BaseCommitSHA: "abc123",
	}

	tests := []struct {
		name string
		data Data
	}{
		{name: "unsupported version", data: Data{Version: CurrentVersion + 1, Mode: ModeGit, Git: &validGit}},
		{name: "wrong mode", data: Data{Version: CurrentVersion, Mode: ModeDirectory, Git: &validGit}},
		{name: "missing git data", data: Data{Version: CurrentVersion, Mode: ModeGit}},
		{name: "missing repo path", data: Data{Version: CurrentVersion, Mode: ModeGit, Git: &GitData{WorktreePath: "/worktree", SessionName: "feature", BranchName: "cs-feature", BaseCommitSHA: "abc123"}}},
		{name: "missing worktree path", data: Data{Version: CurrentVersion, Mode: ModeGit, Git: &GitData{RepoPath: "/repo", SessionName: "feature", BranchName: "cs-feature", BaseCommitSHA: "abc123"}}},
		{name: "missing session name", data: Data{Version: CurrentVersion, Mode: ModeGit, Git: &GitData{RepoPath: "/repo", WorktreePath: "/worktree", BranchName: "cs-feature", BaseCommitSHA: "abc123"}}},
		{name: "missing branch name", data: Data{Version: CurrentVersion, Mode: ModeGit, Git: &GitData{RepoPath: "/repo", WorktreePath: "/worktree", SessionName: "feature", BaseCommitSHA: "abc123"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := RestoreGit(tt.data)
			require.Error(t, err)
		})
	}
}

func TestConflictError(t *testing.T) {
	err := &ConflictError{Paths: []string{"b.txt", "a.txt"}}
	require.Equal(t, "workspace conflicts: b.txt, a.txt", err.Error())
}

func mustApply(t *testing.T, ws Workspace) error {
	t.Helper()
	_, err := ws.Apply()
	return err
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	mustRunGit(t, repo, "init")
	mustRunGit(t, repo, "config", "user.name", "Test User")
	mustRunGit(t, repo, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644))
	mustRunGit(t, repo, "add", "README.md")
	mustRunGit(t, repo, "commit", "-m", "initial")
	return repo
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
}
