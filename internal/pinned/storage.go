// Package pinned owns persistence for the high-attention context injected into
// agent prompts. Pinned content can contain source snippets and credentials, so
// it is stored with the same privacy guarantees as chat history.
package pinned

import (
	"fmt"
	"os"
	"path/filepath"

	"gokin/internal/fileutil"
)

const (
	// MaxContentBytes keeps one pin from consuming an unbounded fraction of the
	// model context window when it is restored and injected into every prompt.
	MaxContentBytes = 64 << 10
	fileName        = "pinned_context.md"
)

// Load returns the persisted pinned context. Legacy permissions are repaired
// to owner-only. Missing storage is reported as os.ErrNotExist so callers can
// distinguish a normal first run from malformed or unsafe state.
func Load(workDir string) (string, error) {
	dir, err := prepareDir(workDir, false)
	if err != nil {
		return "", err
	}
	data, err := fileutil.ReadPrivateFile(filepath.Join(dir, fileName), MaxContentBytes)
	if err != nil {
		return "", fmt.Errorf("load pinned context: %w", err)
	}
	return string(data), nil
}

// Save atomically persists content in an owner-only file. Empty content is a
// durable clear marker; retaining the empty file avoids an unsafe unlink race
// while remaining indistinguishable from a missing pin to Load callers.
func Save(workDir, content string) error {
	if len(content) > MaxContentBytes {
		return fmt.Errorf("pinned context exceeds %d-byte limit", MaxContentBytes)
	}
	dir, err := prepareDir(workDir, true)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, fileName)
	if err := fileutil.SecurePrivateFile(path); err != nil {
		return fmt.Errorf("prepare pinned context file: %w", err)
	}
	if err := fileutil.AtomicWriteString(path, content, 0o600); err != nil {
		return fmt.Errorf("save pinned context: %w", err)
	}
	return nil
}

func prepareDir(workDir string, create bool) (string, error) {
	if workDir == "" {
		return "", fmt.Errorf("pinned context work directory is empty")
	}
	dir := filepath.Join(workDir, ".gokin")
	if !create {
		if _, err := os.Lstat(dir); err != nil {
			return "", fmt.Errorf("inspect pinned context directory: %w", err)
		}
	}
	if err := fileutil.EnsurePrivateDir(dir); err != nil {
		return "", fmt.Errorf("prepare pinned context directory: %w", err)
	}
	return dir, nil
}
