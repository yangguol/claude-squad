package workspace

import (
	"bytes"
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
	"syscall"
	"time"
)

type applyEntry struct {
	Type          EntryType
	Mode          os.FileMode
	Contents      []byte
	SymlinkTarget string
}

var applyPlanStartHook func()
var applyPlanAfterImportsHook func()

var mergeTreeCommand = func(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}

type applyPlan struct {
	TransactionDir string
	SourcePath     string
	OursCommit     string
	TheirsCommit   string
	MergedTree     string
	OursSnapshot   Snapshot
	Creates        map[string]applyEntry
	Updates        map[string]applyEntry
	Deletes        []string
}

type transactionState string

const (
	transactionPrepared   transactionState = "prepared"
	transactionApplying   transactionState = "applying"
	transactionRolledBack transactionState = "rolled_back"
	transactionCommitted  transactionState = "committed"
)

type transactionEntry struct {
	Path                string           `json:"path"`
	Operation           string           `json:"operation"`
	Type                EntryType        `json:"type,omitempty"`
	Mode                os.FileMode      `json:"mode,omitempty"`
	Payload             string           `json:"payload,omitempty"`
	Target              string           `json:"target,omitempty"`
	Backup              string           `json:"backup,omitempty"`
	Displaced           string           `json:"displaced,omitempty"`
	Expected            *Entry           `json:"expected,omitempty"`
	ExpectedTree        map[string]Entry `json:"expected_tree,omitempty"`
	ReplacesDirectory   bool             `json:"replaces_directory,omitempty"`
	RollbackCaptureName string           `json:"rollback_capture_name,omitempty"`
}

// Transaction describes durable recovery metadata for an apply operation.
type Transaction struct {
	Directory        string `json:"-"`
	preserveManifest bool
	Version          int                `json:"version"`
	SessionID        string             `json:"session_id"`
	SourcePath       string             `json:"source_path"`
	MergedTree       string             `json:"merged_tree"`
	State            transactionState   `json:"state"`
	Entries          []transactionEntry `json:"entries"`
}

type fileHandle interface {
	io.Reader
	io.Writer
	Stat() (os.FileInfo, error)
	Sync() error
	Close() error
}

type fileOps interface {
	Lstat(string) (os.FileInfo, error)
	Open(string) (fileHandle, error)
	OpenFile(string, int, os.FileMode) (fileHandle, error)
	Readlink(string) (string, error)
	Symlink(string, string) error
	Rename(string, string) error
	RenameNoReplace(string, string) error
	Remove(string) error
	RemoveAll(string) error
	Mkdir(string, os.FileMode) error
	MkdirAll(string, os.FileMode) error
	Chmod(string, os.FileMode) error
}

type osFileOps struct{}

func (osFileOps) Lstat(name string) (os.FileInfo, error) { return os.Lstat(name) }
func (osFileOps) Open(name string) (fileHandle, error)   { return os.Open(name) }
func (osFileOps) OpenFile(name string, flag int, mode os.FileMode) (fileHandle, error) {
	return os.OpenFile(name, flag, mode)
}
func (osFileOps) Readlink(name string) (string, error)  { return os.Readlink(name) }
func (osFileOps) Symlink(oldname, newname string) error { return os.Symlink(oldname, newname) }
func (osFileOps) Rename(oldpath, newpath string) error  { return os.Rename(oldpath, newpath) }
func (osFileOps) RenameNoReplace(oldpath, newpath string) error {
	return platformRenameNoReplace(oldpath, newpath)
}
func (osFileOps) Remove(name string) error                     { return os.Remove(name) }
func (osFileOps) RemoveAll(path string) error                  { return os.RemoveAll(path) }
func (osFileOps) Mkdir(path string, mode os.FileMode) error    { return os.Mkdir(path, mode) }
func (osFileOps) MkdirAll(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }
func (osFileOps) Chmod(name string, mode os.FileMode) error    { return os.Chmod(name, mode) }

var applyFS fileOps = osFileOps{}
var applyRandomRead = rand.Read
var applySyncDirectory = SyncDirectory
var applyAfterParentClassificationHook func()
var applyAfterManifestValidationHook func()
var preparedPathAfterOpenHook func(string)
var preparedPathAfterLeafStatHook func(string)
var replacementScanBeforeOpenHook func(string)
var replacementScanAfterSourceRootOpenHook func()
var replacementScanAfterRootOpenHook func()
var applyBeforeRevalidateHook func()
var applyAfterBackupHook func(string)
var applyBeforeMutationHook func(string)
var applyAfterMutationValidationHook func(string)
var applyAfterCaptureValidationHook func(string)
var applyRenameNoReplace = func(oldpath, newpath string) error { return applyFS.RenameNoReplace(oldpath, newpath) }

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

func (w *shadowWorkspace) Apply() (result *ApplyResult, returnErr error) {
	if err := validateShadowData(w.data); err != nil {
		return nil, err
	}
	if err := validateShadowIdentity(w.data, true); err != nil {
		return nil, err
	}
	projectPath := filepath.Dir(w.data.RepoPath)
	lock, err := AcquireProjectLock(projectPath, "apply")
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, lock.Close())
	}()
	if err := w.refreshAccessLocked(); err != nil {
		return nil, err
	}
	transactionRoot := filepath.Join(projectPath, "transactions")
	if err := atomicApplyPreflight(w.data.SourcePath, transactionRoot); err != nil {
		return nil, err
	}

	plan, err := w.planApply()
	if err != nil {
		return nil, err
	}
	if len(plan.ChangedPaths()) == 0 {
		if err := cleanupUnpublishedTransaction(plan.TransactionDir); err != nil {
			return nil, err
		}
		return &ApplyResult{}, nil
	}
	prepared, err := w.prepareTransaction(plan, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if applyBeforeRevalidateHook != nil {
		applyBeforeRevalidateHook()
	}
	if err := w.revalidateTouched(plan); err != nil {
		prepared.State = TransactionRollingBack
		if writeErr := writeManifestAtBoundDirectory(prepared, plan.TransactionDir); writeErr != nil {
			return nil, errors.Join(err, writeErr)
		}
		_, rollbackErr := RollbackTransaction(&prepared, plan.TransactionDir)
		return nil, errors.Join(err, rollbackErr)
	}
	if err := ExecuteTransaction(&prepared, plan.TransactionDir); err != nil {
		return nil, err
	}
	warning, err := CleanCommittedTransaction(&prepared, plan.TransactionDir)
	if err != nil {
		warning = &CleanupBlockedWarning{TransactionID: prepared.TransactionID, RetainedPaths: []string{plan.TransactionDir}}
		prepared.State = TransactionCleanupBlocked
		prepared.RecoveryIssues = []RecoveryIssue{{TransactionID: prepared.TransactionID, State: prepared.State, Message: err.Error(), RetainedPaths: warning.RetainedPaths}}
		if writeErr := writeManifestAtBoundDirectory(prepared, plan.TransactionDir); writeErr != nil {
			return nil, errors.Join(err, writeErr)
		}
	}
	w.data.LastAppliedTree = prepared.AppliedTree
	w.data.LastAppliedAt = prepared.AppliedAt
	return &ApplyResult{Changed: plan.ChangedPaths(), TransactionID: prepared.TransactionID, Warning: warning}, nil
}

