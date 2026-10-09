package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	require.Equal(t, workspace.Capabilities{Apply: true, Resume: true}, instance.Capabilities())
	require.Equal(t, filepath.Base(source), instance.DisplayWorkspace())
}

func TestFromInstanceDataReadOnlyShadowDoesNotRecreateWorktreeOrTmux(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "file.txt"), []byte("content\n"), 0o644))
	ws, branch, err := workspace.NewShadow(source, "blocked-directory")
	require.NoError(t, err)
	require.NoError(t, ws.Setup())
	stored := ws.Serialize()
	transactionDir := filepath.Join(filepath.Dir(stored.Shadow.RepoPath), "transactions", "invalid")
	require.NoError(t, os.MkdirAll(transactionDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(transactionDir, "manifest.json"), []byte(`{"version":2,"unexpected":true}`), 0o600))
	require.NoError(t, os.RemoveAll(stored.Shadow.WorktreePath))

	instance, err := FromInstanceData(InstanceData{
		Title: "blocked-directory", Path: source, Branch: branch, Status: Running, Program: "false", Workspace: &stored,
	})
	require.NoError(t, err)
	require.Equal(t, Paused, instance.Status)
	require.True(t, instance.Started())
	require.False(t, instance.Capabilities().Apply)
	require.False(t, instance.Capabilities().Resume)
	require.NoDirExists(t, stored.Shadow.WorktreePath)
	require.Nil(t, instance.tmuxSession)
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

func TestStorageRejectsNullWorkspaceInsteadOfLegacyFallback(t *testing.T) {
	state := &memoryInstanceStorage{instances: json.RawMessage(`[{
		"title":"null-workspace","path":"/repo","branch":"feature","status":3,
		"workspace":null,
		"worktree":{"repo_path":"/repo","worktree_path":"/worktree","session_name":"legacy","branch_name":"feature","base_commit_sha":"base"}
	}]`)}
	storage, err := NewStorage(state)
	require.NoError(t, err)

	_, err = storage.LoadInstances()
	require.ErrorContains(t, err, "workspace field must not be null")
}

type memoryInstanceStorage struct {
	instances json.RawMessage
	saveErr   error
	saveHook  func()
	saves     int
}

func (s *memoryInstanceStorage) SaveInstances(instances json.RawMessage) error {
	s.saves++
	if s.saveHook != nil {
		s.saveHook()
	}
	if s.saveErr != nil {
		return s.saveErr
	}
	s.instances = instances
	return nil
}
func (s *memoryInstanceStorage) GetInstances() json.RawMessage { return s.instances }
func (s *memoryInstanceStorage) DeleteAllInstances() error {
	s.instances = nil
	return nil
}

func appliedDirectoryRecord(t *testing.T) (*memoryInstanceStorage, workspace.Data, workspace.Data, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "file.txt"), []byte("before\n"), 0o644))
	ws, branch, err := workspace.NewShadow(source, "repair-session")
	require.NoError(t, err)
	require.NoError(t, ws.Setup())
	require.NoError(t, os.WriteFile(filepath.Join(ws.WorkDir(), "file.txt"), []byte("after\n"), 0o644))
	result, err := ws.Apply()
	require.NoError(t, err)
	require.NotEmpty(t, result.TransactionID)
	authoritative := ws.Serialize()
	transactionDir := filepath.Join(filepath.Dir(authoritative.Shadow.RepoPath), "transactions", result.TransactionID)
	stored := authoritative
	shadowCopy := *authoritative.Shadow
	stored.Shadow = &shadowCopy
	stored.Shadow.LastAppliedTree = "stale-tree"
	stored.Shadow.LastAppliedAt = "stale-time"
	stored.Shadow.LastAppliedTransactionID = "stale-transaction"
	records, err := json.Marshal([]InstanceData{{
		Title: "repair-session", Path: source, Branch: branch, Status: Paused, Program: "false", Workspace: &stored,
	}})
	require.NoError(t, err)
	return &memoryInstanceStorage{instances: records}, stored, authoritative, transactionDir
}

func TestLoadInstancesRepairsLastAppliedMetadataFromWAL(t *testing.T) {
	state, _, authoritative, transactionDir := appliedDirectoryRecord(t)
	storage, err := NewStorage(state)
	require.NoError(t, err)

	instances, err := storage.LoadInstances()
	require.NoError(t, err)
	require.Len(t, instances, 1)
	repaired := instances[0].ToInstanceData().Workspace.Shadow
	require.NoDirExists(t, transactionDir, "matching complete receipt must be acknowledged after persistence")
	require.Equal(t, authoritative.Shadow.LastAppliedTree, repaired.LastAppliedTree)
	require.Equal(t, authoritative.Shadow.LastAppliedAt, repaired.LastAppliedAt)
	require.Equal(t, authoritative.Shadow.LastAppliedTransactionID, repaired.LastAppliedTransactionID)
}

