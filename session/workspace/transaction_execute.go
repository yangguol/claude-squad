//go:build darwin || linux

package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

var transactionBeforeFilesystemHook func(action, path string)
var transactionAfterFilesystemHook func(action, path string)
var transactionBeforeFinalValidationHook func()
var transactionBeforeCommitHook func() error
var transactionAfterCommitHook func()
var transactionManifestWriteHook func(TransactionState) error
var transactionAfterManifestRenameHook func(TransactionState) error
var transactionAfterManifestSyncHook func(TransactionState) error
var transactionAfterCreatedParentMkdirHook func(string)
var transactionAfterCreatedParentOpenHook func(string)
var replacementScanAfterRootReadHook func()
var cleanupAfterSourceQuarantineHook func(string)
var cleanupAfterQuarantineValidationHook func(string)
var cleanupAfterPrivateParentSyncHook func(string) error
var cleanupAfterPrivateCaptureHook func(string) error
var cleanupAfterPrivateRemovalHook func(string)
var acknowledgeAfterValidationHook func()

// CleanupBlockedWarning reports committed apply material that could not be
// proven transaction-owned. The apply itself remains successful.
type CleanupBlockedWarning struct {
	TransactionID string
	Paths         []string
	RetainedPaths []string
}

func (w *CleanupBlockedWarning) Error() string {
	if w == nil {
		return ""
	}
	return fmt.Sprintf("transaction %s cleanup blocked; retained: %s", w.TransactionID, strings.Join(w.RetainedPaths, ", "))
}

// ExecuteTransaction advances a prepared v2 transaction through its durable
// forward WAL and commits only after every published result is revalidated.
func ExecuteTransaction(manifest *TransactionManifest, transactionDir string) (returnErr error) {
	if manifest == nil {
		return errors.New("transaction manifest is nil")
	}
	if manifest.State != TransactionPrepared && manifest.State != TransactionApplying {
		return fmt.Errorf("cannot execute transaction in state %q", manifest.State)
	}
	roots, err := openForwardRoots(manifest.SourcePath, transactionDir)
	if err != nil {
		manifest.State = TransactionRollingBack
		return errors.Join(err, writeManifestAtBoundDirectory(*manifest, transactionDir))
	}
	defer func() { returnErr = errors.Join(returnErr, roots.close()) }()

	committedDurably := false
	fail := func(original error) error {
		if committedDurably {
			return original
		}
		manifest.State = TransactionRollingBack
		if err := writeManifestBound(*manifest, roots.tx); err != nil {
			return errors.Join(original, err)
		}
		_, rollbackErr := rollbackTransactionBound(manifest, roots)
		return errors.Join(original, rollbackErr)
	}
	manifest.State = TransactionApplying
	if err := writeManifestBound(*manifest, roots.tx); err != nil {
		return fail(err)
	}
	if applyAfterBackupHook != nil {
		applyAfterBackupHook(transactionDir)
	}

	captureOrder := make([]int, 0, len(manifest.Entries))
	for index, entry := range manifest.Entries {
		if entry.Operation == EntryOperationUpdate || entry.Operation == EntryOperationDelete || entry.Operation == EntryOperationCreate && entry.CaptureName != "" {
			captureOrder = append(captureOrder, index)
		}
	}
	sort.SliceStable(captureOrder, func(i, j int) bool {
		return len(manifest.Entries[captureOrder[i]].Path) > len(manifest.Entries[captureOrder[j]].Path)
	})
	for _, index := range captureOrder {
		if err := captureEntryForwardBound(manifest, index, roots); err != nil {
			return fail(fmt.Errorf("capture %q: %w", manifest.Entries[index].Path, err))
		}
	}
	for index := range manifest.Entries {
		if err := applyEntryForwardBound(manifest, index, roots); err != nil {
			return fail(fmt.Errorf("apply %q: %w", manifest.Entries[index].Path, err))
		}
	}
	if transactionBeforeFinalValidationHook != nil {
		transactionBeforeFinalValidationHook()
	}
	if transactionBeforeCommitHook != nil {
		if err := transactionBeforeCommitHook(); err != nil {
			return fail(err)
		}
	}
	if err := validatePublishedResultsBound(*manifest, roots); err != nil {
		return fail(fmt.Errorf("validate published results: %w", err))
	}
	if manifest.AppliedTree == "" || manifest.AppliedAt == "" {
		return fail(errors.New("transaction is missing applied tree or time"))
	}

	manifest.State = TransactionCommitted
	if err := writeManifestBound(*manifest, roots.tx); err != nil {
		persisted, readErr := readManifestBoundStrict(roots.tx)
		if readErr == nil && manifestsCanonicallyEqual(persisted, *manifest) {
			committedDurably = true
			if transactionAfterCommitHook != nil {
				transactionAfterCommitHook()
			}
			return nil
		}
		return fail(errors.Join(fmt.Errorf("commit transaction metadata: %w", err), readErr))
	}
	committedDurably = true
	if transactionAfterCommitHook != nil {
		transactionAfterCommitHook()
	}
	return nil
}

func captureEntryForwardBound(manifest *TransactionManifest, index int, roots *forwardRoots) error {
	entry := &manifest.Entries[index]
	if entry.Operation != EntryOperationUpdate && entry.Operation != EntryOperationDelete && !(entry.Operation == EntryOperationCreate && entry.CaptureName != "") {
		return nil
	}
	switch entry.Phase {
	case EntryPhasePending:
		if err := validateExpectedSourceBound(*manifest, *entry, roots.source); err != nil {
			return err
		}
		if absent, err := absentAt(roots.source, entry.CaptureName); err != nil || !absent {
			return errors.Join(err, os.ErrExist)
		}
		entry.Phase = EntryPhaseCaptureIntent
		if err := writeManifestBound(*manifest, roots.tx); err != nil {
			return err
		}
		if transactionBeforeFilesystemHook != nil {
			transactionBeforeFilesystemHook("capture", entry.Path)
		}
		if applyAfterMutationValidationHook != nil {
			applyAfterMutationValidationHook(entry.Path)
		}
		if err := renameRelativeNoReplace(roots.source, entry.Path, roots.source, entry.CaptureName); err != nil {
			return err
		}
		if transactionAfterFilesystemHook != nil {
			transactionAfterFilesystemHook("capture", entry.Path)
		}
		fallthrough
	case EntryPhaseCaptureIntent:
		if err := validateCapturedSourceBound(*entry, roots.source); err != nil {
			return err
		}
		if applyAfterCaptureValidationHook != nil {
			applyAfterCaptureValidationHook(entry.Path)
		}
		if absent, err := absentAt(roots.source, entry.Path); err != nil || !absent {
			return errors.Join(err, errors.New("captured source target is not absent"))
		}
		entry.Phase = EntryPhaseCaptured
		return writeManifestBound(*manifest, roots.tx)
	case EntryPhaseCaptured, EntryPhasePublishIntent, EntryPhasePublished:
		return nil
	default:
		return fmt.Errorf("cannot capture entry in phase %q", entry.Phase)
	}
}

