package memory

import (
	"fmt"
	"path/filepath"

	"gokin/internal/fileutil"
)

const maxAuxiliaryStoreFileBytes int64 = 16 << 20

func ensureAuxiliaryStoreDir(configDir string) error {
	return fileutil.EnsurePrivateDir(filepath.Join(configDir, "memory"))
}

func readAuxiliaryStore(path string) ([]byte, error) {
	return fileutil.ReadPrivateFile(path, maxAuxiliaryStoreFileBytes)
}

func writeAuxiliaryStore(path string, data []byte) error {
	if int64(len(data)) > maxAuxiliaryStoreFileBytes {
		return fmt.Errorf("auxiliary memory store exceeds %d-byte limit", maxAuxiliaryStoreFileBytes)
	}
	if err := fileutil.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := fileutil.SecurePrivateFile(path); err != nil {
		return err
	}
	return fileutil.AtomicWrite(path, data, 0o600)
}
