package workspace

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	data ShadowData
}

func NewShadow(source, sessionName string) (Workspace, string, error) {
	canonical, err := resolveDirectory(source)
	if err != nil {
		return nil, "", err
	}
	projectID := shadowProjectID(canonical)
	root, err := shadowRoot()
	if err != nil {
		return nil, "", err
	}
	projectPath := filepath.Join(root, projectID)
	repoPath := filepath.Join(projectPath, "repo")
	if err := ensureShadowProject(projectPath, repoPath, shadowProjectMetadata{
		Version: CurrentVersion, ProjectID: projectID, SourcePath: canonical,
	}); err != nil {
		return nil, "", err
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

func RestoreShadow(data Data) (Workspace, error) {
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
	metadata, err := readProjectMetadata(filepath.Dir(stored.RepoPath))
	if err != nil {
		return nil, err
	}
	if err := validateProjectMetadata(metadata, stored.ProjectID, stored.SourcePath); err != nil {
		return nil, err
	}
	return &shadowWorkspace{data: stored}, nil
}

func (w *shadowWorkspace) Mode() Mode { return ModeDirectory }

func (w *shadowWorkspace) Capabilities() Capabilities {
	return Capabilities{BranchPicker: false, Push: false, Apply: true}
}

func (w *shadowWorkspace) WorkDir() string     { return w.data.WorktreePath }
func (w *shadowWorkspace) DisplayName() string { return filepath.Base(w.data.SourcePath) }
func (w *shadowWorkspace) BranchName() string  { return w.data.BranchName }

func (w *shadowWorkspace) Setup() error {
	if err := validateShadowData(w.data); err != nil {
		return err
	}
	valid, err := w.IsValid()
	if err != nil {
		return err
	}
	if valid {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(w.data.WorktreePath), 0o700); err != nil {
		return fmt.Errorf("create shadow worktree directory: %w", err)
	}
	_ = os.RemoveAll(w.data.WorktreePath)
	if _, err := runGit(w.data.RepoPath, nil, "worktree", "add", w.data.WorktreePath, w.data.BranchName); err != nil {
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
	info, err := os.Stat(w.data.WorktreePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat shadow worktree: %w", err)
	}
	if !info.IsDir() {
		return false, nil
	}
	_, err = runGit(w.data.WorktreePath, nil, "rev-parse", "--is-inside-work-tree")
	return err == nil, nil
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

func (w *shadowWorkspace) Apply() (*ApplyResult, error) {
	return nil, &ErrUnsupported{Operation: "apply"}
}

func (w *shadowWorkspace) Remove() error {
	if err := validateShadowData(w.data); err != nil {
		return err
	}
	if _, err := os.Lstat(w.data.WorktreePath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat shadow worktree: %w", err)
	}
	if _, err := runGit(w.data.RepoPath, nil, "worktree", "remove", "--force", w.data.WorktreePath); err != nil {
		return fmt.Errorf("remove shadow worktree: %w", err)
	}
	return nil
}

func (w *shadowWorkspace) Prune() error {
	_, err := runGit(w.data.RepoPath, nil, "worktree", "prune")
	return err
}

func (w *shadowWorkspace) Cleanup() error {
	if err := validateShadowData(w.data); err != nil {
		return err
	}
	var errs []error
	if err := w.Remove(); err != nil {
		errs = append(errs, err)
	}
	if _, err := runGit(w.data.RepoPath, nil, "update-ref", "-d", "refs/heads/"+w.data.BranchName); err != nil {
		errs = append(errs, fmt.Errorf("delete shadow branch: %w", err))
	}
	if err := w.Prune(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
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
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(repoPath, 0o700); err != nil {
			return fmt.Errorf("create shadow repository: %w", err)
		}
		if _, err := runGit(repoPath, nil, "init"); err != nil {
			return fmt.Errorf("initialize shadow repository: %w", err)
		}
		if _, err := runGit(repoPath, nil, "config", "user.name", "Claude Squad"); err != nil {
			return err
		}
		if _, err := runGit(repoPath, nil, "config", "user.email", "claude-squad@local"); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("validate shadow repository: %w", err)
	}
	return nil
}

func importSnapshot(repoPath, source string, snapshot Snapshot) (string, error) {
	index, err := os.CreateTemp(filepath.Dir(repoPath), "index-*")
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
			contents, err = os.ReadFile(filepath.Join(source, filepath.FromSlash(relative)))
			if entry.Mode&0o111 != 0 {
				mode = "100755"
			}
		case EntrySymlink:
			contents = []byte(entry.SymlinkTarget)
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
		if entry.IsDir() && policy.Classify(relative) == ClassShared {
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

func shadowRoot() (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("get config directory: %w", err)
	}
	return filepath.Join(configDir, shadowProjectsDirectory), nil
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

func validateContained(root, target, label string) error {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("shadow %s is not contained by shadow root", label)
	}
	return nil
}

func ensureNoSymlinkParents(root, parent string) error {
	relative, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("shared link parent %q is a symlink", current)
		}
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
