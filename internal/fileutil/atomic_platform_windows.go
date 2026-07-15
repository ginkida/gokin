//go:build windows

package fileutil

import "golang.org/x/sys/windows"

func replaceAtomic(oldPath, newPath string) error {
	oldPtr, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	newPtr, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}

	return windows.MoveFileEx(
		oldPtr,
		newPtr,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}

// Windows does not support opening a directory with os.Open and syncing it in
// the POSIX sense. MoveFileEx with MOVEFILE_WRITE_THROUGH above provides the
// platform's supported write-through behavior for the replacement.
func syncParentDir(string) error {
	return nil
}
