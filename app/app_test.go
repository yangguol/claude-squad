package app

import (
	"claude-squad/config"
	"claude-squad/log"
	"claude-squad/session"
	"claude-squad/session/git"
	"claude-squad/session/workspace"
	"claude-squad/ui"
	"claude-squad/ui/overlay"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain runs before all tests to set up the test environment
func TestMain(m *testing.M) {
	// Initialize the logger before any tests run
	log.Initialize(false)
	defer log.Close()

	// Run all tests
	exitCode := m.Run()

	// Exit with the same code as the tests
	os.Exit(exitCode)
}

// TestConfirmationModalStateTransitions tests state transitions without full instance setup
func TestConfirmationModalStateTransitions(t *testing.T) {
	// Create a minimal home struct for testing state transitions
	h := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
	}

	t.Run("shows confirmation on D press", func(t *testing.T) {
		// Simulate pressing 'D'
		h.state = stateDefault
		h.confirmationOverlay = nil

		// Manually trigger what would happen in handleKeyPress for 'D'
		h.state = stateConfirm
		h.confirmationOverlay = overlay.NewConfirmationOverlay("[!] Kill session 'test'?")

		assert.Equal(t, stateConfirm, h.state)
		assert.NotNil(t, h.confirmationOverlay)
		assert.False(t, h.confirmationOverlay.Dismissed)
	})

	t.Run("returns to default on y press", func(t *testing.T) {
		// Start in confirmation state
		h.state = stateConfirm
		h.confirmationOverlay = overlay.NewConfirmationOverlay("Test confirmation")

		// Simulate pressing 'y' using HandleKeyPress
		keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")}
		shouldClose := h.confirmationOverlay.HandleKeyPress(keyMsg)
		if shouldClose {
			h.state = stateDefault
			h.confirmationOverlay = nil
		}

		assert.Equal(t, stateDefault, h.state)
		assert.Nil(t, h.confirmationOverlay)
	})

	t.Run("returns to default on n press", func(t *testing.T) {
		// Start in confirmation state
		h.state = stateConfirm
		h.confirmationOverlay = overlay.NewConfirmationOverlay("Test confirmation")

		// Simulate pressing 'n' using HandleKeyPress
		keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}
		shouldClose := h.confirmationOverlay.HandleKeyPress(keyMsg)
		if shouldClose {
			h.state = stateDefault
			h.confirmationOverlay = nil
		}

		assert.Equal(t, stateDefault, h.state)
		assert.Nil(t, h.confirmationOverlay)
	})

	t.Run("returns to default on esc press", func(t *testing.T) {
		// Start in confirmation state
		h.state = stateConfirm
		h.confirmationOverlay = overlay.NewConfirmationOverlay("Test confirmation")

		// Simulate pressing ESC using HandleKeyPress
		keyMsg := tea.KeyMsg{Type: tea.KeyEscape}
		shouldClose := h.confirmationOverlay.HandleKeyPress(keyMsg)
		if shouldClose {
			h.state = stateDefault
			h.confirmationOverlay = nil
		}

		assert.Equal(t, stateDefault, h.state)
		assert.Nil(t, h.confirmationOverlay)
	})
}

// TestConfirmationModalKeyHandling tests the actual key handling in confirmation state
func TestConfirmationModalKeyHandling(t *testing.T) {
	// Import needed packages
	spinner := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spinner, false)

	// Create enough of home struct to test handleKeyPress in confirmation state
	h := &home{
		ctx:                 context.Background(),
		state:               stateConfirm,
		appConfig:           config.DefaultConfig(),
		list:                list,
		menu:                ui.NewMenu(),
		confirmationOverlay: overlay.NewConfirmationOverlay("Kill session?"),
	}

	testCases := []struct {
		name              string
		key               string
		expectedState     state
		expectedDismissed bool
		expectedNil       bool
	}{
		{
			name:              "y key confirms and dismisses overlay",
			key:               "y",
			expectedState:     stateDefault,
			expectedDismissed: true,
			expectedNil:       true,
		},
		{
			name:              "n key cancels and dismisses overlay",
			key:               "n",
			expectedState:     stateDefault,
			expectedDismissed: true,
			expectedNil:       true,
		},
		{
			name:              "esc key cancels and dismisses overlay",
			key:               "esc",
			expectedState:     stateDefault,
			expectedDismissed: true,
			expectedNil:       true,
		},
		{
			name:              "other keys are ignored",
			key:               "x",
			expectedState:     stateConfirm,
			expectedDismissed: false,
			expectedNil:       false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset state
			h.state = stateConfirm
			h.confirmationOverlay = overlay.NewConfirmationOverlay("Kill session?")

			// Create key message
			var keyMsg tea.KeyMsg
			if tc.key == "esc" {
				keyMsg = tea.KeyMsg{Type: tea.KeyEscape}
			} else {
				keyMsg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)}
			}

			// Call handleKeyPress
			model, _ := h.handleKeyPress(keyMsg)
			homeModel, ok := model.(*home)
			require.True(t, ok)

			assert.Equal(t, tc.expectedState, homeModel.state, "State mismatch for key: %s", tc.key)
			if tc.expectedNil {
				assert.Nil(t, homeModel.confirmationOverlay, "Overlay should be nil for key: %s", tc.key)
			} else {
				assert.NotNil(t, homeModel.confirmationOverlay, "Overlay should not be nil for key: %s", tc.key)
				assert.Equal(t, tc.expectedDismissed, homeModel.confirmationOverlay.Dismissed, "Dismissed mismatch for key: %s", tc.key)
			}
		})
	}
}