// AcknowledgeApply removes a complete transaction receipt and nothing else.
func (w *shadowWorkspace) AcknowledgeApply(transactionID string) error {
	if transactionID == "" || filepath.Base(transactionID) != transactionID {
		return errors.New("invalid transaction ID")
	}
	projectPath := filepath.Dir(w.data.RepoPath)
	transactionDir := filepath.Join(projectPath, "transactions", transactionID)
	return acknowledgeCompleteTransaction(projectPath, transactionDir, transactionID)
}

func preflightNoReplace(transaction Transaction) error {
	parents := make(map[string]struct{})
	for _, item := range transaction.Entries {
		parent := filepath.Dir(filepath.Join(transaction.SourcePath, filepath.FromSlash(item.Path)))
		for {
			info, err := applyFS.Lstat(parent)
			if err == nil {
				if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
					break
				}
				parent = filepath.Dir(parent)
				continue
			}
			if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
				return fmt.Errorf("inspect atomic no-replace probe parent %q: %w", parent, err)
			}
			parent = filepath.Dir(parent)
		}
		parents[parent] = struct{}{}
	}
	for parent := range parents {
		probe, err := createExclusiveSibling(parent, ".claude-squad-rename-probe-", nil, 0o600)
		if err != nil {
			return fmt.Errorf("prepare atomic no-replace probe in %q: %w", parent, err)
		}
		destination, err := randomSiblingName(probe, ".claude-squad-rename-probe-target-")
		if err != nil {
			return errors.Join(err, removeCreatedTemp(probe))
		}
		if err := applyRenameNoReplace(probe, destination); err != nil {
			return errors.Join(fmt.Errorf("atomic no-replace rename is unavailable in %q: %w", parent, err), removeCreatedTemp(probe), removeCreatedTemp(destination))
		}
		if err := removeCreatedTemp(destination); err != nil {
			return err
		}
	}
	return nil
}

func cleanupCommittedTransaction(transaction Transaction) error {
	marker := transaction.Directory + ".cleanup.json"
	markerTransaction := transaction
	markerTransaction.Directory = ""
	contents, err := json.MarshalIndent(markerTransaction, "", "  ")
	if err != nil {
		return fmt.Errorf("encode committed cleanup marker: %w", err)
	}
	if err := writeExclusiveFile(marker, contents, 0o600); err != nil {
		return fmt.Errorf("write committed cleanup marker: %w", err)
	}
	for _, item := range transaction.Entries {
		if item.Displaced == "" {
			continue
		}
		displaced := filepath.Join(transaction.SourcePath, filepath.FromSlash(item.Displaced))
		if err := applyFS.RemoveAll(displaced); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return fmt.Errorf("cleanup committed transaction displaced source %q: %w", item.Path, err)
		}
	}
	if err := applyFS.RemoveAll(transaction.Directory); err != nil {
		return fmt.Errorf("cleanup committed transaction %q: %w", transaction.Directory, err)
	}
	if err := applyFS.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove committed cleanup marker %q: %w", marker, err)
	}
	return nil
}

func cleanupUnpublishedTransaction(transactionDir string) error {
	if err := applyFS.RemoveAll(transactionDir); err != nil {
		return fmt.Errorf("remove unpublished transaction %q: %w", transactionDir, err)
	}
	if err := applySyncDirectory(filepath.Dir(transactionDir)); err != nil {
		return fmt.Errorf("sync transaction root after removing unpublished transaction: %w", err)
	}
	return nil
}

func (w *shadowWorkspace) prepareTransaction(plan applyPlan, now time.Time) (manifest TransactionManifest, returnErr error) {
	published := false
	defer func() {
		if returnErr != nil && !published {
			returnErr = errors.Join(returnErr, cleanupUnpublishedTransaction(plan.TransactionDir))
		}
	}()
	appliedEntries := make(map[string]Entry, len(plan.OursSnapshot.Entries)+len(plan.Creates)+len(plan.Updates))
	for path, entry := range plan.OursSnapshot.Entries {
		appliedEntries[path] = entry
	}
	removeAppliedPath := func(path string) {
		delete(appliedEntries, path)
		prefix := path + "/"
		for existing := range appliedEntries {
			if strings.HasPrefix(existing, prefix) {
				delete(appliedEntries, existing)
			}
		}
	}
	for _, path := range plan.Deletes {
		removeAppliedPath(path)
	}
	for path, entry := range plan.Creates {
		removeAppliedPath(path)
		appliedEntries[path] = applyEntryIdentity(path, entry)
	}
	for path, entry := range plan.Updates {
		removeAppliedPath(path)
		appliedEntries[path] = applyEntryIdentity(path, entry)
	}
	manifest = TransactionManifest{
		Version: CurrentTransactionVersion, TransactionID: filepath.Base(plan.TransactionDir),
		ProjectID: w.data.ProjectID, SessionID: w.data.SessionID, SourcePath: w.data.SourcePath,
		State: TransactionPrepared, AppliedTree: plan.MergedTree, AppliedAt: now.UTC().Format(time.RFC3339Nano), AppliedEntries: appliedEntries,
	}
	entries, err := preallocateTransactionEntries(plan)
	if err != nil {
		return manifest, err
	}
	manifest.Entries = entries
	if applyAfterParentClassificationHook != nil {
		applyAfterParentClassificationHook()
	}
	if err := revalidatePreparedParents(plan.SourcePath, entries); err != nil {
		return manifest, err
	}
	if err := stageTransactionResults(plan.TransactionDir, entries); err != nil {
		return manifest, err
	}
	projectPath := filepath.Dir(w.data.RepoPath)
	if err := ValidateManifest(projectPath, plan.TransactionDir, manifest); err != nil {
		return manifest, fmt.Errorf("validate prepared transaction manifest: %w", err)
	}
	if applyAfterManifestValidationHook != nil {
		applyAfterManifestValidationHook()
	}
	if err := revalidatePreparedParents(plan.SourcePath, entries); err != nil {
		return manifest, err
	}
	if err := WriteManifestDurable(manifest, plan.TransactionDir); err != nil {
		return manifest, err
	}
	published = true
	return manifest, nil
}

