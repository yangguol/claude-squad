package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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

// RecoveryDisposition summarizes whether recovery completed or retained a
// transaction because continuing could overwrite content not owned by it.
type RecoveryDisposition string

const (
	RecoveryRolledBack RecoveryDisposition = "rolled-back"
	RecoveryBlocked    RecoveryDisposition = "recovery-blocked"
	RecoveryUnchanged  RecoveryDisposition = "unchanged"
)

// ApplyMetadataRepair is populated by the later metadata-repair phase. It is
// part of the recovery report now so callers can consume one stable shape.
type ApplyMetadataRepair struct {
	TransactionID string
	AppliedTree   string
	AppliedAt     string
}

type RecoveryIssue struct {
	TransactionID string
	State         TransactionState
	Message       string
	Paths         []string
	RetainedPaths []string
}

type RecoveryReport struct {
	Disposition RecoveryDisposition
	Issues      []RecoveryIssue
	Repairs     []ApplyMetadataRepair
	Orphans     []string
}

// RecoverProject serializes discovery and automatic recovery for one project.
func RecoverProject(projectPath string, sessions map[string]*ShadowData) (report RecoveryReport, returnErr error) {
	lock, err := AcquireProjectLock(projectPath, "recover")
	if err != nil {
		return RecoveryReport{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	return recoverProjectLocked(projectPath, sessions)
}

func recoverProjectLocked(projectPath string, sessions map[string]*ShadowData) (RecoveryReport, error) {
	discovered, err := DiscoverTransactions(projectPath)
	if err != nil {
		return RecoveryReport{}, err
	}
	report := RecoveryReport{Disposition: RecoveryUnchanged}
	for _, transaction := range discovered {
		if transaction.Invalid != nil || transaction.Legacy || transaction.Orphan {
			message := "transaction requires explicit recovery"
			if transaction.Invalid != nil {
				message = transaction.Invalid.Error()
			} else if transaction.Legacy {
				message = "legacy transaction requires explicit recovery"
			}
			report.Disposition = RecoveryBlocked
			report.Issues = append(report.Issues, RecoveryIssue{Message: message, RetainedPaths: []string{transaction.Directory}})
			if transaction.Orphan {
				report.Orphans = append(report.Orphans, transaction.Directory)
			}
			continue
		}
		manifest := transaction.Manifest
		if manifest == nil {
			continue
		}
		if session, ok := sessions[manifest.SessionID]; ok && session != nil && (session.ProjectID != manifest.ProjectID || session.SourcePath != manifest.SourcePath) {
			report.Disposition = RecoveryBlocked
			report.Issues = append(report.Issues, RecoveryIssue{TransactionID: manifest.TransactionID, State: manifest.State, Message: "transaction session metadata mismatch", RetainedPaths: []string{transaction.Directory}})
			continue
		}
		transactionReport, err := RecoverTransaction(manifest, transaction.Directory)
		if err != nil {
			return RecoveryReport{}, err
		}
		report.Issues = append(report.Issues, transactionReport.Issues...)
		report.Repairs = append(report.Repairs, transactionReport.Repairs...)
		report.Orphans = append(report.Orphans, transactionReport.Orphans...)
		switch transactionReport.Disposition {
		case RecoveryBlocked:
			report.Disposition = RecoveryBlocked
		case RecoveryRolledBack:
			if report.Disposition != RecoveryBlocked {
				report.Disposition = RecoveryRolledBack
			}
		}
	}
	return report, nil
}

func accessStateFromReport(report RecoveryReport) AccessState {
	state := AccessState{Disposition: report.Disposition}
	for _, issue := range report.Issues {
		state.ReadOnly = true
		if state.Message == "" {
			state.Message = issue.Message
		}
		state.Paths = append(state.Paths, issue.Paths...)
		state.RetainedPaths = append(state.RetainedPaths, issue.RetainedPaths...)
	}
	if len(report.Orphans) != 0 {
		state.ReadOnly = true
		state.RetainedPaths = append(state.RetainedPaths, report.Orphans...)
	}
	if state.ReadOnly && state.Message == "" {
		state.Message = "project requires explicit recovery"
	}
	return state
}

// ObservedEntryState records the identity-bearing locations used to reconcile
// an entry whose filesystem mutation may have completed before its phase write.
type ObservedEntryState struct {
	TargetExists           bool
	TargetMatchesExpected  bool
	TargetMatchesResult    bool
	CaptureExists          bool
	CaptureMatchesExpected bool
	RollbackExists         bool
	RollbackMatchesResult  bool
	StageExists            bool
	StageMatchesResult     bool
	QuarantineExists       bool
}

// DiscoverTransactions classifies direct children of a validated transaction
// root. Invalid and legacy manifests are retained unchanged.
func DiscoverTransactions(projectPath string) ([]DiscoveredTransaction, error) {
	if filepath.Clean(projectPath) != projectPath {
		return nil, errors.New("project path is not canonical")
	}
	transactionRoot := filepath.Join(projectPath, "transactions")
	if err := validateCanonicalTrustedDirectory(projectPath, "project"); err != nil {
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
	if err := validateCanonicalTrustedDirectory(transactionRoot, "transaction root"); err != nil {
		return nil, err
	}
	rootDirectory, err := platformOpenTrustedDirectory(transactionRoot)
	if err != nil {
		return nil, fmt.Errorf("open transaction root: %w", err)
	}
	defer rootDirectory.Close()
	entries, err := rootDirectory.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("read transaction root: %w", err)
	}
	currentRootInfo, err := os.Lstat(transactionRoot)
	if err != nil || !os.SameFile(rootInfo, currentRootInfo) || !currentRootInfo.IsDir() || currentRootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errorsJoinMessage(err, "transaction root changed while reading")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]DiscoveredTransaction, 0, len(entries))
	for _, entry := range entries {
		directory := filepath.Join(transactionRoot, entry.Name())
		discovered := DiscoveredTransaction{Directory: directory}
		if strings.HasPrefix(entry.Name(), orphanQuarantinePrefix) {
			discovered.Orphan = true
			if !isRetainedOrphanName(entry.Name()) {
				discovered.Invalid = errors.New("invalid retained orphan quarantine name")
			} else if markerErr := validateRetainedOrphanMarkerAt(rootDirectory, entry.Name()); markerErr != nil {
				discovered.Invalid = markerErr
			} else {
				discovered.Invalid = errors.New("retained orphan cleanup tombstone requires explicit recovery")
			}
			result = append(result, discovered)
			continue
		}
		if strings.HasSuffix(entry.Name(), ".cleanup.json") {
			discovered.Legacy = true
			if err := validateLegacyCleanupMarker(directory, entry); err != nil {
				discovered.Invalid = err
			}
			result = append(result, discovered)
			continue
		}
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
			quarantineName, nameErr := randomProbeName(".orphan-")
			if nameErr != nil {
				discovered.Invalid = nameErr
			} else if removeErr := removeOrphanAt(rootDirectory, entry.Name(), quarantineName); removeErr != nil {
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
	if err := rejectDuplicateJSONMembers(contents); err != nil {
		return TransactionManifest{}, err
	}
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

func rejectDuplicateJSONMembers(contents []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	var parse func() error
	parse = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, structured := token.(json.Delim)
		if !structured {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object member name is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON object member %q", key)
				}
				seen[key] = struct{}{}
				if err := parse(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := parse(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	if err := parse(); err != nil {
		return fmt.Errorf("decode transaction manifest structure: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return fmt.Errorf("decode transaction manifest trailing data: %w", err)
	}
	return nil
}

func manifestVersion(contents []byte) (int, error) {
	if err := rejectDuplicateJSONMembers(contents); err != nil {
		return 0, err
	}
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
	if filepath.Clean(projectPath) != projectPath {
		return errors.New("project path is not canonical")
	}
	if filepath.Clean(transactionDir) != transactionDir {
		return errors.New("transaction path is not canonical")
	}
	if err := validateCanonicalTrustedDirectory(projectPath, "project"); err != nil {
		return err
	}
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
	if projectID != shadowProjectID(metadata.SourcePath) {
		return errors.New("shadow project metadata source identity mismatch")
	}
	if err := validateCanonicalTrustedDirectory(metadata.SourcePath, "source"); err != nil {
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
	if err := validateCanonicalTrustedDirectory(manifest.SourcePath, "source"); err != nil {
		return err
	}

	if err := validateAppliedEntries(manifest); err != nil {
		return err
	}

	sourcePaths, transactionPaths := []manifestNamespacePath{}, []manifestNamespacePath{}
	for index, entry := range manifest.Entries {
		phaseOK := phaseAllowed(entry.Operation, entry.Phase) || entry.Operation == EntryOperationCreate && entry.CaptureName != "" && len(entry.ExpectedTree) != 0 && (entry.Phase == EntryPhaseCaptureIntent || entry.Phase == EntryPhaseCaptured)
		if !validOperation(entry.Operation) || !validEntryPhase(entry.Phase) || !validCleanupPhase(entry.CleanupPhase) || !phaseOK {
			return fmt.Errorf("transaction entry %d has invalid operation or phase", index)
		}
		if err := validateRelativeManifestPath(entry.Path); err != nil {
			return fmt.Errorf("transaction entry %d target: %w", index, err)
		}
		if !manifestTargetParentWillBeReplaced(manifest, entry.Path) {
			if err := ensureReconstructiblePath(manifest.SourcePath, entry.Path, "target"); err != nil {
				return fmt.Errorf("transaction entry %d: %w", index, err)
			}
		}
		targetIdentity := filepath.Join(manifest.SourcePath, filepath.FromSlash(entry.Path))
		if entry.Operation != EntryOperationCreatedParent && !manifestTargetParentWillBeReplaced(manifest, entry.Path) {
			if err := addManifestNamespacePath(&sourcePaths, targetIdentity); err != nil {
				return fmt.Errorf("transaction entry %d target: %w", index, err)
			}
		}
		for _, field := range []struct {
			name, value string
			root        string
			sourceSide  bool
			required    bool
		}{
			{"stage", entry.StagePath, transactionDir, false, entry.Operation == EntryOperationCreate || entry.Operation == EntryOperationUpdate || entry.Operation == EntryOperationCreatedParent},
			{"capture", entry.CaptureName, manifest.SourcePath, true, entry.Operation == EntryOperationUpdate || entry.Operation == EntryOperationDelete},
			{"rollback capture", entry.RollbackCaptureName, manifest.SourcePath, true, entry.Operation == EntryOperationCreate || entry.Operation == EntryOperationUpdate || entry.Operation == EntryOperationCreatedParent},
			{"quarantine", entry.QuarantinePath, transactionDir, false, entry.Operation == EntryOperationUpdate || entry.Operation == EntryOperationDelete},
		} {
			if field.value == "" {
				if field.required {
					return fmt.Errorf("transaction entry %d is missing %s path", index, field.name)
				}
				continue
			}
			if err := validateRelativeManifestPath(field.value); err != nil {
				return fmt.Errorf("transaction entry %d %s: %w", index, field.name, err)
			}
			if field.sourceSide && filepath.Dir(filepath.Join(manifest.SourcePath, filepath.FromSlash(field.value))) != filepath.Dir(filepath.Join(manifest.SourcePath, filepath.FromSlash(entry.Path))) {
				return fmt.Errorf("transaction entry %d %s is not a target sibling", index, field.name)
			}
			if !(field.sourceSide && manifestTargetParentWillBeReplaced(manifest, entry.Path)) && !(entry.Operation == EntryOperationCreatedParent && field.name == "stage") {
				if err := ensureReconstructiblePath(field.root, field.value, field.name); err != nil {
					return fmt.Errorf("transaction entry %d: %w", index, err)
				}
			}
			identity := filepath.Join(field.root, filepath.FromSlash(field.value))
			namespace := &transactionPaths
			if field.sourceSide {
				namespace = &sourcePaths
			}
			if !(field.sourceSide && manifestTargetParentWillBeReplaced(manifest, entry.Path)) && !(entry.Operation == EntryOperationCreatedParent && field.name == "stage") {
				if err := addManifestNamespacePath(namespace, identity); err != nil {
					return fmt.Errorf("transaction entry %d %s: %w", index, field.name, err)
				}
			}
		}
		requireExpected := entry.Operation == EntryOperationUpdate || entry.Operation == EntryOperationDelete
		requireResult := entry.Operation == EntryOperationCreate || entry.Operation == EntryOperationUpdate || entry.Operation == EntryOperationCreatedParent
		if requireExpected && entry.Expected == nil {
			return fmt.Errorf("transaction entry %d is missing expected identity", index)
		}
		if requireResult && entry.Result == nil {
			return fmt.Errorf("transaction entry %d is missing result identity", index)
		}
		if entry.Expected != nil {
			if entry.Expected.Path != entry.Path {
				return fmt.Errorf("transaction entry %d expected path mismatch", index)
			}
			if err := validateEntryIdentity(*entry.Expected, true); err != nil {
				return fmt.Errorf("transaction entry %d expected identity: %w", index, err)
			}
		}
		if entry.Result != nil {
			if entry.Result.Path != entry.Path {
				return fmt.Errorf("transaction entry %d result path mismatch", index)
			}
			allowDirectory := entry.Operation == EntryOperationCreatedParent
			if err := validateEntryIdentity(*entry.Result, allowDirectory); err != nil {
				return fmt.Errorf("transaction entry %d result identity: %w", index, err)
			}
		}
		if (entry.CreatedDevice == 0) != (entry.CreatedInode == 0) {
			return fmt.Errorf("transaction entry %d has partial created-parent ownership", index)
		}
		hasOwnership := entry.CreatedDevice != 0
		if entry.Operation != EntryOperationCreatedParent && hasOwnership {
			return fmt.Errorf("transaction entry %d has invalid created-parent ownership", index)
		}
		if entry.Operation == EntryOperationCreatedParent {
			switch entry.Phase {
			case EntryPhasePending:
				if hasOwnership {
					return fmt.Errorf("transaction entry %d has ownership before created-parent intent", index)
				}
			case EntryPhasePublishIntent, EntryPhasePublished, EntryPhaseRollbackCaptureIntent, EntryPhaseRollbackCaptured, EntryPhaseRestored:
				if !hasOwnership {
					return fmt.Errorf("transaction entry %d is missing created-parent ownership", index)
				}
			}
		}
		treePaths := make([]string, 0, len(entry.ExpectedTree))
		for name, treeEntry := range entry.ExpectedTree {
			if err := validateRelativeManifestPath(name); err != nil || treeEntry.Path != name {
				return fmt.Errorf("transaction entry %d has invalid expected tree path %q", index, name)
			}
			if err := validateEntryIdentity(treeEntry, true); err != nil {
				return fmt.Errorf("transaction entry %d expected tree identity %q: %w", index, name, err)
			}
			if err := ensureReconstructiblePath(manifest.SourcePath, filepath.ToSlash(filepath.Join(filepath.FromSlash(entry.Path), filepath.FromSlash(name))), "expected tree"); err != nil {
				return fmt.Errorf("transaction entry %d expected tree path %q: %w", index, name, err)
			}
			treePaths = append(treePaths, name)
		}
		sort.Strings(treePaths)
		for treeIndex, name := range treePaths {
			for _, other := range treePaths[treeIndex+1:] {
				if strings.HasPrefix(other, name+"/") && entry.ExpectedTree[name].Type != EntryType("directory") {
					return fmt.Errorf("transaction entry %d expected tree paths %q and %q overlap below a non-directory", index, name, other)
				}
			}
		}
	}
	return nil
}

func validateAppliedEntries(manifest TransactionManifest) error {
	if manifest.AppliedEntries == nil {
		return nil
	}
	paths := make([]string, 0, len(manifest.AppliedEntries))
	for path, entry := range manifest.AppliedEntries {
		if err := validateRelativeManifestPath(path); err != nil || entry.Path != path {
			return fmt.Errorf("invalid applied entry path %q", path)
		}
		if err := validateEntryIdentity(entry, true); err != nil {
			return fmt.Errorf("invalid applied entry identity %q: %w", path, err)
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for index, path := range paths {
		for _, descendant := range paths[index+1:] {
			if strings.HasPrefix(descendant, path+"/") && manifest.AppliedEntries[path].Type != EntryType("directory") {
				return fmt.Errorf("applied paths %q and %q overlap below a non-directory", path, descendant)
			}
		}
	}
	for index, transactionEntry := range manifest.Entries {
		applied, exists := manifest.AppliedEntries[transactionEntry.Path]
		switch transactionEntry.Operation {
		case EntryOperationCreate, EntryOperationUpdate:
			if transactionEntry.Result == nil || !exists || !reflect.DeepEqual(applied, *transactionEntry.Result) {
				return fmt.Errorf("transaction entry %d result is inconsistent with applied entries", index)
			}
		case EntryOperationDelete:
			if exists && !publishedCreatedParentAt(manifest, transactionEntry.Path) {
				return fmt.Errorf("transaction entry %d delete remains in applied entries", index)
			}
		}
	}
	return nil
}

func manifestTargetParentWillBeReplaced(manifest TransactionManifest, path string) bool {
	for _, candidate := range manifest.Entries {
		if candidate.Path == path || !strings.HasPrefix(path, candidate.Path+"/") {
			continue
		}
		if (candidate.Operation == EntryOperationCreate || candidate.Operation == EntryOperationUpdate) && candidate.Expected == nil && len(candidate.ExpectedTree) != 0 && candidate.Result != nil && candidate.Result.Type != EntryType("directory") {
			return true
		}
		if candidate.Operation == EntryOperationCreatedParent && candidate.Result != nil && candidate.Result.Type == EntryType("directory") && manifestReplacesNonDirectoryAt(manifest, candidate.Path) {
			return true
		}
	}
	return false
}

func manifestReplacesNonDirectoryAt(manifest TransactionManifest, path string) bool {
	for _, candidate := range manifest.Entries {
		if candidate.Path == path && (candidate.Operation == EntryOperationDelete || candidate.Operation == EntryOperationUpdate) && candidate.Expected != nil && candidate.Expected.Type != EntryType("directory") {
			return true
		}
	}
	return false
}

type manifestNamespacePath struct {
	path       string
	components []manifestPathComponent
}

type manifestPathComponent struct {
	name            string
	identity        *manifestFileIdentity
	caseInsensitive bool
}

func addManifestNamespacePath(paths *[]manifestNamespacePath, candidate string) error {
	resolved, err := resolveManifestNamespacePath(candidate)
	if err != nil {
		return err
	}
	for _, existing := range *paths {
		if manifestPathsOverlap(existing.path, candidate) || manifestComponentsOverlap(existing, resolved) {
			return fmt.Errorf("namespace path %q overlaps or aliases %q", candidate, existing.path)
		}
	}
	*paths = append(*paths, resolved)
	return nil
}

var manifestComponentAfterOpenHook func(string)
var lookupManifestComponent = platformLookupManifestComponent

func resolveManifestNamespacePath(path string) (manifestNamespacePath, error) {
	volume := filepath.VolumeName(path)
	root := volume + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, root), string(filepath.Separator))
	components := make([]manifestPathComponent, 0, len(parts))
	parent, err := platformOpenTrustedDirectory(root)
	if err != nil {
		return manifestNamespacePath{}, fmt.Errorf("open namespace root %q: %w", root, err)
	}
	defer func() { _ = parent.Close() }()
	caseInsensitive := false
	for index, name := range parts {
		lookup, lookupErr := lookupManifestComponent(parent, name)
		if lookupErr != nil && !errors.Is(lookupErr, os.ErrNotExist) {
			return manifestNamespacePath{}, lookupErr
		}
		if errors.Is(lookupErr, os.ErrNotExist) {
			caseInsensitive = caseInsensitive || lookup.caseInsensitive
			components = append(components, manifestPathComponent{name: name, caseInsensitive: caseInsensitive})
			for _, remaining := range parts[index+1:] {
				components = append(components, manifestPathComponent{name: remaining, caseInsensitive: caseInsensitive})
			}
			break
		}
		opened, info, err := openManifestComponentAt(parent, lookup.actualName)
		if err != nil {
			return manifestNamespacePath{}, fmt.Errorf("open namespace component %q: %w", lookup.actualName, err)
		}
		caseInsensitive = lookup.caseInsensitive || lookup.aliasDetected
		components = append(components, manifestPathComponent{name: lookup.actualName, identity: info, caseInsensitive: caseInsensitive})
		if !info.directory && index < len(parts)-1 {
			if opened != nil {
				_ = opened.Close()
			}
			for _, remaining := range parts[index+1:] {
				components = append(components, manifestPathComponent{name: remaining, caseInsensitive: caseInsensitive})
			}
			break
		}
		if index < len(parts)-1 {
			if err := parent.Close(); err != nil {
				_ = opened.Close()
				return manifestNamespacePath{}, err
			}
			parent = opened
		} else if opened != nil {
			if err := opened.Close(); err != nil {
				return manifestNamespacePath{}, err
			}
		}
	}
	return manifestNamespacePath{path: path, components: components}, nil
}

func manifestComponentsOverlap(left, right manifestNamespacePath) bool {
	limit := len(left.components)
	if len(right.components) < limit {
		limit = len(right.components)
	}
	for index := 0; index < limit; index++ {
		leftComponent, rightComponent := left.components[index], right.components[index]
		if leftComponent.identity != nil && rightComponent.identity != nil {
			if *leftComponent.identity != *rightComponent.identity {
				return false
			}
			continue
		}
		if leftComponent.name == rightComponent.name {
			continue
		}
		if !(leftComponent.caseInsensitive || rightComponent.caseInsensitive) || !strings.EqualFold(leftComponent.name, rightComponent.name) {
			return false
		}
	}
	return true
}

var sha256IdentityPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validateEntryIdentity(entry Entry, allowDirectory bool) error {
	if entry.Mode != entry.Mode.Perm() {
		return errors.New("mode contains non-permission bits")
	}
	switch entry.Type {
	case EntryRegular:
		if !sha256IdentityPattern.MatchString(entry.ContentHash) || entry.SymlinkTarget != "" {
			return errors.New("regular entry requires a SHA-256 hash and forbids a symlink target")
		}
	case EntrySymlink:
		if entry.ContentHash != "" || entry.SymlinkTarget == "" || strings.IndexByte(entry.SymlinkTarget, 0) >= 0 {
			return errors.New("symlink entry requires a target and forbids a content hash")
		}
	case EntryType("directory"):
		if !allowDirectory || entry.ContentHash != "" || entry.SymlinkTarget != "" {
			return errors.New("directory identity is not allowed here")
		}
	default:
		return fmt.Errorf("unknown entry type %q", entry.Type)
	}
	return nil
}

func manifestPathsOverlap(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if left == right {
		return true
	}
	separator := string(filepath.Separator)
	return strings.HasPrefix(left, right+separator) || strings.HasPrefix(right, left+separator)
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

func validateCanonicalTrustedDirectory(path, label string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%s path is not canonical", label)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve %s path: %w", label, err)
	}
	if filepath.Clean(resolved) != path {
		return fmt.Errorf("%s path contains a symlinked component", label)
	}
	if err := validateRecoveryDirectory(path, path, label); err != nil {
		return err
	}
	directory, err := platformOpenTrustedDirectory(path)
	if err != nil {
		return fmt.Errorf("open %s path: %w", label, err)
	}
	defer directory.Close()
	if err := platformVerifyCanonicalDirectoryPath(directory, path); err != nil {
		return fmt.Errorf("%s path is not the stored canonical path: %w", label, err)
	}
	return nil
}

func validateLegacyCleanupMarker(path string, entry os.DirEntry) error {
	info, err := os.Lstat(path)
	if err != nil || !entry.Type().IsRegular() || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errorsJoinMessage(err, "legacy cleanup marker is not a real regular file")
	}
	contents, err := readStableRegularFile(path, info)
	if err != nil {
		return err
	}
	if err := rejectDuplicateJSONMembers(contents); err != nil {
		return fmt.Errorf("invalid legacy cleanup marker: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var transaction Transaction
	if err := decoder.Decode(&transaction); err != nil {
		return fmt.Errorf("invalid legacy cleanup marker: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("invalid legacy cleanup marker trailing JSON")
	}
	if transaction.Version != 1 || transaction.State != transactionCommitted || transaction.SourcePath == "" || transaction.SessionID == "" {
		return errors.New("invalid legacy cleanup marker contents")
	}
	return nil
}

func validateTransactionDirectory(projectPath, transactionDir string) error {
	root := filepath.Join(filepath.Clean(projectPath), "transactions")
	if filepath.Dir(filepath.Clean(transactionDir)) != root {
		return errors.New("transaction directory is not a direct child of transaction root")
	}
	if err := validateCanonicalTrustedDirectory(root, "transaction root"); err != nil {
		return err
	}
	return validateCanonicalTrustedDirectory(transactionDir, "transaction directory")
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

var orphanBeforeQuarantineHook func()
var orphanAfterQuarantineHook func(string)

func removeOrphanTransaction(projectPath, transactionDir string) error {
	root := filepath.Join(filepath.Clean(projectPath), "transactions")
	if filepath.Dir(filepath.Clean(transactionDir)) != root {
		return errors.New("orphan transaction is not a direct child of transaction root")
	}
	if err := validateCanonicalTrustedDirectory(projectPath, "project"); err != nil {
		return err
	}
	if err := validateRecoveryDirectory(projectPath, root, "transaction root"); err != nil {
		return err
	}
	rootDirectory, err := platformOpenTrustedDirectory(root)
	if err != nil {
		return fmt.Errorf("open transaction root: %w", err)
	}
	defer rootDirectory.Close()
	if orphanBeforeQuarantineHook != nil {
		orphanBeforeQuarantineHook()
	}
	quarantineName, err := randomProbeName(".orphan-")
	if err != nil {
		return err
	}
	return removeOrphanAt(rootDirectory, filepath.Base(transactionDir), quarantineName)
}
