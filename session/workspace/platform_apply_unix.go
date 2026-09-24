//go:build darwin || linux

package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var platformAtomicRenameNoReplace = platformRenameatNoReplace

func platformVerifyAtomicApply() error { return nil }

func platformFilesystemDeviceID(path string) (uint64, error) {
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Dev), nil
}

func platformAtomicApplyPreflight(sourcePath, transactionRoot string) error {
	projectPath := filepath.Dir(filepath.Clean(transactionRoot))
	projectDirectory, err := platformOpenTrustedDirectory(projectPath)
	if err != nil {
		return fmt.Errorf("open project directory: %w", err)
	}
	defer projectDirectory.Close()
	transactionDirectory, err := platformOpenOrCreateDirectoryAt(projectDirectory, filepath.Base(transactionRoot), 0o700)
	if err != nil {
		return fmt.Errorf("open transaction root: %w", err)
	}
	defer transactionDirectory.Close()
	if err := platformVerifyDirectoryEntry(projectDirectory, filepath.Base(transactionRoot), transactionDirectory); err != nil {
		return fmt.Errorf("transaction root changed: %w", err)
	}
	if atomicApplyAfterTransactionRootVerificationHook != nil {
		atomicApplyAfterTransactionRootVerificationHook()
	}
	sourceDevice, err := filesystemDeviceID(sourcePath)
	if err != nil {
		return fmt.Errorf("inspect source filesystem: %w", err)
	}
	var transactionStat unix.Stat_t
	if err := unix.Fstat(int(transactionDirectory.Fd()), &transactionStat); err != nil {
		return fmt.Errorf("inspect transaction filesystem: %w", err)
	}
	if err := requireSameDevice(sourceDevice, uint64(transactionStat.Dev)); err != nil {
		return err
	}
	return platformProbeAtomicNoReplace(transactionDirectory)
}

func platformProbeAtomicNoReplace(directory *os.File) error {
	first, err := createProbeAt(directory, ".claude-squad-rename-probe-", nil)
	if err != nil {
		return fmt.Errorf("prepare atomic no-replace probe: %w", err)
	}
	firstTarget, err := unusedProbeNameAt(directory, ".claude-squad-rename-probe-target-")
	if err != nil {
		return errors.Join(err, unlinkProbeAt(directory, first))
	}
	if err := platformAtomicRenameNoReplace(int(directory.Fd()), first, firstTarget); err != nil {
		return errors.Join(fmt.Errorf("atomic no-replace rename is unavailable: %w", err), unlinkProbeAt(directory, first))
	}
	if err := unlinkProbeAt(directory, firstTarget); err != nil {
		return err
	}

	probe, err := createProbeAt(directory, ".claude-squad-rename-probe-", nil)
	if err != nil {
		return fmt.Errorf("prepare atomic no-replace collision probe: %w", err)
	}
	const destinationContents = "probe-owned destination"
	destination, err := createProbeAt(directory, ".claude-squad-rename-probe-target-", []byte(destinationContents))
	if err != nil {
		return errors.Join(fmt.Errorf("prepare atomic no-replace collision destination: %w", err), unlinkProbeAt(directory, probe))
	}
	renameErr := platformAtomicRenameNoReplace(int(directory.Fd()), probe, destination)
	contents, readErr := readProbeAt(directory, destination)
	cleanupErr := errors.Join(unlinkProbeAt(directory, probe), unlinkProbeAt(directory, destination))
	if renameErr == nil {
		return errors.Join(errors.New("atomic no-replace rename replaced an existing destination"), cleanupErr)
	}
	if readErr != nil || string(contents) != destinationContents {
		return errors.Join(errors.New("atomic no-replace rename did not preserve an existing destination"), readErr, cleanupErr)
	}
	return cleanupErr
}

func randomProbeName(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

func createProbeAt(directory *os.File, prefix string, contents []byte) (string, error) {
	for attempts := 0; attempts < 100; attempts++ {
		name, err := randomProbeName(prefix)
		if err != nil {
			return "", err
		}
		fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", err
		}
		file := os.NewFile(uintptr(fd), name)
		_, writeErr := file.Write(contents)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return "", errors.Join(writeErr, closeErr, unlinkProbeAt(directory, name))
		}
		return name, nil
	}
	return "", errors.New("could not allocate unique probe name")
}

func unusedProbeNameAt(directory *os.File, prefix string) (string, error) {
	for attempts := 0; attempts < 100; attempts++ {
		name, err := randomProbeName(prefix)
		if err != nil {
			return "", err
		}
		var stat unix.Stat_t
		err = unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate unique probe target")
}

func readProbeAt(directory *os.File, name string) ([]byte, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	contents, readErr := io.ReadAll(file)
	return contents, errors.Join(readErr, file.Close())
}

func unlinkProbeAt(directory *os.File, name string) error {
	err := unix.Unlinkat(int(directory.Fd()), name, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}
