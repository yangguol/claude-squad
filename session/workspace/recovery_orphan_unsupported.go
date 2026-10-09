//go:build !darwin && !linux

package workspace

import (
	"errors"
	"os"
	"runtime"
	"strings"
)

type manifestFileIdentity struct {
	device    uint64
	inode     uint64
	directory bool
}

type manifestComponentLookup struct {
	actualName      string
	aliasDetected   bool
	caseInsensitive bool
}

const orphanQuarantinePrefix = ".orphan-"

func isRetainedOrphanName(name string) bool                 { return strings.HasPrefix(name, orphanQuarantinePrefix) }
func validateRetainedOrphanMarkerAt(*os.File, string) error { return unsupportedAtomicApplyError() }

func platformLookupManifestComponent(*os.File, string) (manifestComponentLookup, error) {
	return manifestComponentLookup{}, errors.New("manifest namespace traversal is unsupported on " + runtime.GOOS)
}

func openManifestComponentAt(*os.File, string) (*os.File, *manifestFileIdentity, error) {
	return nil, nil, errors.New("manifest namespace traversal is unsupported on " + runtime.GOOS)
}

func acknowledgeCompleteTransaction(string, string, string) error {
	return unsupportedAtomicApplyError()
}
func removeCompleteTransaction(string, string) error { return unsupportedAtomicApplyError() }
func removeOrphanAt(*os.File, string, string) error  { return unsupportedAtomicApplyError() }
func platformVerifyCanonicalDirectoryPath(*os.File, string) error {
	return unsupportedAtomicApplyError()
}
