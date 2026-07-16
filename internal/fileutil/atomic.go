package fileutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrAtomicWriteCommitUncertain means the atomic replace already made the new
// target visible, but syncing the parent directory failed. Callers must not
// assume the old contents remain or safely repeat an externally-visible action.
var ErrAtomicWriteCommitUncertain = errors.New("atomic write commit is visible but durability is uncertain")

type atomicWriteCommitUncertainError struct {
	err error
}

func (e *atomicWriteCommitUncertainError) Error() string { return e.err.Error() }
func (e *atomicWriteCommitUncertainError) Unwrap() error { return e.err }
func (e *atomicWriteCommitUncertainError) Is(target error) bool {
	return target == ErrAtomicWriteCommitUncertain
}

// AtomicWrite writes data to a file atomically using a tmp file + rename pattern.
// This prevents a partially-written target if the process is interrupted. The
// file is written to a temporary file in the same directory, its contents and
// mode are synced, then it atomically replaces the target. On POSIX systems the
// parent directory is synced after the rename so the new directory entry is
// durable across a power loss as well.
func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	return atomicWrite(path, data, perm, systemAtomicWriteOps())
}

type atomicWriteOps struct {
	chmod      func(*os.File, os.FileMode) error
	syncFile   func(*os.File) error
	replace    func(string, string) error
	syncParent func(string) error
}

func systemAtomicWriteOps() atomicWriteOps {
	return atomicWriteOps{
		chmod: func(file *os.File, mode os.FileMode) error {
			return file.Chmod(mode)
		},
		syncFile: func(file *os.File) error {
			return file.Sync()
		},
		replace:    replaceAtomic,
		syncParent: syncParentDir,
	}
}

func atomicWrite(path string, data []byte, perm os.FileMode, ops atomicWriteOps) error {
	dir := filepath.Dir(path)

	// Create temporary file in the same directory (required for atomic rename)
	tmp, err := os.CreateTemp(dir, ".gokin-*.tmp")
	if err != nil {
		return fmt.Errorf("create atomic-write temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// Until the replace succeeds, every failure must remove the private temp
	// file. Close is intentionally also attempted on write/sync failures.
	closed := false
	replaced := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		if !replaced {
			_ = os.Remove(tmpPath)
		}
	}()

	// Write data to temporary file
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write atomic-write temp file: %w", err)
	}

	// Apply permissions before Sync so both the contents and mode have reached
	// stable storage before the file becomes visible at the target path. A
	// CreateTemp file starts at 0600, so secret callers never expose broader
	// permissions while their data is being written.
	if err := ops.chmod(tmp, perm); err != nil {
		return fmt.Errorf("set atomic-write temp permissions: %w", err)
	}

	// Sync to disk to ensure data is persisted before rename
	if err := ops.syncFile(tmp); err != nil {
		return fmt.Errorf("sync atomic-write temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		closed = true
		return fmt.Errorf("close atomic-write temp file: %w", err)
	}
	closed = true

	// Atomic rename - this is the key operation that makes the write atomic
	// (or an atomic replace on Windows). The temp file is in the same directory,
	// so this cannot cross filesystem boundaries.
	if err := ops.replace(tmpPath, path); err != nil {
		return fmt.Errorf("replace atomic-write target: %w", err)
	}
	replaced = true

	// Syncing the file alone does not make the rename durable on POSIX. Once the
	// replace succeeds there is no temp path left to clean up; if directory sync
	// fails, return the error so the caller knows durability is uncertain.
	if err := ops.syncParent(dir); err != nil {
		return &atomicWriteCommitUncertainError{
			err: fmt.Errorf("sync atomic-write parent directory: %w", err),
		}
	}
	return nil
}

// AtomicWriteString is a convenience wrapper for AtomicWrite that accepts a string.
func AtomicWriteString(path string, content string, perm os.FileMode) error {
	return AtomicWrite(path, []byte(content), perm)
}
