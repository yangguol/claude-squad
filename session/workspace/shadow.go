package workspace

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"claude-squad/config"
	gitpkg "claude-squad/session/git"
)

const shadowProjectsDirectory = "shadow-projects"

type shadowProjectMetadata struct {
	Version    int    `json:"version"`
	ProjectID  string `json:"project_id"`
	SourcePath string `json:"source_path"`
}

type shadowWorkspace struct {
	data   ShadowData
	access AccessState
}

var lifecycleAccessStatePublishedHook func()

func NewShadow(source, sessionName string) (result Workspace, branchName string, returnErr error) {
	canonical, err := resolveDirectory(source)
	if err != nil {
		return nil, "", err
	}
	projectID := shadowProjectID(canonical)
	root, err := shadowRoot()
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, "", fmt.Errorf("create shadow root: %w", err)
	}
	projectPath := filepath.Join(root, projectID)
	lock, err := AcquireProjectLock(projectPath, "create session")
	if err != nil {
		return nil, "", err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	repoPath := filepath.Join(projectPath, "repo")
	if err := ensureRealContainedPath(root, projectPath, true, "project path"); err != nil {
		return nil, "", err
	}
	if err := ensureShadowProject(projectPath, repoPath, shadowProjectMetadata{
		Version: CurrentVersion, ProjectID: projectID, SourcePath: canonical,
	}); err != nil {
		return nil, "", err
	}
	if report, err := recoverProjectLocked(projectPath, nil); err != nil {
		return nil, "", err
	} else if access := accessStateFromReport(report); access.ReadOnly {
		return nil, "", errors.New(access.Message)
	}

	policy, err := LoadPolicy(canonical)
	if err != nil {
		return nil, "", err
	}
	snapshot, err := Scan(canonical, policy)
	if err != nil {
		return nil, "", err
	}
	baseline, err := importSnapshot(repoPath, canonical, snapshot)
	if err != nil {
		return nil, "", err
	}
	sessionID, err := newSessionID()
	if err != nil {
		return nil, "", err
	}
	branch := "session/" + sessionID
	if _, err := runGit(repoPath, nil, "update-ref", "refs/heads/"+branch, baseline); err != nil {
		return nil, "", fmt.Errorf("create shadow branch: %w", err)
	}

	shared, err := sharedDirectories(canonical, policy)
	if err != nil {
		_, _ = runGit(repoPath, nil, "update-ref", "-d", "refs/heads/"+branch)
		return nil, "", err
	}
	workspace := &shadowWorkspace{data: ShadowData{
		ProjectID: projectID, SourcePath: canonical, RepoPath: repoPath,
		WorktreePath: filepath.Join(projectPath, "worktrees", sessionID),
		SessionID:    sessionID, SessionName: sessionName, BranchName: branch,
		BaselineCommit: baseline, PolicySnapshot: snapshotPolicy(policy), SharedLinks: shared,
	}}
	return workspace, branch, nil
}

func RestoreShadow(data Data) (result Workspace, returnErr error) {
	return restoreShadow(data, nil)
}

// RestoreShadowRecovered constructs a shadow workspace after the caller has
// already recovered the entire project and supplies that project's access state.
func RestoreShadowRecovered(data Data, access AccessState) (Workspace, error) {
	return restoreShadow(data, &access)
}

func restoreShadowLocked(data Data, access AccessState) (Workspace, error) {
	if data.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported workspace data version %d", data.Version)
	}
	if data.Mode != ModeDirectory || data.Shadow == nil {
		return nil, errors.New("invalid shadow workspace data")
	}
	stored := *data.Shadow
	if err := validateShadowData(stored); err != nil {
		return nil, err
	}
	if err := validateShadowIdentity(stored, false); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(stored.WorktreePath); err == nil {
		valid, validateErr := validateWorktreeIdentity(stored)
		if validateErr != nil || !valid {
			return nil, errors.New("shadow worktree identity is invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat shadow worktree: %w", err)
	}
	if lifecycleAccessStatePublishedHook != nil {
		lifecycleAccessStatePublishedHook()
	}
	return &shadowWorkspace{data: stored, access: access}, nil
}

func restoreShadow(data Data, recoveredAccess *AccessState) (result Workspace, returnErr error) {
	if data.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported workspace data version %d", data.Version)
	}
	if data.Mode != ModeDirectory {
		return nil, fmt.Errorf("cannot restore shadow workspace from mode %q", data.Mode)
	}
	if data.Shadow == nil {
		return nil, errors.New("shadow workspace data is missing")
	}
	stored := *data.Shadow
	if err := validateShadowData(stored); err != nil {
		return nil, err
	}
	if err := validateShadowIdentity(stored, false); err != nil {
		return nil, err
	}
	projectPath := filepath.Dir(stored.RepoPath)
	lock, err := AcquireProjectLock(projectPath, "restore session")
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	access := AccessState{}
	if recoveredAccess != nil {
		access = *recoveredAccess
	} else {
		report, err := recoverProjectLocked(projectPath, map[string]*ShadowData{stored.SessionID: &stored})
		if err != nil {
			return nil, fmt.Errorf("recover project: %w", err)
		}
		access = accessStateFromReport(report)
	}
	if _, err := os.Lstat(stored.WorktreePath); err == nil {
		valid, validateErr := validateWorktreeIdentity(stored)
		if validateErr != nil || !valid {
			return nil, errors.New("shadow worktree identity is invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat shadow worktree: %w", err)
	}
	return &shadowWorkspace{data: stored, access: access}, nil
}

func (w *shadowWorkspace) Mode() Mode { return ModeDirectory }

func (w *shadowWorkspace) Capabilities() Capabilities {
	return Capabilities{Apply: !w.access.ReadOnly, Resume: !w.access.ReadOnly}
}

func (w *shadowWorkspace) AccessState() AccessState {
	state := w.access
	state.Paths = append([]string(nil), state.Paths...)
	state.RetainedPaths = append([]string(nil), state.RetainedPaths...)
	return state
}