func TestLoadInstancesSelectsNewestReceiptByAppliedAtThenTransactionID(t *testing.T) {
	state, stored, authoritative, firstTransactionDir := appliedDirectoryRecord(t)
	projectPath := filepath.Dir(stored.Shadow.RepoPath)
	first, err := workspace.ReadManifestStrict(projectPath, firstTransactionDir)
	require.NoError(t, err)
	first.TransactionID = "zzzz-older"
	first.AppliedTree = "older-tree"
	first.AppliedAt = "2026-01-01T00:00:00Z"
	olderDir := filepath.Join(projectPath, "transactions", first.TransactionID)
	require.NoError(t, os.Rename(firstTransactionDir, olderDir))
	require.NoError(t, workspace.WriteManifestDurable(first, olderDir))

	newer := first
	newer.TransactionID = "aaaa-newer"
	newer.AppliedTree = "newest-tree"
	newer.AppliedAt = "2026-01-02T00:00:00Z"
	newerDir := filepath.Join(projectPath, "transactions", newer.TransactionID)
	require.NoError(t, os.Mkdir(newerDir, 0o700))
	require.NoError(t, workspace.WriteManifestDurable(newer, newerDir))

	tied := newer
	tied.TransactionID = "bbbb-newest-tie"
	tied.AppliedTree = "tie-winner-tree"
	tiedDir := filepath.Join(projectPath, "transactions", tied.TransactionID)
	require.NoError(t, os.Mkdir(tiedDir, 0o700))
	require.NoError(t, workspace.WriteManifestDurable(tied, tiedDir))

	storage, err := NewStorage(state)
	require.NoError(t, err)
	instances, err := storage.LoadInstances()
	require.NoError(t, err)
	require.Len(t, instances, 1)
	got := instances[0].ToInstanceData().Workspace.Shadow
	require.Equal(t, "tie-winner-tree", got.LastAppliedTree)
	require.Equal(t, "2026-01-02T00:00:00Z", got.LastAppliedAt)
	require.Equal(t, "bbbb-newest-tie", got.LastAppliedTransactionID)
	require.NoDirExists(t, olderDir)
	require.NoDirExists(t, newerDir)
	require.NoDirExists(t, tiedDir)
	_ = authoritative
}

func TestLoadInstancesWALMetadataOverridesStaleSessionMetadata(t *testing.T) {
	state, _, authoritative, _ := appliedDirectoryRecord(t)
	storage, err := NewStorage(state)
	require.NoError(t, err)
	require.NotEmpty(t, state.instances)

	_, err = storage.LoadInstances()
	require.NoError(t, err)
	var saved []InstanceData
	require.NoError(t, json.Unmarshal(state.instances, &saved))
	require.Equal(t, authoritative.Shadow.LastAppliedTree, saved[0].Workspace.Shadow.LastAppliedTree)
	require.Equal(t, authoritative.Shadow.LastAppliedAt, saved[0].Workspace.Shadow.LastAppliedAt)
	require.Equal(t, authoritative.Shadow.LastAppliedTransactionID, saved[0].Workspace.Shadow.LastAppliedTransactionID)
}

func TestLoadInstancesPersistsRepairBeforeAcknowledgingTransaction(t *testing.T) {
	state, _, _, transactionDir := appliedDirectoryRecord(t)
	storage, err := NewStorage(state)
	require.NoError(t, err)

	_, err = storage.LoadInstances()
	require.NoError(t, err)
	require.Equal(t, 1, state.saves)
	require.NoDirExists(t, transactionDir)
}

func TestApplyMetadataSaveFailureLeavesCompleteWALForRecovery(t *testing.T) {
	state, _, _, transactionDir := appliedDirectoryRecord(t)
	state.saveErr = fmt.Errorf("save unavailable")
	storage, err := NewStorage(state)
	require.NoError(t, err)

	_, err = storage.LoadInstances()
	require.ErrorContains(t, err, "save unavailable")
	require.DirExists(t, transactionDir)
}