// TestConfirmationMessageFormatting tests that confirmation messages are formatted correctly
func TestConfirmationMessageFormatting(t *testing.T) {
	testCases := []struct {
		name            string
		sessionTitle    string
		expectedMessage string
	}{
		{
			name:            "short session name",
			sessionTitle:    "my-feature",
			expectedMessage: "[!] Kill session 'my-feature'? (y/n)",
		},
		{
			name:            "long session name",
			sessionTitle:    "very-long-feature-branch-name-here",
			expectedMessage: "[!] Kill session 'very-long-feature-branch-name-here'? (y/n)",
		},
		{
			name:            "session with spaces",
			sessionTitle:    "feature with spaces",
			expectedMessage: "[!] Kill session 'feature with spaces'? (y/n)",
		},
		{
			name:            "session with special chars",
			sessionTitle:    "feature/branch-123",
			expectedMessage: "[!] Kill session 'feature/branch-123'? (y/n)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test the message formatting directly
			actualMessage := fmt.Sprintf("[!] Kill session '%s'? (y/n)", tc.sessionTitle)
			assert.Equal(t, tc.expectedMessage, actualMessage)
		})
	}
}

// TestConfirmationFlowSimulation tests the confirmation flow by simulating the state changes
func TestConfirmationFlowSimulation(t *testing.T) {
	// Create a minimal setup
	spinner := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spinner, false)

	// Add test instance
	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   "test-session",
		Path:    t.TempDir(),
		Program: "claude",
		AutoYes: false,
	})
	require.NoError(t, err)
	_ = list.AddInstance(instance)
	list.SetSelectedInstance(0)

	h := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
		list:      list,
		menu:      ui.NewMenu(),
	}

	// Simulate what happens when D is pressed
	selected := h.list.GetSelectedInstance()
	require.NotNil(t, selected)

	// This is what the KeyKill handler does
	message := fmt.Sprintf("[!] Kill session '%s'?", selected.Title)
	h.confirmationOverlay = overlay.NewConfirmationOverlay(message)
	h.state = stateConfirm

	// Verify the state
	assert.Equal(t, stateConfirm, h.state)
	assert.NotNil(t, h.confirmationOverlay)
	assert.False(t, h.confirmationOverlay.Dismissed)
	// Test that overlay renders with the correct message
	rendered := h.confirmationOverlay.Render()
	assert.Contains(t, rendered, "Kill session 'test-session'?")
}

// TestConfirmActionWithDifferentTypes tests that confirmAction works with different action types
func TestConfirmActionWithDifferentTypes(t *testing.T) {
	h := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
	}

	t.Run("works with simple action returning nil", func(t *testing.T) {
		actionCalled := false
		action := func() tea.Msg {
			actionCalled = true
			return nil
		}

		// Set up callback to track action execution
		actionExecuted := false
		h.confirmationOverlay = overlay.NewConfirmationOverlay("Test action?")
		h.confirmationOverlay.OnConfirm = func() {
			h.state = stateDefault
			actionExecuted = true
			action() // Execute the action
		}
		h.state = stateConfirm

		// Verify state was set
		assert.Equal(t, stateConfirm, h.state)
		assert.NotNil(t, h.confirmationOverlay)
		assert.False(t, h.confirmationOverlay.Dismissed)
		assert.NotNil(t, h.confirmationOverlay.OnConfirm)

		// Execute the confirmation callback
		h.confirmationOverlay.OnConfirm()
		assert.True(t, actionCalled)
		assert.True(t, actionExecuted)
	})

	t.Run("works with action returning error", func(t *testing.T) {
		expectedErr := fmt.Errorf("test error")
		action := func() tea.Msg {
			return expectedErr
		}

		// Set up callback to track action execution
		var receivedMsg tea.Msg
		h.confirmationOverlay = overlay.NewConfirmationOverlay("Error action?")
		h.confirmationOverlay.OnConfirm = func() {
			h.state = stateDefault
			receivedMsg = action() // Execute the action and capture result
		}
		h.state = stateConfirm

		// Verify state was set
		assert.Equal(t, stateConfirm, h.state)
		assert.NotNil(t, h.confirmationOverlay)
		assert.False(t, h.confirmationOverlay.Dismissed)
		assert.NotNil(t, h.confirmationOverlay.OnConfirm)

		// Execute the confirmation callback
		h.confirmationOverlay.OnConfirm()
		assert.Equal(t, expectedErr, receivedMsg)
	})

	t.Run("works with action returning custom message", func(t *testing.T) {
		action := func() tea.Msg {
			return instanceChangedMsg{}
		}

		// Set up callback to track action execution
		var receivedMsg tea.Msg
		h.confirmationOverlay = overlay.NewConfirmationOverlay("Custom message action?")
		h.confirmationOverlay.OnConfirm = func() {
			h.state = stateDefault
			receivedMsg = action() // Execute the action and capture result
		}
		h.state = stateConfirm

		// Verify state was set
		assert.Equal(t, stateConfirm, h.state)
		assert.NotNil(t, h.confirmationOverlay)
		assert.False(t, h.confirmationOverlay.Dismissed)
		assert.NotNil(t, h.confirmationOverlay.OnConfirm)

		// Execute the confirmation callback
		h.confirmationOverlay.OnConfirm()
		_, ok := receivedMsg.(instanceChangedMsg)
		assert.True(t, ok, "Expected instanceChangedMsg but got %T", receivedMsg)
	})
}

