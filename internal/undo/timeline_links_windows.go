package undo

import (
	"os"

	"golang.org/x/sys/windows"
)

func restoreHasHardLinks(path string, _ os.FileInfo) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return false, err
	}
	return info.NumberOfLinks > 1, nil
}
