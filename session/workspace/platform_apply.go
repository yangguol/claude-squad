package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var filesystemDeviceID = platformFilesystemDeviceID

// VerifyAtomicApplyPlatform rejects platforms without required atomic primitives.
func VerifyAtomicApplyPlatform() error {
	return platformVerifyAtomicApply()
}

// VerifySameFilesystem ensures source mutations and transaction storage share a filesystem.
func VerifySameFilesystem(sourcePath, transactionRoot string) error {
	sourceDevice, err := filesystemDeviceID(sourcePath)
	if err != nil {
		return fmt.Errorf("inspect source filesystem: %w", err)
	}
	transactionDevice, err := filesystemDeviceID(transactionRoot)
	if err != nil {
		return fmt.Errorf("inspect transaction filesystem: %w", err)
	}
	if sourceDevice != transactionDevice {
		return errors.New("atomic apply requires source and transaction storage on the same filesystem")
	}
	return nil
}

// ProbeAtomicNoReplace verifies an atomic no-replace rename in transaction storage.
func ProbeAtomicNoReplace(transactionRoot string) error {
	probe, err := createExclusiveSibling(transactionRoot, ".claude-squad-rename-probe-", nil, 0o600)
	if err != nil {
		return fmt.Errorf("prepare atomic no-replace probe: %w", err)
	}
	destination, err := randomSiblingName(probe, ".claude-squad-rename-probe-target-")
	if err != nil {
		return errors.Join(err, removeCreatedTemp(probe))
	}
	if err := applyRenameNoReplace(probe, destination); err != nil {
		return errors.Join(fmt.Errorf("atomic no-replace rename is unavailable: %w", err), removeCreatedTemp(probe), removeCreatedTemp(destination))
	}
	if err := removeCreatedTemp(destination); err != nil {
		return err
	}
	return nil
}

func atomicApplyPreflight(sourcePath, transactionRoot string) error {
	if err := VerifyAtomicApplyPlatform(); err != nil {
		return err
	}
	if err := os.MkdirAll(transactionRoot, 0o700); err != nil {
		return fmt.Errorf("create transaction root: %w", err)
	}
	if err := VerifySameFilesystem(sourcePath, transactionRoot); err != nil {
		return err
	}
	if err := ProbeAtomicNoReplace(filepath.Clean(transactionRoot)); err != nil {
		return err
	}
	return nil
}
