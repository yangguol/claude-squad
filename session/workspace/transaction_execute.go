package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"syscall"
)

var transactionBeforeFilesystemHook func(action, path string)
var transactionAfterFilesystemHook func(action, path string)
var transactionBeforeFinalValidationHook func()
var transactionBeforeCommitHook func() error
var transactionAfterCommitHook func()

// ExecuteTransaction advances a prepared v2 transaction through its durable
// forward WAL and commits only after every published result is revalidated.
func ExecuteTransaction(manifest *TransactionManifest, transactionDir string) (returnErr error) {
	if manifest == nil {
		return errors.New("transaction manifest is nil")
	}
	if manifest.State != TransactionPrepared && manifest.State != TransactionApplying {
		return fmt.Errorf("cannot execute transaction in state %q", manifest.State)
	}
	manifest.State = TransactionApplying
	if err := WriteManifestDurable(*manifest, transactionDir); err != nil {
		return err
	}
	if applyAfterBackupHook != nil {
		applyAfterBackupHook(transactionDir)
	}
	defer func() {
		if returnErr == nil || manifest.State == TransactionCommitted {
			return
		}
		manifest.State = TransactionRollingBack
		returnErr = errors.Join(returnErr, WriteManifestDurable(*manifest, transactionDir))
	}()

	// Capture deepest source entries first. This makes non-directory ancestors
	// available before their explicitly recorded created-parent entries run.
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
		if err := captureEntryForward(manifest, index, transactionDir); err != nil {
			return fmt.Errorf("capture %q: %w", manifest.Entries[index].Path, err)
		}
	}
	for index := range manifest.Entries {
		if err := ApplyEntryForward(manifest, index, transactionDir); err != nil {
			return fmt.Errorf("apply %q: %w", manifest.Entries[index].Path, err)
		}
	}
	if transactionBeforeFinalValidationHook != nil {
		transactionBeforeFinalValidationHook()
	}
	if err := ValidatePublishedResults(*manifest, transactionDir); err != nil {
		return fmt.Errorf("validate published results: %w", err)
	}
	if manifest.AppliedTree == "" || manifest.AppliedAt == "" {
		return errors.New("transaction is missing applied tree or time")
	}
	if transactionBeforeCommitHook != nil {
		if err := transactionBeforeCommitHook(); err != nil {
			return err
		}
	}
	manifest.State = TransactionCommitted
	if err := WriteManifestDurable(*manifest, transactionDir); err != nil {
		return fmt.Errorf("commit transaction metadata: %w", err)
	}
	if transactionAfterCommitHook != nil {
		transactionAfterCommitHook()
	}
	return nil
}

func captureEntryForward(manifest *TransactionManifest, entryIndex int, transactionDir string) error {
	entry := &manifest.Entries[entryIndex]
	if entry.Operation != EntryOperationUpdate && entry.Operation != EntryOperationDelete && !(entry.Operation == EntryOperationCreate && entry.CaptureName != "") {
		return nil
	}
	target := filepath.Join(manifest.SourcePath, filepath.FromSlash(entry.Path))
	capture := filepath.Join(manifest.SourcePath, filepath.FromSlash(entry.CaptureName))
	switch entry.Phase {
	case EntryPhasePending:
		if err := validateExpectedSource(*manifest, *entry, target); err != nil {
			return err
		}
		if err := requireAbsent(capture); err != nil {
			return fmt.Errorf("capture destination: %w", err)
		}
		entry.Phase = EntryPhaseCaptureIntent
		if err := WriteManifestDurable(*manifest, transactionDir); err != nil {
			return err
		}
		if transactionBeforeFilesystemHook != nil {
			transactionBeforeFilesystemHook("capture", entry.Path)
		}
		if applyAfterMutationValidationHook != nil {
			applyAfterMutationValidationHook(entry.Path)
		}
		if err := renameSourceSiblingNoReplace(manifest.SourcePath, entry.Path, entry.CaptureName); err != nil {
			return err
		}
		if transactionAfterFilesystemHook != nil {
			transactionAfterFilesystemHook("capture", entry.Path)
		}
		if err := syncRenameDirectories(target, capture); err != nil {
			return err
		}
		fallthrough
	case EntryPhaseCaptureIntent:
		if err := validateCapturedSource(*entry, capture); err != nil {
			return err
		}
		if applyAfterCaptureValidationHook != nil {
			applyAfterCaptureValidationHook(entry.Path)
		}
		absent, err := pathAbsent(target)
		if err != nil || !absent {
			return errors.Join(err, errors.New("captured source target is not absent"))
		}
		entry.Phase = EntryPhaseCaptured
		return WriteManifestDurable(*manifest, transactionDir)
	case EntryPhaseCaptured, EntryPhasePublishIntent, EntryPhasePublished:
		return nil
	default:
		return fmt.Errorf("cannot capture entry in phase %q", entry.Phase)
	}
}