func (w *shadowWorkspace) WorkDir() string     { return w.data.WorktreePath }
func (w *shadowWorkspace) DisplayName() string { return filepath.Base(w.data.SourcePath) }
func (w *shadowWorkspace) BranchName() string  { return w.data.BranchName }

func (w *shadowWorkspace) Setup() (returnErr error) {
	projectPath := filepath.Dir(w.data.RepoPath)
	lock, err := AcquireProjectLock(projectPath, "resume")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	return w.setupLocked()
}

func (w *shadowWorkspace) refreshAccessLocked() error {
	projectPath := filepath.Dir(w.data.RepoPath)
	report, err := recoverProjectLocked(projectPath, map[string]*ShadowData{w.data.SessionID: &w.data})
	if err != nil {
		return err
	}
	w.access = accessStateFromReport(report)
	if lifecycleAccessStatePublishedHook != nil {
		lifecycleAccessStatePublishedHook()
	}
	if w.access.ReadOnly {
		return fmt.Errorf("workspace is read-only: %s", w.access.Message)
	}
	return nil
}

func (w *shadowWorkspace) setupLocked() error {
	if err := w.refreshAccessLocked(); err != nil {
		return err
	}
	return w.ensureWorktreeLocked()
}

func (w *shadowWorkspace) ensureWorktreeLocked() error {
	if err := validateShadowData(w.data); err != nil {
		return err
	}
	if err := validateShadowIdentity(w.data, false); err != nil {
		return err
	}
	valid, err := w.IsValid()
	if err != nil {
		return err
	}
	if valid {
		return nil
	}
	if err := ensureRealContainedPath(filepath.Dir(w.data.RepoPath), filepath.Dir(w.data.WorktreePath), true, "worktrees path"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(w.data.WorktreePath), 0o700); err != nil {
		return fmt.Errorf("create shadow worktree directory: %w", err)
	}
	if err := ensureRealContainedPath(filepath.Dir(w.data.RepoPath), filepath.Dir(w.data.WorktreePath), false, "worktrees path"); err != nil {
		return err
	}
	if err := removeContainedPath(filepath.Dir(w.data.RepoPath), w.data.WorktreePath); err != nil {
		return err
	}
	if _, err := runGit(w.data.RepoPath, nil, "worktree", "prune"); err != nil {
		return fmt.Errorf("prune stale shadow worktrees: %w", err)
	}
	if _, err := runGit(w.data.RepoPath, nil, "worktree", "add", "--", w.data.WorktreePath, w.data.BranchName); err != nil {
		return fmt.Errorf("create shadow worktree: %w", err)
	}
	if err := w.createSharedLinks(); err != nil {
		_, _ = runGit(w.data.RepoPath, nil, "worktree", "remove", "--force", w.data.WorktreePath)
		_ = os.RemoveAll(w.data.WorktreePath)
		_, _ = runGit(w.data.RepoPath, nil, "worktree", "prune")
		return err
	}
	return nil
}

func (w *shadowWorkspace) IsValid() (bool, error) {
	if err := validateShadowData(w.data); err != nil {
		return false, err
	}
	if err := validateShadowIdentity(w.data, false); err != nil {
		return false, err
	}
	info, err := os.Lstat(w.data.WorktreePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat shadow worktree: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	if err := ensureRealContainedPath(filepath.Dir(w.data.RepoPath), w.data.WorktreePath, false, "worktree path"); err != nil {
		return false, err
	}
	valid, err := validateWorktreeIdentity(w.data)
	if err != nil || !valid {
		return false, err
	}
	mergeBase, err := runGit(w.data.RepoPath, nil, "merge-base", "--is-ancestor", w.data.BaselineCommit, "refs/heads/"+w.data.BranchName)
	return err == nil && strings.TrimSpace(mergeBase) == "", nil
}

func (w *shadowWorkspace) IsDirty() (bool, error) {
	output, err := runGit(w.data.WorktreePath, nil, append([]string{"status", "--porcelain", "--"}, w.pathspecs()...)...)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) != "", nil
}

func (w *shadowWorkspace) IsBranchCheckedOut() (bool, error) {
	return false, &ErrUnsupported{Operation: "branch checkout check"}
}

func (w *shadowWorkspace) Diff() *gitpkg.DiffStats {
	stats := &gitpkg.DiffStats{}
	if _, err := runGit(w.data.WorktreePath, nil, append([]string{"add", "-N", "--"}, w.pathspecs()...)...); err != nil {
		stats.Error = err
		return stats
	}
	output, err := runGit(w.data.WorktreePath, nil, append([]string{"--no-pager", "diff", w.data.BaselineCommit, "--"}, w.pathspecs()...)...)
	if err != nil {
		stats.Error = err
		return stats
	}
	stats.Content = output
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			stats.Added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			stats.Removed++
		}
	}
	return stats
}

func (w *shadowWorkspace) DiffNumstat() *gitpkg.DiffStats {
	stats := &gitpkg.DiffStats{}
	if _, err := runGit(w.data.WorktreePath, nil, append([]string{"add", "-N", "--"}, w.pathspecs()...)...); err != nil {
		stats.Error = err
		return stats
	}
	output, err := runGit(w.data.WorktreePath, nil, append([]string{"--no-pager", "diff", "--numstat", w.data.BaselineCommit, "--"}, w.pathspecs()...)...)
	if err != nil {
		stats.Error = err
		return stats
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 2 {
			continue
		}
		added, addErr := strconv.Atoi(fields[0])
		removed, removeErr := strconv.Atoi(fields[1])
		if addErr == nil && removeErr == nil {
			stats.Added += added
			stats.Removed += removed
		}
	}
	return stats
}

func (w *shadowWorkspace) CommitChanges(message string) error {
	dirty, err := w.IsDirty()
	if err != nil || !dirty {
		return err
	}
	if _, err := runGit(w.data.WorktreePath, nil, append([]string{"add", "-A", "--"}, w.pathspecs()...)...); err != nil {
		return fmt.Errorf("stage shadow changes: %w", err)
	}
	if _, err := runGit(w.data.WorktreePath, nil, "commit", "-m", message); err != nil {
		return fmt.Errorf("commit shadow changes: %w", err)
	}
	return nil
}

