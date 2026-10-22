//go:build unix

package plugins

import (
	"errors"
	"os"
	"syscall"
)

func lockFile(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errBusy
	}
	return err
}
func unlockFile(file *os.File) { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }
