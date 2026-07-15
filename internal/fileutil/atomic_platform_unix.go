//go:build !windows

package fileutil

import "os"

func replaceAtomic(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func syncParentDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}

	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