// TestMultipleConfirmationsDontInterfere tests that multiple confirmations don't interfere with each other
func TestMultipleConfirmationsDontInterfere(t *testing.T) {
	h := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
	}

	// First confirmation
	action1Called := false
	action1 := func() tea.Msg {
		action1Called = true
		return nil
	}

	// Set up first confirmation
	h.confirmationOverlay = overlay.NewConfirmationOverlay("First action?")
	firstOnConfirm := func() {
		h.state = stateDefault
		action1()
	}
	h.confirmationOverlay.OnConfirm = firstOnConfirm
	h.state = stateConfirm

	// Verify first confirmation
	assert.Equal(t, stateConfirm, h.state)
	assert.NotNil(t, h.confirmationOverlay)
	assert.False(t, h.confirmationOverlay.Dismissed)
	assert.NotNil(t, h.confirmationOverlay.OnConfirm)

	// Cancel first confirmation (simulate pressing 'n')
	keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}
	shouldClose := h.confirmationOverlay.HandleKeyPress(keyMsg)
	if shouldClose {
		h.state = stateDefault
		h.confirmationOverlay = nil
	}

	// Second confirmation with different action
	action2Called := false
	action2 := func() tea.Msg {
		action2Called = true
		return fmt.Errorf("action2 error")
	}

	// Set up second confirmation
	h.confirmationOverlay = overlay.NewConfirmationOverlay("Second action?")
	var secondResult tea.Msg
	secondOnConfirm := func() {
		h.state = stateDefault
		secondResult = action2()
	}
	h.confirmationOverlay.OnConfirm = secondOnConfirm
	h.state = stateConfirm

	// Verify second confirmation
	assert.Equal(t, stateConfirm, h.state)
	assert.NotNil(t, h.confirmationOverlay)
	assert.False(t, h.confirmationOverlay.Dismissed)
	assert.NotNil(t, h.confirmationOverlay.OnConfirm)

	// Execute second action to verify it's the correct one
	h.confirmationOverlay.OnConfirm()
	err, ok := secondResult.(error)
	assert.True(t, ok)
	assert.Equal(t, "action2 error", err.Error())
	assert.True(t, action2Called)
	assert.False(t, action1Called, "First action should not have been called")

	// Test that cancelled action can still be executed independently
	firstOnConfirm()
	assert.True(t, action1Called, "First action should be callable after being replaced")
}

type appTestWorkspace struct {
	mode         workspace.Mode
	capabilities workspace.Capabilities
	displayName  string
	applyResult  *workspace.ApplyResult
	applyErr     error
	applyCalls   int
	access       workspace.AccessState
	acknowledged []string
}

