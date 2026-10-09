//go:build !darwin && !linux

package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnsupportedAtomicApplyDoesNotPublishManifestOrMutateSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	transactionRoot := filepath.Join(root, "transactions")
	require.NoError(t, os.Mkdir(source, 0o700))
	sourceFile := filepath.Join(source, "file")
	require.NoError(t, os.WriteFile(sourceFile, []byte("before"), 0o600))

	err := atomicApplyPreflight(source, transactionRoot)
	require.ErrorContains(t, err, "atomic apply is unsupported")
	contents, readErr := os.ReadFile(sourceFile)
	require.NoError(t, readErr)
	require.Equal(t, "before", string(contents))
	require.NoDirExists(t, transactionRoot)
}
