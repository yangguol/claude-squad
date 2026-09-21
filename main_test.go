package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
