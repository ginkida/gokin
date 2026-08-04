// Package securefs provides race-aware primitives for owner-only durable
// state. It intentionally has no dependencies on other internal packages so
// security-sensitive storage and logging can both use it without import cycles.
package securefs

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// EnsurePrivateDir creates path when necessary, verifies that its final
// component is a real directory rather than a symlink, and makes it
// owner-only. Opening the directory and comparing identities closes the
// ordinary symlink-swap window before chmod is applied.
func EnsurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}

	before, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect private directory: %w", err)
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private storage path %q is not a real directory", path)
	}

	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open private directory: %w", err)
	}
	defer dir.Close()

	opened, err := dir.Stat()
	if err != nil {
		return fmt.Errorf("stat private directory: %w", err)
	}
	if !opened.IsDir() || !os.SameFile(before, opened) {
		return fmt.Errorf("private storage directory changed while opening")
	}
	if err := dir.Chmod(0o700); err != nil {
		return fmt.Errorf("set private directory permissions: %w", err)
	}
	return nil
}

// EnsureUserPrivateDir is EnsurePrivateDir for a directory the USER placed:
// a symlinked final component is followed instead of refused.
//
// Gokin's own subdirectories have no reason to be links, and EnsurePrivateDir
// rightly refuses one there. The user's top-level configuration directory is
// different: GNU stow and home-manager symlink ~/.config/gokin wholesale, and
// refusing that meant Gokin could not read the user's config or API keys at all
// — it failed to start. Following the link is also what every other component
// of the path already does.
//
// The swap window stays closed the same way: the directory that is OPENED is
// compared with the one that was inspected. The resolved directory is still made
// owner-only, because it holds credentials and directory modes are not tracked
// by git.
func EnsureUserPrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect private directory: %w", err)
	}
	if !before.IsDir() {
		return fmt.Errorf("private storage path %q is not a directory", path)
	}
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open private directory: %w", err)
	}
	defer dir.Close()

	opened, err := dir.Stat()
	if err != nil {
		return fmt.Errorf("stat private directory: %w", err)
	}
	if !opened.IsDir() || !os.SameFile(before, opened) {
		return fmt.Errorf("private storage directory changed while opening")
	}
	if err := dir.Chmod(0o700); err != nil {
		return fmt.Errorf("set private directory permissions: %w", err)
	}
	return nil
}

// SecurePrivateFile repairs an existing regular file to owner-only mode. A
// missing file is valid because callers commonly invoke this immediately
// before an atomic create. Symlinks and special files are rejected without
// chmod-following them.
func SecurePrivateFile(path string) error {
	file, err := openExistingPrivateFile(path, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return file.Close()
}

// OpenPrivateRead opens an existing owner-only regular file for reading. It
// rejects symlinks and special files and compares the opened descriptor with
// the pre-open Lstat result, so a path swap cannot redirect the read.
func OpenPrivateRead(path string) (*os.File, error) {
	return openExistingPrivateFile(path, os.O_RDONLY)
}

// OpenRegularRead opens a stable regular file without changing its mode. It is
// intended for explicitly-selected or repository-owned files whose parent and
// permission policy belongs to the user, while still rejecting final symlinks,
// special files, and path swaps.
func OpenRegularRead(path string) (*os.File, error) {
	return openExistingRegularFile(path, os.O_RDONLY, false)
}

// OpenPrivateAppend opens or creates an owner-only regular file for append.
// Existing symlinks and special files are rejected before any write. O_EXCL
// closes the missing-path race: if another object appears between inspection
// and creation, the operation retries through the verified existing-file path.
func OpenPrivateAppend(path string) (*os.File, error) {
	return openOrCreatePrivateFile(path, os.O_WRONLY|os.O_APPEND)
}

// OpenPrivateReadWrite opens or creates an owner-only regular file for both
// reading and writing. It is intended for retained advisory-lock files, where
// following a symlink could make two processes lock different inodes while
// both believe they own the same durable resource.
func OpenPrivateReadWrite(path string) (*os.File, error) {
	return openOrCreatePrivateFile(path, os.O_RDWR)
}

// OpenPrivateReadWriteExisting opens an existing owner-only regular file for
// both reading and writing without creating it. Lease probes use this variant
// so checking an unused identity never leaves an orphan lock file behind.
func OpenPrivateReadWriteExisting(path string) (*os.File, error) {
	return openExistingPrivateFile(path, os.O_RDWR)
}

func openOrCreatePrivateFile(path string, flags int) (*os.File, error) {
	for attempt := 0; attempt < 3; attempt++ {
		file, err := openExistingPrivateFile(path, flags)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}

		file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|flags, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("create private file: %w", err)
		}
		if err := verifyPrivateFilePath(path, file, nil); err != nil {
			_ = file.Close()
			return nil, err
		}
		return file, nil
	}
	return nil, fmt.Errorf("private file path %q changed repeatedly during creation", path)
}

