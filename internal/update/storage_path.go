package update

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gokin/internal/fileutil"
)

func ensurePrivateUpdateDir(rawPath, purpose string) error {
	if strings.TrimSpace(rawPath) == "" {
		return fmt.Errorf("%s directory is empty", purpose)
	}
	absolute, err := filepath.Abs(rawPath)
	if err != nil {
		return fmt.Errorf("resolve %s directory: %w", purpose, err)
	}
	absolute = filepath.Clean(absolute)
	if filepath.Dir(absolute) == absolute {
		return fmt.Errorf("%s directory cannot be a filesystem root", purpose)
	}
	if cwd, err := os.Getwd(); err == nil && filepath.Clean(cwd) == absolute {
		return fmt.Errorf("%s directory cannot be the working directory", purpose)
	}
	if err := fileutil.EnsurePrivateDir(absolute); err != nil {
		return fmt.Errorf("secure %s directory: %w", purpose, err)
	}
	return nil
}
