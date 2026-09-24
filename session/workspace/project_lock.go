package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// OperationInProgressError reports that another process holds a project's operation lock.
type OperationInProgressError struct {
	ProjectID string
	Operation string
}

func (e *OperationInProgressError) Error() string {
	return fmt.Sprintf("cannot %s project %q: another operation is in progress", e.Operation, e.ProjectID)
}

// ProjectLock is an advisory, process-scoped lock for one shadow project.
type ProjectLock struct {
	file *os.File
}

// AcquireProjectLock acquires a project's advisory lock without waiting.
func AcquireProjectLock(projectPath, operation string) (*ProjectLock, error) {
	projectID := filepath.Base(filepath.Clean(projectPath))
	shadowProjects := filepath.Dir(filepath.Clean(projectPath))
	if err := os.MkdirAll(shadowProjects, 0o700); err != nil {
		return nil, fmt.Errorf("create shadow projects directory: %w", err)
	}
	shadowProjects, err := filepath.EvalSymlinks(shadowProjects)
	if err != nil {
		return nil, fmt.Errorf("resolve shadow projects directory: %w", err)
	}
	lockRoot := filepath.Join(shadowProjects, ".locks")
	if err := ensureRealContainedPath(shadowProjects, lockRoot, true, "project lock directory"); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(lockRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create project lock directory: %w", err)
	}
	if err := ensureRealContainedPath(shadowProjects, lockRoot, false, "project lock directory"); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(lockRoot, projectID+".lock")
	if err := ensureRealContainedPath(shadowProjects, lockPath, true, "project lock file"); err != nil {
		return nil, err
	}
	file, err := platformOpenProjectLock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("open project lock: %w", err)
	}
	if err := platformAcquireProjectLock(file); err != nil {
		closeErr := file.Close()
		if isProjectLockContention(err) {
			return nil, errors.Join(&OperationInProgressError{ProjectID: projectID, Operation: operation}, closeErr)
		}
		return nil, errors.Join(fmt.Errorf("acquire project lock: %w", err), closeErr)
	}
	return &ProjectLock{file: file}, nil
}

// Close releases the project lock.
func (l *ProjectLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return errors.Join(platformReleaseProjectLock(file), file.Close())
}