func (w *shadowWorkspace) PushChanges(string, bool) error {
	return &ErrUnsupported{Operation: "push"}
}

func (w *shadowWorkspace) Remove() error {
	if err := validateShadowData(w.data); err != nil {
		return err
	}
	if err := validateShadowIdentity(w.data, false); err != nil {
		return err
	}
	if err := ensureRealContainedPath(filepath.Dir(w.data.RepoPath), w.data.WorktreePath, true, "worktree path"); err != nil {
		return err
	}
	if _, err := os.Lstat(w.data.WorktreePath); err == nil {
		if _, err := runGit(w.data.RepoPath, nil, "worktree", "remove", "--force", "--", w.data.WorktreePath); err != nil {
			return fmt.Errorf("remove shadow worktree: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat shadow worktree: %w", err)
	}
	if err := w.Prune(); err != nil {
		return err
	}
	registered, err := w.worktreeRegistered()
	if err != nil {
		return err
	}
	if registered {
		return errors.New("shadow worktree remains registered after removal")
	}
	return nil
}

func (w *shadowWorkspace) Prune() error {
	if err := validateShadowData(w.data); err != nil {
		return err
	}
	if err := ensureRealContainedPath(filepath.Dir(w.data.RepoPath), w.data.RepoPath, false, "repository path"); err != nil {
		return err
	}
	if err := validateGitMetadata(w.data.RepoPath); err != nil {
		return err
	}
	_, err := runGit(w.data.RepoPath, nil, "worktree", "prune")
	return err
}

func (w *shadowWorkspace) Cleanup() error {
	if err := w.Remove(); err != nil {
		return err
	}
	checkedOut, err := w.branchRegistered()
	if err != nil {
		return err
	}
	if checkedOut {
		return errors.New("shadow branch remains checked out after worktree removal")
	}
	if _, err := runGit(w.data.RepoPath, nil, "update-ref", "-d", "refs/heads/"+w.data.BranchName); err != nil {
		return fmt.Errorf("delete shadow branch: %w", err)
	}
	return nil
}

func (w *shadowWorkspace) Serialize() Data {
	copy := w.data
	copy.PolicySnapshot = append([]PolicyRuleData(nil), w.data.PolicySnapshot...)
	copy.SharedLinks = append([]string(nil), w.data.SharedLinks...)
	return Data{Version: CurrentVersion, Mode: ModeDirectory, Shadow: &copy}
}

func (w *shadowWorkspace) createSharedLinks() error {
	for _, relative := range w.data.SharedLinks {
		source := filepath.Join(w.data.SourcePath, filepath.FromSlash(relative))
		if err := ensureRealContainedPath(w.data.SourcePath, source, false, "shared source path"); err != nil {
			return fmt.Errorf("validate shared directory %q: %w", relative, err)
		}
		info, err := os.Lstat(source)
		if err != nil {
			return fmt.Errorf("validate shared directory %q: %w", relative, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("shared path %q is not a source directory", relative)
		}
		destination := filepath.Join(w.data.WorktreePath, filepath.FromSlash(relative))
		if err := validateContained(w.data.WorktreePath, destination, "shared link destination"); err != nil {
			return err
		}
		if _, err := os.Lstat(destination); err == nil {
			return fmt.Errorf("shared link destination %q already exists", relative)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect shared link destination %q: %w", relative, err)
		}
		if err := ensureNoSymlinkParents(w.data.WorktreePath, filepath.Dir(destination)); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return fmt.Errorf("create shared link parent for %q: %w", relative, err)
		}
		if err := os.Symlink(source, destination); err != nil {
			return fmt.Errorf("create shared link %q: %w", relative, err)
		}
	}
	return nil
}

func (w *shadowWorkspace) pathspecs() []string {
	result := []string{"."}
	for _, relative := range w.data.SharedLinks {
		result = append(result, ":(exclude)"+relative)
	}
	return result
}

func ensureShadowProject(projectPath, repoPath string, expected shadowProjectMetadata) error {
	if info, err := os.Lstat(projectPath); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("shadow project path is not a real directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect shadow project: %w", err)
	}
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		return fmt.Errorf("create shadow project: %w", err)
	}
	metadataPath := filepath.Join(projectPath, "metadata.json")
	metadata, err := readProjectMetadata(projectPath)
	if errors.Is(err, os.ErrNotExist) {
		contents, marshalErr := json.Marshal(expected)
		if marshalErr != nil {
			return marshalErr
		}
		file, createErr := os.OpenFile(metadataPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			if errors.Is(createErr, os.ErrExist) {
				metadata, err = readProjectMetadata(projectPath)
			} else {
				return fmt.Errorf("create shadow metadata: %w", createErr)
			}
		} else {
			_, writeErr := file.Write(contents)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				_ = os.Remove(metadataPath)
				return fmt.Errorf("write shadow metadata: %w", errors.Join(writeErr, closeErr))
			}
			metadata = expected
			err = nil
		}
	}
	if err != nil {
		return err
	}
	if err := validateProjectMetadata(metadata, expected.ProjectID, expected.SourcePath); err != nil {
		return err
	}
	if err := ensureRealContainedPath(projectPath, repoPath, true, "repository path"); err != nil {
		return err
	}
	if err := os.MkdirAll(repoPath, 0o700); err != nil {
		return fmt.Errorf("create shadow repository: %w", err)
	}
	if err := ensureRealContainedPath(projectPath, repoPath, false, "repository path"); err != nil {
		return err
	}
	gitMetadata := filepath.Join(repoPath, ".git")
	if _, err := os.Lstat(gitMetadata); errors.Is(err, os.ErrNotExist) {
		if _, initErr := runGit(repoPath, nil, "init", "--", repoPath); initErr != nil {
			return fmt.Errorf("initialize shadow repository: %w", initErr)
		}
	} else if err != nil {
		return fmt.Errorf("inspect shadow git metadata: %w", err)
	}
	if err := validateGitMetadata(repoPath); err != nil {
		return err
	}
	for key, value := range map[string]string{"user.name": "Claude Squad", "user.email": "claude-squad@local"} {
		current, err := runGit(repoPath, nil, "config", "--local", "--get", key)
		if err != nil || strings.TrimSpace(current) != value {
			if _, err := runGit(repoPath, nil, "config", "--local", key, value); err != nil {
				return fmt.Errorf("configure shadow repository %s: %w", key, err)
			}
		}
	}
	return validateGitMetadata(repoPath)
}

