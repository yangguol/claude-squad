package session

import (
	"claude-squad/cmd/cmd_test"
	"claude-squad/log"
	"claude-squad/session/git"
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

	instance, err := NewInstance(InstanceOptions{Title: "revived", Path: t.TempDir(), Program: "claude", Workspace: &fakeWorkspace{mode: workspace.ModeGit}})
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

	instance, err := NewInstance(InstanceOptions{Title: "alive", Path: t.TempDir(), Program: "claude", Workspace: &fakeWorkspace{mode: workspace.ModeGit}})
	require.NoError(t, err)
	instance.SetTmuxSession(tmux.NewTmuxSessionWithDeps("alive", "claude", ptyFactory, cmdExec))

	require.NoError(t, instance.Start(false))
	require.Equal(t, Running, instance.Status)
	require.Equal(t, 1, ptyFactory.calls)
}

type fakeWorkspace struct {
	mode          workspace.Mode
	capabilities  workspace.Capabilities
	workDir       string
	displayName   string
	branchName    string
	valid         bool
	validErr      error
	dirty         bool
	dirtyCalls    int
	diff          *git.DiffStats
	events        *[]string
	setupErr      error
	cleanupErr    error
	removeErr     error
	pruneErr      error
	setupCalls    int
	cleanupCalls  int
	commitCalls   int
	removeCalls   int
	pruneCalls    int
	serializeData workspace.Data
	accessState   workspace.AccessState
}

func (w *fakeWorkspace) record(event string) {
	if w.events != nil {
		*w.events = append(*w.events, event)
	}
}
func (w *fakeWorkspace) Mode() workspace.Mode                   { return w.mode }
func (w *fakeWorkspace) Capabilities() workspace.Capabilities   { return w.capabilities }
func (w *fakeWorkspace) AccessState() workspace.AccessState     { return w.accessState }
func (w *fakeWorkspace) WorkDir() string                        { return w.workDir }
func (w *fakeWorkspace) DisplayName() string                    { return w.displayName }
func (w *fakeWorkspace) BranchName() string                     { return w.branchName }
func (w *fakeWorkspace) Setup() error                           { w.setupCalls++; w.record("setup"); return w.setupErr }
func (w *fakeWorkspace) IsValid() (bool, error)                 { return w.valid, w.validErr }
func (w *fakeWorkspace) IsDirty() (bool, error)                 { w.dirtyCalls++; return w.dirty, nil }
func (w *fakeWorkspace) IsBranchCheckedOut() (bool, error)      { return false, nil }
func (w *fakeWorkspace) Diff() *git.DiffStats                   { w.record("diff"); return w.diff }
func (w *fakeWorkspace) DiffNumstat() *git.DiffStats            { return w.diff }
func (w *fakeWorkspace) CommitChanges(string) error             { w.commitCalls++; w.record("commit"); return nil }
func (w *fakeWorkspace) PushChanges(string, bool) error         { return nil }
func (w *fakeWorkspace) Apply() (*workspace.ApplyResult, error) { return &workspace.ApplyResult{}, nil }
func (w *fakeWorkspace) AcknowledgeApply(string) error          { return nil }
func (w *fakeWorkspace) Remove() error                          { w.removeCalls++; w.record("remove"); return w.removeErr }
func (w *fakeWorkspace) Prune() error                           { w.pruneCalls++; w.record("prune"); return w.pruneErr }
func (w *fakeWorkspace) Cleanup() error                         { w.cleanupCalls++; w.record("cleanup"); return w.cleanupErr }
func (w *fakeWorkspace) Serialize() workspace.Data              { return w.serializeData }

func TestReadOnlySessionDisablesApplyAndResume(t *testing.T) {
	ws := &fakeWorkspace{mode: workspace.ModeDirectory, capabilities: workspace.Capabilities{Apply: false, Resume: false}, accessState: workspace.AccessState{ReadOnly: true, Message: "manual recovery required"}}
	instance, err := NewInstance(InstanceOptions{Title: "blocked", Path: t.TempDir(), Workspace: ws})
	require.NoError(t, err)
	instance.started = true
	instance.Status = Paused

	require.False(t, instance.Capabilities().Apply)
	require.False(t, instance.Capabilities().Resume)
	_, err = instance.Apply()
	require.ErrorContains(t, err, "read-only")
	require.ErrorContains(t, instance.Resume(), "read-only")
	require.Zero(t, ws.setupCalls)
}

