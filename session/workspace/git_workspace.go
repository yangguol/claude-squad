package workspace

import (
	"fmt"

	"claude-squad/session/git"
)

type gitWorkspace struct {
	worktree *git.GitWorktree
}

func NewGit(path, sessionName, branch string) (Workspace, string, error) {
	if branch == "" {
		worktree, branchName, err := git.NewGitWorktree(path, sessionName)
		if err != nil {
			return nil, "", err
		}
		return &gitWorkspace{worktree: worktree}, branchName, nil
	}

	worktree, err := git.NewGitWorktreeFromBranch(path, branch, sessionName)
	if err != nil {
		return nil, "", err
	}
	return &gitWorkspace{worktree: worktree}, branch, nil
}

func RestoreGit(data Data) (Workspace, error) {
	if data.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported workspace data version %d", data.Version)
	}
	if data.Mode != ModeGit {
		return nil, fmt.Errorf("cannot restore git workspace from mode %q", data.Mode)
	}
	if data.Git == nil {
		return nil, fmt.Errorf("git workspace data is missing")
	}
	if err := validateGitData(*data.Git); err != nil {
		return nil, err
	}

	stored := data.Git
	worktree := git.NewGitWorktreeFromStorage(
		stored.RepoPath,
		stored.WorktreePath,
		stored.SessionName,
		stored.BranchName,
		stored.BaseCommitSHA,
		stored.IsExistingBranch,
	)
	return &gitWorkspace{worktree: worktree}, nil
}

func validateGitData(data GitData) error {
	fields := []struct {
		name  string
		value string
	}{
		{name: "repo path", value: data.RepoPath},
		{name: "worktree path", value: data.WorktreePath},
		{name: "session name", value: data.SessionName},
		{name: "branch name", value: data.BranchName},
	}
	for _, field := range fields {
		if field.value == "" {
			return fmt.Errorf("git workspace %s is missing", field.name)
		}
	}
	return nil
}

func (w *gitWorkspace) Mode() Mode { return ModeGit }

func (w *gitWorkspace) Capabilities() Capabilities {
	return Capabilities{BranchPicker: true, Push: true, Apply: false}
}

func (w *gitWorkspace) WorkDir() string { return w.worktree.GetWorktreePath() }

func (w *gitWorkspace) DisplayName() string { return w.worktree.GetRepoName() }

func (w *gitWorkspace) BranchName() string { return w.worktree.GetBranchName() }

func (w *gitWorkspace) Setup() error { return w.worktree.Setup() }

func (w *gitWorkspace) IsValid() (bool, error) { return w.worktree.IsValidWorktree() }

func (w *gitWorkspace) IsDirty() (bool, error) { return w.worktree.IsDirty() }

func (w *gitWorkspace) IsBranchCheckedOut() (bool, error) {
	return w.worktree.IsBranchCheckedOut()
}

func (w *gitWorkspace) Diff() *git.DiffStats { return w.worktree.Diff() }

func (w *gitWorkspace) DiffNumstat() *git.DiffStats { return w.worktree.DiffNumstat() }

func (w *gitWorkspace) CommitChanges(message string) error {
	return w.worktree.CommitChanges(message)
}

func (w *gitWorkspace) PushChanges(message string, open bool) error {
	return w.worktree.PushChanges(message, open)
}

func (w *gitWorkspace) Apply() (*ApplyResult, error) {
	return nil, &ErrUnsupported{Operation: "apply"}
}

func (w *gitWorkspace) Remove() error { return w.worktree.Remove() }

func (w *gitWorkspace) Prune() error { return w.worktree.Prune() }

func (w *gitWorkspace) Cleanup() error { return w.worktree.Cleanup() }

func (w *gitWorkspace) Serialize() Data {
	stored := w.worktree.Data()
	return Data{
		Version: CurrentVersion,
		Mode:    ModeGit,
		Git: &GitData{
			RepoPath:         stored.RepoPath,
			WorktreePath:     stored.WorktreePath,
			SessionName:      stored.SessionName,
			BranchName:       stored.BranchName,
			BaseCommitSHA:    stored.BaseCommitSHA,
			IsExistingBranch: stored.IsExistingBranch,
		},
	}
}

var _ Workspace = (*gitWorkspace)(nil)
