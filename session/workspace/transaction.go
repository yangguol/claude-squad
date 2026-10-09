package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const CurrentTransactionVersion = 2

type TransactionState string
type EntryOperation string
type EntryPhase string
type CleanupPhase string

// DurabilityPoint identifies a stable, post-durability crash boundary. The
// callback is a package-private test seam; production execution leaves it as a
// no-op.
type DurabilityPoint string

var durabilityCheckpoint = func(DurabilityPoint) {}

func durabilityPathPoint(point DurabilityPoint, path string) DurabilityPoint {
	return DurabilityPoint(string(point) + ":" + path)
}

const (
	DurabilityManifestBeforePersistence DurabilityPoint = "manifest.before-persistence"
	DurabilityManifestAfterPersistence  DurabilityPoint = "manifest.after-persistence"
	DurabilityForwardCapture            DurabilityPoint = "forward.capture"
	DurabilityForwardPublish            DurabilityPoint = "forward.publish"
	DurabilityForwardReadyToCommit      DurabilityPoint = "forward.ready-to-commit"
	DurabilityForwardCommit             DurabilityPoint = "forward.commit"
	DurabilityRollbackCapture           DurabilityPoint = "rollback.capture"
	DurabilityRollbackRestore           DurabilityPoint = "rollback.restore"
	DurabilityRollbackComplete          DurabilityPoint = "rollback.complete"
	DurabilityCleanupQuarantine         DurabilityPoint = "cleanup.quarantine"
	DurabilityCleanupRemoval            DurabilityPoint = "cleanup.removal"
	DurabilityCleanupComplete           DurabilityPoint = "cleanup.complete"
)

const (
	TransactionPrepared        TransactionState = "prepared"
	TransactionApplying        TransactionState = "applying"
	TransactionRollingBack     TransactionState = "rolling-back"
	TransactionRolledBack      TransactionState = "rolled-back"
	TransactionRecoveryBlocked TransactionState = "recovery-blocked"
	TransactionCommitted       TransactionState = "committed"
	TransactionCleaning        TransactionState = "cleaning"
	TransactionComplete        TransactionState = "complete"
	TransactionCleanupBlocked  TransactionState = "cleanup-blocked"
)

const (
	EntryOperationCreate        EntryOperation = "create"
	EntryOperationUpdate        EntryOperation = "update"
	EntryOperationDelete        EntryOperation = "delete"
	EntryOperationCreatedParent EntryOperation = "created-parent-directory"
)

const (
	EntryPhasePending               EntryPhase = "pending"
	EntryPhaseCaptureIntent         EntryPhase = "capture-intent"
	EntryPhaseCaptured              EntryPhase = "captured"
	EntryPhasePublishIntent         EntryPhase = "publish-intent"
	EntryPhasePublished             EntryPhase = "published"
	EntryPhaseRollbackCaptureIntent EntryPhase = "rollback-capture-intent"
	EntryPhaseRollbackCaptured      EntryPhase = "rollback-captured"
	EntryPhaseRestored              EntryPhase = "restored"
)

const (
	CleanupPhasePending          CleanupPhase = "pending"
	CleanupPhaseQuarantineIntent CleanupPhase = "quarantine-intent"
	CleanupPhaseQuarantined      CleanupPhase = "quarantined"
	CleanupPhaseRemoved          CleanupPhase = "removed"
)

type TransactionEntry struct {
	Path                string           `json:"path"`
	StagePath           string           `json:"stage_path,omitempty"`
	CaptureName         string           `json:"capture_name,omitempty"`
	RollbackCaptureName string           `json:"rollback_capture_name,omitempty"`
	QuarantinePath      string           `json:"quarantine_path,omitempty"`
	Operation           EntryOperation   `json:"operation"`
	Phase               EntryPhase       `json:"phase"`
	CleanupPhase        CleanupPhase     `json:"cleanup_phase,omitempty"`
	Expected            *Entry           `json:"expected,omitempty"`
	Result              *Entry           `json:"result,omitempty"`
	ExpectedTree        map[string]Entry `json:"expected_tree,omitempty"`
	CreatedDevice       uint64           `json:"created_device,omitempty"`
	CreatedInode        uint64           `json:"created_inode,omitempty"`
	stageContents       []byte
	stageSymlinkTarget  string
	stageMode           os.FileMode
	preparedParents     map[string]EntryType
}

type TransactionManifest struct {
	Version        int                `json:"version"`
	TransactionID  string             `json:"transaction_id"`
	ProjectID      string             `json:"project_id"`
	SessionID      string             `json:"session_id"`
	SourcePath     string             `json:"source_path"`
	State          TransactionState   `json:"state"`
	AppliedTree    string             `json:"applied_tree,omitempty"`
	AppliedAt      string             `json:"applied_at,omitempty"`
	AppliedEntries map[string]Entry   `json:"applied_entries,omitempty"`
	Entries        []TransactionEntry `json:"entries"`
	RecoveryIssues []RecoveryIssue    `json:"recovery_issues,omitempty"`
}

func WriteManifestDurable(manifest TransactionManifest, transactionDir string) error {
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode transaction manifest: %w", err)
	}

	var temporary string
	var file fileHandle
	for attempts := 0; attempts < 100; attempts++ {
		temporary, err = randomSiblingName(filepath.Join(transactionDir, "manifest.json"), ".manifest-")
		if err != nil {
			return fmt.Errorf("name transaction manifest temporary: %w", err)
		}
		file, err = applyFS.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("create transaction manifest temporary: %w", err)
		}
		break
	}
	if file == nil {
		return errors.New("could not allocate exclusive transaction manifest temporary")
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = removeCreatedTemp(temporary)
		}
	}()

	written, writeErr := file.Write(contents)
	if writeErr == nil && written != len(contents) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return errors.Join(fmt.Errorf("write transaction manifest temporary: %w", writeErr), file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync transaction manifest temporary: %w", err), file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close transaction manifest temporary: %w", err)
	}

	manifestPath := filepath.Join(transactionDir, "manifest.json")
	if err := applyFS.Rename(temporary, manifestPath); err != nil {
		return fmt.Errorf("publish transaction manifest: %w", err)
	}
	removeTemporary = false
	if err := SyncDirectory(transactionDir); err != nil {
		return fmt.Errorf("sync transaction directory: %w", err)
	}
	return nil
}

func SyncDirectory(path string) error {
	directory, err := applyFS.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