func importSnapshot(repoPath, source string, snapshot Snapshot) (string, error) {
	return importSnapshotIn(repoPath, source, snapshot, filepath.Dir(repoPath))
}

func importSnapshotIn(repoPath, source string, snapshot Snapshot, temporaryDirectory string) (string, error) {
	index, err := os.CreateTemp(temporaryDirectory, "index-*")
	if err != nil {
		return "", fmt.Errorf("create temporary index: %w", err)
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(indexPath); err != nil {
		return "", err
	}
	defer os.Remove(indexPath)
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	for _, relative := range snapshot.SortedPaths() {
		entry := snapshot.Entries[relative]
		var contents []byte
		mode := "100644"
		switch entry.Type {
		case EntryRegular:
			path := filepath.Join(source, filepath.FromSlash(relative))
			if err := ensureRealContainedPath(source, path, false, "snapshot source path"); err != nil {
				return "", fmt.Errorf("import %q: %w", relative, err)
			}
			info, statErr := os.Lstat(path)
			if statErr != nil || !info.Mode().IsRegular() {
				return "", fmt.Errorf("import %q: source is no longer a regular file", relative)
			}
			if info.Mode().Perm() != entry.Mode.Perm() {
				return "", fmt.Errorf("import %q: source mode changed after scan", relative)
			}
			file, openErr := os.Open(path)
			if openErr != nil {
				return "", fmt.Errorf("import %q: %w", relative, openErr)
			}
			openedInfo, statErr := file.Stat()
			if statErr != nil || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm() != entry.Mode.Perm() || !os.SameFile(info, openedInfo) {
				_ = file.Close()
				return "", fmt.Errorf("import %q: source changed while opening", relative)
			}
			contents, err = io.ReadAll(file)
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				return "", fmt.Errorf("import %q: %w", relative, errors.Join(err, closeErr))
			}
			finalInfo, statErr := os.Lstat(path)
			if statErr != nil || !finalInfo.Mode().IsRegular() || finalInfo.Mode().Perm() != entry.Mode.Perm() || !os.SameFile(info, finalInfo) {
				return "", fmt.Errorf("import %q: source changed while reading", relative)
			}
			sum := sha256.Sum256(contents)
			if hex.EncodeToString(sum[:]) != entry.ContentHash {
				return "", fmt.Errorf("import %q: source content changed after scan", relative)
			}
			if entry.Mode&0o111 != 0 {
				mode = "100755"
			}
		case EntrySymlink:
			path := filepath.Join(source, filepath.FromSlash(relative))
			if err := validateContained(source, path, "snapshot source path"); err != nil {
				return "", fmt.Errorf("import %q: %w", relative, err)
			}
			if err := ensureRealContainedPath(source, filepath.Dir(path), false, "snapshot source parent"); err != nil {
				return "", fmt.Errorf("import %q: %w", relative, err)
			}
			info, statErr := os.Lstat(path)
			if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
				return "", fmt.Errorf("import %q: source is no longer a symlink", relative)
			}
			target, readErr := os.Readlink(path)
			if readErr != nil || target != entry.SymlinkTarget {
				return "", fmt.Errorf("import %q: symlink target changed after scan", relative)
			}
			finalInfo, statErr := os.Lstat(path)
			finalTarget, readErr := os.Readlink(path)
			if statErr != nil || finalInfo.Mode()&os.ModeSymlink == 0 || readErr != nil || finalTarget != target {
				return "", fmt.Errorf("import %q: symlink changed while reading", relative)
			}
			contents = []byte(target)
			mode = "120000"
		default:
			return "", fmt.Errorf("unsupported snapshot entry type %q", entry.Type)
		}
		hash, hashErr := runGitInput(repoPath, env, contents, "hash-object", "-w", "--stdin")
		if hashErr != nil {
			return "", fmt.Errorf("import %q: %w", relative, hashErr)
		}
		if _, err := runGit(repoPath, env, "update-index", "--add", "--cacheinfo", mode, strings.TrimSpace(hash), relative); err != nil {
			return "", fmt.Errorf("index %q: %w", relative, err)
		}
	}
	tree, err := runGit(repoPath, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write baseline tree: %w", err)
	}
	commit, err := runGit(repoPath, nil, "commit-tree", strings.TrimSpace(tree), "-m", "Claude Squad session baseline")
	if err != nil {
		return "", fmt.Errorf("create baseline commit: %w", err)
	}
	return strings.TrimSpace(commit), nil
}