func revalidatePreparedParents(sourceRoot string, entries []TransactionEntry) error {
	checked := make(map[string]EntryType)
	for _, entry := range entries {
		for parent, expectedType := range entry.preparedParents {
			if previous, ok := checked[parent]; ok {
				if previous != expectedType {
					return fmt.Errorf("inconsistent prepared parent classification for %q", parent)
				}
				continue
			}
			checked[parent] = expectedType
			actualType, err := platformPreparedPathType(sourceRoot, parent)
			if err != nil {
				return fmt.Errorf("revalidate prepared parent %q: %w", parent, err)
			}
			if actualType != expectedType {
				return fmt.Errorf("parent classification changed for %q", parent)
			}
		}
	}
	return nil
}

func preallocateTransactionEntries(plan applyPlan) ([]TransactionEntry, error) {
	replacedDirectories := make([]string, 0)
	for _, name := range plan.ChangedPaths() {
		if _, changed := plan.Creates[name]; changed && planReplacesDirectory(plan, name) {
			replacedDirectories = append(replacedDirectories, name)
		}
		if _, changed := plan.Updates[name]; changed && planReplacesDirectory(plan, name) {
			replacedDirectories = append(replacedDirectories, name)
		}
	}

	entries := make([]TransactionEntry, 0, len(plan.ChangedPaths()))
	createdParents := make(map[string]struct{})
	reservedSourceNames := make(map[string]struct{})
	reservedPrivateNames := make(map[string]struct{})
	allocateSourceName := func(path, prefix string) (string, error) {
		for attempts := 0; attempts < 100; attempts++ {
			candidate, err := randomSourceSibling(path, prefix)
			if err == nil {
				err = platformRequirePathAbsent(plan.SourcePath, candidate)
			}
			if errors.Is(err, os.ErrExist) {
				continue
			}
			if err != nil {
				return "", err
			}
			if _, reserved := reservedSourceNames[candidate]; reserved {
				continue
			}
			reservedSourceNames[candidate] = struct{}{}
			return candidate, nil
		}
		return "", errors.New("could not allocate unique absent source sibling name")
	}
	allocatePrivateName := func(category string) (string, error) {
		for attempts := 0; attempts < 100; attempts++ {
			candidate, err := randomTransactionPath(category)
			if err == nil {
				err = platformRequirePathAbsent(plan.TransactionDir, candidate)
			}
			if errors.Is(err, os.ErrExist) {
				continue
			}
			if err != nil {
				return "", err
			}
			if _, reserved := reservedPrivateNames[candidate]; reserved {
				continue
			}
			reservedPrivateNames[candidate] = struct{}{}
			return candidate, nil
		}
		return "", errors.New("could not allocate unique absent transaction path")
	}
	var err error
	for _, name := range plan.ChangedPaths() {
		if pathBelowAny(name, replacedDirectories) {
			continue
		}
		if err := validatePlannedApplyPath(plan.SourcePath, name); err != nil {
			return nil, err
		}
		planned, create := plan.Creates[name]
		if !create {
			planned = plan.Updates[name]
		}
		item := TransactionEntry{Path: name, Phase: EntryPhasePending, CleanupPhase: CleanupPhasePending, preparedParents: make(map[string]EntryType)}
		if create || planned.Type != "" {
			if create {
				item.Operation = EntryOperationCreate
			} else {
				item.Operation = EntryOperationUpdate
			}
			item.StagePath, err = allocatePrivateName("stage")
			if err != nil {
				return nil, err
			}
			result := applyEntryIdentity(name, planned)
			item.Result = &result
			item.stageContents, item.stageSymlinkTarget, item.stageMode = planned.Contents, planned.SymlinkTarget, planned.Mode
			for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name))); parent != "."; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
				parentEntry, exists, inspectErr := snapshotOne(plan.SourcePath, parent)
				parentType := EntryType("")
				if exists {
					parentType = parentEntry.Type
				}
				item.preparedParents[parent] = parentType
				if inspectErr != nil && !errors.Is(inspectErr, syscall.ENOTDIR) {
					return nil, fmt.Errorf("inspect planned parent %q: %w", parent, inspectErr)
				}
				if exists {
					if parentEntry.Type != EntryType("directory") {
						createdParents[parent] = struct{}{}
					}
					break
				}
				createdParents[parent] = struct{}{}
			}
		} else {
			item.Operation = EntryOperationDelete
		}
		if expected, exists := plan.OursSnapshot.Entries[name]; exists {
			item.Expected = cloneEntry(expected)
			item.CaptureName, err = allocateSourceName(name, ".claude-squad-displaced-")
			if err != nil {
				return nil, err
			}
			item.QuarantinePath, err = allocatePrivateName("quarantine")
			if err != nil {
				return nil, err
			}
		} else if item.Operation != EntryOperationDelete && planReplacesDirectory(plan, name) {
			item.CaptureName, err = allocateSourceName(name, ".claude-squad-displaced-")
			if err != nil {
				return nil, err
			}
			item.QuarantinePath, err = allocatePrivateName("quarantine")
			if err != nil {
				return nil, err
			}
			item.ExpectedTree, err = snapshotDirectoryTree(plan.SourcePath, name)
			if err != nil {
				return nil, fmt.Errorf("snapshot replacement directory %q: %w", name, err)
			}
		}
		if item.Operation == EntryOperationCreate || item.Operation == EntryOperationUpdate {
			item.RollbackCaptureName, err = allocateSourceName(name, ".claude-squad-rollback-")
			if err != nil {
				return nil, err
			}
		}
		entries = append(entries, item)
	}

	parents := make([]string, 0, len(createdParents))
	for parent := range createdParents {
		parents = append(parents, parent)
	}
	sort.Slice(parents, func(i, j int) bool {
		left, right := strings.Count(parents[i], "/"), strings.Count(parents[j], "/")
		if left != right {
			return left < right
		}
		return parents[i] < parents[j]
	})
	parentEntries := make([]TransactionEntry, 0, len(parents))
	for _, parent := range parents {
		result := Entry{Path: parent, Type: EntryType("directory"), Mode: 0o755}
		rollbackName, err := allocateSourceName(parent, ".claude-squad-rollback-")
		if err != nil {
			return nil, err
		}
		stagePath, err := allocatePrivateName("stage-parent")
		if err != nil {
			return nil, err
		}
		parentEntries = append(parentEntries, TransactionEntry{
			Path: parent, StagePath: stagePath, Operation: EntryOperationCreatedParent, Phase: EntryPhasePending,
			CleanupPhase: CleanupPhasePending, RollbackCaptureName: rollbackName, Result: &result,
		})
	}
	return append(parentEntries, entries...), nil
}

