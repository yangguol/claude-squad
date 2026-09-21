package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type applyEntry struct {
	Type          EntryType
	Mode          os.FileMode
	Contents      []byte
	SymlinkTarget string
}

var applyPlanAfterImportsHook func()

var mergeTreeCommand = func(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}

type applyPlan struct {
	TransactionDir string
	OursCommit     string
	TheirsCommit   string
	MergedTree     string
	OursSnapshot   Snapshot
	Creates        map[string]applyEntry
	Updates        map[string]applyEntry
	Deletes        []string
}

func (p applyPlan) ChangedPaths() []string {
	paths := make([]string, 0, len(p.Creates)+len(p.Updates)+len(p.Deletes))
	for name := range p.Creates {
		paths = append(paths, name)
	}
	for name := range p.Updates {
		paths = append(paths, name)
	}
	paths = append(paths, p.Deletes...)
	sort.Strings(paths)
	return paths
}

func (w *shadowWorkspace) Apply() (*ApplyResult, error) {
	plan, err := w.planApply()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(plan.TransactionDir)
	return &ApplyResult{Changed: plan.ChangedPaths()}, nil
}

func (w *shadowWorkspace) planApply() (plan applyPlan, returnErr error) {
	if err := validateShadowData(w.data); err != nil {
		return plan, err
	}
	if err := validateShadowIdentity(w.data, true); err != nil {
		return plan, err
	}
	if err := ensureRealContainedPath(w.data.SourcePath, w.data.SourcePath, false, "source path"); err != nil {
		return plan, err
	}
	policy, err := policyFromSnapshot(w.data.PolicySnapshot)
	if err != nil {
		return plan, err
	}

	transactionRoot := filepath.Join(filepath.Dir(w.data.RepoPath), "transactions")
	if err := ensureRealContainedPath(filepath.Dir(w.data.RepoPath), transactionRoot, true, "transaction root"); err != nil {
		return plan, err
	}
	if err := os.MkdirAll(transactionRoot, 0o700); err != nil {
		return plan, fmt.Errorf("create transaction root: %w", err)
	}
	if err := ensureRealContainedPath(filepath.Dir(w.data.RepoPath), transactionRoot, false, "transaction root"); err != nil {
		return plan, err
	}
	transactionDir, err := os.MkdirTemp(transactionRoot, w.data.SessionID+"-")
	if err != nil {
		return plan, fmt.Errorf("create apply transaction: %w", err)
	}
	plan.TransactionDir = transactionDir
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(transactionDir)
		}
	}()
	if err := ensureRealContainedPath(transactionRoot, transactionDir, false, "transaction directory"); err != nil {
		return plan, err
	}

	oursSnapshot, err := Scan(w.data.SourcePath, policy)
	if err != nil {
		return plan, fmt.Errorf("snapshot current source: %w", err)
	}
	ours, err := w.importApplyCommit(w.data.SourcePath, oursSnapshot, "Claude Squad apply source", transactionDir)
	if err != nil {
		return plan, err
	}
	theirsSnapshot, err := Scan(w.data.WorktreePath, policy)
	if err != nil {
		return plan, fmt.Errorf("snapshot agent worktree: %w", err)
	}
	delete(theirsSnapshot.Entries, ".git")
	theirs, err := w.importApplyCommit(w.data.WorktreePath, theirsSnapshot, "Claude Squad apply agent", transactionDir)
	if err != nil {
		return plan, err
	}
	if applyPlanAfterImportsHook != nil {
		applyPlanAfterImportsHook()
	}
	if err := verifyApplySnapshot(w.data.SourcePath, policy, oursSnapshot, false, "source"); err != nil {
		return plan, err
	}
	if err := verifyApplySnapshot(w.data.WorktreePath, policy, theirsSnapshot, true, "agent worktree"); err != nil {
		return plan, err
	}
	plan.OursSnapshot, plan.OursCommit, plan.TheirsCommit = oursSnapshot, ours, theirs

	mergedTree, conflictStages, err := w.mergeApplyCommits(ours, theirs)
	if err != nil {
		return plan, err
	}
	if len(conflictStages) != 0 {
		paths, err := w.conflictPaths(transactionDir, conflictStages)
		if err != nil {
			return plan, err
		}
		if err := verifyApplySnapshot(w.data.SourcePath, policy, oursSnapshot, false, "source"); err != nil {
			return plan, err
		}
		if err := verifyApplySnapshot(w.data.WorktreePath, policy, theirsSnapshot, true, "agent worktree"); err != nil {
			return plan, err
		}
		return plan, &ConflictError{Paths: paths}
	}
	plan.MergedTree = mergedTree
	plan.Creates = make(map[string]applyEntry)
	plan.Updates = make(map[string]applyEntry)
	if err := w.buildApplyManifest(&plan); err != nil {
		return plan, err
	}
	if err := verifyApplySnapshot(w.data.SourcePath, policy, oursSnapshot, false, "source"); err != nil {
		return plan, err
	}
	if err := verifyApplySnapshot(w.data.WorktreePath, policy, theirsSnapshot, true, "agent worktree"); err != nil {
		return plan, err
	}
	return plan, nil
}