// ApplyEntryForward reconciles and advances one entry using only names already
// present in the manifest.
func ApplyEntryForward(manifest *TransactionManifest, entryIndex int, transactionDir string) error {
	if manifest == nil || entryIndex < 0 || entryIndex >= len(manifest.Entries) {
		return errors.New("invalid transaction entry index")
	}
	entry := &manifest.Entries[entryIndex]
	if applyBeforeMutationHook != nil {
		applyBeforeMutationHook(entry.Path)
	}
	target := filepath.Join(manifest.SourcePath, filepath.FromSlash(entry.Path))
	if entry.Operation == EntryOperationDelete {
		if entry.Phase != EntryPhaseCaptured {
			return fmt.Errorf("delete is not captured: %s", entry.Phase)
		}
		return nil
	}
	if entry.Operation == EntryOperationUpdate && entry.Phase == EntryPhasePending {
		if err := captureEntryForward(manifest, entryIndex, transactionDir); err != nil {
			return err
		}
	}
	if entry.Operation == EntryOperationCreatedParent {
		return publishCreatedParent(manifest, entryIndex, transactionDir, target)
	}
	if entry.Operation != EntryOperationCreate && entry.Operation != EntryOperationUpdate {
		return fmt.Errorf("unsupported forward operation %q", entry.Operation)
	}
	if entry.Operation == EntryOperationCreate && (entry.Phase == EntryPhasePending || entry.Phase == EntryPhaseCaptured) || entry.Operation == EntryOperationUpdate && entry.Phase == EntryPhaseCaptured {
		stage := filepath.Join(transactionDir, filepath.FromSlash(entry.StagePath))
		if err := validateIdentityAt(stage, *entry.Result); err != nil {
			return fmt.Errorf("validate staged result: %w", err)
		}
		if err := requireAbsent(target); err != nil {
			if errors.Is(err, os.ErrExist) {
				return &ConflictError{Paths: []string{entry.Path}}
			}
			return fmt.Errorf("publication target: %w", err)
		}
		entry.Phase = EntryPhasePublishIntent
		if err := WriteManifestDurable(*manifest, transactionDir); err != nil {
			return err
		}
		if transactionBeforeFilesystemHook != nil {
			transactionBeforeFilesystemHook("publish", entry.Path)
		}
		if err := renameAcrossDirectoriesNoReplace(stage, target); err != nil {
			return err
		}
		if transactionAfterFilesystemHook != nil {
			transactionAfterFilesystemHook("publish", entry.Path)
		}
		if err := syncRenameDirectories(stage, target); err != nil {
			return err
		}
	}
	if entry.Phase == EntryPhasePublishIntent {
		if err := validateIdentityAt(target, *entry.Result); err != nil {
			return err
		}
		stage := filepath.Join(transactionDir, filepath.FromSlash(entry.StagePath))
		absent, err := pathAbsent(stage)
		if err != nil || !absent {
			return errors.Join(err, errors.New("published stage is not absent"))
		}
		entry.Phase = EntryPhasePublished
		return WriteManifestDurable(*manifest, transactionDir)
	}
	if entry.Phase != EntryPhasePublished {
		return fmt.Errorf("entry is not published: %s", entry.Phase)
	}
	return validateIdentityAt(target, *entry.Result)
}

func publishCreatedParent(manifest *TransactionManifest, index int, transactionDir, target string) error {
	entry := &manifest.Entries[index]
	if entry.Phase == EntryPhasePending {
		if err := requireAbsent(target); err != nil {
			return err
		}
		entry.Phase = EntryPhasePublishIntent
		if err := WriteManifestDurable(*manifest, transactionDir); err != nil {
			return err
		}
		if transactionBeforeFilesystemHook != nil {
			transactionBeforeFilesystemHook("create-parent", entry.Path)
		}
		if err := applyFS.Mkdir(target, entry.Result.Mode.Perm()); err != nil {
			return err
		}
		if transactionAfterFilesystemHook != nil {
			transactionAfterFilesystemHook("create-parent", entry.Path)
		}
		if err := applySyncDirectory(filepath.Dir(target)); err != nil {
			return err
		}
	}
	if entry.Phase == EntryPhasePublishIntent {
		if err := validateIdentityAt(target, *entry.Result); err != nil {
			return err
		}
		entry.Phase = EntryPhasePublished
		return WriteManifestDurable(*manifest, transactionDir)
	}
	if entry.Phase != EntryPhasePublished {
		return fmt.Errorf("created parent is not published: %s", entry.Phase)
	}
	return validateIdentityAt(target, *entry.Result)
}

// ValidatePublishedResults checks the complete forward image immediately before
// the durable committed transition.
func ValidatePublishedResults(manifest TransactionManifest, _ string) error {
	for _, entry := range manifest.Entries {
		target := filepath.Join(manifest.SourcePath, filepath.FromSlash(entry.Path))
		if entry.Operation == EntryOperationDelete {
			if entry.Phase != EntryPhaseCaptured {
				return fmt.Errorf("delete %q is incomplete", entry.Path)
			}
			absent, err := pathAbsent(target)
			if err != nil {
				return err
			}
			if !absent && !publishedCreatedParentAt(manifest, entry.Path) {
				return fmt.Errorf("deleted target %q is present", entry.Path)
			}
			continue
		}
		if entry.Phase != EntryPhasePublished || entry.Result == nil {
			return fmt.Errorf("result %q is incomplete", entry.Path)
		}
		if err := validateIdentityAt(target, *entry.Result); err != nil {
			return fmt.Errorf("result %q: %w", entry.Path, err)
		}
	}
	return nil
}

