//go:build linux

package workspace

import "os"

// Linux does not expose an authoritative stored pathname for an open file
// descriptor. Descriptor-relative traversal still verifies identity and rejects
// symlinks; aliases that the filesystem itself does not distinguish remain
// outside the portable contract.
func platformVerifyCanonicalDirectoryPath(_ *os.File, _ string) error { return nil }
