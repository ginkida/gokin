package ui

import (
	"fmt"
	"os"
	"path/filepath"

	"gokin/internal/fileutil"
)

// readPrivateHistoryFile reads a history file only after verifying and
// repairing its owning directory. This rejects a symlinked gokin state
// directory before a history read can escape into an attacker-chosen path.
func readPrivateHistoryFile(path string, maxBytes int64) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("history path is empty")
	}
	dir := filepath.Dir(path)
	if _, err := os.Lstat(dir); err != nil {
		return nil, fmt.Errorf("inspect history directory: %w", err)
	}
	if err := fileutil.EnsurePrivateDir(dir); err != nil {
		return nil, fmt.Errorf("prepare history directory: %w", err)
	}
	data, err := fileutil.ReadPrivateFile(path, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	return data, nil
}

// writePrivateHistoryFile atomically replaces a bounded owner-only history
// file. The size check happens before any directory is created or repaired.
func writePrivateHistoryFile(path string, data []byte, maxBytes int64) error {
	if path == "" {
		return fmt.Errorf("history path is empty")
	}
	if maxBytes <= 0 || int64(len(data)) > maxBytes {
		return fmt.Errorf("history exceeds %d-byte limit", maxBytes)
	}
	dir := filepath.Dir(path)
	if err := fileutil.EnsurePrivateDir(dir); err != nil {
		return fmt.Errorf("prepare history directory: %w", err)
	}
	if err := fileutil.SecurePrivateFile(path); err != nil {
		return fmt.Errorf("prepare history file: %w", err)
	}
	if err := fileutil.AtomicWrite(path, data, 0o600); err != nil {
		return fmt.Errorf("write history: %w", err)
	}
	return nil
}