// ApplyEntryForward reconciles and advances one entry using only names already present in the manifest.
func ApplyEntryForward(manifest *TransactionManifest, entryIndex int, transactionDir string) (returnErr error) {
	if manifest == nil {
		return errors.New("transaction manifest is nil")
	}
	roots, err := openForwardRoots(manifest.SourcePath, transactionDir)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, roots.close()) }()
	return applyEntryForwardBound(manifest, entryIndex, roots)
}

func applyEntryForwardBound(manifest *TransactionManifest, index int, roots *forwardRoots) error {
	if index < 0 || index >= len(manifest.Entries) {
		return errors.New("invalid transaction entry index")
	}
	entry := &manifest.Entries[index]
	if applyBeforeMutationHook != nil {
		applyBeforeMutationHook(entry.Path)
	}
	if entry.Operation == EntryOperationDelete {
		if entry.Phase != EntryPhaseCaptured {
			return fmt.Errorf("delete is not captured: %s", entry.Phase)
		}
		return nil
	}
	if entry.Operation == EntryOperationCreatedParent {
		return publishCreatedParentBound(manifest, index, roots)
	}
	if entry.Operation != EntryOperationCreate && entry.Operation != EntryOperationUpdate {
		return fmt.Errorf("unsupported forward operation %q", entry.Operation)
	}
	if entry.Operation == EntryOperationCreate && (entry.Phase == EntryPhasePending || entry.Phase == EntryPhaseCaptured) || entry.Operation == EntryOperationUpdate && entry.Phase == EntryPhaseCaptured {
		if err := validateIdentityBound(roots.tx, entry.StagePath, *entry.Result); err != nil {
			return fmt.Errorf("validate staged result: %w", err)
		}
		if absent, err := absentAt(roots.source, entry.Path); err != nil || !absent {
			return errors.Join(err, &ConflictError{Paths: []string{entry.Path}})
		}
		entry.Phase = EntryPhasePublishIntent
		if err := writeManifestBound(*manifest, roots.tx); err != nil {
			return err
		}
		if transactionBeforeFilesystemHook != nil {
			transactionBeforeFilesystemHook("publish", entry.Path)
		}
		if err := renameRelativeNoReplace(roots.tx, entry.StagePath, roots.source, entry.Path); err != nil {
			return err
		}
		if transactionAfterFilesystemHook != nil {
			transactionAfterFilesystemHook("publish", entry.Path)
		}
	}
	if entry.Phase == EntryPhasePublishIntent {
		if err := validateIdentityBound(roots.source, entry.Path, *entry.Result); err != nil {
			return err
		}
		if absent, err := absentAt(roots.tx, entry.StagePath); err != nil || !absent {
			return errors.Join(err, errors.New("published stage is not absent"))
		}
		entry.Phase = EntryPhasePublished
		return writeManifestBound(*manifest, roots.tx)
	}
	if entry.Phase != EntryPhasePublished {
		return fmt.Errorf("entry is not published: %s", entry.Phase)
	}
	return validateIdentityBound(roots.source, entry.Path, *entry.Result)
}

func publishCreatedParentBound(manifest *TransactionManifest, index int, roots *forwardRoots) error {
	entry := &manifest.Entries[index]
	if entry.Phase == EntryPhasePending {
		if absent, err := absentAt(roots.source, entry.Path); err != nil || !absent {
			return errors.Join(err, os.ErrExist)
		}
		stat, err := mkdirRelative(roots.tx, entry.StagePath, entry.Path, entry.Result.Mode)
		if err != nil {
			return err
		}
		entry.CreatedDevice, entry.CreatedInode = uint64(stat.Dev), uint64(stat.Ino)
		entry.Phase = EntryPhasePublishIntent
		if err := writeManifestBound(*manifest, roots.tx); err != nil {
			return err
		}
	}
	if entry.Phase == EntryPhasePublishIntent {
		if entry.CreatedDevice == 0 || entry.CreatedInode == 0 {
			return errors.New("created-parent publish intent has no durable ownership identity")
		}
		stageStat, stageErr := entryStatAt(roots.tx, entry.StagePath)
		sourceStat, sourceErr := entryStatAt(roots.source, entry.Path)
		stageOwned := stageErr == nil && uint64(stageStat.Dev) == entry.CreatedDevice && uint64(stageStat.Ino) == entry.CreatedInode
		sourceOwned := sourceErr == nil && uint64(sourceStat.Dev) == entry.CreatedDevice && uint64(sourceStat.Ino) == entry.CreatedInode
		switch {
		case stageOwned && errors.Is(sourceErr, os.ErrNotExist):
			if transactionBeforeFilesystemHook != nil {
				transactionBeforeFilesystemHook("create-parent", entry.Path)
			}
			if err := renameRelativeNoReplace(roots.tx, entry.StagePath, roots.source, entry.Path); err != nil {
				return err
			}
			if transactionAfterFilesystemHook != nil {
				transactionAfterFilesystemHook("create-parent", entry.Path)
			}
		case sourceOwned:
			if !errors.Is(stageErr, os.ErrNotExist) {
				return errors.New("created parent ownership exists at both stage and source")
			}
		default:
			return errors.Join(stageErr, sourceErr, errors.New("created parent ownership is not safely reconcilable"))
		}
		stat, err := entryStatAt(roots.source, entry.Path)
		if err != nil || uint64(stat.Dev) != entry.CreatedDevice || uint64(stat.Ino) != entry.CreatedInode {
			return errors.Join(err, errors.New("created parent changed identity"))
		}
		entry.Phase = EntryPhasePublished
		return writeManifestBound(*manifest, roots.tx)
	}
	if entry.Phase != EntryPhasePublished {
		return fmt.Errorf("created parent is not published: %s", entry.Phase)
	}
	stat, err := entryStatAt(roots.source, entry.Path)
	if err != nil || uint64(stat.Dev) != entry.CreatedDevice || uint64(stat.Ino) != entry.CreatedInode {
		return errors.Join(err, errors.New("created parent changed identity"))
	}
	return nil
}