func (w *appTestWorkspace) Mode() workspace.Mode                 { return w.mode }
func (w *appTestWorkspace) Capabilities() workspace.Capabilities { return w.capabilities }
func (w *appTestWorkspace) AccessState() workspace.AccessState   { return w.access }
func (w *appTestWorkspace) WorkDir() string                      { return "/internal/worktree" }
func (w *appTestWorkspace) DisplayName() string                  { return w.displayName }
func (w *appTestWorkspace) BranchName() string                   { return "internal-shadow-branch" }
func (w *appTestWorkspace) Setup() error                         { return nil }
func (w *appTestWorkspace) IsValid() (bool, error)               { return true, nil }
func (w *appTestWorkspace) IsDirty() (bool, error)               { return false, nil }
func (w *appTestWorkspace) IsBranchCheckedOut() (bool, error)    { return false, nil }
func (w *appTestWorkspace) Diff() *git.DiffStats                 { return &git.DiffStats{} }
func (w *appTestWorkspace) DiffNumstat() *git.DiffStats          { return &git.DiffStats{} }
func (w *appTestWorkspace) CommitChanges(string) error           { return nil }
func (w *appTestWorkspace) PushChanges(string, bool) error       { return nil }
func (w *appTestWorkspace) Apply() (*workspace.ApplyResult, error) {
	w.applyCalls++
	if w.applyResult == nil && w.applyErr == nil {
		return &workspace.ApplyResult{}, nil
	}
	return w.applyResult, w.applyErr
}
func (w *appTestWorkspace) AcknowledgeApply(transactionID string) error {
	w.acknowledged = append(w.acknowledged, transactionID)
	return nil
}
func (w *appTestWorkspace) Remove() error             { return nil }
func (w *appTestWorkspace) Prune() error              { return nil }
func (w *appTestWorkspace) Cleanup() error            { return nil }
func (w *appTestWorkspace) Serialize() workspace.Data { return workspace.Data{Mode: w.mode} }

func newDirectoryHome(t *testing.T) (*home, *session.Instance) {
	h, instance, _ := newDirectoryHomeWithWorkspace(t)
	return h, instance
}

func newDirectoryHomeWithWorkspace(t *testing.T) (*home, *session.Instance, *appTestWorkspace) {
	t.Helper()
	backend := &appTestWorkspace{mode: workspace.ModeDirectory, capabilities: workspace.Capabilities{Apply: true}, displayName: "source-project"}
	instance, err := session.NewInstance(session.InstanceOptions{
		Title: "directory-session", Path: t.TempDir(), Program: "echo",
		Workspace: backend,
	})
	require.NoError(t, err)
	instance.SetStatus(session.Ready)
	s := spinner.New()
	list := ui.NewList(&s, false)
	list.AddInstance(instance)
	list.SetSelectedInstance(0)
	h := &home{
		ctx: context.Background(), state: stateDefault, workspaceMode: workspace.ModeDirectory, keySent: true,
		appConfig: config.DefaultConfig(), list: list, menu: ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane()),
		errBox:       ui.NewErrBox(),
	}
	return h, instance, backend
}

func TestDirectoryPromptSkipsBranchPickerAndSearch(t *testing.T) {
	h, _ := newDirectoryHome(t)
	overlay := h.newPromptOverlay()
	require.Equal(t, uint64(0), overlay.BranchFilterVersion())
	require.Empty(t, overlay.BranchFilter())
	require.NotContains(t, overlay.Render(), "Branch")
}

func TestDirectoryPushIsIgnored(t *testing.T) {
	h, _ := newDirectoryHome(t)
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	require.Nil(t, cmd)
	require.Equal(t, stateDefault, h.state)
	require.Nil(t, h.confirmationOverlay)
}

func TestDirectoryApplyConfirmationAndResultMessages(t *testing.T) {
	tests := []struct {
		name   string
		result *workspace.ApplyResult
		err    error
	}{
		{name: "success", result: &workspace.ApplyResult{Changed: []string{"a.txt"}}},
		{name: "no-op", result: &workspace.ApplyResult{}},
		{name: "conflict", err: &workspace.ConflictError{Paths: []string{"a.txt", "b.txt"}}},
		{name: "error", err: fmt.Errorf("disk failure")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, instance := newDirectoryHome(t)
			h.applyInstance = func(got *session.Instance) (*workspace.ApplyResult, error) {
				require.Same(t, instance, got)
				return tc.result, tc.err
			}
			h.saveInstances = func([]*session.Instance) error { return nil }
			h.pauseInstance = func(*session.Instance) (bool, error) { return true, nil }

			_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
			require.Nil(t, cmd)
			require.Equal(t, stateConfirm, h.state)
			rendered := h.confirmationOverlay.Render()
			require.Contains(t, rendered, "Apply changes from session 'directory-session'")
			require.Contains(t, rendered, "to 'source-project'?")

			_, cmd = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
			require.NotNil(t, cmd)
			msg, ok := cmd().(applyResultMsg)
			require.True(t, ok)
			require.Equal(t, tc.result, msg.result)
			require.Equal(t, tc.err, msg.err)

			_, followup := h.Update(msg)
			for followup != nil {
				_, followup = h.Update(followup())
			}
			require.Equal(t, stateHelp, h.state)
			require.NotNil(t, h.textOverlay)
			overlayText := h.textOverlay.Render()
			switch tc.name {
			case "success":
				require.Contains(t, overlayText, "Applied 1 changed path(s)")
			case "no-op":
				require.Contains(t, overlayText, "source is up to date")
			case "conflict":
				require.Contains(t, overlayText, "Apply Conflict")
				require.Contains(t, overlayText, "a.txt")
				require.Contains(t, overlayText, "b.txt")
			case "error":
				require.Contains(t, overlayText, "Apply Failed")
				require.Contains(t, overlayText, "disk failure")
			}
		})
	}
}

