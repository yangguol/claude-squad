package workspace

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DiscoveredTransaction is a transaction directory classified without
// interpreting invalid or legacy bytes as the current WAL format.
type DiscoveredTransaction struct {
	Directory string
	Manifest  *TransactionManifest
	Legacy    bool
	Orphan    bool
	Invalid   error
}

// DiscoverTransactions classifies direct children of a validated transaction
// root. Invalid and legacy manifests are retained unchanged.
func DiscoverTransactions(projectPath string) ([]DiscoveredTransaction, error) {
	projectPath = filepath.Clean(projectPath)
	transactionRoot := filepath.Join(projectPath, "transactions")
	if err := validateRecoveryDirectory(projectPath, projectPath, "project"); err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(transactionRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect transaction root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("transaction root is not a real directory")
	}
	if err := ensureRealContainedPath(projectPath, transactionRoot, false, "transaction root"); err != nil {
		return nil, err
	}
	rootDirectory, err := platformOpenTrustedDirectory(transactionRoot)
	if err != nil {
		return nil, fmt.Errorf("open transaction root: %w", err)
	}
	entries, readErr := rootDirectory.ReadDir(-1)
	closeErr := rootDirectory.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("read transaction root: %w", err)
	}
	currentRootInfo, err := os.Lstat(transactionRoot)
	if err != nil || !os.SameFile(rootInfo, currentRootInfo) || !currentRootInfo.IsDir() || currentRootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errorsJoinMessage(err, "transaction root changed while reading")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]DiscoveredTransaction, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".cleanup.json") {
			continue
		}
		directory := filepath.Join(transactionRoot, entry.Name())
		discovered := DiscoveredTransaction{Directory: directory}
		info, statErr := os.Lstat(directory)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || entry.Type()&os.ModeSymlink != 0 {
			discovered.Invalid = errorsJoinMessage(statErr, "transaction entry is not a real directory")
			result = append(result, discovered)
			continue
		}
		manifestPath := filepath.Join(directory, "manifest.json")
		manifestInfo, manifestErr := os.Lstat(manifestPath)
		if errors.Is(manifestErr, os.ErrNotExist) {
			discovered.Orphan = true
			if removeErr := removeOrphanTransaction(projectPath, directory); removeErr != nil {
				discovered.Invalid = removeErr
			}
			result = append(result, discovered)
			continue
		}
		if manifestErr != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0 {
			discovered.Invalid = errorsJoinMessage(manifestErr, "transaction manifest is not a real regular file")
			result = append(result, discovered)
			continue
		}
		contents, readErr := readStableRegularFile(manifestPath, manifestInfo)
		if readErr != nil {
			discovered.Invalid = readErr
			result = append(result, discovered)
			continue
		}
		version, versionErr := manifestVersion(contents)
		if versionErr != nil {
			discovered.Invalid = versionErr
			result = append(result, discovered)
			continue
		}
		if version == 1 {
			discovered.Legacy = true
			result = append(result, discovered)
			continue
		}
		if version != CurrentTransactionVersion {
			discovered.Invalid = fmt.Errorf("unsupported transaction manifest version %d", version)
			result = append(result, discovered)
			continue
		}
		manifest, decodeErr := decodeManifestStrict(contents)
		if decodeErr == nil {
			decodeErr = ValidateManifest(projectPath, directory, manifest)
		}
		if decodeErr != nil {
			discovered.Invalid = decodeErr
		} else {
			discovered.Manifest = &manifest
		}
		result = append(result, discovered)
	}
	return result, nil
}

