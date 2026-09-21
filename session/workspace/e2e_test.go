package workspace_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"claude-squad/cmd/cmd_test"
	"claude-squad/session"
	"claude-squad/session/tmux"
	"claude-squad/session/workspace"

	"github.com/stretchr/testify/require"
)

func TestDirectoryWorkspaceEndToEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := filepath.Join(t.TempDir(), "plain-project")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "shared.txt"), []byte("base\n"), 0o644))
	require.NoDirExists(t, filepath.Join(source, ".git"))

	first := startDirectoryInstance(t, source, "directory-one")
	second := startDirectoryInstance(t, source, "directory-two")
	require.Equal(t, workspace.ModeDirectory, first.WorkspaceMode())
	require.Equal(t, workspace.ModeDirectory, second.WorkspaceMode())
	require.NotEqual(t, first.WorkDir(), second.WorkDir())

	require.NoError(t, os.WriteFile(filepath.Join(first.WorkDir(), "shared.txt"), []byte("first\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(second.WorkDir(), "shared.txt"), []byte("second\n"), 0o644))
	require.Equal(t, "base\n", readE2EFile(t, filepath.Join(source, "shared.txt")))
	require.Equal(t, "first\n", readE2EFile(t, filepath.Join(first.WorkDir(), "shared.txt")))
	require.Equal(t, "second\n", readE2EFile(t, filepath.Join(second.WorkDir(), "shared.txt")))

	firstDiff := first.ComputeDiff()
	require.NoError(t, firstDiff.Error)
	require.Contains(t, firstDiff.Content, "+first")
	require.NotContains(t, firstDiff.Content, "+second")
	secondDiff := second.ComputeDiff()
	require.NoError(t, secondDiff.Error)
	require.Contains(t, secondDiff.Content, "+second")
	require.NotContains(t, secondDiff.Content, "+first")

	result, err := first.Apply()
	require.NoError(t, err)
	require.Equal(t, []string{"shared.txt"}, result.Changed)
	require.Equal(t, "first\n", readE2EFile(t, filepath.Join(source, "shared.txt")))

	beforeConflict := readE2EFile(t, filepath.Join(source, "shared.txt"))
	_, err = second.Apply()
	var conflict *workspace.ConflictError
	require.True(t, errors.As(err, &conflict), "expected ConflictError, got %v", err)
	require.Equal(t, []string{"shared.txt"}, conflict.Paths)
	require.Equal(t, beforeConflict, readE2EFile(t, filepath.Join(source, "shared.txt")), "a conflicting apply must not mutate the source")
	require.NoDirExists(t, filepath.Join(source, ".git"))
}

func startDirectoryInstance(t *testing.T, source, title string) *session.Instance {
	t.Helper()
	hasSessionCalls := 0
	commandExecutor := cmd_test.MockCmdExec{
		RunFunc: func(command *exec.Cmd) error {
			if strings.Contains(command.String(), "has-session") {
				hasSessionCalls++
				if hasSessionCalls == 1 {
					return fmt.Errorf("session not found")
				}
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	instance, err := session.NewInstance(session.InstanceOptions{Title: title, Path: source, Program: "claude"})
	require.NoError(t, err)
	instance.SetTmuxSession(tmux.NewTmuxSessionWithDeps(title, "claude", &e2ePtyFactory{directory: t.TempDir()}, commandExecutor))
	require.NoError(t, instance.Start(true))
	t.Cleanup(func() { require.NoError(t, instance.Kill()) })
	return instance
}

type e2ePtyFactory struct {
	directory string
	count     int
}

func (factory *e2ePtyFactory) Start(*exec.Cmd) (*os.File, error) {
	factory.count++
	return os.OpenFile(filepath.Join(factory.directory, fmt.Sprintf("pty-%d", factory.count)), os.O_CREATE|os.O_RDWR, 0o600)
}

func (*e2ePtyFactory) Close() {}

func readE2EFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(contents)
}