// ValidatePublishedResults checks the complete forward image immediately before commit.
func ValidatePublishedResults(manifest TransactionManifest, transactionDir string) (returnErr error) {
	roots, err := openForwardRoots(manifest.SourcePath, transactionDir)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, roots.close()) }()
	return validatePublishedResultsBound(manifest, roots)
}

func validatePublishedResultsBound(manifest TransactionManifest, roots *forwardRoots) error {
	if err := roots.verify(); err != nil {
		return err
	}
	for _, entry := range manifest.Entries {
		if entry.Operation == EntryOperationDelete {
			if entry.Phase != EntryPhaseCaptured {
				return fmt.Errorf("delete %q is incomplete", entry.Path)
			}
			if absent, err := absentAt(roots.source, entry.Path); err != nil || !absent && !publishedCreatedParentAt(manifest, entry.Path) {
				return errors.Join(err, fmt.Errorf("deleted target %q is present", entry.Path))
			}
			continue
		}
		if entry.Phase != EntryPhasePublished || entry.Result == nil {
			return fmt.Errorf("result %q is incomplete", entry.Path)
		}
		if err := validateIdentityBound(roots.source, entry.Path, *entry.Result); err != nil {
			return fmt.Errorf("result %q: %w", entry.Path, err)
		}
	}
	directories := make([]string, 0)
	for path, entry := range manifest.AppliedEntries {
		if entry.Type == EntryType("directory") {
			directories = append(directories, path)
		}
	}
	for _, entry := range manifest.Entries {
		if entry.Operation == EntryOperationCreatedParent {
			directories = append(directories, entry.Path)
		}
	}
	sort.Strings(directories)
	for index, path := range directories {
		if index != 0 && path == directories[index-1] {
			continue
		}
		actual, err := snapshotSubtreeAt(roots.source, path)
		if err != nil {
			return err
		}
		expected := expectedSubtree(manifest.AppliedEntries, path)
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("applied directory %q has unexpected complete tree", path)
		}
	}
	return roots.verify()
}

func expectedSubtree(entries map[string]Entry, root string) map[string]Entry {
	result := make(map[string]Entry)
	prefix := root + "/"
	for path, entry := range entries {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		relative := strings.TrimPrefix(path, prefix)
		entry.Path = relative
		result[relative] = entry
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative))); parent != "."; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			if _, ok := result[parent]; !ok {
				result[parent] = Entry{Path: parent, Type: EntryType("directory"), Mode: 0o755}
			}
		}
	}
	return result
}

func publishedCreatedParentAt(manifest TransactionManifest, path string) bool {
	for _, entry := range manifest.Entries {
		if entry.Path == path && entry.Operation == EntryOperationCreatedParent && entry.Phase == EntryPhasePublished {
			return true
		}
	}
	return false
}

func validateExpectedSourceBound(manifest TransactionManifest, entry TransactionEntry, root *os.File) error {
	if len(entry.ExpectedTree) != 0 {
		actual, err := snapshotSubtreeAt(root, entry.Path)
		if err != nil || !reflect.DeepEqual(actual, entry.ExpectedTree) {
			return errors.Join(err, &ConflictError{Paths: []string{entry.Path}})
		}
		return nil
	}
	if entry.Expected == nil {
		return errors.New("entry is missing expected source identity")
	}
	return validateIdentityBound(root, entry.Path, *entry.Expected)
}

func validateCapturedSourceBound(entry TransactionEntry, root *os.File) error {
	if len(entry.ExpectedTree) != 0 {
		actual, err := snapshotSubtreeAt(root, entry.CaptureName)
		if err != nil || !reflect.DeepEqual(actual, entry.ExpectedTree) {
			return errors.Join(err, &ConflictError{Paths: []string{entry.Path}})
		}
		return nil
	}
	if entry.Expected == nil {
		return errors.New("entry is missing captured identity")
	}
	return validateIdentityBound(root, entry.CaptureName, *entry.Expected)
}

func validateIdentityBound(root *os.File, relative string, expected Entry) error {
	actual, exists, err := snapshotEntryAt(root, relative)
	actual.Path = expected.Path
	if expected.Type == EntrySymlink {
		actual.Mode = expected.Mode
	}
	if err != nil || !exists || !reflect.DeepEqual(actual, expected) {
		return errors.Join(err, &ConflictError{Paths: []string{expected.Path}})
	}
	return nil
}

func writeManifestAtBoundDirectory(manifest TransactionManifest, transactionDir string) (returnErr error) {
	root, bindings, directory, err := openBoundAbsoluteDirectory(transactionDir)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, closeOpenedDirectoryBindings(root, bindings)) }()
	return writeManifestBound(manifest, directory)
}