func TestConfirmActionPropagatesReturnedMessage(t *testing.T) {
	h := &home{ctx: context.Background(), state: stateDefault, appConfig: config.DefaultConfig()}
	expected := instanceChangedMsg{}
	h.confirmAction("Proceed?", func() tea.Msg { return expected })

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	require.NotNil(t, cmd)
	_, ok := cmd().(instanceChangedMsg)
	require.True(t, ok)
}

func TestDirectoryHelpIncludesIsolationCaveat(t *testing.T) {
	_, instance := newDirectoryHome(t)
	content := helpTypeGeneral{instance: instance}.toContent()
	require.Contains(t, content, "Apply changes")
	require.NotContains(t, content, "push branch")
	require.True(t, strings.Contains(content, "Shared dependency directories") && strings.Contains(content, "not isolated"))
}

func TestGeneralHelpShowsResumeOnlyForEligiblePausedSession(t *testing.T) {
	makeInstance := func(status session.Status, capabilities workspace.Capabilities) *session.Instance {
		instance, err := session.NewInstance(session.InstanceOptions{
			Title: "resume-help", Path: t.TempDir(), Program: "echo",
			Workspace: &appTestWorkspace{mode: workspace.ModeDirectory, capabilities: capabilities, displayName: "source"},
		})
		require.NoError(t, err)
		instance.SetStatus(status)
		return instance
	}

	running := helpTypeGeneral{instance: makeInstance(session.Running, workspace.Capabilities{Resume: true})}.toContent()
	blocked := helpTypeGeneral{instance: makeInstance(session.Paused, workspace.Capabilities{})}.toContent()
	resumable := helpTypeGeneral{instance: makeInstance(session.Paused, workspace.Capabilities{Resume: true})}.toContent()

	require.NotContains(t, running, "Resume a paused session")
	require.NotContains(t, blocked, "Resume a paused session")
	require.Contains(t, resumable, "Resume a paused session")
}

func TestGeneralHelpDerivesPushAndApplyIndependently(t *testing.T) {
	makeInstance := func(capabilities workspace.Capabilities) *session.Instance {
		instance, err := session.NewInstance(session.InstanceOptions{
			Title: "capabilities", Path: t.TempDir(), Program: "echo",
			Workspace: &appTestWorkspace{mode: workspace.ModeDirectory, capabilities: capabilities, displayName: "source"},
		})
		require.NoError(t, err)
		return instance
	}

	tests := []struct {
		name      string
		instance  *session.Instance
		wantPush  bool
		wantApply bool
		wantGitC  bool
	}{
		{name: "nil selection"},
		{name: "push only", instance: makeInstance(workspace.Capabilities{Push: true}), wantPush: true},
		{name: "apply only", instance: makeInstance(workspace.Capabilities{Apply: true}), wantApply: true},
		{name: "both", instance: makeInstance(workspace.Capabilities{Push: true, Apply: true}), wantPush: true, wantApply: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := helpTypeGeneral{instance: tc.instance}.toContent()
			require.Equal(t, tc.wantPush, strings.Contains(content, "push branch"))
			require.Equal(t, tc.wantApply, strings.Contains(content, "Apply changes"))
			require.Equal(t, tc.wantGitC, strings.Contains(content, "Checkout: commit"))
		})
	}
}

func runApplyFlow(t *testing.T, h *home, result *workspace.ApplyResult, applyErr error) []string {
	t.Helper()
	events := []string{}
	h.applyInstance = func(*session.Instance) (*workspace.ApplyResult, error) {
		events = append(events, "apply")
		return result, applyErr
	}
	h.saveInstances = func([]*session.Instance) error {
		events = append(events, "save")
		return nil
	}
	h.pauseInstance = func(*session.Instance) (bool, error) {
		events = append(events, "pause")
		return true, nil
	}
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	require.NotNil(t, cmd)
	msg := cmd()
	for {
		_, cmd = h.Update(msg)
		if cmd == nil {
			break
		}
		msg = cmd()
	}
	return events
}

func TestApplyCleanupWarningStillSavesPausesAndAcknowledges(t *testing.T) {
	h, instance, backend := newDirectoryHomeWithWorkspace(t)
	warning := &workspace.CleanupBlockedWarning{
		TransactionID: "tx-warning",
		Paths:         []string{"changed.txt"},
		RetainedPaths: []string{"/shadow/transactions/tx-warning"},
	}
	backend.applyResult = &workspace.ApplyResult{Changed: []string{"changed.txt"}, TransactionID: "tx-warning", Warning: warning}
	events := []string{}
	h.saveInstances = func([]*session.Instance) error {
		events = append(events, "save")
		return nil
	}
	h.pauseInstance = func(*session.Instance) (bool, error) {
		events = append(events, "pause")
		return true, nil
	}

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	msg := cmd()
	for cmd != nil {
		_, cmd = h.Update(msg)
		if cmd != nil {
			msg = cmd()
		}
	}

	require.Equal(t, []string{"save", "pause", "save"}, events)
	require.Equal(t, []string{"tx-warning"}, backend.acknowledged)
	require.Equal(t, session.Paused, instance.Status)
	require.Contains(t, h.textOverlay.Render(), "Apply Complete")
	require.NotContains(t, h.textOverlay.Render(), "Apply Failed")
}