func sharedDirectories(source string, policy Policy) ([]string, error) {
	var result []string
	err := filepath.WalkDir(source, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == source {
			return nil
		}
		relative, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() && policy.ClassifyEntry(relative, true) == ClassShared {
			result = append(result, relative)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan shared directories: %w", err)
	}
	return result, nil
}

func snapshotPolicy(policy Policy) []PolicyRuleData {
	result := make([]PolicyRuleData, 0, len(policy.rules))
	for _, rule := range policy.rules {
		result = append(result, PolicyRuleData{Pattern: rule.pattern, Class: rule.class, Negate: rule.negate})
	}
	return result
}

func shadowProjectID(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

func newSessionID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

// ValidateStoredShadowReferences verifies raw persisted state before reset can
// overwrite it. Any malformed record fails closed.
func ValidateStoredShadowReferences() error {
	root, err := shadowRoot()
	if err != nil {
		return err
	}
	_, err = storedShadowProjectReferences(root)
	return err
}

var (
	resetProjectLockedHook    func(string)
	resetAfterEnumerationHook func()
	resetAfterValidationHook  func()
)

// WithResetPreflight validates stored references, locks every shadow project in
// canonical path order, recovers safe transactions, and keeps the locks held
// while run performs destructive reset cleanup.
func WithResetPreflight(run func() error) error {
	return withResetPreflight(func(_ func() error) error { return run() })
}

// WithReset runs reset with a shadow cleanup function bound to the exact set of
// projects validated under lock. Callers must use that function instead of
// independently enumerating projects during the destructive sequence.
func WithReset(run func(cleanupShadowProjects func() error) error) error {
	return withResetPreflight(run)
}

func withResetPreflight(run func(func() error) error) (returnErr error) {
	root, err := shadowRoot()
	if err != nil {
		return err
	}
	references, err := storedShadowProjectReferences(root)
	if err != nil {
		return err
	}
	projects, err := enumerateShadowProjects(root)
	if err != nil {
		return err
	}
	if resetAfterEnumerationHook != nil {
		resetAfterEnumerationHook()
	}

	locks := make([]*ProjectLock, 0, len(projects))
	defer func() {
		for index := len(locks) - 1; index >= 0; index-- {
			returnErr = errors.Join(returnErr, locks[index].Close())
		}
	}()
	for _, projectPath := range projects {
		lock, err := AcquireProjectLock(projectPath, "reset")
		if err != nil {
			return err
		}
		locks = append(locks, lock)
		if resetProjectLockedHook != nil {
			resetProjectLockedHook(projectPath)
		}
	}
	if err := validateResetSnapshot(root, references, projects); err != nil {
		return err
	}

	// Classify every locked project without mutation before recovering any one
	// project. This makes the reset decision global rather than project-ordered.
	for _, projectPath := range projects {
		metadata, err := readProjectMetadata(projectPath)
		if err != nil {
			return fmt.Errorf("validate shadow project %q: %w", filepath.Base(projectPath), err)
		}
		if err := validateCleanupMetadata(metadata, filepath.Base(projectPath)); err != nil {
			return err
		}
		discovered, err := classifyTransactions(projectPath)
		if err != nil {
			return fmt.Errorf("classify shadow project %q: %w", filepath.Base(projectPath), err)
		}
		if access := accessStateFromReport(classificationReport(discovered)); access.ReadOnly {
			return fmt.Errorf("shadow project %q requires explicit recovery: %s", filepath.Base(projectPath), access.Message)
		}
	}
	for _, projectPath := range projects {
		report, err := recoverProjectLocked(projectPath, nil)
		if err != nil {
			return fmt.Errorf("recover shadow project %q: %w", filepath.Base(projectPath), err)
		}
		if access := accessStateFromReport(report); access.ReadOnly {
			return fmt.Errorf("shadow project %q requires explicit recovery: %s", filepath.Base(projectPath), access.Message)
		}
	}
	if err := validateResetSnapshot(root, references, projects); err != nil {
		return err
	}
	if resetAfterValidationHook != nil {
		resetAfterValidationHook()
	}
	cleanup := func() error { return cleanupShadowProjects(root, projects) }
	return run(cleanup)
}

func enumerateShadowProjects(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read shadow project root: %w", err)
	}
	projects := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".locks" {
			continue
		}
		projectPath := filepath.Join(root, entry.Name())
		if err := validateShadowProjectCandidate(root, projectPath, entry); err != nil {
			return nil, err
		}
		projects = append(projects, projectPath)
	}
	sort.Strings(projects)
	return projects, nil
}

