package workspace

import (
	"fmt"
	"os"
	"path/filepath"

	"claude-squad/session/git"
)

func DetectMode(path string) (Mode, error) {
	resolved, err := resolveDirectory(path)
	if err != nil {
		return "", err
	}
	if git.IsGitRepo(resolved) {
		return ModeGit, nil
	}
	return ModeDirectory, nil
}

func New(path, sessionName, branch string) (Workspace, string, error) {
	resolved, err := resolveDirectory(path)
	if err != nil {
		return nil, "", err
	}
	if git.IsGitRepo(resolved) {
		return NewGit(resolved, sessionName, branch)
	}
	return NewShadow(resolved, sessionName)
}

func resolveDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path %q: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path %q: %w", absolute, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat workspace path %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path %q is not a directory", resolved)
	}
	return resolved, nil
}