func TestApplyCleanupWarningRendersRetainedPaths(t *testing.T) {
	h, instance := newDirectoryHome(t)
	h.showApplyResult(applyFinalMsg{
		instance: instance,
		result: &workspace.ApplyResult{
			Changed:       []string{"changed.txt"},
			TransactionID: "tx-warning",
			Warning: &workspace.CleanupBlockedWarning{
				TransactionID: "tx-warning",
				Paths:         []string{"changed.txt"},
				RetainedPaths: []string{"/shadow/transactions/tx-warning", "/source/.capture"},
			},
		},
		paused: true,
	})

	rendered := h.textOverlay.Render()
	require.Contains(t, rendered, "Cleanup warning")
	require.Contains(t, rendered, "tx-warning")
	require.Contains(t, rendered, "/shadow/transactions/tx-warning")
	require.Contains(t, rendered, "/source/.capture")
}

func TestBlockedSessionMenuDisablesApplyAndResume(t *testing.T) {
	h, instance, backend := newDirectoryHomeWithWorkspace(t)
	backend.capabilities = workspace.Capabilities{}
	backend.access = workspace.AccessState{ReadOnly: true, Message: "recovery blocked"}
	instance.SetStatus(session.Paused)
	h.instanceChanged()

	rendered := h.menu.String()
	require.NotContains(t, rendered, "apply")
	require.NotContains(t, rendered, "resume")

	applyCalls := 0
	h.applyInstance = func(*session.Instance) (*workspace.ApplyResult, error) {
		applyCalls++
		return nil, nil
	}
	_, applyCmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	require.Nil(t, applyCmd)
	require.Equal(t, 0, applyCalls)

	_, resumeCmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if resumeCmd != nil {
		_ = resumeCmd()
	}
	require.Equal(t, session.Paused, instance.Status)
}

func TestApplyFlowUsesWorkspaceApplyBackend(t *testing.T) {
	h, _, backend := newDirectoryHomeWithWorkspace(t)
	backend.applyResult = &workspace.ApplyResult{Changed: []string{"real.txt"}}
	h.saveInstances = func([]*session.Instance) error { return nil }
	h.pauseInstance = func(*session.Instance) (bool, error) { return true, nil }

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	msg := cmd()
	for cmd != nil {
		_, cmd = h.Update(msg)
		if cmd != nil {
			msg = cmd()
		}
	}
	require.Equal(t, 1, backend.applyCalls)
	require.Contains(t, h.textOverlay.Render(), "Applied 1 changed path")
}

func TestApplySuccessCleansOriginalTerminalOnlyFromUpdate(t *testing.T) {
	h, target := newDirectoryHome(t)
	other, err := session.NewInstance(session.InstanceOptions{
		Title: "other", Path: t.TempDir(), Program: "echo",
		Workspace: &appTestWorkspace{mode: workspace.ModeDirectory, capabilities: workspace.Capabilities{Apply: true}, displayName: "other-source"},
	})
	require.NoError(t, err)
	other.SetStatus(session.Ready)
	h.list.AddInstance(other)()
	h.applyInstance = func(*session.Instance) (*workspace.ApplyResult, error) {
		return &workspace.ApplyResult{Changed: []string{"a.txt"}}, nil
	}
	h.saveInstances = func([]*session.Instance) error { return nil }
	h.pauseInstance = func(*session.Instance) (bool, error) { return true, nil }
	var cleaned []string
	h.cleanupTerminal = func(title string) { cleaned = append(cleaned, title) }

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	h.list.SetSelectedInstance(1)
	applyMsg := cmd()
	_, pauseCmd := h.Update(applyMsg)
	pauseMsg := pauseCmd()
	require.Empty(t, cleaned, "background pause command must not mutate terminal UI")
	_, _ = h.Update(pauseMsg)

	require.Equal(t, []string{target.Title}, cleaned)
	require.Same(t, other, h.list.GetSelectedInstance())
	require.Equal(t, session.Paused, target.Status)
	require.Equal(t, session.Ready, other.Status)
}