func TestReadOnlyRestoreDoesNotStartTmuxOrRecreateWorktree(t *testing.T) {
	ws := &fakeWorkspace{mode: workspace.ModeDirectory, accessState: workspace.AccessState{ReadOnly: true, Message: "manual recovery required"}}
	instance, err := NewInstance(InstanceOptions{Title: "blocked", Path: t.TempDir(), Workspace: ws})
	require.NoError(t, err)
	instance.started = true
	instance.Status = Paused
	require.ErrorContains(t, instance.Resume(), "read-only")
	require.Zero(t, ws.setupCalls)
}

func TestInstanceWorkspaceSetupPrecedesTmuxStartAndUsesWorkDir(t *testing.T) {
	events := []string{}
	ws := &fakeWorkspace{mode: workspace.ModeDirectory, workDir: "/workspace-dir", branchName: "session/test", events: &events}
	hasSessionCalls := 0
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") {
				hasSessionCalls++
				if hasSessionCalls == 1 {
					return fmt.Errorf("missing")
				}
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	pty := &recordingPtyFactory{t: t, events: &events}
	instance, err := NewInstance(InstanceOptions{Title: "workspace-start", Path: t.TempDir(), Program: "claude", Workspace: ws})
	require.NoError(t, err)
	instance.SetTmuxSession(tmux.NewTmuxSessionWithDeps(instance.Title, instance.Program, pty, cmdExec))

	require.NoError(t, instance.Start(true))
	require.Equal(t, []string{"setup", "tmux-start"}, events[:2])
	require.Contains(t, pty.command, "-c /workspace-dir")
	require.Equal(t, "session/test", instance.Branch)
}

type recordingPtyFactory struct {
	t       *testing.T
	events  *[]string
	command string
}

func (p *recordingPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	if p.command == "" {
		*p.events = append(*p.events, "tmux-start")
		p.command = cmd.String()
	}
	return os.OpenFile(filepath.Join(p.t.TempDir(), "pty"), os.O_CREATE|os.O_RDWR, 0o644)
}
func (*recordingPtyFactory) Close() {}

func TestStartCleansWorkspaceWhenTmuxStartFailsBeforeStarted(t *testing.T) {
	ws := &fakeWorkspace{mode: workspace.ModeDirectory, workDir: t.TempDir()}
	instance, err := NewInstance(InstanceOptions{Title: "failed-start", Path: t.TempDir(), Program: "claude", Workspace: ws})
	require.NoError(t, err)
	instance.SetTmuxSession(tmux.NewTmuxSessionWithDeps(instance.Title, instance.Program, &failingPtyFactory{}, cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("missing") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}))

	require.Error(t, instance.Start(true))
	require.False(t, instance.Started())
	require.Equal(t, 1, ws.cleanupCalls)
}

type failingPtyFactory struct{}

func (*failingPtyFactory) Start(*exec.Cmd) (*os.File, error) { return nil, fmt.Errorf("pty failed") }
func (*failingPtyFactory) Close()                            {}

func TestResumePreservesPreexistingValidWorkspaceWhenTmuxStartFails(t *testing.T) {
	ws := &fakeWorkspace{mode: workspace.ModeDirectory, workDir: t.TempDir(), valid: true}
	instance, err := NewInstance(InstanceOptions{Title: "resume-existing", Path: t.TempDir(), Program: "claude", Workspace: ws})
	require.NoError(t, err)
	instance.started = true
	instance.Status = Paused
	instance.SetTmuxSession(tmux.NewTmuxSessionWithDeps(instance.Title, instance.Program, &failingPtyFactory{}, cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("missing") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}))

	require.Error(t, instance.Resume())
	require.Equal(t, Paused, instance.Status)
	require.Zero(t, ws.setupCalls)
	require.Zero(t, ws.cleanupCalls, "resume must not clean a workspace it did not acquire")
}

func TestPreparePauseReportsPausedWhenRemoveSucceededButPruneFailed(t *testing.T) {
	ws := &fakeWorkspace{mode: workspace.ModeGit, workDir: t.TempDir(), valid: true, pruneErr: fmt.Errorf("prune failed")}
	instance, err := NewInstance(InstanceOptions{Title: "pause-prune", Path: t.TempDir(), Workspace: ws})
	require.NoError(t, err)
	instance.started = true
	instance.Status = Running
	instance.tmuxSession = tmux.NewTmuxSessionWithDeps(instance.Title, "", &nullPtyFactory{t: t}, cmd_test.MockCmdExec{})

	paused, err := instance.PreparePause()
	require.True(t, paused, "removed workspace means the session is physically paused")
	require.ErrorContains(t, err, "failed to prune workspace")
	require.Equal(t, Running, instance.Status, "PreparePause leaves synchronous status mutation to its caller")
	require.Equal(t, 1, ws.removeCalls)
	require.Equal(t, 1, ws.pruneCalls)
}

