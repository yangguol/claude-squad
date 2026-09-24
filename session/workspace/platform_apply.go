package workspace

import (
	"errors"
	"fmt"
)

var filesystemDeviceID = platformFilesystemDeviceID
var atomicApplyAfterTransactionRootVerificationHook func()

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

// ProbeAtomicNoReplace verifies successful and colliding atomic no-replace renames.
func ProbeAtomicNoReplace(transactionRoot string) error {
	if err := VerifyAtomicApplyPlatform(); err != nil {
		return err
	}
	directory, err := platformOpenTrustedDirectory(transactionRoot)
	if err != nil {
		return fmt.Errorf("open transaction root: %w", err)
	}
	defer directory.Close()
	return platformProbeAtomicNoReplace(directory)
}

func atomicApplyPreflight(sourcePath, transactionRoot string) error {
	if err := VerifyAtomicApplyPlatform(); err != nil {
		return err
	}
	return platformAtomicApplyPreflight(sourcePath, transactionRoot)
}

func requireSameDevice(sourceDevice, transactionDevice uint64) error {
	if sourceDevice != transactionDevice {
		return errors.New("atomic apply requires source and transaction storage on the same filesystem")
	}
	return nil
}
