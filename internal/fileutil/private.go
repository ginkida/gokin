package fileutil

import (
	"os"

	"gokin/internal/securefs"
)

// EnsurePrivateDir creates path when necessary, verifies that its final
// component is a real directory rather than a symlink, and makes it
// owner-only. Opening the directory and comparing identities closes the
// ordinary symlink-swap window before chmod is applied.
func EnsurePrivateDir(path string) error {
	return securefs.EnsurePrivateDir(path)
}

// EnsureUserPrivateDir is EnsurePrivateDir for a directory the USER placed: a
// symlinked final component is followed rather than refused, so dotfiles
// layouts that symlink the whole config directory keep working.
func EnsureUserPrivateDir(path string) error {
	return securefs.EnsureUserPrivateDir(path)
}

// SecurePrivateFile repairs an existing regular file to owner-only mode. A
// missing file is valid because callers commonly invoke this immediately
// before an atomic create. Symlinks and special files are rejected without
// chmod-following them.
func SecurePrivateFile(path string) error {
	return securefs.SecurePrivateFile(path)
}

// OpenPrivateRead opens an existing owner-only regular file for reading. It
// rejects symlinks and special files and compares the opened descriptor with
// the pre-open Lstat result, so a path swap cannot redirect the read.
func OpenPrivateRead(path string) (*os.File, error) {
	return securefs.OpenPrivateRead(path)
}

// OpenRegularRead opens a stable regular file without changing its mode.
func OpenRegularRead(path string) (*os.File, error) {
	return securefs.OpenRegularRead(path)
}

// OpenPrivateAppend opens or creates an owner-only regular file for append.
// Existing symlinks and special files are rejected before any write. O_EXCL
// closes the missing-path race: if another object appears between inspection
// and creation, the operation retries through the verified existing-file path.
func OpenPrivateAppend(path string) (*os.File, error) {
	return securefs.OpenPrivateAppend(path)
}

// OpenPrivateReadWrite opens or creates a verified owner-only regular file for
// reading and writing without following a final symlink.
func OpenPrivateReadWrite(path string) (*os.File, error) {
	return securefs.OpenPrivateReadWrite(path)
}

// ReadPrivateFile reads a stable owner-only regular file with an explicit size
// ceiling. The opened inode is checked before and after the read so concurrent
// truncation or replacement is reported instead of being accepted as state.
func ReadPrivateFile(path string, maxBytes int64) ([]byte, error) {
	return securefs.ReadPrivateFile(path, maxBytes)
}

// ReadRegularFile reads a stable regular file with an explicit size ceiling
// while preserving its existing mode.
func ReadRegularFile(path string, maxBytes int64) ([]byte, error) {
	return securefs.ReadRegularFile(path, maxBytes)
}

// ReadResolvedRegularFile reads a user-authored file THROUGH a final symlink,
// keeping the integrity checks against the resolved target and leaving its mode
// alone. Use it only for files the user places and manages.
func ReadResolvedRegularFile(path string, maxBytes int64) ([]byte, error) {
	return securefs.ReadResolvedRegularFile(path, maxBytes)
}