func validatePlannedApplyPath(sourceRoot, name string) error {
	if name == "" || filepath.IsAbs(name) || strings.ContainsRune(name, 0) || strings.Contains(name, "\\") || filepath.ToSlash(filepath.Clean(filepath.FromSlash(name))) != name {
		return fmt.Errorf("unsafe apply path %q", name)
	}
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("unsafe apply path %q", name)
		}
	}
	target := filepath.Join(sourceRoot, filepath.FromSlash(name))
	if err := validateContained(sourceRoot, target, "apply path"); err != nil {
		return err
	}
	return ensureRealContainedPath(sourceRoot, filepath.Dir(target), true, "apply path parent")
}

func applyEntryIdentity(path string, entry applyEntry) Entry {
	identity := Entry{Path: path, Type: entry.Type, Mode: entry.Mode.Perm(), SymlinkTarget: entry.SymlinkTarget}
	if entry.Type == EntryRegular {
		hash := sha256.Sum256(entry.Contents)
		identity.ContentHash = hex.EncodeToString(hash[:])
	}
	return identity
}

func cloneEntry(entry Entry) *Entry {
	copy := entry
	return &copy
}

func createExclusiveTransactionDirectory(transactionRoot, sessionID string) (string, error) {
	for attempts := 0; attempts < 100; attempts++ {
		var nonce [16]byte
		if _, err := applyRandomRead(nonce[:]); err != nil {
			return "", err
		}
		name := sessionID + "-" + hex.EncodeToString(nonce[:])
		if filepath.Base(name) != name {
			return "", errors.New("invalid transaction directory name")
		}
		path := filepath.Join(transactionRoot, name)
		err := applyFS.Mkdir(path, 0o700)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		return path, nil
	}
	return "", errors.New("could not allocate exclusive transaction directory")
}

func randomTransactionPath(category string) (string, error) {
	var nonce [12]byte
	if _, err := applyRandomRead(nonce[:]); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join(category, hex.EncodeToString(nonce[:]))), nil
}

func randomAbsentTransactionPath(transactionDir, category string) (string, error) {
	for attempts := 0; attempts < 100; attempts++ {
		candidate, err := randomTransactionPath(category)
		if err != nil {
			return "", err
		}
		_, err = applyFS.Lstat(filepath.Join(transactionDir, filepath.FromSlash(candidate)))
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate absent transaction path")
}

func randomSourceSibling(path, prefix string) (string, error) {
	var nonce [12]byte
	if _, err := applyRandomRead(nonce[:]); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join(filepath.Dir(filepath.FromSlash(path)), prefix+hex.EncodeToString(nonce[:]))), nil
}

func randomAbsentSourceSibling(sourceRoot, path, prefix string) (string, error) {
	for attempts := 0; attempts < 100; attempts++ {
		candidate, err := randomSourceSibling(path, prefix)
		if err != nil {
			return "", err
		}
		_, err = applyFS.Lstat(filepath.Join(sourceRoot, filepath.FromSlash(candidate)))
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate absent source sibling name")
}

func stageTransactionResults(transactionDir string, entries []TransactionEntry) error {
	createdRoots := make(map[string]struct{})
	for _, entry := range entries {
		if entry.StagePath == "" {
			continue
		}
		stage := filepath.Join(transactionDir, filepath.FromSlash(entry.StagePath))
		if err := ensureRealContainedPath(transactionDir, filepath.Dir(stage), true, "transaction stage parent"); err != nil {
			return fmt.Errorf("validate stage for %q: %w", entry.Path, err)
		}
		if err := applyFS.MkdirAll(filepath.Dir(stage), 0o700); err != nil {
			return fmt.Errorf("create transaction stage parent: %w", err)
		}
		createdRoots[filepath.Dir(stage)] = struct{}{}
		if entry.Operation == EntryOperationCreatedParent {
			continue
		}
		if entry.Result != nil && entry.Result.Type == EntrySymlink {
			if err := applyFS.Symlink(entry.stageSymlinkTarget, stage); err != nil {
				return fmt.Errorf("stage symlink %q: %w", entry.Path, err)
			}
			continue
		}
		file, err := applyFS.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, entry.stageMode.Perm())
		if err != nil {
			return fmt.Errorf("stage regular file %q: %w", entry.Path, err)
		}
		written, writeErr := file.Write(entry.stageContents)
		if writeErr == nil && written != len(entry.stageContents) {
			writeErr = io.ErrShortWrite
		}
		if writeErr == nil {
			writeErr = file.Sync()
		}
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return fmt.Errorf("stage regular file %q: %w", entry.Path, err)
		}
	}
	for directory := range createdRoots {
		if err := SyncDirectory(directory); err != nil {
			return fmt.Errorf("sync transaction stage directory: %w", err)
		}
	}
	if len(createdRoots) != 0 {
		if err := SyncDirectory(transactionDir); err != nil {
			return fmt.Errorf("sync transaction directory after staging: %w", err)
		}
	}
	return nil
}

