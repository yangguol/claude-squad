//go:build darwin || linux

package workspace

import "golang.org/x/sys/unix"

func platformVerifyAtomicApply() error { return nil }

func platformFilesystemDeviceID(path string) (uint64, error) {
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Dev), nil
}
