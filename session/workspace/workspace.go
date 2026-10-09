// Package workspace defines isolated session workspace contracts.
package workspace

import (
	"fmt"
	"strings"

	"claude-squad/session/git"
)

const CurrentVersion = 1

type Mode string

const (
	ModeGit       Mode = "git"
	ModeDirectory Mode = "directory"
)

type Capabilities struct {
	BranchPicker bool
	Push         bool
	Apply        bool
}

type ApplyResult struct {
	Changed       []string
	TransactionID string
	Warning       *CleanupBlockedWarning
}

type ConflictError struct {
	Paths []string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("workspace conflicts: %s", strings.Join(e.Paths, ", "))
}

type ErrUnsupported struct {
	Operation string
}

func (e *ErrUnsupported) Error() string {
	if e.Operation == "" {
		return "workspace operation is unsupported"
	}
	return fmt.Sprintf("workspace operation %q is unsupported", e.Operation)
}

type GitData struct {
	RepoPath         string `json:"repo_path"`
	WorktreePath     string `json:"worktree_path"`
	SessionName      string `json:"session_name"`
	BranchName       string `json:"branch_name"`
	BaseCommitSHA    string `json:"base_commit_sha"`
	IsExistingBranch bool   `json:"is_existing_branch"`
}

type PolicyRuleData struct {
	Pattern string     `json:"pattern"`
	Class   EntryClass `json:"class"`
	Negate  bool       `json:"negate,omitempty"`
}

type ShadowData struct {
	ProjectID       string           `json:"project_id"`
	SourcePath      string           `json:"source_path"`
	RepoPath        string           `json:"repo_path"`
	WorktreePath    string           `json:"worktree_path"`
	SessionID       string           `json:"session_id"`
	SessionName     string           `json:"session_name"`
	BranchName      string           `json:"branch_name"`
	BaselineCommit  string           `json:"baseline_commit"`
	PolicySnapshot  []PolicyRuleData `json:"policy_snapshot"`
	SharedLinks     []string         `json:"shared_links,omitempty"`
	LastAppliedTree string           `json:"last_applied_tree,omitempty"`
	LastAppliedAt   string           `json:"last_applied_at,omitempty"`
}

type Data struct {
	Version int         `json:"version"`
	Mode    Mode        `json:"mode"`
	Git     *GitData    `json:"git,omitempty"`
	Shadow  *ShadowData `json:"shadow,omitempty"`
}

type Workspace interface {
	Mode() Mode
	Capabilities() Capabilities
	WorkDir() string
	DisplayName() string
	BranchName() string
	Setup() error
	IsValid() (bool, error)
	IsDirty() (bool, error)
	IsBranchCheckedOut() (bool, error)
	Diff() *git.DiffStats
	DiffNumstat() *git.DiffStats
	CommitChanges(string) error
	PushChanges(string, bool) error
	Apply() (*ApplyResult, error)
	Remove() error
	Prune() error
	Cleanup() error
	Serialize() Data
}