func legacyTransactionFromManifest(directory string, manifest TransactionManifest) Transaction {
	transaction := Transaction{Directory: directory, preserveManifest: true, Version: 1, SessionID: manifest.SessionID, SourcePath: manifest.SourcePath, MergedTree: manifest.AppliedTree, State: transactionPrepared}
	for _, entry := range manifest.Entries {
		if entry.Operation == EntryOperationCreatedParent {
			continue
		}
		item := transactionEntry{Path: entry.Path, Operation: string(entry.Operation), Expected: entry.Expected, ExpectedTree: entry.ExpectedTree}
		if entry.Result != nil {
			item.Type, item.Mode, item.Target = entry.Result.Type, entry.Result.Mode, entry.Result.SymlinkTarget
		}
		item.Payload = entry.StagePath
		item.Backup = entry.QuarantinePath
		item.Displaced = entry.CaptureName
		item.RollbackCaptureName = entry.RollbackCaptureName
		item.ReplacesDirectory = entry.Expected == nil && entry.CaptureName != ""
		transaction.Entries = append(transaction.Entries, item)
	}
	return transaction
}

func writeExclusiveFile(name string, contents []byte, mode os.FileMode) error {
	file, err := applyFS.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(contents)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func writeTransactionManifest(transaction Transaction) error {
	return writeLegacyTransactionManifest(transaction)
}

func writeLegacyTransactionManifest(transaction Transaction) error {
	if transaction.preserveManifest {
		return nil
	}
	contents, err := json.MarshalIndent(transaction, "", "  ")
	if err != nil {
		return err
	}
	name := filepath.Join(transaction.Directory, "manifest.json")
	temporary, err := createExclusiveSibling(filepath.Dir(name), ".manifest-", contents, 0o600)
	if err != nil {
		return fmt.Errorf("write transaction manifest: %w", err)
	}
	if err := applyFS.Rename(temporary, name); err != nil {
		return errors.Join(fmt.Errorf("publish transaction manifest: %w", err), removeCreatedTemp(temporary))
	}
	return nil
}

func randomSiblingName(name, prefix string) (string, error) {
	var nonce [12]byte
	if _, err := applyRandomRead(nonce[:]); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(name), prefix+hex.EncodeToString(nonce[:])), nil
}

func createExclusiveSibling(parent, prefix string, contents []byte, mode os.FileMode) (string, error) {
	for attempts := 0; attempts < 100; attempts++ {
		var nonce [12]byte
		if _, err := applyRandomRead(nonce[:]); err != nil {
			return "", err
		}
		name := filepath.Join(parent, prefix+hex.EncodeToString(nonce[:]))
		file, err := applyFS.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, writeErr := file.Write(contents)
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return "", errors.Join(err, removeCreatedTemp(name))
		}
		return name, nil
	}
	return "", errors.New("could not allocate exclusive temporary sibling")
}

func createExclusiveSymlinkSibling(parent, prefix, target string) (string, error) {
	for attempts := 0; attempts < 100; attempts++ {
		var nonce [12]byte
		if _, err := applyRandomRead(nonce[:]); err != nil {
			return "", err
		}
		name := filepath.Join(parent, prefix+hex.EncodeToString(nonce[:]))
		if err := applyFS.Symlink(target, name); errors.Is(err, os.ErrExist) {
			continue
		} else if err != nil {
			return "", err
		}
		return name, nil
	}
	return "", errors.New("could not allocate exclusive temporary symlink sibling")
}

func removeCreatedTemp(name string) error {
	if err := applyFS.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove transaction temporary %q: %w", name, err)
	}
	return nil
}

func (w *shadowWorkspace) revalidateTouched(plan applyPlan) error {
	for _, name := range plan.ChangedPaths() {
		actual, exists, err := snapshotOne(w.data.SourcePath, name)
		expected, wanted := plan.OursSnapshot.Entries[name]
		if err != nil {
			if !errors.Is(err, syscall.ENOTDIR) || wanted {
				return fmt.Errorf("revalidate %q: %w", name, err)
			}
			exists = false
		}
		if exists != wanted || (exists && !reflect.DeepEqual(actual, expected)) {
			if !wanted && exists && actual.Type == EntryType("directory") && planReplacesDirectory(plan, name) {
				continue
			}
			return &ConflictError{Paths: []string{name}}
		}
	}
	return nil
}

func snapshotDirectoryTree(sourceRoot, name string) (map[string]Entry, error) {
	return platformSnapshotDirectoryTree(sourceRoot, name)
}

