package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResetCleansNativeWorktreesBeforeShadowProjects(t *testing.T) {
	var calls []string
	step := func(name string) func() error {
		return func() error {
			calls = append(calls, name)
			return nil
		}
	}

	require.NoError(t, resetAll(step("storage"), step("tmux"), step("worktrees"), step("shadow"), step("daemon")))
	require.Equal(t, []string{"storage", "tmux", "worktrees", "shadow", "daemon"}, calls)
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

	err := resetAll(step("storage"), step("tmux"), step("worktrees"), step("shadow"), step("daemon"))
	require.ErrorContains(t, err, "failed to cleanup shadow projects")
	require.ErrorIs(t, err, os.ErrPermission)
	require.Equal(t, []string{"storage", "tmux", "worktrees", "shadow"}, calls)
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