func writeManifestBound(manifest TransactionManifest, directory *os.File) error {
	if transactionManifestWriteHook != nil {
		if err := transactionManifestWriteHook(manifest.State); err != nil {
			return err
		}
	}
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	name, err := randomProbeName(".manifest-")
	if err != nil {
		return err
	}
	fd, err := openExclusiveAt(directory, name, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	written, writeErr := file.Write(contents)
	if writeErr == nil && written != len(contents) {
		writeErr = errors.New("short manifest write")
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = unlinkAt(directory, name)
		return err
	}
	if err := renameReplaceAt(directory, name, "manifest.json"); err != nil {
		_ = unlinkAt(directory, name)
		return err
	}
	if transactionAfterManifestRenameHook != nil {
		if err := transactionAfterManifestRenameHook(manifest.State); err != nil {
			return err
		}
	}
	if err := directory.Sync(); err != nil {
		return err
	}
	if transactionAfterManifestSyncHook != nil {
		if err := transactionAfterManifestSyncHook(manifest.State); err != nil {
			return err
		}
	}
	return nil
}

// ObserveEntryState snapshots every predeclared location through bound roots.
// Matching is identity based; callers must not infer ownership from a pathname.
func ObserveEntryState(manifest TransactionManifest, entry TransactionEntry, transactionDir string) (state ObservedEntryState, returnErr error) {
	roots, err := openForwardRoots(manifest.SourcePath, transactionDir)
	if err != nil {
		return state, err
	}
	defer func() { returnErr = errors.Join(returnErr, roots.close()) }()
	return observeEntryStateBound(entry, roots)
}

func observeEntryStateBound(entry TransactionEntry, roots *forwardRoots) (state ObservedEntryState, returnErr error) {
	var err error
	observe := func(root *os.File, name string, expected *Entry) (bool, bool, error) {
		if name == "" {
			return false, false, nil
		}
		actual, exists, err := snapshotEntryAt(root, name)
		if err != nil || !exists || expected == nil {
			return exists, false, err
		}
		actual.Path = expected.Path
		if expected.Type == EntrySymlink {
			actual.Mode = expected.Mode
		}
		return true, reflect.DeepEqual(actual, *expected), nil
	}
	var errs []error
	state.TargetExists, state.TargetMatchesExpected, err = observe(roots.source, entry.Path, entry.Expected)
	errs = append(errs, err)
	_, state.TargetMatchesResult, err = observe(roots.source, entry.Path, entry.Result)
	errs = append(errs, err)
	state.CaptureExists, state.CaptureMatchesExpected, err = observe(roots.source, entry.CaptureName, entry.Expected)
	errs = append(errs, err)
	state.RollbackExists, state.RollbackMatchesResult, err = observe(roots.source, entry.RollbackCaptureName, entry.Result)
	errs = append(errs, err)
	state.StageExists, state.StageMatchesResult, err = observe(roots.tx, entry.StagePath, entry.Result)
	errs = append(errs, err)
	state.QuarantineExists, _, err = observe(roots.tx, entry.QuarantinePath, nil)
	errs = append(errs, err)
	if entry.Operation == EntryOperationCreatedParent && entry.CreatedDevice != 0 {
		if stat, statErr := entryStatAt(roots.source, entry.Path); statErr == nil {
			state.TargetMatchesResult = uint64(stat.Dev) == entry.CreatedDevice && uint64(stat.Ino) == entry.CreatedInode
		} else if !errors.Is(statErr, os.ErrNotExist) {
			errs = append(errs, statErr)
		}
		if stat, statErr := entryStatAt(roots.tx, entry.StagePath); statErr == nil {
			state.StageMatchesResult = uint64(stat.Dev) == entry.CreatedDevice && uint64(stat.Ino) == entry.CreatedInode
		} else if !errors.Is(statErr, os.ErrNotExist) {
			errs = append(errs, statErr)
		}
		if stat, statErr := entryStatAt(roots.source, entry.RollbackCaptureName); statErr == nil {
			state.RollbackMatchesResult = uint64(stat.Dev) == entry.CreatedDevice && uint64(stat.Ino) == entry.CreatedInode
		} else if !errors.Is(statErr, os.ErrNotExist) {
			errs = append(errs, statErr)
		}
	}
	return state, errors.Join(errs...)
}

// QuarantineSourceTemporary atomically moves one validated source capture into
// its predeclared transaction-private quarantine before any recursive removal.
func QuarantineSourceTemporary(manifest *TransactionManifest, entryIndex int, transactionDir string) (returnErr error) {
	if manifest == nil || entryIndex < 0 || entryIndex >= len(manifest.Entries) {
		return errors.New("invalid cleanup entry")
	}
	entry := &manifest.Entries[entryIndex]
	if entry.CaptureName == "" {
		entry.CleanupPhase = CleanupPhaseRemoved
		return writeManifestAtBoundDirectory(*manifest, transactionDir)
	}
	roots, err := openForwardRoots(manifest.SourcePath, transactionDir)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, roots.close()) }()
	return quarantineSourceTemporaryBound(manifest, entryIndex, roots)
}

func quarantineSourceTemporaryBound(manifest *TransactionManifest, entryIndex int, roots *forwardRoots) error {
	entry := &manifest.Entries[entryIndex]
	if err := preflightCleanupEntryBound(*entry, roots); err != nil {
		return err
	}
	if entry.CleanupPhase == CleanupPhaseRemoved {
		return nil
	}
	if err := ensurePrivateParentBound(roots.tx, entry.QuarantinePath); err != nil {
		return err
	}
	quarantineExists, err := absentAt(roots.tx, entry.QuarantinePath)
	if err != nil {
		return err
	}
	quarantineExists = !quarantineExists
	if entry.CleanupPhase == CleanupPhasePending {
		if quarantineExists {
			return errors.New("cleanup quarantine is occupied")
		}
		var observed unix.Stat_t
		if entry.Expected != nil {
			observed, err = validateIdentityStatBound(roots.source, entry.CaptureName, *entry.Expected)
		} else {
			var actual map[string]Entry
			observed, err = entryStatAt(roots.source, entry.CaptureName)
			if err == nil {
				actual, err = snapshotSubtreeAt(roots.source, entry.CaptureName)
			}
			if err == nil && !reflect.DeepEqual(actual, entry.ExpectedTree) {
				err = errors.New("captured source tree identity mismatch")
			}
		}
		if err != nil {
			return fmt.Errorf("source temporary is not transaction-owned: %w", err)
		}
		entry.CleanupPhase = CleanupPhaseQuarantineIntent
		if err := writeManifestBound(*manifest, roots.tx); err != nil {
			return err
		}
		if err := renameRelativeNoReplaceBound(roots.source, entry.CaptureName, roots.tx, entry.QuarantinePath, &observed); err != nil {
			return err
		}
		if cleanupAfterSourceQuarantineHook != nil {
			cleanupAfterSourceQuarantineHook(entry.QuarantinePath)
		}
		quarantineExists = true
	}
	if entry.CleanupPhase == CleanupPhaseQuarantineIntent {
		if !quarantineExists {
			return errors.New("cleanup quarantine capture is missing")
		}
		if err := validateCleanupQuarantineBound(*entry, roots.tx); err != nil {
			return fmt.Errorf("cleanup quarantine identity mismatch: %w", err)
		}
		entry.CleanupPhase = CleanupPhaseQuarantined
		if err := writeManifestBound(*manifest, roots.tx); err != nil {
			return err
		}
	}
	if entry.CleanupPhase == CleanupPhaseQuarantined {
		if !quarantineExists {
			removing := privateRemovingPath(entry.QuarantinePath)
			identity, err := entryStatAt(roots.tx, removing)
			if errors.Is(err, os.ErrNotExist) {
				entry.CleanupPhase = CleanupPhaseRemoved
				return writeManifestBound(*manifest, roots.tx)
			}
			if err != nil {
				return err
			}
			if err := validateCleanupPathBound(*entry, roots.tx, removing); err != nil {
				return fmt.Errorf("private cleanup capture identity mismatch: %w", err)
			}
			if err := removePrivateRelativeBound(roots.tx, entry.QuarantinePath, identity, true); err != nil {
				return err
			}
			entry.CleanupPhase = CleanupPhaseRemoved
			return writeManifestBound(*manifest, roots.tx)
		}
		identity, err := entryStatAt(roots.tx, entry.QuarantinePath)
		if err != nil {
			return err
		}
		if err := validateCleanupQuarantineBound(*entry, roots.tx); err != nil {
			return fmt.Errorf("cleanup quarantine identity mismatch: %w", err)
		}
		validated, err := entryStatAt(roots.tx, entry.QuarantinePath)
		if err != nil || !sameOrphanIdentity(identity, validated) {
			return errors.Join(err, errors.New("cleanup quarantine changed while validating"))
		}
		if err := removePrivateRelativeBound(roots.tx, entry.QuarantinePath, validated, false); err != nil {
			return err
		}
		entry.CleanupPhase = CleanupPhaseRemoved
		return writeManifestBound(*manifest, roots.tx)
	}
	return nil
}