func planReplacesDirectory(plan applyPlan, name string) bool {
	prefix := name + "/"
	for path := range plan.OursSnapshot.Entries {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func snapshotOne(root, name string) (Entry, bool, error) {
	path := filepath.Join(root, filepath.FromSlash(name))
	info, err := applyFS.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	entry := Entry{Path: name, Mode: info.Mode().Perm()}
	switch {
	case info.IsDir():
		entry.Type = EntryType("directory")
	case info.Mode().IsRegular():
		entry.Type = EntryRegular
		hash, err := hashOpenedRegularFileWithOps(path, info)
		if err != nil {
			return Entry{}, false, err
		}
		entry.ContentHash = hash
	case info.Mode()&os.ModeSymlink != 0:
		entry.Type = EntrySymlink
		entry.SymlinkTarget, err = applyFS.Readlink(path)
		if err != nil {
			return Entry{}, false, err
		}
	default:
		return Entry{}, false, fmt.Errorf("unsupported existing path type")
	}
	return entry, true, nil
}

func (w *shadowWorkspace) executeTransaction(transaction *Transaction) error {
	for index := range transaction.Entries {
		item := &transaction.Entries[index]
		if item.Backup == "" {
			continue
		}
		if err := w.revalidateTransactionEntry(*transaction, *item, false); err != nil {
			return fmt.Errorf("revalidate before backup %q: %w", item.Path, err)
		}
		if err := w.backupEntry(*transaction, item); err != nil {
			return fmt.Errorf("backup %q: %w", item.Path, err)
		}
	}
	transaction.State = transactionApplying
	if err := writeLegacyTransactionManifest(*transaction); err != nil {
		return err
	}
	if applyAfterBackupHook != nil {
		applyAfterBackupHook(transaction.Directory)
	}
	order := transactionExecutionOrder(transaction.Entries)
	applied := make([]int, 0, len(order))
	createdDirs := make([]string, 0)
	for _, index := range order {
		item := transaction.Entries[index]
		if applyBeforeMutationHook != nil {
			applyBeforeMutationHook(item.Path)
		}
		if err := w.revalidateTransactionEntry(*transaction, item, false); err != nil {
			rollbackErr := w.rollbackTransaction(transaction, applied, createdDirs)
			return errors.Join(fmt.Errorf("apply %q: %w", item.Path, err), wrapRollbackError(rollbackErr))
		}
		newDirs, mutated, err := w.applyTransactionEntry(*transaction, item)
		createdDirs = append(createdDirs, newDirs...)
		if mutated {
			applied = append(applied, index)
		}
		if err != nil {
			rollbackErr := w.rollbackTransaction(transaction, applied, createdDirs)
			return errors.Join(fmt.Errorf("apply %q: %w", item.Path, err), wrapRollbackError(rollbackErr))
		}
	}
	transaction.State = transactionCommitted
	if err := writeLegacyTransactionManifest(*transaction); err != nil {
		rollbackErr := w.rollbackTransaction(transaction, applied, createdDirs)
		return errors.Join(fmt.Errorf("commit transaction metadata: %w", err), wrapRollbackError(rollbackErr))
	}
	return nil
}

func wrapRollbackError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("rollback transaction: %w", err)
}

func (w *shadowWorkspace) revalidateTransactionEntry(transaction Transaction, item transactionEntry, _ bool) error {
	if err := validateApplyPath(w.data.SourcePath, item.Path); err != nil {
		return err
	}
	actual, exists, err := snapshotOne(w.data.SourcePath, item.Path)
	if errors.Is(err, syscall.ENOTDIR) {
		exists, err = false, nil
	}
	if err != nil {
		return err
	}
	if item.ReplacesDirectory {
		if exists && actual.Type == EntryType("directory") {
			return nil
		}
		return &ConflictError{Paths: []string{item.Path}}
	}
	if item.Backup == "" {
		if exists {
			if item.ReplacesDirectory && actual.Type == EntryType("directory") {
				return nil
			}
			return &ConflictError{Paths: []string{item.Path}}
		}
		return nil
	}
	if item.Expected == nil {
		return errors.New("transaction entry is missing expected source state")
	}
	if !exists || !reflect.DeepEqual(actual, *item.Expected) {
		return &ConflictError{Paths: []string{item.Path}}
	}
	return nil
}

func readOpenedRegularFileWithOps(path string, expected os.FileInfo) ([]byte, error) {
	file, err := applyFS.Open(path)
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		_ = file.Close()
		return nil, errorsJoinMessage(statErr, "source changed while opening")
	}
	contents, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	final, statErr := applyFS.Lstat(path)
	if statErr != nil || !final.Mode().IsRegular() || !os.SameFile(expected, final) {
		return nil, errorsJoinMessage(statErr, "source changed while reading")
	}
	return contents, nil
}

func hashOpenedRegularFileWithOps(path string, expected os.FileInfo) (string, error) {
	contents, err := readOpenedRegularFileWithOps(path, expected)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(contents)
	return hex.EncodeToString(hash[:]), nil
}

func snapshotPath(path, displayName string) (Entry, bool, error) {
	info, err := applyFS.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	entry := Entry{Path: displayName, Mode: info.Mode().Perm()}
	if info.Mode()&os.ModeSymlink != 0 {
		entry.Type = EntrySymlink
		entry.SymlinkTarget, err = applyFS.Readlink(path)
		return entry, true, err
	}
	if !info.Mode().IsRegular() {
		return Entry{}, false, errors.New("backup is not a regular file or symlink")
	}
	entry.Type = EntryRegular
	file, err := applyFS.Open(path)
	if err != nil {
		return Entry{}, false, err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return Entry{}, false, err
	}
	entry.ContentHash = hex.EncodeToString(hash.Sum(nil))
	return entry, true, nil
}

func transactionExecutionOrder(entries []transactionEntry) []int {
	order := make([]int, 0, len(entries))
	for index := len(entries) - 1; index >= 0; index-- {
		if entries[index].Operation == "delete" {
			order = append(order, index)
		}
	}
	for index := range entries {
		if entries[index].Operation != "delete" {
			order = append(order, index)
		}
	}
	return order
}

func (w *shadowWorkspace) backupEntry(transaction Transaction, item *transactionEntry) error {
	source := filepath.Join(w.data.SourcePath, filepath.FromSlash(item.Path))
	backup := filepath.Join(transaction.Directory, filepath.FromSlash(item.Backup))
	if err := ensureRealContainedPath(transaction.Directory, filepath.Dir(backup), true, "backup parent"); err != nil {
		return err
	}
	if err := applyFS.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		return err
	}
	info, err := applyFS.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := applyFS.Readlink(source)
		if readErr != nil {
			return readErr
		}
		return applyFS.Symlink(target, backup)
	}
	if info.IsDir() {
		return nil
	}
	if !info.Mode().IsRegular() {
		return errors.New("source backup is not a regular file, directory, or symlink")
	}
	contents, err := readOpenedRegularFileWithOps(source, info)
	if err != nil {
		return err
	}
	return writeExclusiveFile(backup, contents, info.Mode().Perm())
}

