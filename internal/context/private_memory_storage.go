package context

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gokin/internal/fileutil"
)

const (
	maxSessionMemoryFileBytes int64 = 1 << 20
	maxWorkingMemoryFileBytes int64 = 256 << 10
)

const contextMemoryTruncationMarker = "\n\n_[memory truncated to the safe size limit]_\n"

func contextMemoryDir(workDir string) string {
	return filepath.Join(workDir, ".gokin")
}

func prepareContextMemoryDir(workDir string, create bool) (string, error) {
	dir := contextMemoryDir(workDir)
	if !create {
		info, err := os.Lstat(dir)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("context memory directory %q is not a real directory", dir)
		}
	}
	if err := fileutil.EnsurePrivateDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func readContextMemoryFile(workDir, name string, maxBytes int64) ([]byte, error) {
	dir, err := prepareContextMemoryDir(workDir, false)
	if err != nil {
		return nil, err
	}
	return fileutil.ReadPrivateFile(filepath.Join(dir, name), maxBytes)
}

func writeContextMemoryFile(workDir, name string, data []byte, maxBytes int64) error {
	if maxBytes <= 0 || int64(len(data)) > maxBytes {
		return fmt.Errorf("context memory file exceeds %d-byte limit", maxBytes)
	}
	dir, err := prepareContextMemoryDir(workDir, true)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if err := fileutil.SecurePrivateFile(path); err != nil {
		return err
	}
	return fileutil.AtomicWrite(path, data, 0o600)
}

func removeContextMemoryFile(workDir, name string) error {
	dir, err := prepareContextMemoryDir(workDir, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if err := fileutil.SecurePrivateFile(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func boundContextMemoryContent(content string, maxBytes int64) string {
	if maxBytes <= 0 {
		return ""
	}
	if int64(len(content)) <= maxBytes {
		return content
	}
	limit := int(maxBytes)
	if limit <= len(contextMemoryTruncationMarker) {
		return truncateUTF8Safe(content, limit)
	}
	prefix := truncateUTF8Safe(content, limit-len(contextMemoryTruncationMarker))
	return prefix + contextMemoryTruncationMarker
}