func verifyApplySnapshot(root string, policy Policy, expected Snapshot, omitGit bool, label string) error {
	current, err := Scan(root, policy)
	if err != nil {
		return fmt.Errorf("re-snapshot %s: %w", label, err)
	}
	if omitGit {
		delete(current.Entries, ".git")
	}
	if !reflect.DeepEqual(expected, current) {
		return fmt.Errorf("%s changed while planning apply", label)
	}
	return nil
}

func policyFromSnapshot(snapshot []PolicyRuleData) (Policy, error) {
	policy := Policy{rules: make([]policyRule, 0, len(snapshot))}
	for _, stored := range snapshot {
		if stored.Pattern == "" || strings.IndexByte(stored.Pattern, 0) >= 0 {
			return Policy{}, errors.New("shadow apply policy contains an invalid pattern")
		}
		switch stored.Class {
		case ClassIncluded, ClassExcluded, ClassShared, ClassGenerated:
		default:
			return Policy{}, fmt.Errorf("shadow apply policy contains invalid class %q", stored.Class)
		}
		policy.rules = append(policy.rules, policyRule{pattern: stored.Pattern, class: stored.Class, negate: stored.Negate})
	}
	return policy, nil
}

func (w *shadowWorkspace) importApplyCommit(source string, snapshot Snapshot, message string, transactionDir ...string) (string, error) {
	temporaryDirectory := filepath.Dir(w.data.RepoPath)
	if len(transactionDir) != 0 {
		temporaryDirectory = transactionDir[0]
	}
	rootCommit, err := importSnapshotIn(w.data.RepoPath, source, snapshot, temporaryDirectory)
	if err != nil {
		return "", err
	}
	tree, err := runGit(w.data.RepoPath, nil, "rev-parse", "--verify", strings.TrimSpace(rootCommit)+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("resolve apply snapshot tree: %w", err)
	}
	commit, err := runGit(w.data.RepoPath, nil, "commit-tree", strings.TrimSpace(tree), "-p", w.data.BaselineCommit, "-m", message)
	if err != nil {
		return "", fmt.Errorf("create apply snapshot commit: %w", err)
	}
	return strings.TrimSpace(commit), nil
}

type conflictStage struct {
	mode, object, stage, path string
}

func (w *shadowWorkspace) mergeApplyCommits(ours, theirs string) (string, []conflictStage, error) {
	if !validObjectID(ours) || !validObjectID(theirs) {
		return "", nil, errors.New("invalid apply commit object ID")
	}
	output, commandErr := mergeTreeCommand(w.data.RepoPath, "merge-tree", "--write-tree", "-z", ours, theirs)
	mergedTree, stages, parseErr := parseMergeTreeOutput(output)
	if parseErr != nil {
		return "", nil, fmt.Errorf("git merge-tree returned malformed output: %w", parseErr)
	}
	for _, item := range stages {
		if err := validateApplyPath(w.data.SourcePath, item.path); err != nil {
			return "", nil, err
		}
	}
	if commandErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(commandErr, &exitErr) || exitErr.ExitCode() != 1 || len(stages) == 0 {
			return "", nil, fmt.Errorf("git merge-tree failed: %w", commandErr)
		}
	} else if len(stages) != 0 {
		return "", nil, errors.New("git merge-tree reported conflict stages with success status")
	}
	return mergedTree, stages, nil
}