func (w *shadowWorkspace) applyTransactionEntry(transaction Transaction, item transactionEntry) ([]string, bool, error) {
	target := filepath.Join(w.data.SourcePath, filepath.FromSlash(item.Path))
	if item.Operation == "delete" {
		if err := w.displaceExpectedSource(transaction, item, target); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	createdDirs, err := w.createMissingParents(filepath.Dir(target))
	if err != nil {
		return createdDirs, false, err
	}
	if err := ensureRealContainedPath(w.data.SourcePath, filepath.Dir(target), false, "apply path parent"); err != nil {
		return createdDirs, false, err
	}
	var temporary string
	if item.Type == EntrySymlink {
		temporary, err = createExclusiveSymlinkSibling(filepath.Dir(target), ".claude-squad-apply-", item.Target)
	} else {
		payload := filepath.Join(transaction.Directory, filepath.FromSlash(item.Payload))
		contents, readErr := os.ReadFile(payload)
		if readErr != nil {
			return createdDirs, false, readErr
		}
		temporary, err = createExclusiveSibling(filepath.Dir(target), ".claude-squad-apply-", contents, item.Mode)
		if err != nil {
			return createdDirs, false, err
		}
	}
	if item.Backup != "" {
		if err := w.displaceExpectedSource(transaction, item, target); err != nil {
			return createdDirs, false, errors.Join(err, removeCreatedTemp(temporary))
		}
	}
	err = applyRenameNoReplace(temporary, target)
	if err != nil {
		return createdDirs, item.Backup != "", errors.Join(err, removeCreatedTemp(temporary))
	}
	return createdDirs, true, nil
}

func (w *shadowWorkspace) displaceExpectedSource(transaction Transaction, item transactionEntry, target string) error {
	if item.Displaced == "" || item.Backup == "" {
		return errors.New("transaction entry is missing displacement metadata")
	}
	displaced := filepath.Join(w.data.SourcePath, filepath.FromSlash(item.Displaced))
	if filepath.Dir(displaced) != filepath.Dir(target) {
		return errors.New("transaction displacement is not a target sibling")
	}
	if applyAfterMutationValidationHook != nil {
		applyAfterMutationValidationHook(item.Path)
	}
	if err := applyRenameNoReplace(target, displaced); err != nil {
		return err
	}
	valid, validationErr := capturedEntryMatches(displaced, item)
	if !valid || validationErr != nil {
		restoreErr := applyRenameNoReplace(displaced, target)
		if restoreErr == nil {
			return errors.Join(&ConflictError{Paths: []string{item.Path}}, validationErr)
		}
		return errors.Join(&ConflictError{Paths: []string{item.Path}}, validationErr, fmt.Errorf("retain captured source at %q after restore refusal: %w", displaced, restoreErr))
	}
	if applyAfterCaptureValidationHook != nil {
		applyAfterCaptureValidationHook(item.Path)
	}
	return nil
}

func capturedEntryMatches(displaced string, item transactionEntry) (bool, error) {
	if item.Expected != nil {
		actual, exists, err := snapshotPath(displaced, item.Path)
		return err == nil && exists && reflect.DeepEqual(actual, *item.Expected), err
	}
	if item.ReplacesDirectory {
		info, err := applyFS.Lstat(displaced)
		if err != nil {
			return false, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, nil
		}
		actual, err := snapshotDirectoryTree(filepath.Dir(displaced), filepath.Base(displaced))
		if err != nil {
			return false, err
		}
		return reflect.DeepEqual(actual, item.ExpectedTree), nil
	}
	return false, errors.New("transaction entry is missing expected source state")
}

func (w *shadowWorkspace) createMissingParents(parent string) ([]string, error) {
	var missing []string
	for current := parent; current != w.data.SourcePath; current = filepath.Dir(current) {
		info, err := applyFS.Lstat(current)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return missing, fmt.Errorf("apply parent %q is not a real directory", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return missing, err
		}
		missing = append(missing, current)
	}
	created := make([]string, 0, len(missing))
	for index := len(missing) - 1; index >= 0; index-- {
		if err := applyFS.Mkdir(missing[index], 0o755); err != nil {
			return created, err
		}
		created = append(created, missing[index])
	}
	return created, nil
}

func (w *shadowWorkspace) rollbackTransaction(transaction *Transaction, applied []int, createdDirs []string) error {
	var rollbackErr error
	var removedReplacementAncestors []string
	for position := len(applied) - 1; position >= 0; position-- {
		item := transaction.Entries[applied[position]]
		target := filepath.Join(w.data.SourcePath, filepath.FromSlash(item.Path))
		if err := validateApplyPath(w.data.SourcePath, item.Path); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
			continue
		}
		capturedApplied, err := w.captureAppliedForRollback(*transaction, item, removedReplacementAncestors)
		if err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("refuse to overwrite post-apply change at %q: %w", item.Path, err))
			continue
		}
		if item.ReplacesDirectory {
			removedReplacementAncestors = append(removedReplacementAncestors, item.Path)
		}
		if item.Backup == "" {
			if capturedApplied != "" {
				rollbackErr = errors.Join(rollbackErr, removeCreatedTemp(capturedApplied))
			}
			continue
		}
		backup := filepath.Join(transaction.Directory, filepath.FromSlash(item.Backup))
		if item.Displaced != "" {
			displaced := filepath.Join(w.data.SourcePath, filepath.FromSlash(item.Displaced))
			if err := applyRenameNoReplace(displaced, target); err == nil {
				rollbackErr = errors.Join(rollbackErr, removeCreatedTemp(capturedApplied))
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				rollbackErr = errors.Join(rollbackErr, err)
				continue
			}
		}
		info, err := applyFS.Lstat(backup)
		if err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
			continue
		}
		if err := w.createRollbackParents(filepath.Dir(target)); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
			continue
		}
		var temporary string
		if info.Mode()&os.ModeSymlink != 0 {
			targetValue, readErr := applyFS.Readlink(backup)
			if readErr == nil {
				temporary, readErr = createExclusiveSymlinkSibling(filepath.Dir(target), ".claude-squad-rollback-", targetValue)
			}
			err = readErr
		} else {
			contents, readErr := os.ReadFile(backup)
			if readErr == nil {
				temporary, readErr = createExclusiveSibling(filepath.Dir(target), ".claude-squad-rollback-", contents, info.Mode().Perm())
			}
			err = readErr
		}
		if err == nil {
			err = applyRenameNoReplace(temporary, target)
			if err != nil {
				err = errors.Join(err, removeCreatedTemp(temporary))
			}
		}
		rollbackErr = errors.Join(rollbackErr, err)
	}
	sort.Slice(createdDirs, func(i, j int) bool { return len(createdDirs[i]) > len(createdDirs[j]) })
	for index := range createdDirs {
		info, err := applyFS.Lstat(createdDirs[index])
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("inspect rollback-created directory %q: %w", createdDirs[index], err))
			continue
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if err := applyFS.Remove(createdDirs[index]); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove rollback-created directory %q: %w", createdDirs[index], err))
		}
	}
	if rollbackErr == nil {
		transaction.State = transactionRolledBack
		if err := writeLegacyTransactionManifest(*transaction); err != nil {
			return err
		}
		return applyFS.RemoveAll(transaction.Directory)
	}
	return rollbackErr
}