func validateResetSnapshot(root string, references map[string]bool, projects []string) error {
	currentReferences, err := storedShadowProjectReferences(root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(references, currentReferences) {
		return errors.New("persisted shadow references changed while locking")
	}
	currentProjects, err := enumerateShadowProjects(root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(projects, currentProjects) {
		return errors.New("shadow project set changed while locking")
	}
	return nil
}

// CleanupShadowProjects removes validated shadow projects that are not referenced
// by persisted sessions. Invalid projects and incomplete apply transactions are
// preserved and reported so reset cannot silently discard recoverable state.
func cleanupShadowProjects(root string, projects []string) error {
	current, err := enumerateShadowProjects(root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(projects, current) {
		return errors.New("shadow project set changed after validation")
	}
	for _, projectPath := range projects {
		active, err := shadowProjectHasActiveSessions(projectPath)
		if err != nil {
			return fmt.Errorf("inspect shadow project %q active sessions: %w", filepath.Base(projectPath), err)
		}
		if active {
			continue
		}
		if err := removeShadowProject(root, projectPath); err != nil {
			return err
		}
	}
	return nil
}

func CleanupShadowProjects() error {
	root, err := shadowRoot()
	if err != nil {
		return err
	}
	referenced, referenceErr := storedShadowProjectReferences(root)
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read shadow project root: %w", err)
	}

	var errs []error
	if referenceErr != nil {
		errs = append(errs, referenceErr)
	}
	for _, entry := range entries {
		if entry.Name() == ".locks" {
			continue
		}
		projectPath := filepath.Join(root, entry.Name())
		if err := validateShadowProjectCandidate(root, projectPath, entry); err != nil {
			errs = append(errs, err)
			continue
		}
		metadata, err := readProjectMetadata(projectPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("validate shadow project %q: %w", entry.Name(), err))
			continue
		}
		if err := validateCleanupMetadata(metadata, entry.Name()); err != nil {
			errs = append(errs, err)
			continue
		}
		incomplete, err := FindIncompleteTransactions(projectPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("inspect shadow project %q transactions: %w", entry.Name(), err))
			continue
		}
		if len(incomplete) != 0 {
			directories := make([]string, len(incomplete))
			for index := range incomplete {
				directories[index] = incomplete[index].Directory
			}
			errs = append(errs, fmt.Errorf("shadow project %q has incomplete apply transactions: %s", entry.Name(), strings.Join(directories, ", ")))
			continue
		}
		if referenceErr != nil || referenced[entry.Name()] {
			continue
		}
		active, err := shadowProjectHasActiveSessions(projectPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("inspect shadow project %q active sessions: %w", entry.Name(), err))
			continue
		}
		if active {
			continue
		}
		if err := removeShadowProject(root, projectPath); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func storedShadowProjectReferences(root string) (map[string]bool, error) {
	references := make(map[string]bool)
	configDir := filepath.Dir(root)
	info, err := os.Lstat(configDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("shadow config directory is not a real directory")
	}
	resolvedConfigDir, err := filepath.EvalSymlinks(configDir)
	if err != nil || filepath.Clean(resolvedConfigDir) != filepath.Clean(configDir) {
		return nil, errors.New("shadow config directory identity is invalid")
	}
	statePath := filepath.Join(configDir, config.StateFileName)
	info, err = os.Lstat(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return references, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect persisted session state: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("persisted session state is not a regular file")
	}
	contents, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("read persisted session state: %w", err)
	}
	var stateFields map[string]json.RawMessage
	if err := json.Unmarshal(contents, &stateFields); err != nil {
		return nil, fmt.Errorf("decode persisted session state: %w", err)
	}
	instancesJSON, present := stateFields["instances"]
	if !present || string(instancesJSON) == "null" {
		return nil, errors.New("persisted session state has missing or null instances")
	}
	var instances []json.RawMessage
	if err := json.Unmarshal(instancesJSON, &instances); err != nil {
		return nil, fmt.Errorf("decode persisted instances: %w", err)
	}
	var errs []error
	for index, rawInstance := range instances {
		var fields map[string]json.RawMessage
		if len(rawInstance) == 0 || string(rawInstance) == "null" || json.Unmarshal(rawInstance, &fields) != nil || fields == nil {
			errs = append(errs, fmt.Errorf("reject persisted instance %d: malformed instance object", index))
			continue
		}
		var title string
		if rawTitle, ok := fields["title"]; ok {
			_ = json.Unmarshal(rawTitle, &title)
		}
		rawWorkspace, hasWorkspace := fields["workspace"]
		if !hasWorkspace {
			// Legacy Git records store only worktree data and remain valid when
			// they contain enough data to restore a Git workspace.
			rawWorktree, legacy := fields["worktree"]
			if !legacy {
				errs = append(errs, fmt.Errorf("reject persisted workspace %q: workspace data is missing", title))
				continue
			}
			var worktree GitData
			if string(rawWorktree) == "null" || json.Unmarshal(rawWorktree, &worktree) != nil {
				errs = append(errs, fmt.Errorf("reject persisted legacy Git worktree %q: malformed worktree data", title))
				continue
			}
			if err := validateGitData(worktree); err != nil {
				errs = append(errs, fmt.Errorf("reject persisted legacy Git worktree %q: %w", title, err))
			}
			continue
		}
		if string(rawWorkspace) == "null" {
			errs = append(errs, fmt.Errorf("reject persisted workspace %q: workspace data is null", title))
			continue
		}
		var workspace Data
		if err := json.Unmarshal(rawWorkspace, &workspace); err != nil {
			errs = append(errs, fmt.Errorf("reject persisted workspace %q: %w", title, err))
			continue
		}
		switch workspace.Mode {
		case ModeGit:
			if workspace.Version != CurrentVersion || workspace.Git == nil || workspace.Shadow != nil {
				errs = append(errs, fmt.Errorf("reject persisted Git workspace %q: malformed workspace data", title))
				continue
			}
			if err := validateGitData(*workspace.Git); err != nil {
				errs = append(errs, fmt.Errorf("reject persisted Git workspace %q: %w", title, err))
			}
			continue
		case ModeDirectory:
			if workspace.Version != CurrentVersion || workspace.Shadow == nil || workspace.Git != nil {
				errs = append(errs, fmt.Errorf("reject persisted directory workspace %q: shadow data is missing", title))
				continue
			}
		default:
			errs = append(errs, fmt.Errorf("reject persisted workspace %q: invalid mode %q", title, workspace.Mode))
			continue
		}
		data := *workspace.Shadow
		if len(data.ProjectID) == 64 {
			references[data.ProjectID] = true
		}
		if err := validateStoredShadowReference(root, data); err != nil {
			errs = append(errs, fmt.Errorf("reject persisted shadow workspace %q: %w", title, err))
		}
	}
	if len(errs) != 0 {
		return nil, errors.Join(errs...)
	}
	return references, nil
}

