package main

import (
	"claude-squad/config"
	"claude-squad/session"
	"claude-squad/session/workspace"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResetPreflightsShadowReferencesBeforeClearingStorage(t *testing.T) {
	var calls []string
	step := func(name string) func() error {
		return func() error {
			calls = append(calls, name)
			return nil
		}
	}

	require.NoError(t, resetAll(step("preflight"), step("storage"), step("tmux"), step("worktrees"), step("shadow"), step("daemon")))
	require.Equal(t, []string{"preflight", "storage", "tmux", "worktrees", "shadow", "daemon"}, calls)
}

func TestResetCleansNativeWorktreesBeforeShadowProjects(t *testing.T) {
	var calls []string
	step := func(name string) func() error {
		return func() error {
			calls = append(calls, name)
			return nil
		}
	}

	require.NoError(t, resetAll(step("preflight"), step("storage"), step("tmux"), step("worktrees"), step("shadow"), step("daemon")))
	require.Equal(t, []string{"preflight", "storage", "tmux", "worktrees", "shadow", "daemon"}, calls)
}

func TestResetStopsAndReportsShadowCleanupFailure(t *testing.T) {
	var calls []string
	step := func(name string) func() error {
		return func() error {
			calls = append(calls, name)
			if name == "shadow" {
				return os.ErrPermission
			}
			return nil
		}
	}

	err := resetAll(step("preflight"), step("storage"), step("tmux"), step("worktrees"), step("shadow"), step("daemon"))
	require.ErrorContains(t, err, "failed to cleanup shadow projects")
	require.ErrorIs(t, err, os.ErrPermission)
	require.Equal(t, []string{"preflight", "storage", "tmux", "worktrees", "shadow"}, calls)
}

func TestResetPreflightFailurePreservesStorage(t *testing.T) {
	var calls []string
	err := resetAll(
		func() error { calls = append(calls, "preflight"); return os.ErrInvalid },
		func() error { calls = append(calls, "storage"); return nil },
		func() error { return nil }, func() error { return nil }, func() error { return nil }, func() error { return nil },
	)
	require.ErrorContains(t, err, "failed to validate persisted shadow references")
	require.ErrorIs(t, err, os.ErrInvalid)
	require.Equal(t, []string{"preflight"}, calls)
}

func TestResetMalformedPersistedStateDoesNotClearStateOrShadowProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".claude-squad")
	project := filepath.Join(configDir, "shadow-projects", strings.Repeat("a", 64))
	require.NoError(t, os.MkdirAll(project, 0o700))
	statePath := filepath.Join(configDir, config.StateFileName)
	malformed := []byte(`{"instances":null}`)
	require.NoError(t, os.WriteFile(statePath, malformed, 0o600))
	state := config.LoadState()
	storage, err := session.NewStorage(state)
	require.NoError(t, err)

	err = resetAll(workspace.ValidateStoredShadowReferences, storage.DeleteAllInstances,
		func() error { return nil }, func() error { return nil }, workspace.CleanupShadowProjects, func() error { return nil })
	require.ErrorContains(t, err, "missing or null instances")
	contents, readErr := os.ReadFile(statePath)
	require.NoError(t, readErr)
	require.Equal(t, malformed, contents)
	require.DirExists(t, project)
}

func TestValidateWorkingDirectoryAllowsPlainDirectory(t *testing.T) {
	require.NoError(t, validateWorkingDirectory(t.TempDir()))
}

func TestValidateWorkingDirectoryRejectsMissingPath(t *testing.T) {
	err := validateWorkingDirectory(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}

func TestValidateWorkingDirectoryRejectsRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(path, []byte("content"), 0o644))

	require.Error(t, validateWorkingDirectory(path))
}
