package security

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Resolve only the cache base, before adding any managed path components.
// Repository-controlled XDG locations never become internal writable grants.
func managedCacheBase(workspace string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	root, err := canonicalSandboxPath(workspace)
	if err != nil {
		return "", err
	}
	logicalRoot, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	logicalBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	repositoryControlled := pathWithin(logicalBase, logicalRoot) || pathWithin(logicalBase, root)
	base, err = resolveCacheBase(base)
	if err != nil {
		return "", err
	}
	if repositoryControlled || pathWithin(base, root) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".cache")
		if runtime.GOOS == "darwin" {
			base = filepath.Join(home, "Library", "Caches")
		}
		base, err = resolveCacheBase(base)
		if err != nil {
			return "", err
		}
		if pathWithin(base, root) {
			return "", fmt.Errorf("%w: managed cache must be outside the workspace; configure a user cache directory outside %s", errUnsafeSandboxScope, root)
		}
	}
	return base, nil
}

func seedToolchains(source, target string) error {
	dest, err := os.OpenRoot(target)
	if err != nil {
		return err
	}
	defer dest.Close()
	file, err := os.Open(filepath.Join(source, "golang.org"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	entries, err := file.ReadDir(1000)
	if err != nil && len(entries) == 0 {
		return nil
	}
	for _, entry := range entries {
		suffix := "." + runtime.GOOS + "-" + runtime.GOARCH
		if !strings.HasPrefix(entry.Name(), "toolchain@v0.0.1-go") || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		path := filepath.Join(source, "golang.org", entry.Name())
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			continue
		}
		version := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "toolchain@v0.0.1-"), suffix)
		executable := filepath.Join(path, "bin", "go")
		if info, err := os.Stat(executable); err != nil || info.Mode()&0111 == 0 {
			continue
		}
		if err := dest.Symlink(executable, version); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}

func resolveCacheBase(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	ancestor := path
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("no existing cache ancestor for %s", path)
		}
		ancestor = parent
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(ancestor, path)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, rel), nil
}