func validateStoredShadowReference(root string, data ShadowData) error {
	if err := validateShadowData(data); err != nil {
		return err
	}
	projectPath := filepath.Join(root, data.ProjectID)
	info, err := os.Lstat(projectPath)
	if err != nil {
		return fmt.Errorf("inspect persisted project path: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("persisted project path is not a real directory")
	}
	if err := ensureRealContainedPath(root, projectPath, false, "persisted project path"); err != nil {
		return err
	}
	metadata, err := readProjectMetadata(projectPath)
	if err != nil {
		return fmt.Errorf("read persisted project metadata: %w", err)
	}
	if err := validateProjectMetadata(metadata, data.ProjectID, data.SourcePath); err != nil {
		return err
	}
	return nil
}

func shadowProjectHasActiveSessions(projectPath string) (bool, error) {
	repoPath := filepath.Join(projectPath, "repo")
	info, err := os.Lstat(repoPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("shadow repository path is not a real directory")
	}
	if err := ensureRealContainedPath(projectPath, repoPath, false, "repository path"); err != nil {
		return false, err
	}
	if err := validateGitMetadata(repoPath); err != nil {
		return false, err
	}
	output, err := runGit(repoPath, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	canonicalRepo, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		path := strings.TrimPrefix(line, "worktree ")
		canonicalPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return false, fmt.Errorf("resolve registered shadow worktree %q: %w", path, err)
		}
		if filepath.Clean(canonicalPath) == filepath.Clean(canonicalRepo) {
			continue
		}
		if err := ensureRealContainedPath(projectPath, canonicalPath, false, "registered worktree path"); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func validateShadowProjectCandidate(root, projectPath string, entry os.DirEntry) error {
	if len(entry.Name()) != 64 {
		return fmt.Errorf("reject shadow project path %q: invalid project ID", projectPath)
	}
	if _, err := hex.DecodeString(entry.Name()); err != nil {
		return fmt.Errorf("reject shadow project path %q: invalid project ID", projectPath)
	}
	info, err := os.Lstat(projectPath)
	if err != nil {
		return fmt.Errorf("inspect shadow project path %q: %w", projectPath, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || entry.Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("reject shadow project path %q: not a real directory", projectPath)
	}
	if err := ensureRealContainedPath(root, projectPath, false, "project path"); err != nil {
		return fmt.Errorf("reject shadow project path %q: %w", projectPath, err)
	}
	return nil
}

func validateCleanupMetadata(metadata shadowProjectMetadata, projectID string) error {
	if metadata.Version != CurrentVersion || metadata.ProjectID != projectID ||
		!filepath.IsAbs(metadata.SourcePath) || filepath.Clean(metadata.SourcePath) != metadata.SourcePath ||
		shadowProjectID(metadata.SourcePath) != projectID {
		return fmt.Errorf("reject shadow project %q: malformed metadata", projectID)
	}
	return nil
}

var cleanupBeforeQuarantine = func(string) {}

func removeShadowProject(root, projectPath string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("shadow root changed before quarantine")
	}
	projectInfo, err := os.Lstat(projectPath)
	if err != nil || !projectInfo.IsDir() || projectInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("shadow project %q changed before quarantine", projectPath)
	}
	if filepath.Dir(projectPath) != root {
		return fmt.Errorf("shadow project %q is not a direct child of shadow root", projectPath)
	}
	if err := ensureRealContainedPath(root, projectPath, false, "project path"); err != nil {
		return err
	}

	cleanupBeforeQuarantine(projectPath)
	currentRootInfo, err := os.Lstat(root)
	if err != nil || !os.SameFile(rootInfo, currentRootInfo) {
		return errors.New("shadow root changed before quarantine")
	}
	currentProjectInfo, err := os.Lstat(projectPath)
	if err != nil || !os.SameFile(projectInfo, currentProjectInfo) || !currentProjectInfo.IsDir() || currentProjectInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("shadow project %q changed before quarantine", projectPath)
	}

	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return fmt.Errorf("generate shadow quarantine name: %w", err)
	}
	quarantine := filepath.Join(root, ".cleanup-"+hex.EncodeToString(random))
	if filepath.Dir(quarantine) != root {
		return errors.New("invalid shadow quarantine path")
	}
	if _, err := os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return fmt.Errorf("shadow quarantine path %q already exists", quarantine)
		}
		return fmt.Errorf("inspect shadow quarantine path %q: %w", quarantine, err)
	}
	if err := os.Rename(projectPath, quarantine); err != nil {
		return fmt.Errorf("quarantine shadow project %q: %w", projectPath, err)
	}
	quarantinedInfo, err := os.Lstat(quarantine)
	if err != nil || !os.SameFile(projectInfo, quarantinedInfo) || !quarantinedInfo.IsDir() || quarantinedInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("quarantined shadow project %q has invalid identity", quarantine)
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return fmt.Errorf("remove quarantined shadow project %q: %w", quarantine, err)
	}
	return nil
}

func shadowRoot() (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("get config directory: %w", err)
	}
	root := filepath.Join(configDir, shadowProjectsDirectory)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create shadow root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve shadow root: %w", err)
	}
	return resolved, nil
}

func readProjectMetadata(projectPath string) (shadowProjectMetadata, error) {
	metadataPath := filepath.Join(projectPath, "metadata.json")
	info, err := os.Lstat(metadataPath)
	if err != nil {
		return shadowProjectMetadata{}, err
	}
	if !info.Mode().IsRegular() {
		return shadowProjectMetadata{}, errors.New("shadow metadata is not a regular file")
	}
	contents, err := os.ReadFile(metadataPath)
	if err != nil {
		return shadowProjectMetadata{}, err
	}
	var metadata shadowProjectMetadata
	if err := json.Unmarshal(contents, &metadata); err != nil {
		return shadowProjectMetadata{}, fmt.Errorf("decode shadow metadata: %w", err)
	}
	return metadata, nil
}

func validateProjectMetadata(metadata shadowProjectMetadata, projectID, source string) error {
	if metadata.Version != CurrentVersion || metadata.ProjectID != projectID || metadata.SourcePath != source {
		return fmt.Errorf("shadow project metadata mismatch for project %q", projectID)
	}
	return nil
}

func validateShadowData(data ShadowData) error {
	fields := []struct{ name, value string }{
		{"project ID", data.ProjectID}, {"source path", data.SourcePath}, {"repository path", data.RepoPath},
		{"worktree path", data.WorktreePath}, {"session ID", data.SessionID}, {"branch name", data.BranchName},
		{"baseline commit", data.BaselineCommit},
	}
	for _, field := range fields {
		if field.value == "" {
			return fmt.Errorf("shadow workspace %s is missing", field.name)
		}
	}
	if len(data.ProjectID) != 64 || shadowProjectID(data.SourcePath) != data.ProjectID {
		return errors.New("shadow workspace project ID does not match source path")
	}
	if len(data.SessionID) != 32 {
		return errors.New("shadow workspace session ID is invalid")
	}
	if _, err := hex.DecodeString(data.SessionID); err != nil {
		return errors.New("shadow workspace session ID is invalid")
	}
	if data.BranchName != "session/"+data.SessionID {
		return errors.New("shadow workspace branch name does not match session ID")
	}
	if len(data.BaselineCommit) != 40 {
		return errors.New("shadow workspace baseline commit is invalid")
	}
	if _, err := hex.DecodeString(data.BaselineCommit); err != nil {
		return errors.New("shadow workspace baseline commit is invalid")
	}
	root, err := shadowRoot()
	if err != nil {
		return err
	}
	projectPath := filepath.Join(root, data.ProjectID)
	if filepath.Clean(data.RepoPath) != filepath.Join(projectPath, "repo") {
		return errors.New("shadow workspace repository path is outside its project")
	}
	if filepath.Clean(data.WorktreePath) != filepath.Join(projectPath, "worktrees", data.SessionID) {
		return errors.New("shadow workspace worktree path is outside its project")
	}
	if err := validateContained(root, data.RepoPath, "repository path"); err != nil {
		return err
	}
	return validateContained(root, data.WorktreePath, "worktree path")
}

