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
	Changed []string
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

// ShadowData is populated by the directory workspace implementation.
type ShadowData struct{}

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
