//go:build unix

package security

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Directory descriptors anchor every operation. O_NOFOLLOW refuses symlinks
// even if another process swaps a component after it has been inspected.
func privateCacheDirectory(base string, parts ...string) (string, error) {
	path := filepath.Join(append([]string{base}, parts...)...)
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(fd) }()
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	baseCount := len(strings.Split(strings.TrimPrefix(filepath.Clean(base), "/"), "/"))
	for i, name := range components {
		if name == "" || name == "." || name == ".." {
			return "", fmt.Errorf("invalid managed cache component %q", name)
		}
		if err := unix.Mkdirat(fd, name, 0700); err != nil && err != unix.EEXIST {
			return "", fmt.Errorf("create managed cache %s: %w", path, err)
		}
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return "", fmt.Errorf("%w: managed cache refuses symlink or non-directory at %s: %v", errUnsafeSandboxScope, strings.Join(components[:i+1], "/"), err)
		}
		_ = unix.Close(fd)
		fd = next
		if i >= baseCount {
			if err := unix.Fchmod(fd, 0700); err != nil {
				return "", err
			}
		}
	}
	return path, nil
}