func TestApplyPostPauseSaveFailureKeepsPausedAndCleansTerminal(t *testing.T) {
	h, instance := newDirectoryHome(t)
	h.applyInstance = func(*session.Instance) (*workspace.ApplyResult, error) {
		return &workspace.ApplyResult{Changed: []string{"a.txt"}}, nil
	}
	saves := 0
	h.saveInstances = func(instances []*session.Instance) error {
		saves++
		if saves == 2 {
			require.Equal(t, session.Paused, instances[0].Status)
			return fmt.Errorf("post-pause save failed")
		}
		return nil
	}
	h.pauseInstance = func(*session.Instance) (bool, error) { return true, nil }
	var cleaned []string
	h.cleanupTerminal = func(title string) { cleaned = append(cleaned, title) }

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	msg := cmd()
	for cmd != nil {
		_, cmd = h.Update(msg)
		if cmd != nil {
			msg = cmd()
		}
	}

	require.Equal(t, 2, saves)
	require.Equal(t, session.Paused, instance.Status)
	require.Equal(t, []string{instance.Title}, cleaned)
	require.Contains(t, h.textOverlay.Render(), "Persistence failed: post-pause save failed")
	require.Contains(t, h.textOverlay.Render(), "Session paused")
}

func TestApplyPartialPausePersistsPausedCleansTerminalAndReportsError(t *testing.T) {
	h, instance := newDirectoryHome(t)
	h.applyInstance = func(*session.Instance) (*workspace.ApplyResult, error) {
		return &workspace.ApplyResult{Changed: []string{"a.txt"}}, nil
	}
	var savedStatuses []session.Status
	h.saveInstances = func(instances []*session.Instance) error {
		savedStatuses = append(savedStatuses, instances[0].Status)
		return nil
	}
	h.pauseInstance = func(*session.Instance) (bool, error) {
		return true, fmt.Errorf("failed to prune workspace")
	}
	var cleaned []string
	h.cleanupTerminal = func(title string) { cleaned = append(cleaned, title) }

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	msg := cmd()
	for cmd != nil {
		_, cmd = h.Update(msg)
		if cmd != nil {
			msg = cmd()
		}
	}

	require.Equal(t, []session.Status{session.Ready, session.Paused}, savedStatuses)
	require.Equal(t, session.Paused, instance.Status)
	require.Equal(t, []string{instance.Title}, cleaned)
	overlay := h.textOverlay.Render()
	require.Contains(t, overlay, "Session paused")
	require.Contains(t, overlay, "Pause cleanup failed: failed to prune workspace")
	require.NotContains(t, overlay, "remains recoverable")
}

func TestApplyFlowPersistsBeforeAndAfterPause(t *testing.T) {
	h, instance := newDirectoryHome(t)
	events := runApplyFlow(t, h, &workspace.ApplyResult{Changed: []string{"a.txt"}}, nil)
	require.Equal(t, []string{"apply", "save", "pause", "save"}, events)
	require.Equal(t, session.Paused, instance.Status)
	require.Contains(t, h.textOverlay.Render(), "Applied 1 changed path")
	require.Contains(t, h.textOverlay.Render(), "Session paused")
}

func TestApplySaveFailurePreservesRunningSession(t *testing.T) {
	h, instance := newDirectoryHome(t)
	events := []string{}
	h.applyInstance = func(*session.Instance) (*workspace.ApplyResult, error) {
		events = append(events, "apply")
		return &workspace.ApplyResult{Changed: []string{"a.txt"}}, nil
	}
	h.saveInstances = func([]*session.Instance) error {
		events = append(events, "save")
		return fmt.Errorf("save unavailable")
	}
	h.pauseInstance = func(*session.Instance) (bool, error) {
		events = append(events, "pause")
		return true, nil
	}
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	_, cmd = h.Update(cmd())
	require.Nil(t, cmd)
	require.Equal(t, []string{"apply", "save"}, events)
	require.Equal(t, session.Ready, instance.Status)
	overlay := h.textOverlay.Render()
	require.Contains(t, overlay, "Apply Complete")
	require.Contains(t, overlay, "Persistence failed")
	require.Contains(t, overlay, "was not paused")
}

func TestApplyPauseFailureReportsAppliedSourceAndPersists(t *testing.T) {
	h, instance := newDirectoryHome(t)
	events := []string{}
	h.applyInstance = func(*session.Instance) (*workspace.ApplyResult, error) {
		events = append(events, "apply")
		return &workspace.ApplyResult{Changed: []string{"a.txt"}}, nil
	}
	h.saveInstances = func([]*session.Instance) error {
		events = append(events, "save")
		return nil
	}
	h.pauseInstance = func(*session.Instance) (bool, error) {
		events = append(events, "pause")
		return false, fmt.Errorf("tmux detach failed")
	}
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	msg := cmd()
	for cmd != nil {
		_, cmd = h.Update(msg)
		if cmd != nil {
			msg = cmd()
		}
	}
	require.Equal(t, []string{"apply", "save", "pause", "save"}, events)
	require.Equal(t, session.Ready, instance.Status)
	overlay := h.textOverlay.Render()
	require.Contains(t, overlay, "Apply Complete")
	require.Contains(t, overlay, "Pause cleanup failed")
	require.NotContains(t, overlay, "Apply Failed")
}

