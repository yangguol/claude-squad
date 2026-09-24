package workspace

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type recordingManifestFile struct {
	fileHandle
	events *[]string
	kind   string
}

func (f recordingManifestFile) Write(contents []byte) (int, error) {
	*f.events = append(*f.events, "write")
	return f.fileHandle.Write(contents)
}

func (f recordingManifestFile) Sync() error {
	*f.events = append(*f.events, f.kind+" sync")
	return f.fileHandle.Sync()
}

func (f recordingManifestFile) Close() error {
	if f.kind == "file" {
		*f.events = append(*f.events, "close")
	}
	return f.fileHandle.Close()
}

type recordingManifestOps struct {
	fileOps
	events     *[]string
	failRename bool
	partial    bool
}

func (o recordingManifestOps) Open(name string) (fileHandle, error) {
	file, err := o.fileOps.Open(name)
	if err != nil {
		return nil, err
	}
	return recordingManifestFile{fileHandle: file, events: o.events, kind: "directory"}, nil
}

func (o recordingManifestOps) OpenFile(name string, flag int, mode os.FileMode) (fileHandle, error) {
	file, err := o.fileOps.OpenFile(name, flag, mode)
	if err != nil {
		return nil, err
	}
	if o.partial {
		return partialManifestFile{fileHandle: file, events: o.events}, nil
	}
	return recordingManifestFile{fileHandle: file, events: o.events, kind: "file"}, nil
}

func (o recordingManifestOps) Rename(oldpath, newpath string) error {
	*o.events = append(*o.events, "rename")
	if o.failRename {
		return errors.New("injected replacement failure")
	}
	return o.fileOps.Rename(oldpath, newpath)
}

type partialManifestFile struct {
	fileHandle
	events *[]string
}

func (f partialManifestFile) Write(contents []byte) (int, error) {
	*f.events = append(*f.events, "write")
	if len(contents) == 0 {
		return 0, nil
	}
	return f.fileHandle.Write(contents[:len(contents)-1])
}

func TestWriteManifestDurableSyncsFileBeforeRenameAndDirectoryAfterRename(t *testing.T) {
	transactionDir := t.TempDir()
	events := []string{}
	original := applyFS
	applyFS = recordingManifestOps{fileOps: original, events: &events}
	t.Cleanup(func() { applyFS = original })

	manifest := TransactionManifest{Version: CurrentTransactionVersion, State: TransactionPrepared}
	require.NoError(t, WriteManifestDurable(manifest, transactionDir))
	require.Equal(t, []string{"write", "file sync", "close", "rename", "directory sync"}, events)

	contents, err := os.ReadFile(filepath.Join(transactionDir, "manifest.json"))
	require.NoError(t, err)
	var persisted TransactionManifest
	require.NoError(t, json.Unmarshal(contents, &persisted))
	require.Equal(t, CurrentTransactionVersion, persisted.Version)
}

func TestWriteManifestDurableRetainsPreviousManifestWhenReplacementFails(t *testing.T) {
	transactionDir := t.TempDir()
	manifestPath := filepath.Join(transactionDir, "manifest.json")
	previous := TransactionManifest{Version: CurrentTransactionVersion, TransactionID: "previous", State: TransactionPrepared}
	contents, err := json.Marshal(previous)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifestPath, contents, 0o600))

	events := []string{}
	original := applyFS
	applyFS = recordingManifestOps{fileOps: original, events: &events, failRename: true}
	t.Cleanup(func() { applyFS = original })

	err = WriteManifestDurable(TransactionManifest{Version: CurrentTransactionVersion, TransactionID: "replacement", State: TransactionApplying}, transactionDir)
	require.ErrorContains(t, err, "injected replacement failure")

	retained, readErr := os.ReadFile(manifestPath)
	require.NoError(t, readErr)
	var persisted TransactionManifest
	require.NoError(t, json.Unmarshal(retained, &persisted))
	require.Equal(t, previous, persisted)
	matches, globErr := filepath.Glob(filepath.Join(transactionDir, ".manifest-*"))
	require.NoError(t, globErr)
	require.Empty(t, matches)
}

func TestWriteManifestDurableRejectsPartialWrite(t *testing.T) {
	transactionDir := t.TempDir()
	events := []string{}
	original := applyFS
	applyFS = recordingManifestOps{fileOps: original, events: &events, partial: true}
	t.Cleanup(func() { applyFS = original })

	err := WriteManifestDurable(TransactionManifest{Version: CurrentTransactionVersion, State: TransactionPrepared}, transactionDir)
	require.ErrorIs(t, err, io.ErrShortWrite)
	require.NoFileExists(t, filepath.Join(transactionDir, "manifest.json"))
	matches, globErr := filepath.Glob(filepath.Join(transactionDir, ".manifest-*"))
	require.NoError(t, globErr)
	require.Empty(t, matches)
}

func TestTransactionStateJSONUsesApprovedHyphenatedNames(t *testing.T) {
	require.Equal(t, 2, CurrentTransactionVersion)

	states := []TransactionState{
		TransactionPrepared,
		TransactionApplying,
		TransactionRollingBack,
		TransactionRolledBack,
		TransactionRecoveryBlocked,
		TransactionCommitted,
		TransactionCleaning,
		TransactionComplete,
		TransactionCleanupBlocked,
	}
	contents, err := json.Marshal(states)
	require.NoError(t, err)
	require.JSONEq(t, `["prepared","applying","rolling-back","rolled-back","recovery-blocked","committed","cleaning","complete","cleanup-blocked"]`, string(contents))
}