func TestPauseStopsWhenWorkspaceValidationFails(t *testing.T) {
	ws := &fakeWorkspace{mode: workspace.ModeGit, workDir: t.TempDir(), validErr: fmt.Errorf("validation failed"), dirty: true}
	instance, err := NewInstance(InstanceOptions{Title: "pause-validation", Path: t.TempDir(), Workspace: ws})
	require.NoError(t, err)
	instance.started = true
	instance.Status = Running
	instance.tmuxSession = tmux.NewTmuxSessionWithDeps(instance.Title, "", &nullPtyFactory{t: t}, cmd_test.MockCmdExec{})

	err = instance.Pause()
	require.ErrorContains(t, err, "failed to validate workspace")
	require.Equal(t, Running, instance.Status)
	require.Zero(t, ws.dirtyCalls)
	require.Zero(t, ws.commitCalls)
	require.Zero(t, ws.removeCalls)
	require.Zero(t, ws.pruneCalls)
}

func TestInstanceWorkspacePauseResumeKillAndDiffDelegate(t *testing.T) {
	ws := &fakeWorkspace{mode: workspace.ModeGit, workDir: filepath.Join(t.TempDir(), "missing"), branchName: "branch", valid: true, dirty: true, diff: &git.DiffStats{Added: 2}}
	instance, err := NewInstance(InstanceOptions{Title: "lifecycle", Path: t.TempDir(), Workspace: ws})
	require.NoError(t, err)
	instance.started = true
	instance.Status = Running
	instance.tmuxSession = tmux.NewTmuxSessionWithDeps(instance.Title, "", &nullPtyFactory{t: t}, cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("missing") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	})

	require.Same(t, ws.diff, instance.ComputeDiff())
	require.NoError(t, instance.Pause())
	require.Equal(t, 1, ws.commitCalls)
	require.Equal(t, 1, ws.removeCalls)
	require.Equal(t, 1, ws.pruneCalls)

	ws.valid = false
	require.Error(t, instance.Resume()) // tmux start fails after setup
	require.Equal(t, 1, ws.setupCalls)
	require.Equal(t, 1, ws.cleanupCalls)

	instance.Status = Running
	require.Error(t, instance.Kill()) // tmux close fails, workspace still cleaned
	require.Equal(t, 2, ws.cleanupCalls)
}

func TestFactoryCreatedGitInstanceRepoNameAndDisplay(t *testing.T) {
	instance, repo := startFactoryGitInstance(t, "factory-display")

	repoName, err := instance.RepoName()
	require.NoError(t, err)
	require.Equal(t, filepath.Base(repo), repoName)
	require.Equal(t, filepath.Base(repo), instance.workspace.DisplayName())
	require.Equal(t, workspace.ModeGit, instance.WorkspaceMode())
	require.Equal(t, instance.workspace.WorkDir(), instance.WorkDir())
}

func TestFactoryCreatedGitInstanceSerialization(t *testing.T) {
	instance, _ := startFactoryGitInstance(t, "factory-serialization")

	data := instance.ToInstanceData()
	serialized := instance.workspace.Serialize().Git
	require.NotNil(t, serialized)
	require.Equal(t, workspace.CurrentVersion, data.Workspace.Version)
	require.Equal(t, workspace.ModeGit, data.Workspace.Mode)
	require.Equal(t, serialized, data.Workspace.Git)
	require.Nil(t, data.Worktree)
}

func TestFactoryCreatedGitInstanceComputeDiff(t *testing.T) {
	instance, _ := startFactoryGitInstance(t, "factory-diff")

	require.NoError(t, os.WriteFile(filepath.Join(instance.WorkDir(), "change.txt"), []byte("change\n"), 0o644))
	diff := instance.ComputeDiff()
	require.NoError(t, diff.Error)
	require.Contains(t, diff.Content, "change.txt")
}

func TestFactoryCreatedGitInstancePause(t *testing.T) {
	instance, _ := startFactoryGitInstance(t, "factory-pause")
	worktreePath := instance.WorkDir()

	require.NoError(t, instance.Pause())
	require.Equal(t, Paused, instance.Status)
	require.NoDirExists(t, worktreePath)
}

func TestFactoryCreatedGitInstanceKillCleansWorkspaceExactlyOnce(t *testing.T) {
	instance, _ := startFactoryGitInstance(t, "factory-cleanup")
	counter := &cleanupCountingWorkspace{Workspace: instance.workspace}
	instance.workspace = counter
	worktreePath := instance.WorkDir()

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
		if _, err := os.Stat(instance.WorkDir()); err == nil {
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