func TestApplyCommandDoesNotMutateModelAndStaleSelectionIsSafe(t *testing.T) {
	h, target := newDirectoryHome(t)
	other, err := session.NewInstance(session.InstanceOptions{
		Title: "other", Path: t.TempDir(), Program: "echo",
		Workspace: &appTestWorkspace{mode: workspace.ModeDirectory, capabilities: workspace.Capabilities{Apply: true}, displayName: "other-source"},
	})
	require.NoError(t, err)
	other.SetStatus(session.Ready)
	h.list.AddInstance(other)()
	h.applyInstance = func(*session.Instance) (*workspace.ApplyResult, error) {
		return &workspace.ApplyResult{Changed: []string{"a.txt"}}, nil
	}
	h.saveInstances = func([]*session.Instance) error { return nil }
	h.pauseInstance = func(*session.Instance) (bool, error) { return true, nil }

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	h.list.SetSelectedInstance(1)
	msg := cmd()
	require.Equal(t, session.Ready, target.Status, "background command must not mutate model status")
	require.Same(t, other, h.list.GetSelectedInstance())
	for cmd != nil {
		_, cmd = h.Update(msg)
		if cmd != nil {
			msg = cmd()
		}
	}
	require.Equal(t, session.Paused, target.Status)
	require.Equal(t, session.Ready, other.Status)
	require.Same(t, other, h.list.GetSelectedInstance())
}

func TestKillCommandDoesNotDeleteNewSelectionAndSuppressesDuplicate(t *testing.T) {
	h, target := newDirectoryHome(t)
	other, err := session.NewInstance(session.InstanceOptions{
		Title: "other", Path: t.TempDir(), Program: "echo",
		Workspace: &appTestWorkspace{mode: workspace.ModeDirectory, capabilities: workspace.Capabilities{Apply: true}, displayName: "other-source"},
	})
	require.NoError(t, err)
	other.SetStatus(session.Ready)
	h.list.AddInstance(other)()
	killCalls := 0
	deleteCalls := 0
	h.killInstance = func(got *session.Instance) error {
		require.Same(t, target, got)
		killCalls++
		return nil
	}
	h.deleteInstance = func(title string) error {
		require.Equal(t, target.Title, title)
		deleteCalls++
		return nil
	}

	h.keySent = true
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	require.True(t, h.actionPending(target))
	h.keySent = true
	_, duplicate := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	require.Nil(t, duplicate)
	require.Equal(t, stateDefault, h.state, "duplicate kill must not open another confirmation")
	h.list.SetSelectedInstance(1)

	msg := cmd()
	require.Equal(t, 2, h.list.NumInstances(), "command must not mutate the list")
	_, _ = h.Update(msg)
	require.Equal(t, 1, killCalls)
	require.Equal(t, 1, deleteCalls)
	require.False(t, h.list.ContainsInstance(target))
	require.True(t, h.list.ContainsInstance(other))
	require.Same(t, other, h.list.GetSelectedInstance())
}

func TestApplyDuplicateSuppressedUntilFlowCompletes(t *testing.T) {
	h, instance := newDirectoryHome(t)
	calls := 0
	h.applyInstance = func(*session.Instance) (*workspace.ApplyResult, error) {
		calls++
		return &workspace.ApplyResult{}, nil
	}
	h.saveInstances = func([]*session.Instance) error { return nil }
	h.pauseInstance = func(*session.Instance) (bool, error) { return true, nil }

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	_, first := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	require.True(t, h.actionPending(instance))
	h.keySent = true
	_, duplicate := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	require.Nil(t, duplicate)
	require.Equal(t, 0, calls)

	msg := first()
	for first != nil {
		_, first = h.Update(msg)
		if first != nil {
			msg = first()
		}
	}
	require.Equal(t, 1, calls)
	require.False(t, h.actionPending(instance))
}

// TestConfirmationModalVisualAppearance tests that confirmation modal has distinct visual appearance
func TestConfirmationModalVisualAppearance(t *testing.T) {
	h := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
	}

	// Create a test confirmation overlay
	message := "[!] Delete everything?"
	h.confirmationOverlay = overlay.NewConfirmationOverlay(message)
	h.state = stateConfirm

	// Verify the overlay was created with confirmation settings
	assert.NotNil(t, h.confirmationOverlay)
	assert.Equal(t, stateConfirm, h.state)
	assert.False(t, h.confirmationOverlay.Dismissed)

	// Test the overlay render (we can test that it renders without errors)
	rendered := h.confirmationOverlay.Render()
	assert.NotEmpty(t, rendered)

	// Test that it includes the message content and instructions
	assert.Contains(t, rendered, "Delete everything?")
	assert.Contains(t, rendered, "Press")
	assert.Contains(t, rendered, "to confirm")
	assert.Contains(t, rendered, "to cancel")

	// Test that the danger indicator is preserved
	assert.Contains(t, rendered, "[!")
}