func preflightCleanupEntryBound(entry TransactionEntry, roots *forwardRoots) error {
	if entry.CleanupPhase == CleanupPhaseRemoved {
		return nil
	}
	if entry.QuarantinePath == "" || entry.Expected == nil && len(entry.ExpectedTree) == 0 {
		return errors.New("source cleanup entry lacks quarantine identity")
	}
	quarantineAbsent, err := absentAt(roots.tx, entry.QuarantinePath)
	if err != nil {
		return err
	}
	switch entry.CleanupPhase {
	case CleanupPhasePending:
		if !quarantineAbsent {
			return errors.New("cleanup quarantine is occupied")
		}
		if entry.Expected != nil {
			if _, err := validateIdentityStatBound(roots.source, entry.CaptureName, *entry.Expected); err != nil {
				return fmt.Errorf("source temporary is not transaction-owned: %w", err)
			}
		} else {
			actual, err := snapshotSubtreeAt(roots.source, entry.CaptureName)
			if err != nil || !reflect.DeepEqual(actual, entry.ExpectedTree) {
				return fmt.Errorf("source temporary is not transaction-owned: %w", errors.Join(err, errors.New("captured source tree identity mismatch")))
			}
		}
	case CleanupPhaseQuarantineIntent:
		if quarantineAbsent {
			return errors.New("cleanup quarantine capture is missing")
		}
		if err := validateCleanupQuarantineBound(entry, roots.tx); err != nil {
			return fmt.Errorf("cleanup quarantine identity mismatch: %w", err)
		}
	case CleanupPhaseQuarantined:
		if !quarantineAbsent {
			if err := validateCleanupQuarantineBound(entry, roots.tx); err != nil {
				return fmt.Errorf("cleanup quarantine identity mismatch: %w", err)
			}
		} else {
			removing := privateRemovingPath(entry.QuarantinePath)
			if _, err := entryStatAt(roots.tx, removing); errors.Is(err, os.ErrNotExist) {
				return nil
			} else if err != nil {
				return err
			}
			if err := validateCleanupPathBound(entry, roots.tx, removing); err != nil {
				return fmt.Errorf("private cleanup capture identity mismatch: %w", err)
			}
		}
	}
	return nil
}

func cleanupRetainedPaths(manifest TransactionManifest, entry TransactionEntry, transactionDir string) []string {
	retained := make([]string, 0, 2)
	if entry.CleanupPhase == CleanupPhasePending || entry.CleanupPhase == CleanupPhaseQuarantineIntent {
		if _, err := os.Lstat(filepath.Join(manifest.SourcePath, filepath.FromSlash(entry.CaptureName))); err == nil {
			retained = append(retained, entry.CaptureName)
		}
	}
	if _, err := os.Lstat(filepath.Join(transactionDir, filepath.FromSlash(entry.QuarantinePath))); err == nil {
		retained = append(retained, entry.QuarantinePath)
	}
	removing := privateRemovingPath(entry.QuarantinePath)
	if _, err := os.Lstat(filepath.Join(transactionDir, filepath.FromSlash(removing))); err == nil {
		retained = append(retained, removing)
	}
	return retained
}

func validateCleanupQuarantineBound(entry TransactionEntry, root *os.File) error {
	return validateCleanupPathBound(entry, root, entry.QuarantinePath)
}

func validateCleanupPathBound(entry TransactionEntry, root *os.File, relative string) error {
	if entry.Expected != nil {
		expected := *entry.Expected
		expected.Path = relative
		return validateIdentityBound(root, relative, expected)
	}
	actual, err := snapshotSubtreeAt(root, relative)
	if err != nil || !reflect.DeepEqual(actual, entry.ExpectedTree) {
		return errors.Join(err, errors.New("captured source tree identity mismatch"))
	}
	return nil
}