func parseMergeTreeOutput(output []byte) (string, []conflictStage, error) {
	records := bytes.Split(output, []byte{0})
	if len(records) < 2 || len(records[len(records)-1]) != 0 || !validObjectID(string(records[0])) {
		return "", nil, errors.New("missing tree or terminating NUL")
	}
	mergedTree := string(records[0])
	index := 1
	var stages []conflictStage
	byPath := make(map[string]map[string]bool)
	for index < len(records)-1 && len(records[index]) != 0 {
		record := records[index]
		index++
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			return "", nil, errors.New("malformed conflict stage record")
		}
		fields := bytes.Split(record[:tab], []byte{' '})
		if len(fields) != 3 || len(fields[0]) != 6 || !validGitMode(string(fields[0])) || !validObjectID(string(fields[1])) || (string(fields[2]) != "1" && string(fields[2]) != "2" && string(fields[2]) != "3") {
			return "", nil, errors.New("invalid conflict stage record")
		}
		path := string(record[tab+1:])
		if path == "" {
			return "", nil, errors.New("empty conflict stage path")
		}
		stage := string(fields[2])
		if byPath[path] == nil {
			byPath[path] = make(map[string]bool)
		}
		if byPath[path][stage] {
			return "", nil, errors.New("duplicate conflict stage")
		}
		byPath[path][stage] = true
		stages = append(stages, conflictStage{mode: string(fields[0]), object: string(fields[1]), stage: stage, path: path})
	}
	if index == len(records)-1 {
		if len(stages) != 0 {
			return "", nil, errors.New("conflict stages missing informational section")
		}
		return mergedTree, stages, nil
	}
	if len(records[index]) != 0 {
		return "", nil, errors.New("missing conflict section terminator")
	}
	index++
	for index < len(records)-1 {
		count, err := strconv.Atoi(string(records[index]))
		if err != nil || count < 0 || index+count+2 >= len(records) {
			return "", nil, errors.New("malformed informational message")
		}
		index++
		for range count {
			if len(records[index]) == 0 {
				return "", nil, errors.New("empty informational path")
			}
			index++
		}
		if len(records[index]) == 0 || len(records[index+1]) == 0 {
			return "", nil, errors.New("incomplete informational message")
		}
		index += 2
	}
	for _, set := range byPath {
		if len(set) == 0 {
			return "", nil, errors.New("empty conflict stage set")
		}
	}
	return mergedTree, stages, nil
}

func validGitMode(value string) bool {
	if len(value) != 6 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '7' {
			return false
		}
	}
	return true
}