// ReadPrivateFile reads a stable owner-only regular file with an explicit size
// ceiling. The opened inode is checked before and after the read so concurrent
// truncation or replacement is reported instead of being accepted as state.
func ReadPrivateFile(path string, maxBytes int64) ([]byte, error) {
	return readRegularFile(path, maxBytes, true)
}

// ReadRegularFile reads a stable regular file with an explicit size ceiling
// without modifying its permissions.
func ReadRegularFile(path string, maxBytes int64) ([]byte, error) {
	return readRegularFile(path, maxBytes, false)
}

// ReadResolvedRegularFile reads a stable regular file THROUGH a final symlink.
//
// The other readers here reject a symlinked final component because Gokin's own
// durable state has no reason to be one, and refusing it closes a redirect
// window. A file the USER authored and placed is different: symlinking a config
// into a dotfiles repository is a first-class, widespread pattern, and refusing
// to follow it silently reverts the user to defaults.
//
// The integrity checks still apply, just against the RESOLVED object: it must be
// a regular file, the opened descriptor must be that same inode, and it must not
// change while being read. The mode is left alone — the target belongs to the
// user, and repairing it would rewrite a file tracked in their repository.
func ReadResolvedRegularFile(path string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("regular file read limit must be positive")
	}
	before, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect file: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("path %q does not resolve to a regular file", path)
	}
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("file changed while opening")
	}
	return readOpenedRegularFile(path, file, opened, maxBytes)
}

func readRegularFile(path string, maxBytes int64, private bool) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("regular file read limit must be positive")
	}
	var file *os.File
	var err error
	if private {
		file, err = OpenPrivateRead(path)
	} else {
		file, err = OpenRegularRead(path)
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	before, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat private file before read: %w", err)
	}
	return readOpenedRegularFile(path, file, before, maxBytes)
}

// readOpenedRegularFile is the shared bounded-read body. The inode is compared
// before and after so concurrent truncation or replacement is reported instead
// of being accepted as state.
func readOpenedRegularFile(path string, file *os.File, before os.FileInfo, maxBytes int64) ([]byte, error) {
	if before.Size() < 0 || before.Size() > maxBytes {
		return nil, fmt.Errorf("file %q exceeds %d-byte limit", path, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file %q exceeds %d-byte limit", path, maxBytes)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat file after read: %w", err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) ||
		int64(len(data)) != after.Size() {
		return nil, fmt.Errorf("storage file changed while reading")
	}
	return data, nil
}

func openExistingPrivateFile(path string, flags int) (*os.File, error) {
	return openExistingRegularFile(path, flags, true)
}

func openExistingRegularFile(path string, flags int, private bool) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect private file: %w", err)
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("private storage path %q is not a regular file", path)
	}

	file, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return nil, fmt.Errorf("open private file: %w", err)
	}
	if err := verifyRegularFilePath(path, file, before, private); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func verifyPrivateFilePath(path string, file *os.File, before os.FileInfo) error {
	return verifyRegularFilePath(path, file, before, true)
}

func verifyRegularFilePath(path string, file *os.File, before os.FileInfo, private bool) error {
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat private file: %w", err)
	}
	if !opened.Mode().IsRegular() || (before != nil && !os.SameFile(before, opened)) {
		return fmt.Errorf("private storage file changed while opening")
	}
	after, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect private file after opening: %w", err)
	}
	if !after.Mode().IsRegular() || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, after) {
		return fmt.Errorf("private storage file changed while opening")
	}
	if private {
		if err := file.Chmod(0o600); err != nil {
			return fmt.Errorf("set private file permissions: %w", err)
		}
	}
	return nil
}
