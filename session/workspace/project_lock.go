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

var (
	projectLockAfterDirectoryVerificationHook func()
	projectLockXattrProbe                     = func(string) error { return nil }
)

// AcquireProjectLock acquires a project's advisory lock without waiting.
func AcquireProjectLock(projectPath, operation string) (*ProjectLock, error) {
	if err := platformVerifyProjectLock(); err != nil {
		return nil, err
	}
	cleanProjectPath := filepath.Clean(projectPath)
	projectID := filepath.Base(cleanProjectPath)
	if err := validateProjectLockID(projectID); err != nil {
		return nil, err
	}
	shadowProjects := filepath.Dir(cleanProjectPath)
	if err := os.MkdirAll(shadowProjects, 0o700); err != nil {
		return nil, fmt.Errorf("create shadow projects directory: %w", err)
	}
	shadowDirectory, err := platformOpenTrustedDirectory(shadowProjects)
	if err != nil {
		return nil, fmt.Errorf("open shadow projects directory: %w", err)
	}
	defer shadowDirectory.Close()
	lockDirectory, err := platformOpenOrCreateLockDirectoryAt(shadowDirectory)
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
	file, err := platformOpenProjectLockAt(lockDirectory, projectID)
	if err != nil {
		return nil, fmt.Errorf("open project lock: %w", err)
	}
	if err := platformVerifyDirectoryEntry(shadowDirectory, ".locks", lockDirectory); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("project lock directory changed: %w", err)
	}
	if err := platformAcquireProjectLock(file); err != nil {
		closeErr := file.Close()
		if isProjectLockContention(err) {
			return nil, errors.Join(&OperationInProgressError{ProjectID: projectID, Operation: operation}, closeErr)
		}
		return nil, errors.Join(fmt.Errorf("acquire project lock: %w", err), closeErr)
	}
	if err := platformVerifyDirectoryEntry(shadowDirectory, ".locks", lockDirectory); err != nil {
		return nil, errors.Join(fmt.Errorf("project lock directory changed: %w", err), platformReleaseProjectLock(file), file.Close())
	}
	return &ProjectLock{file: file}, nil
}

func validateProjectLockID(projectID string) error {
	if projectID == "" || projectID == "." || projectID == ".." || projectID == ".locks" || filepath.Base(projectID) != projectID {
		return fmt.Errorf("invalid project ID %q", projectID)
	}
	return nil
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