func TestLoadInstancesReportsOrphanCommittedTransaction(t *testing.T) {
	state, stored, authoritative, firstTransactionDir := appliedDirectoryRecord(t)
	firstWorkspace, err := workspace.RestoreShadow(authoritative)
	require.NoError(t, err)
	require.NoError(t, firstWorkspace.AcknowledgeApply(filepath.Base(firstTransactionDir)))
	orphan, _, err := workspace.NewShadow(stored.Shadow.SourcePath, "orphan-apply")
	require.NoError(t, err)
	require.NoError(t, orphan.Setup())
	require.NoError(t, os.WriteFile(filepath.Join(orphan.WorkDir(), "orphan.txt"), []byte("orphan\n"), 0o644))
	result, err := orphan.Apply()
	require.NoError(t, err)
	require.NotEmpty(t, result.TransactionID)
	transactionDir := filepath.Join(filepath.Dir(stored.Shadow.RepoPath), "transactions", result.TransactionID)

	storage, err := NewStorage(state)
	require.NoError(t, err)
	instances, err := storage.LoadInstances()
	require.NoError(t, err)
	require.Len(t, instances, 1)
	access := instances[0].AccessState()
	require.False(t, access.ReadOnly)
	require.Equal(t, workspace.RecoveryUnchanged, access.Disposition)
	require.Equal(t, "committed apply receipt has no stored session", access.Message)
	require.Empty(t, access.RetainedPaths)
	require.NoDirExists(t, transactionDir)
}

func TestLoadInstancesCleanupBlockedRepairsExactlyAndNeverAcknowledges(t *testing.T) {
	state, stored, _, transactionDir := appliedDirectoryRecord(t)
	projectPath := filepath.Dir(stored.Shadow.RepoPath)
	manifest, err := workspace.ReadManifestStrict(projectPath, transactionDir)
	require.NoError(t, err)
	manifest.State = workspace.TransactionCleanupBlocked
	manifest.AppliedTree = strings.Repeat("c", 40)
	manifest.AppliedAt = "2026-02-03T04:05:06.000000007Z"
	manifest.RecoveryIssues = []workspace.RecoveryIssue{{
		TransactionID: manifest.TransactionID,
		State:         workspace.TransactionCleanupBlocked,
		Message:       "cleanup remains blocked",
		Paths:         []string{"file.txt"},
		RetainedPaths: []string{transactionDir},
	}}
	require.NoError(t, workspace.WriteManifestDurable(manifest, transactionDir))

	storage, err := NewStorage(state)
	require.NoError(t, err)
	instances, err := storage.LoadInstances()
	require.NoError(t, err)
	require.Len(t, instances, 1)
	got := instances[0].ToInstanceData().Workspace.Shadow
	require.Equal(t, strings.Repeat("c", 40), got.LastAppliedTree)
	require.Equal(t, "2026-02-03T04:05:06.000000007Z", got.LastAppliedAt)
	require.Equal(t, manifest.TransactionID, got.LastAppliedTransactionID)
	access := instances[0].AccessState()
	require.Equal(t, workspace.AccessState{
		ReadOnly: true, Disposition: workspace.RecoveryUnchanged,
		Message: "cleanup remains blocked", Paths: []string{"file.txt"}, RetainedPaths: []string{transactionDir},
	}, access)
	require.False(t, instances[0].Capabilities().Apply)
	require.False(t, instances[0].Capabilities().Resume)
	require.DirExists(t, transactionDir, "cleanup-blocked receipt must never be acknowledged")
}

func TestLoadInstancesHoldsProjectLockThroughSavePublicationAndAcknowledgement(t *testing.T) {
	state, stored, _, transactionDir := appliedDirectoryRecord(t)
	projectPath := filepath.Dir(stored.Shadow.RepoPath)
	probes := 0
	probe := func() {
		lock, err := workspace.AcquireProjectLock(projectPath, "load contention probe")
		if lock != nil {
			_ = lock.Close()
		}
		var busy *workspace.OperationInProgressError
		require.ErrorAs(t, err, &busy)
		probes++
	}
	state.saveHook = probe
	loadInstancePublishedHook = probe
	t.Cleanup(func() { loadInstancePublishedHook = nil })

	storage, err := NewStorage(state)
	require.NoError(t, err)
	_, err = storage.LoadInstances()
	require.NoError(t, err)
	require.Equal(t, 2, probes)
	require.NoDirExists(t, transactionDir)
}

func TestLoadInstancesRestoresBlockedSessionsReadOnly(t *testing.T) {
	state, stored, _, _ := appliedDirectoryRecord(t)
	invalid := filepath.Join(filepath.Dir(stored.Shadow.RepoPath), "transactions", "invalid")
	require.NoError(t, os.MkdirAll(invalid, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(invalid, "manifest.json"), []byte(`{"version":2,"unexpected":true}`), 0o600))
	storage, err := NewStorage(state)
	require.NoError(t, err)

	instances, err := storage.LoadInstances()
	require.NoError(t, err)
	require.True(t, instances[0].AccessState().ReadOnly)
	require.False(t, instances[0].Capabilities().Apply)
	require.False(t, instances[0].Capabilities().Resume)
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