// ReadManifestStrict reads one current manifest without following symlinks,
// rejects non-canonical JSON structure, and validates all reconstructed paths.
func ReadManifestStrict(projectPath, transactionDir string) (TransactionManifest, error) {
	if err := validateTransactionDirectory(projectPath, transactionDir); err != nil {
		return TransactionManifest{}, err
	}
	manifestPath := filepath.Join(transactionDir, "manifest.json")
	info, err := os.Lstat(manifestPath)
	if err != nil {
		return TransactionManifest{}, fmt.Errorf("inspect transaction manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return TransactionManifest{}, errors.New("transaction manifest is not a real regular file")
	}
	contents, err := readStableRegularFile(manifestPath, info)
	if err != nil {
		return TransactionManifest{}, err
	}
	manifest, err := decodeManifestStrict(contents)
	if err != nil {
		return TransactionManifest{}, err
	}
	if err := ValidateManifest(projectPath, transactionDir, manifest); err != nil {
		return TransactionManifest{}, err
	}
	return manifest, nil
}

func decodeManifestStrict(contents []byte) (TransactionManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var manifest TransactionManifest
	if err := decoder.Decode(&manifest); err != nil {
		return TransactionManifest{}, fmt.Errorf("decode transaction manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return TransactionManifest{}, fmt.Errorf("decode transaction manifest trailing data: %w", err)
	}
	return manifest, nil
}

func manifestVersion(contents []byte) (int, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	var envelope struct {
		Version int `json:"version"`
	}
	if err := decoder.Decode(&envelope); err != nil {
		return 0, fmt.Errorf("decode transaction manifest version: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return 0, errors.New("transaction manifest contains trailing JSON")
	}
	return envelope.Version, nil
}

// ValidateManifest verifies that a v2 manifest can only reconstruct paths from
// the trusted project, transaction, and source roots.
func ValidateManifest(projectPath, transactionDir string, manifest TransactionManifest) error {
	projectPath = filepath.Clean(projectPath)
	transactionDir = filepath.Clean(transactionDir)
	if err := validateTransactionDirectory(projectPath, transactionDir); err != nil {
		return err
	}
	metadata, err := readProjectMetadataStrict(projectPath)
	if err != nil {
		return fmt.Errorf("read project metadata: %w", err)
	}
	projectID := filepath.Base(projectPath)
	if err := validateProjectMetadata(metadata, projectID, metadata.SourcePath); err != nil {
		return err
	}
	if manifest.Version != CurrentTransactionVersion {
		return fmt.Errorf("unsupported transaction manifest version %d", manifest.Version)
	}
	if manifest.TransactionID == "" || manifest.TransactionID != filepath.Base(transactionDir) || filepath.Base(manifest.TransactionID) != manifest.TransactionID {
		return errors.New("transaction ID does not match transaction directory")
	}
	if manifest.ProjectID != projectID || manifest.SourcePath != metadata.SourcePath {
		return errors.New("transaction project or source mismatch")
	}
	if manifest.SessionID == "" || strings.IndexByte(manifest.SessionID, 0) >= 0 {
		return errors.New("transaction session ID is missing or invalid")
	}
	if !validTransactionState(manifest.State) {
		return fmt.Errorf("unknown transaction state %q", manifest.State)
	}
	if !filepath.IsAbs(manifest.SourcePath) || filepath.Clean(manifest.SourcePath) != manifest.SourcePath {
		return errors.New("transaction source path is not canonical")
	}
	if err := validateRecoveryDirectory(manifest.SourcePath, manifest.SourcePath, "source"); err != nil {
		return err
	}

	targets, temporaryPaths := map[string]bool{}, map[string]bool{}
	for index, entry := range manifest.Entries {
		if !validOperation(entry.Operation) || !validEntryPhase(entry.Phase) || !validCleanupPhase(entry.CleanupPhase) || !phaseAllowed(entry.Operation, entry.Phase) {
			return fmt.Errorf("transaction entry %d has invalid operation or phase", index)
		}
		if err := validateRelativeManifestPath(entry.Path); err != nil {
			return fmt.Errorf("transaction entry %d target: %w", index, err)
		}
		if err := ensureReconstructiblePath(manifest.SourcePath, entry.Path, "target"); err != nil {
			return fmt.Errorf("transaction entry %d: %w", index, err)
		}
		if targets[entry.Path] {
			return fmt.Errorf("duplicate transaction target path %q", entry.Path)
		}
		targets[entry.Path] = true
		for _, field := range []struct {
			name, value string
			root        string
			sourceSide  bool
		}{
			{"stage", entry.StagePath, transactionDir, false}, {"capture", entry.CaptureName, manifest.SourcePath, true},
			{"rollback capture", entry.RollbackCaptureName, manifest.SourcePath, true}, {"quarantine", entry.QuarantinePath, transactionDir, false},
		} {
			if field.value == "" {
				continue
			}
			if err := validateRelativeManifestPath(field.value); err != nil {
				return fmt.Errorf("transaction entry %d %s: %w", index, field.name, err)
			}
			if field.sourceSide && filepath.Dir(filepath.Join(manifest.SourcePath, filepath.FromSlash(field.value))) != filepath.Dir(filepath.Join(manifest.SourcePath, filepath.FromSlash(entry.Path))) {
				return fmt.Errorf("transaction entry %d %s is not a target sibling", index, field.name)
			}
			if err := ensureReconstructiblePath(field.root, field.value, field.name); err != nil {
				return fmt.Errorf("transaction entry %d: %w", index, err)
			}
			if temporaryPaths[field.value] {
				return fmt.Errorf("duplicate transaction temporary path %q", field.value)
			}
			temporaryPaths[field.value] = true
		}
		if entry.Expected != nil && entry.Expected.Path != "" && entry.Expected.Path != entry.Path {
			return fmt.Errorf("transaction entry %d expected path mismatch", index)
		}
		if entry.Result != nil && entry.Result.Path != "" && entry.Result.Path != entry.Path {
			return fmt.Errorf("transaction entry %d result path mismatch", index)
		}
		for name, treeEntry := range entry.ExpectedTree {
			if err := validateRelativeManifestPath(name); err != nil || (treeEntry.Path != "" && treeEntry.Path != name) {
				return fmt.Errorf("transaction entry %d has invalid expected tree path %q", index, name)
			}
		}
	}
	return nil
}

func validTransactionState(state TransactionState) bool {
	switch state {
	case TransactionPrepared, TransactionApplying, TransactionRollingBack, TransactionRolledBack, TransactionRecoveryBlocked, TransactionCommitted, TransactionCleaning, TransactionComplete, TransactionCleanupBlocked:
		return true
	}
	return false
}

func validOperation(operation EntryOperation) bool {
	switch operation {
	case EntryOperationCreate, EntryOperationUpdate, EntryOperationDelete, EntryOperationCreatedParent:
		return true
	}
	return false
}
func validEntryPhase(phase EntryPhase) bool {
	switch phase {
	case EntryPhasePending, EntryPhaseCaptureIntent, EntryPhaseCaptured, EntryPhasePublishIntent, EntryPhasePublished, EntryPhaseRollbackCaptureIntent, EntryPhaseRollbackCaptured, EntryPhaseRestored:
		return true
	}
	return false
}
func validCleanupPhase(phase CleanupPhase) bool {
	switch phase {
	case CleanupPhasePending, CleanupPhaseQuarantineIntent, CleanupPhaseQuarantined, CleanupPhaseRemoved:
		return true
	}
	return false
}
func phaseAllowed(operation EntryOperation, phase EntryPhase) bool {
	switch operation {
	case EntryOperationCreate:
		return phase == EntryPhasePending || phase == EntryPhasePublishIntent || phase == EntryPhasePublished
	case EntryOperationUpdate:
		return validEntryPhase(phase)
	case EntryOperationDelete:
		return phase == EntryPhasePending || phase == EntryPhaseCaptureIntent || phase == EntryPhaseCaptured || phase == EntryPhaseRestored
	case EntryOperationCreatedParent:
		return phase == EntryPhasePending || phase == EntryPhasePublishIntent || phase == EntryPhasePublished || phase == EntryPhaseRollbackCaptureIntent || phase == EntryPhaseRollbackCaptured || phase == EntryPhaseRestored
	}
	return false
}

func validateRelativeManifestPath(name string) error {
	if name == "" || filepath.IsAbs(name) || strings.ContainsRune(name, 0) || strings.Contains(name, "\\") {
		return fmt.Errorf("unsafe path %q", name)
	}
	converted := filepath.FromSlash(name)
	if filepath.Clean(converted) != converted || filepath.ToSlash(converted) != name || converted == "." {
		return fmt.Errorf("unsafe path %q", name)
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("unsafe path %q", name)
		}
	}
	return nil
}

func ensureReconstructiblePath(root, relative, label string) error {
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := validateContained(root, target, label); err != nil {
		return err
	}
	parent := filepath.Dir(target)
	relativeParent, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	current := root
	for _, component := range strings.Split(relativeParent, string(filepath.Separator)) {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			return fmt.Errorf("inspect %s parent: %w", label, statErr)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s parent %q is not a real directory", label, current)
		}
	}
	return nil
}

func readProjectMetadataStrict(projectPath string) (shadowProjectMetadata, error) {
	metadataPath := filepath.Join(projectPath, "metadata.json")
	info, err := os.Lstat(metadataPath)
	if err != nil {
		return shadowProjectMetadata{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return shadowProjectMetadata{}, errors.New("shadow metadata is not a real regular file")
	}
	contents, err := readStableRegularFile(metadataPath, info)
	if err != nil {
		return shadowProjectMetadata{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var metadata shadowProjectMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return shadowProjectMetadata{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return shadowProjectMetadata{}, errors.New("shadow metadata contains trailing JSON")
	}
	return metadata, nil
}

func validateTransactionDirectory(projectPath, transactionDir string) error {
	root := filepath.Join(filepath.Clean(projectPath), "transactions")
	if filepath.Dir(filepath.Clean(transactionDir)) != root {
		return errors.New("transaction directory is not a direct child of transaction root")
	}
	if err := validateRecoveryDirectory(projectPath, root, "transaction root"); err != nil {
		return err
	}
	return validateRecoveryDirectory(root, transactionDir, "transaction directory")
}

func validateRecoveryDirectory(root, path, label string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a real directory", label)
	}
	if filepath.Clean(root) != filepath.Clean(path) {
		return ensureRealContainedPath(root, path, false, label)
	}
	return nil
}

func readStableRegularFile(path string, expected os.FileInfo) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open regular file: %w", err)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		_ = file.Close()
		return nil, errorsJoinMessage(statErr, "transaction manifest changed while opening")
	}
	contents, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	current, statErr := os.Lstat(path)
	if statErr != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, current) {
		return nil, errorsJoinMessage(statErr, "transaction manifest changed while reading")
	}
	return contents, nil
}