// CleanCommittedTransaction cleans only transaction-owned private material and
// converts ownership ambiguity into a durable warning, never rollback.
func CleanCommittedTransaction(manifest *TransactionManifest, transactionDir string) (*CleanupBlockedWarning, error) {
	if manifest == nil {
		return nil, errors.New("transaction manifest is nil")
	}
	if manifest.State == TransactionComplete {
		return nil, nil
	}
	if manifest.State == TransactionCleanupBlocked {
		issue := RecoveryIssue{}
		if len(manifest.RecoveryIssues) != 0 {
			issue = manifest.RecoveryIssues[0]
		}
		return &CleanupBlockedWarning{TransactionID: manifest.TransactionID, Paths: issue.Paths, RetainedPaths: issue.RetainedPaths}, nil
	}
	if manifest.State != TransactionCommitted && manifest.State != TransactionCleaning {
		return nil, fmt.Errorf("cannot clean transaction in state %q", manifest.State)
	}
	manifest.State = TransactionCleaning
	if err := writeManifestAtBoundDirectory(*manifest, transactionDir); err != nil {
		return nil, err
	}
	for index := range manifest.Entries {
		entry := &manifest.Entries[index]
		if entry.CaptureName == "" {
			if entry.CleanupPhase != CleanupPhaseRemoved {
				entry.CleanupPhase = CleanupPhaseRemoved
				if err := writeManifestAtBoundDirectory(*manifest, transactionDir); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err := QuarantineSourceTemporary(manifest, index, transactionDir); err != nil {
			retained := cleanupRetainedPaths(*manifest, *entry, transactionDir)
			warning := &CleanupBlockedWarning{TransactionID: manifest.TransactionID, Paths: []string{entry.Path}, RetainedPaths: retained}
			manifest.State = TransactionCleanupBlocked
			manifest.RecoveryIssues = []RecoveryIssue{{TransactionID: manifest.TransactionID, State: manifest.State, Message: err.Error(), Paths: warning.Paths, RetainedPaths: warning.RetainedPaths}}
			if writeErr := writeManifestAtBoundDirectory(*manifest, transactionDir); writeErr != nil {
				return nil, errors.Join(err, writeErr)
			}
			return warning, nil
		}
	}
	manifest.State = TransactionComplete
	manifest.RecoveryIssues = nil
	if err := writeManifestAtBoundDirectory(*manifest, transactionDir); err != nil {
		return nil, err
	}
	return nil, nil
}

// PreflightRecoverTransaction observes the same descriptor-bound identities and
// reconciliation decisions as recovery without writing manifests or mutating paths.
func PreflightRecoverTransaction(manifest *TransactionManifest, transactionDir string) (report RecoveryReport, returnErr error) {
	if manifest == nil {
		return RecoveryReport{}, errors.New("transaction manifest is nil")
	}
	copyManifest := *manifest
	copyManifest.Entries = append([]TransactionEntry(nil), manifest.Entries...)
	switch copyManifest.State {
	case TransactionCommitted, TransactionCleaning:
		roots, err := openForwardRoots(copyManifest.SourcePath, transactionDir)
		if err != nil {
			return RecoveryReport{}, err
		}
		defer func() { returnErr = errors.Join(returnErr, roots.close()) }()
		for _, entry := range copyManifest.Entries {
			if entry.CaptureName == "" {
				continue
			}
			if err := preflightCleanupEntryBound(entry, roots); err != nil {
				return RecoveryReport{Disposition: RecoveryBlocked, Issues: []RecoveryIssue{{
					TransactionID: copyManifest.TransactionID, State: TransactionCleanupBlocked,
					Message: err.Error(), Paths: []string{entry.Path},
					RetainedPaths: cleanupRetainedPaths(copyManifest, entry, transactionDir),
				}}}, nil
			}
		}
	case TransactionPrepared, TransactionApplying, TransactionRollingBack:
		roots, err := openForwardRoots(copyManifest.SourcePath, transactionDir)
		if err != nil {
			return RecoveryReport{}, err
		}
		defer func() { returnErr = errors.Join(returnErr, roots.close()) }()
		for index := len(copyManifest.Entries) - 1; index >= 0; index-- {
			issue, err := preflightRollbackEntryBound(&copyManifest, index, roots)
			if err != nil {
				return RecoveryReport{}, err
			}
			if issue != nil {
				issue.TransactionID, issue.State = copyManifest.TransactionID, TransactionRecoveryBlocked
				return RecoveryReport{Disposition: RecoveryBlocked, Issues: []RecoveryIssue{*issue}}, nil
			}
		}
	case TransactionRecoveryBlocked:
		return RecoveryReport{Disposition: RecoveryBlocked, Issues: append([]RecoveryIssue(nil), copyManifest.RecoveryIssues...)}, nil
	case TransactionCleanupBlocked:
		return RecoveryReport{Disposition: RecoveryBlocked, Issues: append([]RecoveryIssue(nil), copyManifest.RecoveryIssues...)}, nil
	}
	return RecoveryReport{Disposition: RecoveryUnchanged}, nil
}

// RecoverTransaction reverses every pre-commit state and leaves committed
// transactions for the committed-cleanup path.
func RecoverTransaction(manifest *TransactionManifest, transactionDir string) (RecoveryReport, error) {
	if manifest == nil {
		return RecoveryReport{}, errors.New("transaction manifest is nil")
	}
	switch manifest.State {
	case TransactionCommitted, TransactionCleaning:
		warning, err := CleanCommittedTransaction(manifest, transactionDir)
		if err != nil {
			return RecoveryReport{}, err
		}
		report := RecoveryReport{Disposition: RecoveryUnchanged, Repairs: []ApplyMetadataRepair{{
			SessionID: manifest.SessionID, TransactionID: manifest.TransactionID,
			AppliedTree: manifest.AppliedTree, AppliedAt: manifest.AppliedAt, State: manifest.State,
		}}}
		if warning != nil {
			report.Issues = append([]RecoveryIssue(nil), manifest.RecoveryIssues...)
		}
		return report, nil
	case TransactionComplete, TransactionCleanupBlocked:
		return RecoveryReport{
			Disposition: RecoveryUnchanged,
			Issues:      append([]RecoveryIssue(nil), manifest.RecoveryIssues...),
			Repairs: []ApplyMetadataRepair{{
				SessionID: manifest.SessionID, TransactionID: manifest.TransactionID,
				AppliedTree: manifest.AppliedTree, AppliedAt: manifest.AppliedAt, State: manifest.State,
			}},
		}, nil
	case TransactionRolledBack:
		return RecoveryReport{Disposition: RecoveryRolledBack}, nil
	case TransactionRecoveryBlocked:
		return RecoveryReport{Disposition: RecoveryBlocked, Issues: append([]RecoveryIssue(nil), manifest.RecoveryIssues...)}, nil
	default:
		return RollbackTransaction(manifest, transactionDir)
	}
}

// RollbackTransaction durably reverses the forward WAL in reverse entry order.
func RollbackTransaction(manifest *TransactionManifest, transactionDir string) (report RecoveryReport, returnErr error) {
	if manifest == nil {
		return RecoveryReport{}, errors.New("transaction manifest is nil")
	}
	roots, err := openForwardRoots(manifest.SourcePath, transactionDir)
	if err != nil {
		return RecoveryReport{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, roots.close()) }()
	return rollbackTransactionBound(manifest, roots)
}

func rollbackTransactionBound(manifest *TransactionManifest, roots *forwardRoots) (RecoveryReport, error) {
	if manifest.State == TransactionRolledBack {
		return RecoveryReport{Disposition: RecoveryRolledBack}, nil
	}
	manifest.State = TransactionRollingBack
	if err := writeManifestBound(*manifest, roots.tx); err != nil {
		return RecoveryReport{}, err
	}
	for index := len(manifest.Entries) - 1; index >= 0; index-- {
		issue, err := rollbackEntryBound(manifest, index, roots)
		if err != nil {
			return RecoveryReport{}, err
		}
		if issue != nil {
			manifest.State = TransactionRecoveryBlocked
			issue.TransactionID, issue.State = manifest.TransactionID, manifest.State
			manifest.RecoveryIssues = []RecoveryIssue{*issue}
			if err := writeManifestBound(*manifest, roots.tx); err != nil {
				return RecoveryReport{}, err
			}
			return RecoveryReport{Disposition: RecoveryBlocked, Issues: append([]RecoveryIssue(nil), manifest.RecoveryIssues...)}, nil
		}
	}
	manifest.State = TransactionRolledBack
	if err := writeManifestBound(*manifest, roots.tx); err != nil {
		return RecoveryReport{}, err
	}
	return RecoveryReport{Disposition: RecoveryRolledBack}, nil
}

func rollbackEntryBound(manifest *TransactionManifest, index int, roots *forwardRoots) (*RecoveryIssue, error) {
	return reconcileRollbackEntryBound(manifest, index, roots, false)
}

func preflightRollbackEntryBound(manifest *TransactionManifest, index int, roots *forwardRoots) (*RecoveryIssue, error) {
	return reconcileRollbackEntryBound(manifest, index, roots, true)
}

func reconcileRollbackEntryBound(manifest *TransactionManifest, index int, roots *forwardRoots, preflight bool) (*RecoveryIssue, error) {
	entry := &manifest.Entries[index]
	observe := func() (ObservedEntryState, error) {
		state, err := observeEntryStateBound(*entry, roots)
		if err != nil {
			return state, err
		}
		if len(entry.ExpectedTree) != 0 && state.TargetExists && !state.TargetMatchesResult {
			actual, treeErr := snapshotSubtreeAt(roots.source, entry.Path)
			if treeErr != nil {
				return state, treeErr
			}
			state.TargetMatchesExpected = reflect.DeepEqual(actual, entry.ExpectedTree)
		}
		if len(entry.ExpectedTree) != 0 && state.CaptureExists {
			actual, treeErr := snapshotSubtreeAt(roots.source, entry.CaptureName)
			if treeErr != nil {
				return state, treeErr
			}
			state.CaptureMatchesExpected = reflect.DeepEqual(actual, entry.ExpectedTree)
		}
		return state, nil
	}
	block := func(message string, paths, retained []string) *RecoveryIssue {
		return &RecoveryIssue{Message: message, Paths: paths, RetainedPaths: retained}
	}
	persist := func(phase EntryPhase) error {
		entry.Phase = phase
		if preflight {
			return nil
		}
		return writeManifestBound(*manifest, roots.tx)
	}
	type simulatedLocation struct {
		path     string
		identity unix.Stat_t
	}
	var simulatedRollbackResult *simulatedLocation
	observeRollbackResult := func() (unix.Stat_t, error) {
		if simulatedRollbackResult != nil && simulatedRollbackResult.path == entry.RollbackCaptureName {
			return simulatedRollbackResult.identity, nil
		}
		return validateIdentityStatBound(roots.source, entry.RollbackCaptureName, *entry.Result)
	}
	retainRollbackResult := func() *RecoveryIssue {
		if entry.StagePath == "" {
			return block("published result has no private retention path", []string{entry.Path}, []string{entry.RollbackCaptureName})
		}
		if absent, err := absentAt(roots.tx, entry.StagePath); err != nil || !absent {
			return block("private retention path is occupied", []string{entry.Path}, []string{entry.RollbackCaptureName, entry.StagePath})
		}
		observed, err := observeRollbackResult()
		if err != nil {
			return block("retained published result changed before private capture", []string{entry.Path}, []string{entry.RollbackCaptureName})
		}
		if preflight {
			return nil
		}
		if transactionBeforeFilesystemHook != nil {
			transactionBeforeFilesystemHook("retain-result", entry.Path)
		}
		if err := renameRelativeNoReplaceBound(roots.source, entry.RollbackCaptureName, roots.tx, entry.StagePath, &observed); err != nil {
			return block("published result could not be retained safely", []string{entry.Path}, []string{entry.RollbackCaptureName, entry.StagePath})
		}
		return nil
	}

	state, err := observe()
	if err != nil {
		return nil, err
	}
	if entry.Phase == EntryPhaseRestored {
		return nil, nil
	}

	// Content equality is not ownership. Only a durable publication phase can
	// authorize capture of a result, and created parents additionally require
	// their durable inode identity.
	publicationPossible := false
	switch entry.Phase {
	case EntryPhasePublishIntent, EntryPhasePublished, EntryPhaseRollbackCaptureIntent, EntryPhaseRollbackCaptured:
		publicationPossible = true
	}
	published := publicationPossible && state.TargetMatchesResult
	simulatedCapture := false
	if entry.Operation == EntryOperationCreate && entry.Phase == EntryPhasePending && state.TargetExists {
		return block("pending create target is externally owned", []string{entry.Path}, []string{entry.Path, entry.StagePath}), nil
	}
	if entry.Phase == EntryPhasePublishIntent && state.StageExists {
		if state.TargetExists {
			return block("publish intent has both target and stage", []string{entry.Path}, []string{entry.Path, entry.StagePath}), nil
		}
		if !state.StageMatchesResult {
			return block("publish intent stage has ambiguous ownership", []string{entry.Path}, []string{entry.StagePath}), nil
		}
		published = false
	}
	if published && !state.RollbackMatchesResult {
		if state.RollbackExists {
			return block("rollback capture is occupied", []string{entry.Path}, []string{entry.Path, entry.RollbackCaptureName, entry.CaptureName}), nil
		}
		if err := persist(EntryPhaseRollbackCaptureIntent); err != nil {
			return nil, err
		}
		observed, statErr := validateIdentityStatBound(roots.source, entry.Path, *entry.Result)
		if statErr != nil {
			return block("published result changed before capture", []string{entry.Path}, []string{entry.Path, entry.CaptureName}), nil
		}
		if preflight {
			simulatedCapture = true
			simulatedRollbackResult = &simulatedLocation{path: entry.RollbackCaptureName, identity: observed}
			state.TargetExists = false
			state.TargetMatchesResult = false
			state.RollbackExists = true
			state.RollbackMatchesResult = true
			if err := persist(EntryPhaseRollbackCaptured); err != nil {
				return nil, err
			}
		} else {
			if transactionBeforeFilesystemHook != nil {
				transactionBeforeFilesystemHook("rollback-capture", entry.Path)
			}
			if err := renameRelativeNoReplaceBound(roots.source, entry.Path, roots.source, entry.RollbackCaptureName, &observed); err != nil {
				return block("published result could not be captured safely", []string{entry.Path}, []string{entry.Path, entry.RollbackCaptureName, entry.CaptureName}), nil
			}
			if transactionAfterFilesystemHook != nil {
				transactionAfterFilesystemHook("rollback-capture", entry.Path)
			}
			state, err = observe()
			if err != nil {
				return nil, err
			}
			if !state.RollbackMatchesResult || state.TargetExists {
				return block("published result capture is ambiguous", []string{entry.Path}, []string{entry.RollbackCaptureName, entry.CaptureName}), nil
			}
			if err := persist(EntryPhaseRollbackCaptured); err != nil {
				return nil, err
			}
		}
	} else if state.RollbackMatchesResult && !state.TargetExists {
		if entry.Phase != EntryPhaseRollbackCaptured {
			if err := persist(EntryPhaseRollbackCaptured); err != nil {
				return nil, err
			}
		}
	} else if published && state.RollbackMatchesResult {
		return block("result exists at both target and rollback capture", []string{entry.Path}, []string{entry.Path, entry.RollbackCaptureName, entry.CaptureName}), nil
	}

	if !simulatedCapture {
		state, err = observe()
		if err != nil {
			return nil, err
		}
	}
	switch entry.Operation {
	case EntryOperationUpdate, EntryOperationDelete, EntryOperationCreate:
		if entry.Operation == EntryOperationCreate && entry.CaptureName == "" {
			if state.TargetExists {
				return block("created target has unowned or ambiguous content", []string{entry.Path}, []string{entry.Path, entry.RollbackCaptureName}), nil
			}
			if state.RollbackMatchesResult {
				if issue := retainRollbackResult(); issue != nil {
					return issue, nil
				}
			}
			return nil, persist(EntryPhaseRestored)
		}
		if state.TargetMatchesExpected && !state.CaptureExists {
			if state.RollbackMatchesResult {
				if issue := retainRollbackResult(); issue != nil {
					return issue, nil
				}
			}
			return nil, persist(EntryPhaseRestored)
		}
		if state.TargetExists {
			return block("restore target is occupied", []string{entry.Path}, []string{entry.CaptureName, entry.RollbackCaptureName}), nil
		}
		if !state.CaptureMatchesExpected {
			// Pending capture means the original still belongs at the target. Any
			// other absence is ambiguous and must be retained for inspection.
			if entry.Phase == EntryPhasePending || entry.Phase == EntryPhaseCaptureIntent && !state.CaptureExists {
				return nil, persist(EntryPhaseRestored)
			}
			return block("captured original is missing or changed", []string{entry.Path}, []string{entry.CaptureName, entry.RollbackCaptureName}), nil
		}
		var observed unix.Stat_t
		var statErr error
		if entry.Expected != nil {
			observed, statErr = validateIdentityStatBound(roots.source, entry.CaptureName, *entry.Expected)
		} else {
			observed, statErr = entryStatAt(roots.source, entry.CaptureName)
		}
		if statErr != nil {
			return block("captured original changed before restore", []string{entry.Path}, []string{entry.CaptureName, entry.RollbackCaptureName}), nil
		}
		if preflight {
			if state.RollbackMatchesResult {
				if issue := retainRollbackResult(); issue != nil {
					return issue, nil
				}
			}
			return nil, persist(EntryPhaseRestored)
		}
		if transactionBeforeFilesystemHook != nil {
			transactionBeforeFilesystemHook("restore", entry.Path)
		}
		if err := renameRelativeNoReplaceBound(roots.source, entry.CaptureName, roots.source, entry.Path, &observed); err != nil {
			return block("captured original could not be restored safely", []string{entry.Path}, []string{entry.CaptureName, entry.RollbackCaptureName}), nil
		}
		if transactionAfterFilesystemHook != nil {
			transactionAfterFilesystemHook("restore", entry.Path)
		}
		if state.RollbackMatchesResult {
			if issue := retainRollbackResult(); issue != nil {
				return issue, nil
			}
		}
		return nil, persist(EntryPhaseRestored)
	case EntryOperationCreatedParent:
		if state.TargetExists {
			return block("created target has unowned or ambiguous content", []string{entry.Path}, []string{entry.Path, entry.RollbackCaptureName}), nil
		}
		if state.RollbackMatchesResult {
			if issue := retainRollbackResult(); issue != nil {
				return issue, nil
			}
		}
		return nil, persist(EntryPhaseRestored)
	default:
		return nil, fmt.Errorf("unsupported rollback operation %q", entry.Operation)
	}
}

func manifestsCanonicallyEqual(left, right TransactionManifest) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func readManifestBoundStrict(directory *os.File) (TransactionManifest, error) {
	contents, err := readFileAt(directory, "manifest.json")
	if err != nil {
		return TransactionManifest{}, err
	}
	return decodeManifestStrict(contents)
}
