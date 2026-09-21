package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"claude-squad/session/workspace"
	"github.com/stretchr/testify/require"
)

func TestInstanceDataGitWorkspaceRoundTrip(t *testing.T) {
	stored := workspace.Data{
		Version: workspace.CurrentVersion,
		Mode:    workspace.ModeGit,
		Git: &workspace.GitData{
			RepoPath: "/repo", WorktreePath: "/worktree", SessionName: "git-session",
			BranchName: "feature", BaseCommitSHA: "abc123", IsExistingBranch: true,
		},
	}
	instance, err := FromInstanceData(InstanceData{
		Title: "git-session", Path: "/repo", Branch: "feature", Status: Paused, Workspace: &stored,
	})
	require.NoError(t, err)
	require.Equal(t, workspace.ModeGit, instance.WorkspaceMode())
	require.Equal(t, stored, *instance.ToInstanceData().Workspace)
	require.True(t, instance.Capabilities().Push)
}

func TestInstanceDataDirectoryWorkspaceRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "file.txt"), []byte("content\n"), 0o644))
	ws, branch, err := workspace.NewShadow(source, "directory-session")
	require.NoError(t, err)
	require.NoError(t, ws.Setup())
	t.Cleanup(func() { _ = ws.Cleanup() })

	stored := ws.Serialize()
	instance, err := FromInstanceData(InstanceData{
		Title: "directory-session", Path: source, Branch: branch, Status: Paused, Workspace: &stored,
	})
	require.NoError(t, err)
	require.Equal(t, workspace.ModeDirectory, instance.WorkspaceMode())
	require.Equal(t, stored, *instance.ToInstanceData().Workspace)
	require.Equal(t, workspace.Capabilities{Apply: true}, instance.Capabilities())
	require.Equal(t, filepath.Base(source), instance.DisplayWorkspace())
}

func TestPresentEmptyWorkspaceDoesNotFallBackToLegacyWorktree(t *testing.T) {
	record := `{
		"title":"malformed","path":"/repo","branch":"feature","status":3,
		"workspace":{},
		"worktree":{"repo_path":"/repo","worktree_path":"/worktree","session_name":"legacy","branch_name":"feature","base_commit_sha":"base"}
	}`
	var data InstanceData
	require.NoError(t, json.Unmarshal([]byte(record), &data))
	require.NotNil(t, data.Workspace)

	_, err := FromInstanceData(data)
	require.ErrorContains(t, err, `unsupported workspace mode ""`)
}

func TestStorageRejectsPresentMalformedWorkspaceInsteadOfLegacyFallback(t *testing.T) {
	state := &memoryInstanceStorage{instances: json.RawMessage(`[{
		"title":"malformed","path":"/repo","branch":"feature","status":3,
		"workspace":{},
		"worktree":{"repo_path":"/repo","worktree_path":"/worktree","session_name":"legacy","branch_name":"feature","base_commit_sha":"base"}
	}]`)}
	storage, err := NewStorage(state)
	require.NoError(t, err)

	_, err = storage.LoadInstances()
	require.ErrorContains(t, err, `unsupported workspace mode ""`)
}

type memoryInstanceStorage struct {
	instances json.RawMessage
}

func (s *memoryInstanceStorage) SaveInstances(instances json.RawMessage) error {
	s.instances = instances
	return nil
}
func (s *memoryInstanceStorage) GetInstances() json.RawMessage { return s.instances }
func (s *memoryInstanceStorage) DeleteAllInstances() error {
	s.instances = nil
	return nil
}

func TestLegacyWorktreeRecordRestoresAsGitWorkspace(t *testing.T) {
	legacy := `{
		"title":"legacy","path":"/repo","branch":"feature","status":3,
		"worktree":{"repo_path":"/repo","worktree_path":"/worktree","session_name":"legacy","branch_name":"feature","base_commit_sha":"base","is_existing_branch":true}
	}`
	var data InstanceData
	require.NoError(t, json.Unmarshal([]byte(legacy), &data))

	instance, err := FromInstanceData(data)
	require.NoError(t, err)
	require.Equal(t, workspace.ModeGit, instance.WorkspaceMode())
	saved := instance.ToInstanceData()
	require.Nil(t, saved.Worktree)
	require.Equal(t, workspace.Data{
		Version: workspace.CurrentVersion,
		Mode:    workspace.ModeGit,
		Git: &workspace.GitData{
			RepoPath: "/repo", WorktreePath: "/worktree", SessionName: "legacy",
			BranchName: "feature", BaseCommitSHA: "base", IsExistingBranch: true,
		},
	}, *saved.Workspace)

	encoded, err := json.Marshal(saved)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"worktree"`)
}