func (w *shadowWorkspace) captureAppliedForRollback(transaction Transaction, item transactionEntry, removedReplacementAncestors []string) (string, error) {
	target := filepath.Join(w.data.SourcePath, filepath.FromSlash(item.Path))
	if item.Operation == "delete" {
		_, exists, err := snapshotOne(w.data.SourcePath, item.Path)
		if errors.Is(err, syscall.ENOTDIR) && pathBelowAny(item.Path, removedReplacementAncestors) {
			exists, err = false, nil
		}
		if err != nil || exists {
			return "", errors.Join(err, &ConflictError{Paths: []string{item.Path}})
		}
		return "", nil
	}
	if item.RollbackCaptureName == "" {
		return "", errors.New("transaction entry is missing preallocated rollback capture name")
	}
	captured := filepath.Join(w.data.SourcePath, filepath.FromSlash(item.RollbackCaptureName))
	if err := applyRenameNoReplace(target, captured); err != nil {
		return "", err
	}
	expected := Entry{Path: item.Path, Type: item.Type, Mode: item.Mode.Perm(), SymlinkTarget: item.Target}
	if item.Type == EntryRegular {
		payload := filepath.Join(transaction.Directory, filepath.FromSlash(item.Payload))
		payloadEntry, _, snapshotErr := snapshotPath(payload, item.Path)
		if snapshotErr != nil {
			return captured, snapshotErr
		}
		expected.ContentHash = payloadEntry.ContentHash
	}
	actual, exists, snapshotErr := snapshotPath(captured, item.Path)
	if snapshotErr == nil && exists && reflect.DeepEqual(actual, expected) {
		return captured, nil
	}
	restoreErr := applyRenameNoReplace(captured, target)
	return captured, errors.Join(snapshotErr, restoreErr, &ConflictError{Paths: []string{item.Path}})
}

func pathBelowAny(name string, ancestors []string) bool {
	for _, ancestor := range ancestors {
		if strings.HasPrefix(name, ancestor+"/") {
			return true
		}
	}
	return false
}

func (w *shadowWorkspace) createRollbackParents(parent string) error {
	_, err := w.createMissingParents(parent)
	return err
}

// FindIncompleteTransactions returns prepared or applying transactions below a project directory.
func FindIncompleteTransactions(root string) ([]Transaction, error) {
	transactionRoot := filepath.Join(root, "transactions")
	if err := ensureRealContainedPath(root, transactionRoot, true, "transaction root"); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(transactionRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	byDirectory := make(map[string]Transaction)
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".cleanup.json") {
			marker := filepath.Join(transactionRoot, entry.Name())
			if err := ensureRealContainedPath(transactionRoot, marker, false, "transaction cleanup marker"); err != nil {
				return nil, err
			}
			contents, err := os.ReadFile(marker)
			if err != nil {
				return nil, err
			}
			var transaction Transaction
			if err := json.Unmarshal(contents, &transaction); err != nil {
				return nil, fmt.Errorf("decode transaction cleanup marker %q: %w", entry.Name(), err)
			}
			transaction.Directory = strings.TrimSuffix(marker, ".cleanup.json")
			if transaction.State != transactionCommitted {
				return nil, fmt.Errorf("transaction cleanup marker %q has non-committed state %q", entry.Name(), transaction.State)
			}
			byDirectory[transaction.Directory] = transaction
			continue
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		directory := filepath.Join(transactionRoot, entry.Name())
		if err := ensureRealContainedPath(transactionRoot, directory, false, "transaction directory"); err != nil {
			return nil, err
		}
		contents, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
		if errors.Is(err, os.ErrNotExist) {
			if transaction, recorded := byDirectory[directory]; recorded && transaction.State == transactionCommitted {
				continue
			}
			markerContents, markerErr := os.ReadFile(directory + ".cleanup.json")
			if markerErr == nil {
				var transaction Transaction
				if err := json.Unmarshal(markerContents, &transaction); err == nil && transaction.State == transactionCommitted {
					transaction.Directory = directory
					byDirectory[directory] = transaction
					continue
				}
			}
			return nil, fmt.Errorf("transaction directory %q is missing manifest.json", entry.Name())
		}
		if err != nil {
			return nil, err
		}
		var transaction Transaction
		if err := json.Unmarshal(contents, &transaction); err != nil {
			return nil, fmt.Errorf("decode transaction %q: %w", entry.Name(), err)
		}
		transaction.Directory = directory
		if transaction.State == transactionPrepared || transaction.State == transactionApplying {
			byDirectory[transaction.Directory] = transaction
		}
	}
	result := make([]Transaction, 0, len(byDirectory))
	for _, transaction := range byDirectory {
		result = append(result, transaction)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Directory < result[j].Directory })
	return result, nil
}

func (w *shadowWorkspace) planApply() (plan applyPlan, returnErr error) {
	if applyPlanStartHook != nil {
		applyPlanStartHook()
	}
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
	transactionDir, err := createExclusiveTransactionDirectory(transactionRoot, w.data.SessionID)
	if err != nil {
		return plan, fmt.Errorf("create apply transaction: %w", err)
	}
	plan.TransactionDir = transactionDir
	if err := applySyncDirectory(transactionRoot); err != nil {
		return plan, errors.Join(fmt.Errorf("sync transaction root after creating transaction: %w", err), cleanupUnpublishedTransaction(transactionDir))
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, cleanupUnpublishedTransaction(transactionDir))
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
	plan.SourcePath, plan.OursSnapshot, plan.OursCommit, plan.TheirsCommit = w.data.SourcePath, oursSnapshot, ours, theirs

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