func publishedCreatedParentAt(manifest TransactionManifest, path string) bool {
	for _, entry := range manifest.Entries {
		if entry.Path == path && entry.Operation == EntryOperationCreatedParent && entry.Phase == EntryPhasePublished {
			return true
		}
	}
	return false
}

func validateExpectedSource(manifest TransactionManifest, entry TransactionEntry, target string) error {
	if entry.Operation == EntryOperationCreate && len(entry.ExpectedTree) != 0 {
		actual, err := snapshotDirectoryTree(manifest.SourcePath, entry.Path)
		if err != nil || !reflect.DeepEqual(actual, entry.ExpectedTree) {
			return errors.Join(err, &ConflictError{Paths: []string{entry.Path}})
		}
		return nil
	}
	if entry.Expected != nil {
		return validateIdentityAt(target, *entry.Expected)
	}
	if len(entry.ExpectedTree) != 0 {
		actual, err := snapshotDirectoryTree(manifest.SourcePath, entry.Path)
		if err != nil || !reflect.DeepEqual(actual, entry.ExpectedTree) {
			return errors.Join(err, &ConflictError{Paths: []string{entry.Path}})
		}
		return nil
	}
	return errors.New("entry is missing expected source identity")
}

func validateCapturedSource(entry TransactionEntry, capture string) error {
	if entry.Operation == EntryOperationCreate && len(entry.ExpectedTree) != 0 {
		actual, err := snapshotDirectoryTree(filepath.Dir(capture), filepath.Base(capture))
		if err != nil || !reflect.DeepEqual(actual, entry.ExpectedTree) {
			return errors.Join(err, &ConflictError{Paths: []string{entry.Path}})
		}
		return nil
	}
	if entry.Expected != nil {
		return validateIdentityAt(capture, *entry.Expected)
	}
	if len(entry.ExpectedTree) != 0 {
		actual, err := snapshotDirectoryTree(filepath.Dir(capture), filepath.Base(capture))
		if err != nil || !reflect.DeepEqual(actual, entry.ExpectedTree) {
			return errors.Join(err, &ConflictError{Paths: []string{entry.Path}})
		}
		return nil
	}
	return errors.New("entry is missing captured identity")
}

func validateIdentityAt(path string, expected Entry) error {
	if expected.Type == EntryType("directory") {
		info, err := applyFS.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != expected.Mode.Perm() {
			return errors.Join(err, &ConflictError{Paths: []string{expected.Path}})
		}
		return nil
	}
	actual, exists, err := snapshotPath(path, expected.Path)
	if expected.Type == EntrySymlink {
		actual.Mode = expected.Mode
	}
	if err != nil || !exists || !reflect.DeepEqual(actual, expected) {
		return errors.Join(err, &ConflictError{Paths: []string{expected.Path}})
	}
	return nil
}

func pathAbsent(path string) (bool, error) {
	_, err := applyFS.Lstat(path)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return true, nil
	}
	return false, err
}

func requireAbsent(path string) error {
	absent, err := pathAbsent(path)
	if err != nil {
		return err
	}
	if !absent {
		return os.ErrExist
	}
	return nil
}

func renameAcrossDirectoriesNoReplace(from, to string) (returnErr error) {
	fromParent, err := platformOpenTrustedDirectory(filepath.Dir(from))
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, fromParent.Close()) }()
	toParent, err := platformOpenTrustedDirectory(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, toParent.Close()) }()
	return platformRenameatNoReplaceBetween(int(fromParent.Fd()), filepath.Base(from), int(toParent.Fd()), filepath.Base(to))
}

func renameSourceSiblingNoReplace(sourceRoot, fromRelative, toRelative string) (returnErr error) {
	fromParent := filepath.Dir(filepath.Join(sourceRoot, filepath.FromSlash(fromRelative)))
	toParent := filepath.Dir(filepath.Join(sourceRoot, filepath.FromSlash(toRelative)))
	if fromParent != toParent {
		return errors.New("source capture is not a sibling")
	}
	parent, err := platformOpenTrustedDirectory(fromParent)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, parent.Close()) }()
	return platformRenameatNoReplaceBetween(int(parent.Fd()), filepath.Base(filepath.FromSlash(fromRelative)), int(parent.Fd()), filepath.Base(filepath.FromSlash(toRelative)))
}

func syncRenameDirectories(from, to string) error {
	fromDir, toDir := filepath.Dir(from), filepath.Dir(to)
	if err := applySyncDirectory(fromDir); err != nil {
		return err
	}
	if toDir != fromDir {
		return applySyncDirectory(toDir)
	}
	return nil
}
