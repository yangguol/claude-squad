package ui

import (
	"claude-squad/keys"
	"claude-squad/session"
	"claude-squad/session/git"
	"claude-squad/session/workspace"
	"testing"

	"github.com/stretchr/testify/require"
)

type capabilityWorkspace struct {
	mode         workspace.Mode
	capabilities workspace.Capabilities
	displayName  string
	branchName   string
}

func (w *capabilityWorkspace) Mode() workspace.Mode                 { return w.mode }
func (w *capabilityWorkspace) Capabilities() workspace.Capabilities { return w.capabilities }
func (w *capabilityWorkspace) AccessState() workspace.AccessState   { return workspace.AccessState{} }
func (w *capabilityWorkspace) WorkDir() string                      { return "/internal/worktree" }
func (w *capabilityWorkspace) DisplayName() string                  { return w.displayName }
func (w *capabilityWorkspace) BranchName() string                   { return w.branchName }
func (w *capabilityWorkspace) Setup() error                         { return nil }
func (w *capabilityWorkspace) IsValid() (bool, error)               { return true, nil }
func (w *capabilityWorkspace) IsDirty() (bool, error)               { return false, nil }
func (w *capabilityWorkspace) IsBranchCheckedOut() (bool, error)    { return false, nil }
func (w *capabilityWorkspace) Diff() *git.DiffStats                 { return &git.DiffStats{} }
func (w *capabilityWorkspace) DiffNumstat() *git.DiffStats          { return &git.DiffStats{} }
func (w *capabilityWorkspace) CommitChanges(string) error           { return nil }
func (w *capabilityWorkspace) PushChanges(string, bool) error       { return nil }
func (w *capabilityWorkspace) Apply() (*workspace.ApplyResult, error) {
	return &workspace.ApplyResult{}, nil
}
func (w *capabilityWorkspace) AcknowledgeApply(string) error { return nil }
func (w *capabilityWorkspace) Remove() error                 { return nil }
func (w *capabilityWorkspace) Prune() error                  { return nil }
func (w *capabilityWorkspace) Cleanup() error                { return nil }
func (w *capabilityWorkspace) Serialize() workspace.Data     { return workspace.Data{Mode: w.mode} }

func testInstanceWithWorkspace(t *testing.T, mode workspace.Mode, capabilities workspace.Capabilities, displayName, branchName string) *session.Instance {
	t.Helper()
	instance, err := session.NewInstance(session.InstanceOptions{
		Title:     "test",
		Path:      t.TempDir(),
		Program:   "echo",
		Workspace: &capabilityWorkspace{mode: mode, capabilities: capabilities, displayName: displayName, branchName: branchName},
	})
	require.NoError(t, err)
	instance.Branch = branchName
	return instance
}

func TestMenuGitActionsRemainAvailable(t *testing.T) {
	instance := testInstanceWithWorkspace(t, workspace.ModeGit, workspace.Capabilities{BranchPicker: true, Push: true}, "repo", "feature")
	menu := NewMenu()
	menu.SetInstance(instance)

	require.Contains(t, menu.options, keys.KeySubmit)
	require.Contains(t, menu.options, keys.KeyCheckout)
	require.NotContains(t, menu.options, keys.KeyApply)
	require.Contains(t, menu.String(), "push branch")
	require.Contains(t, menu.String(), "checkout")
}

func TestMenuDirectoryActionsFollowCapabilities(t *testing.T) {
	instance := testInstanceWithWorkspace(t, workspace.ModeDirectory, workspace.Capabilities{Apply: true}, "project", "internal-shadow-branch")
	menu := NewMenu()
	menu.SetInstance(instance)

	require.NotContains(t, menu.options, keys.KeySubmit)
	require.NotContains(t, menu.options, keys.KeyCheckout)
	require.Contains(t, menu.options, keys.KeyApply)
	rendered := menu.String()
	require.NotContains(t, rendered, "push branch")
	require.Contains(t, rendered, "c apply")
}

func TestMenuBlockedDirectoryOmitsCheckout(t *testing.T) {
	instance := testInstanceWithWorkspace(t, workspace.ModeDirectory, workspace.Capabilities{}, "project", "internal-shadow-branch")
	menu := NewMenu()
	menu.SetInstance(instance)

	require.NotContains(t, menu.options, keys.KeyCheckout)
	require.NotContains(t, menu.String(), "checkout")
}
