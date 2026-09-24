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
	file        *os.File
	coordinator *os.File
}

var projectLockAfterDirectoryVerificationHook func()

// AcquireProjectLock acquires a project's advisory lock without waiting.
func AcquireProjectLock(projectPath, operation string) (*ProjectLock, error) {
	if err := platformVerifyProjectLock(); err != nil {
		return nil, err
	}
	projectID := filepath.Base(filepath.Clean(projectPath))
	shadowProjects := filepath.Dir(filepath.Clean(projectPath))
	if err := os.MkdirAll(shadowProjects, 0o700); err != nil {
		return nil, fmt.Errorf("create shadow projects directory: %w", err)
	}
	shadowDirectory, err := platformOpenTrustedDirectory(shadowProjects)
	if err != nil {
		return nil, fmt.Errorf("open shadow projects directory: %w", err)
	}
	defer shadowDirectory.Close()
	lockDirectory, err := platformOpenOrCreateDirectoryAt(shadowDirectory, ".locks", 0o700)
	if err != nil {
		return nil, fmt.Errorf("open project lock directory: %w", err)
	}
	defer lockDirectory.Close()
	if err := platformVerifyDirectoryEntry(shadowDirectory, ".locks", lockDirectory); err != nil {
		return nil, fmt.Errorf("project lock directory changed: %w", err)
	}
	if projectLockAfterDirectoryVerificationHook != nil {
		projectLockAfterDirectoryVerificationHook()
	}
	file, coordinator, err := platformOpenProjectLockAt(shadowDirectory, lockDirectory, projectID)
	if err != nil {
		return nil, fmt.Errorf("open project lock: %w", err)
	}
	if err := platformAcquireProjectLock(coordinator); err != nil {
		closeErr := errors.Join(file.Close(), coordinator.Close())
		if isProjectLockContention(err) {
			return nil, errors.Join(&OperationInProgressError{ProjectID: projectID, Operation: operation}, closeErr)
		}
		return nil, errors.Join(fmt.Errorf("acquire project lock: %w", err), closeErr)
	}
	return &ProjectLock{file: file, coordinator: coordinator}, nil
}

// Close releases the project lock.
func (l *ProjectLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	coordinator := l.coordinator
	l.file = nil
	l.coordinator = nil
	return errors.Join(platformReleaseProjectLock(coordinator), file.Close(), coordinator.Close())
}
