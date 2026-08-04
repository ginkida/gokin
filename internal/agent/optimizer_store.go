package agent

import (
	"fmt"
	"path/filepath"

	"gokin/internal/fileutil"
)

const maxOptimizerStoreFileBytes int64 = 16 << 20

func ensureOptimizerStoreDir(configDir string) error {
	return fileutil.EnsurePrivateDir(filepath.Join(configDir, "memory"))
}

func readOptimizerStore(path string) ([]byte, error) {
	return fileutil.ReadPrivateFile(path, maxOptimizerStoreFileBytes)
}

func writeOptimizerStore(path string, data []byte) error {
	if int64(len(data)) > maxOptimizerStoreFileBytes {
		return fmt.Errorf("optimizer store exceeds %d-byte limit", maxOptimizerStoreFileBytes)
	}
	if err := fileutil.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := fileutil.SecurePrivateFile(path); err != nil {
		return err
	}
	return fileutil.AtomicWrite(path, data, 0o600)
}