func validateShadowIdentity(data ShadowData, requireWorktree bool) error {
	projectPath := filepath.Dir(data.RepoPath)
	if err := ensureRealContainedPath(filepath.Dir(projectPath), projectPath, false, "project path"); err != nil {
		return err
	}
	metadata, err := readProjectMetadata(projectPath)
	if err != nil {
		return err
	}
	if err := validateProjectMetadata(metadata, data.ProjectID, data.SourcePath); err != nil {
		return err
	}
	if err := ensureRealContainedPath(projectPath, data.RepoPath, false, "repository path"); err != nil {
		return err
	}
	if err := validateGitMetadata(data.RepoPath); err != nil {
		return err
	}
	objectType, err := runGit(data.RepoPath, nil, "cat-file", "-t", data.BaselineCommit)
	if err != nil || strings.TrimSpace(objectType) != "commit" {
		return errors.New("shadow baseline does not resolve to a commit")
	}
	branchCommit, err := runGit(data.RepoPath, nil, "rev-parse", "--verify", "refs/heads/"+data.BranchName+"^{commit}")
	if err != nil {
		return errors.New("shadow branch does not resolve to a commit")
	}
	if _, err := runGit(data.RepoPath, nil, "merge-base", "--is-ancestor", data.BaselineCommit, strings.TrimSpace(branchCommit)); err != nil {
		return errors.New("shadow baseline is not an ancestor of its branch")
	}
	if requireWorktree {
		valid, err := (&shadowWorkspace{data: data}).IsValid()
		if err != nil || !valid {
			return errors.New("shadow worktree identity is invalid")
		}
	}
	return nil
}

func validateGitMetadata(repoPath string) error {
	gitMetadata := filepath.Join(repoPath, ".git")
	info, err := os.Lstat(gitMetadata)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("shadow git metadata is not an exact local directory")
	}
	if err := ensureRealContainedPath(repoPath, gitMetadata, false, "git metadata"); err != nil {
		return err
	}
	gitDir, err := runGit(repoPath, nil, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil || filepath.Clean(strings.TrimSpace(gitDir)) != gitMetadata {
		return errors.New("shadow repository identity is invalid")
	}
	commonDir, err := runGit(repoPath, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || filepath.Clean(strings.TrimSpace(commonDir)) != gitMetadata {
		return errors.New("shadow git metadata identity is invalid")
	}
	return nil
}

func validateWorktreeIdentity(data ShadowData) (bool, error) {
	if err := ensureRealContainedPath(filepath.Dir(data.RepoPath), data.WorktreePath, false, "worktree path"); err != nil {
		return false, err
	}
	topLevel, err := runGit(data.WorktreePath, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return false, nil
	}
	canonicalTopLevel, err := filepath.EvalSymlinks(strings.TrimSpace(topLevel))
	if err != nil || filepath.Clean(canonicalTopLevel) != filepath.Clean(data.WorktreePath) {
		return false, nil
	}
	commonDir, err := runGit(data.WorktreePath, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || filepath.Clean(strings.TrimSpace(commonDir)) != filepath.Join(data.RepoPath, ".git") {
		return false, nil
	}
	branch, err := runGit(data.WorktreePath, nil, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || strings.TrimSpace(branch) != "refs/heads/"+data.BranchName {
		return false, nil
	}
	return true, nil
}

func (w *shadowWorkspace) worktreeRegistered() (bool, error) {
	output, err := runGit(w.data.RepoPath, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	want := filepath.Clean(w.data.WorktreePath)
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "worktree ") && filepath.Clean(strings.TrimPrefix(line, "worktree ")) == want {
			return true, nil
		}
	}
	return false, nil
}

func (w *shadowWorkspace) branchRegistered() (bool, error) {
	output, err := runGit(w.data.RepoPath, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	want := "branch refs/heads/" + w.data.BranchName
	for _, line := range strings.Split(output, "\n") {
		if line == want {
			return true, nil
		}
	}
	return false, nil
}

func validateContained(root, target, label string) error {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("shadow %s is not contained by shadow root", label)
	}
	return nil
}

func ensureNoSymlinkParents(root, parent string) error {
	return ensureRealContainedPath(root, parent, true, "path")
}

func ensureRealContainedPath(root, target string, allowMissing bool, label string) error {
	if err := validateContained(root, target, label); err != nil && filepath.Clean(root) != filepath.Clean(target) {
		return err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("shadow %s root is not a real directory", label)
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) && allowMissing {
			continue
		}
		if statErr != nil {
			return fmt.Errorf("inspect shadow %s: %w", label, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("shadow %s %q is a symlink", label, current)
		}
		if current != target && !info.IsDir() {
			return fmt.Errorf("shadow %s ancestor %q is not a directory", label, current)
		}
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		resolvedTarget = target
	} else if err != nil {
		return err
	}
	if filepath.Clean(resolvedRoot) == filepath.Clean(resolvedTarget) {
		return nil
	}
	return validateContained(resolvedRoot, resolvedTarget, label)
}

func removeContainedPath(root, target string) error {
	if err := ensureRealContainedPath(root, target, true, "worktree path"); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("remove shadow worktree path: %w", err)
	}
	return nil
}

func runGit(dir string, extraEnv []string, args ...string) (string, error) {
	return runGitInput(dir, extraEnv, nil, args...)
}

func runGitInput(dir string, extraEnv []string, input []byte, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	if input != nil {
		cmd.Stdin = strings.NewReader(string(input))
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %s (%w)", strings.Join(args, " "), strings.TrimSpace(string(output)), err)
	}
	return string(output), nil
}

var _ Workspace = (*shadowWorkspace)(nil)
