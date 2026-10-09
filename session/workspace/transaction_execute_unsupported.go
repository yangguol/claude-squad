//go:build !darwin && !linux

package workspace

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
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

func unsupportedAtomicApplyError() error {
	return errors.New("atomic apply is unsupported on " + runtime.GOOS)
}

func publishedCreatedParentAt(TransactionManifest, string) bool { return false }

func ExecuteTransaction(*TransactionManifest, string) error     { return unsupportedAtomicApplyError() }
func ApplyEntryForward(*TransactionManifest, int, string) error { return unsupportedAtomicApplyError() }
func ValidatePublishedResults(TransactionManifest, string) error {
	return unsupportedAtomicApplyError()
}
func ObserveEntryState(TransactionManifest, TransactionEntry, string) (ObservedEntryState, error) {
	return ObservedEntryState{}, unsupportedAtomicApplyError()
}
func QuarantineSourceTemporary(*TransactionManifest, int, string) error {
	return unsupportedAtomicApplyError()
}
func CleanCommittedTransaction(*TransactionManifest, string) (*CleanupBlockedWarning, error) {
	return nil, unsupportedAtomicApplyError()
}
func PreflightRecoverTransaction(*TransactionManifest, string) (RecoveryReport, error) {
	return RecoveryReport{}, unsupportedAtomicApplyError()
}
func RecoverTransaction(*TransactionManifest, string) (RecoveryReport, error) {
	return RecoveryReport{}, unsupportedAtomicApplyError()
}
func RollbackTransaction(*TransactionManifest, string) (RecoveryReport, error) {
	return RecoveryReport{}, unsupportedAtomicApplyError()
}
func writeManifestAtBoundDirectory(TransactionManifest, string) error {
	return unsupportedAtomicApplyError()
}
