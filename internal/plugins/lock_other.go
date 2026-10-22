//go:build !unix && !windows

package plugins

import (
	"fmt"
	"os"
)

func lockFile(*os.File) error {
	return fmt.Errorf("plugin management locking is unavailable on this platform")
}
func unlockFile(*os.File) {}
