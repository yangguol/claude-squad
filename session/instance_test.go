package session

import (
	"claude-squad/cmd/cmd_test"
	"claude-squad/log"
	"claude-squad/session/tmux"
	"claude-squad/session/workspace"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	log.Initialize(false)
	defer log.Close()
	os.Exit(m.Run())
}

// nullPtyFactory hands back a throwaway file instead of a real PTY.
type nullPtyFactory struct {
	t     *testing.T
	calls int
}

func (p *nullPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	p.calls++
	return os.OpenFile(filepath.Join(p.t.TempDir(), "pty"), os.O_CREATE|os.O_RDWR, 0644)
}

func (p *nullPtyFactory) Close() {}

// When the tmux server dies between runs, every session goes with it while the worktree
// and branch survive on disk. Restoring such an instance must park it as Paused so the
// user can resume it. Returning an error instead is not an option: LoadInstances aborts on
// the first failure, so one dead session would hide every other instance.
// See https://github.com/smtg-ai/claude-squad/issues/216.
func TestStartPausesInstanceWhenTmuxSessionNoLongerExists(t *testing.T) {
	ptyFactory := &nullPtyFactory{t: t}
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") {
				return fmt.Errorf("can't find session")
			}
			return nil
		},
	}

	instance, err := NewInstance(InstanceOptions{Title: "revived", Path: t.TempDir(), Program: "claude"})
	require.NoError(t, err)
	instance.SetTmuxSession(tmux.NewTmuxSessionWithDeps("revived", "claude", ptyFactory, cmdExec))

	require.NoError(t, instance.Start(false), "a dead tmux session is recoverable, not a startup failure")
	require.Equal(t, Paused, instance.Status)
	require.True(t, instance.Started())
	require.Zero(t, ptyFactory.calls, "should not attach to a session that does not exist")
}

// The happy path is unchanged: an instance whose session survived comes back Running.
func TestStartRestoresInstanceWhenTmuxSessionSurvives(t *testing.T) {
	ptyFactory := &nullPtyFactory{t: t}
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error { return nil },
	}

	instance, err := NewInstance(InstanceOptions{Title: "alive", Path: t.TempDir(), Program: "claude"})
	require.NoError(t, err)
	instance.SetTmuxSession(tmux.NewTmuxSessionWithDeps("alive", "claude", ptyFactory, cmdExec))

	require.NoError(t, instance.Start(false))
	require.Equal(t, Running, instance.Status)
	require.Equal(t, 1, ptyFactory.calls)
}

func TestFactoryCreatedGitInstanceRepoNameAndDisplay(t *testing.T) {
	instance, repo := startFactoryGitInstance(t, "factory-display")

	repoName, err := instance.RepoName()
	require.NoError(t, err)
	require.Equal(t, filepath.Base(repo), repoName)
	require.Equal(t, filepath.Base(repo), instance.sessionWorkspace.DisplayName())
	require.NotNil(t, instance.gitWorktree)
	require.Equal(t, instance.sessionWorkspace.WorkDir(), instance.GetWorktreePath())
}

func TestFactoryCreatedGitInstanceSerialization(t *testing.T) {
	instance, _ := startFactoryGitInstance(t, "factory-serialization")

	data := instance.ToInstanceData()
	serialized := instance.sessionWorkspace.Serialize().Git
	require.NotNil(t, serialized)
	require.Equal(t, serialized.RepoPath, data.Worktree.RepoPath)
	require.Equal(t, serialized.WorktreePath, data.Worktree.WorktreePath)
	require.Equal(t, serialized.BranchName, data.Worktree.BranchName)
	require.Equal(t, serialized.BaseCommitSHA, data.Worktree.BaseCommitSHA)
}

func TestFactoryCreatedGitInstanceComputeDiff(t *testing.T) {
	instance, _ := startFactoryGitInstance(t, "factory-diff")

	require.NoError(t, os.WriteFile(filepath.Join(instance.GetWorktreePath(), "change.txt"), []byte("change\n"), 0o644))
	diff := instance.ComputeDiff()
	require.NoError(t, diff.Error)
	require.Contains(t, diff.Content, "change.txt")
}

func TestFactoryCreatedGitInstancePause(t *testing.T) {
	instance, _ := startFactoryGitInstance(t, "factory-pause")
	worktreePath := instance.GetWorktreePath()

	require.NoError(t, instance.Pause())
	require.Equal(t, Paused, instance.Status)
	require.NoDirExists(t, worktreePath)
}

func TestFactoryCreatedGitInstanceKillCleansWorkspaceExactlyOnce(t *testing.T) {
	instance, _ := startFactoryGitInstance(t, "factory-cleanup")
	counter := &cleanupCountingWorkspace{Workspace: instance.sessionWorkspace}
	instance.sessionWorkspace = counter
	worktreePath := instance.GetWorktreePath()

	require.NoError(t, instance.Kill())
	require.Equal(t, 1, counter.cleanupCalls)
	require.NoDirExists(t, worktreePath)
}

type cleanupCountingWorkspace struct {
	workspace.Workspace
	cleanupCalls int
}

func (w *cleanupCountingWorkspace) Cleanup() error {
	w.cleanupCalls++
	return w.Workspace.Cleanup()
}

func startFactoryGitInstance(t *testing.T, title string) (*Instance, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo := initSessionGitRepo(t)
	ptyFactory := &nullPtyFactory{t: t}
	hasSessionCalls := 0
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") {
				hasSessionCalls++
				if hasSessionCalls == 1 {
					return fmt.Errorf("session not found")
				}
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return nil, nil },
	}
	instance, err := NewInstance(InstanceOptions{Title: title, Path: repo, Program: "claude"})
	require.NoError(t, err)
	instance.SetTmuxSession(tmux.NewTmuxSessionWithDeps(title, "claude", ptyFactory, cmdExec))
	require.NoError(t, instance.Start(true))
	t.Cleanup(func() {
		if _, err := os.Stat(instance.GetWorktreePath()); err == nil {
			_ = instance.Kill()
		}
	})
	return instance, repo
}

func initSessionGitRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	runSessionGit(t, repo, "init")
	runSessionGit(t, repo, "config", "user.name", "Test User")
	runSessionGit(t, repo, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644))
	runSessionGit(t, repo, "add", "README.md")
	runSessionGit(t, repo, "commit", "-m", "initial")
	return repo
}

func runSessionGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
}