func (w *shadowWorkspace) conflictPaths(transactionDir string, stages []conflictStage) ([]string, error) {
	indexPath := filepath.Join(transactionDir, "conflicts.index")
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	var input bytes.Buffer
	expected := make(map[string]map[string]bool)
	for _, item := range stages {
		if !validObjectID(item.object) || !validGitMode(item.mode) {
			return nil, errors.New("merge conflict contains invalid index data")
		}
		fmt.Fprintf(&input, "%s %s %s\t%s", item.mode, item.object, item.stage, item.path)
		input.WriteByte(0)
		if expected[item.path] == nil {
			expected[item.path] = make(map[string]bool)
		}
		expected[item.path][item.stage] = true
	}
	if _, err := runGitInput(w.data.RepoPath, env, input.Bytes(), "update-index", "-z", "--index-info"); err != nil {
		return nil, fmt.Errorf("build conflict index: %w", err)
	}
	output, err := runGit(w.data.RepoPath, env, "ls-files", "-u", "-z")
	if err != nil {
		return nil, fmt.Errorf("list merge conflicts: %w", err)
	}
	records := strings.Split(output, "\x00")
	if len(records) == 0 || records[len(records)-1] != "" {
		return nil, errors.New("git ls-files returned truncated conflict data")
	}
	actual := make(map[string]map[string]bool)
	set := make(map[string]struct{})
	for _, record := range records[:len(records)-1] {
		index := strings.IndexByte(record, '\t')
		if index < 0 {
			return nil, errors.New("git ls-files returned malformed conflict data")
		}
		fields := strings.Split(record[:index], " ")
		if len(fields) != 3 || !validGitMode(fields[0]) || !validObjectID(fields[1]) || (fields[2] != "1" && fields[2] != "2" && fields[2] != "3") {
			return nil, errors.New("git ls-files returned invalid conflict stage")
		}
		name := record[index+1:]
		if err := validateApplyPath(w.data.SourcePath, name); err != nil {
			return nil, err
		}
		if actual[name] == nil {
			actual[name] = make(map[string]bool)
		}
		actual[name][fields[2]] = true
		set[name] = struct{}{}
	}
	if !reflect.DeepEqual(expected, actual) {
		return nil, errors.New("git conflict index did not preserve exact stages and paths")
	}
	paths := make([]string, 0, len(set))
	for name := range set {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return paths, nil
}

func (w *shadowWorkspace) buildApplyManifest(plan *applyPlan) error {
	output, err := runGit(w.data.RepoPath, nil, "diff-tree", "--no-commit-id", "--raw", "-r", "-z", plan.OursCommit, plan.MergedTree)
	if err != nil {
		return fmt.Errorf("build apply manifest: %w", err)
	}
	records := strings.Split(output, "\x00")
	for index := 0; index < len(records); {
		header := records[index]
		index++
		if header == "" {
			continue
		}
		fields := strings.Fields(header)
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") {
			return errors.New("git diff-tree returned malformed manifest data")
		}
		status := fields[4]
		if index >= len(records) {
			return errors.New("git diff-tree omitted a manifest path")
		}
		name := records[index]
		index++
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if index >= len(records) {
				return errors.New("git diff-tree omitted a rename path")
			}
			name = records[index]
			index++
		}
		if err := validateApplyPath(w.data.SourcePath, name); err != nil {
			return err
		}
		if status[0] == 'D' {
			plan.Deletes = append(plan.Deletes, name)
			continue
		}
		entry, err := w.readApplyEntry(fields[1], fields[3])
		if err != nil {
			return fmt.Errorf("read merged path %q: %w", name, err)
		}
		if status[0] == 'A' {
			plan.Creates[name] = entry
		} else {
			plan.Updates[name] = entry
		}
	}
	sort.Strings(plan.Deletes)
	return nil
}

func (w *shadowWorkspace) readApplyEntry(modeText, object string) (applyEntry, error) {
	if !validObjectID(object) {
		return applyEntry{}, errors.New("invalid merged object ID")
	}
	mode, err := strconv.ParseUint(modeText, 8, 32)
	if err != nil {
		return applyEntry{}, errors.New("invalid merged file mode")
	}
	contents, err := runGitBytes(w.data.RepoPath, "cat-file", "blob", object)
	if err != nil {
		return applyEntry{}, err
	}
	switch mode {
	case 0100644, 0100755:
		return applyEntry{Type: EntryRegular, Mode: os.FileMode(mode & 0o777), Contents: contents}, nil
	case 0120000:
		return applyEntry{Type: EntrySymlink, SymlinkTarget: string(contents)}, nil
	default:
		return applyEntry{}, fmt.Errorf("unsupported merged mode %06o", mode)
	}
}

func validateApplyPath(sourceRoot, name string) error {
	if name == "" || filepath.IsAbs(name) || strings.ContainsRune(name, 0) || strings.Contains(name, "\\") {
		return fmt.Errorf("unsafe apply path %q", name)
	}
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("unsafe apply path %q", name)
		}
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || filepath.ToSlash(clean) != name {
		return fmt.Errorf("unsafe apply path %q", name)
	}
	target := filepath.Join(sourceRoot, clean)
	if err := validateContained(sourceRoot, target, "apply path"); err != nil {
		return err
	}
	if err := ensureRealContainedPath(sourceRoot, filepath.Dir(target), true, "apply path parent"); err != nil {
		return fmt.Errorf("unsafe apply path %q: %w", name, err)
	}
	return nil
}

func validObjectID(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func runGitBytes(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
	}
	return output, nil
}
