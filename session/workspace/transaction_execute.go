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
)

var transactionBeforeFilesystemHook func(action, path string)
var transactionAfterFilesystemHook func(action, path string)
var transactionBeforeFinalValidationHook func()
var transactionBeforeCommitHook func() error
var transactionAfterCommitHook func()
var transactionManifestWriteHook func(TransactionState) error
var transactionAfterManifestRenameHook func(TransactionState) error
var transactionAfterCreatedParentMkdirHook func(string)
var transactionAfterCreatedParentOpenHook func(string)
var replacementScanAfterRootReadHook func()

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
		return errors.Join(original, writeManifestBound(*manifest, roots.tx))
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
	return directory.Sync()
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
