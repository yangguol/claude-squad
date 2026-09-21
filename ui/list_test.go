package ui

import (
	"claude-squad/session"
	"claude-squad/session/workspace"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/require"
)

func newTestList(titles ...string) *List {
	s := spinner.New()
	l := NewList(&s, false)
	for _, t := range titles {
		inst, _ := session.NewInstance(session.InstanceOptions{
			Title:   t,
			Path:    ".",
			Program: "echo",
		})
		l.AddInstance(inst)
	}
	return l
}

func TestMoveUp(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(1) // select "b"

	moved := l.MoveUp()
	require.True(t, moved)
	require.Equal(t, 0, l.selectedIdx)
	require.Equal(t, "b", l.items[0].Title)
	require.Equal(t, "a", l.items[1].Title)
	require.Equal(t, "c", l.items[2].Title)
}

func TestMoveUp_AtTop(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(0)

	moved := l.MoveUp()
	require.False(t, moved)
	require.Equal(t, 0, l.selectedIdx)
	require.Equal(t, "a", l.items[0].Title)
}

func TestMoveDown(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(1) // select "b"

	moved := l.MoveDown()
	require.True(t, moved)
	require.Equal(t, 2, l.selectedIdx)
	require.Equal(t, "a", l.items[0].Title)
	require.Equal(t, "c", l.items[1].Title)
	require.Equal(t, "b", l.items[2].Title)
}

func TestMoveDown_AtBottom(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(2)

	moved := l.MoveDown()
	require.False(t, moved)
	require.Equal(t, 2, l.selectedIdx)
	require.Equal(t, "c", l.items[2].Title)
}

func TestMoveWithSingleItem(t *testing.T) {
	l := newTestList("only")
	l.SetSelectedInstance(0)

	require.False(t, l.MoveUp())
	require.False(t, l.MoveDown())
}

func TestDirectoryInstanceRendersModeAndSourceName(t *testing.T) {
	instance := testInstanceWithWorkspace(t, workspace.ModeDirectory, workspace.Capabilities{Apply: true}, "source-project", "internal-shadow-branch")
	instance.SetStatus(session.Paused)
	renderer := &InstanceRenderer{spinner: func() *spinner.Model { s := spinner.New(); return &s }(), width: 80}

	rendered := renderer.Render(instance, 1, true, false)
	require.Contains(t, rendered, "Directory (isolated)")
	require.Contains(t, rendered, "source-project")
	require.NotContains(t, rendered, "internal-shadow-branch")
	require.NotContains(t, rendered, "/internal/worktree")
}

func TestMixedWorkspaceInstancesRenderTheirOwnMode(t *testing.T) {
	s := spinner.New()
	l := NewList(&s, false)
	l.SetSize(100, 20)
	gitInstance := testInstanceWithWorkspace(t, workspace.ModeGit, workspace.Capabilities{BranchPicker: true, Push: true}, "repo", "feature")
	directoryInstance := testInstanceWithWorkspace(t, workspace.ModeDirectory, workspace.Capabilities{Apply: true}, "source-project", "internal-shadow-branch")
	gitInstance.SetStatus(session.Paused)
	directoryInstance.SetStatus(session.Paused)
	l.AddInstance(gitInstance)
	l.AddInstance(directoryInstance)

	rendered := strings.ReplaceAll(l.String(), "\x1b", "")
	require.Contains(t, rendered, "Git")
	require.Contains(t, rendered, "feature")
	require.Contains(t, rendered, "Directory (isolated)")
	require.Contains(t, rendered, "source-project")
	require.NotContains(t, rendered, "internal-shadow-branch")
}