func removeOrphanTransaction(projectPath, transactionDir string) error {
	root := filepath.Join(filepath.Clean(projectPath), "transactions")
	if filepath.Dir(filepath.Clean(transactionDir)) != root {
		return errors.New("orphan transaction is not a direct child of transaction root")
	}
	if err := validateRecoveryDirectory(projectPath, root, "transaction root"); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect transaction root: %w", err)
	}
	info, err := os.Lstat(transactionDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errorsJoinMessage(err, "orphan transaction is not a real directory")
	}
	if err := ensureRealContainedPath(root, transactionDir, false, "orphan transaction"); err != nil {
		return err
	}
	manifestPath := filepath.Join(transactionDir, "manifest.json")
	if _, err := os.Lstat(manifestPath); !errors.Is(err, os.ErrNotExist) {
		return errorsJoinMessage(err, "orphan transaction acquired a manifest")
	}
	currentRootInfo, err := os.Lstat(root)
	if err != nil || !os.SameFile(rootInfo, currentRootInfo) || !currentRootInfo.IsDir() || currentRootInfo.Mode()&os.ModeSymlink != 0 {
		return errorsJoinMessage(err, "transaction root changed before orphan quarantine")
	}
	currentInfo, err := os.Lstat(transactionDir)
	if err != nil || !os.SameFile(info, currentInfo) || !currentInfo.IsDir() || currentInfo.Mode()&os.ModeSymlink != 0 {
		return errorsJoinMessage(err, "orphan transaction changed before quarantine")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	quarantine := filepath.Join(root, ".orphan-"+hex.EncodeToString(nonce[:]))
	if err := os.Rename(transactionDir, quarantine); err != nil {
		return fmt.Errorf("quarantine orphan transaction: %w", err)
	}
	quarantined, err := os.Lstat(quarantine)
	if err != nil || !quarantined.IsDir() || quarantined.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, quarantined) {
		return errorsJoinMessage(err, "quarantined orphan transaction changed identity")
	}
	if _, err := os.Lstat(filepath.Join(quarantine, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		return errorsJoinMessage(err, "quarantined orphan transaction contains a manifest")
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return fmt.Errorf("remove quarantined orphan transaction: %w", err)
	}
	return nil
}
