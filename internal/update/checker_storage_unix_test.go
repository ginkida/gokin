//go:build !windows

package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckerCacheStorageIsPrivate(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "update")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("create permissive cache dir: %v", err)
	}
	checker := NewChecker(DefaultConfig(), dir)
	if err := checker.SaveCache(&UpdateCache{LatestVersion: "v1.2.3"}); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat cache dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("cache dir mode = %o, want 700", got)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "update_cache.json"))
	if err != nil {
		t.Fatalf("stat cache file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("cache file mode = %o, want 600", got)
	}
}

func TestCheckerCacheRejectsSymlinkPaths(t *testing.T) {
	root := t.TempDir()
	externalDir := t.TempDir()
	symlinkedDir := filepath.Join(root, "update")
	if err := os.Symlink(externalDir, symlinkedDir); err != nil {
		t.Fatalf("symlink cache dir: %v", err)
	}
	checker := NewChecker(DefaultConfig(), symlinkedDir)
	if err := checker.SaveCache(&UpdateCache{}); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlinked directory error = %v", err)
	}

	realDir := filepath.Join(root, "real-update")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatalf("create real cache dir: %v", err)
	}
	externalFile := filepath.Join(externalDir, "external.json")
	if err := os.WriteFile(externalFile, []byte("unchanged"), 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	cachePath := filepath.Join(realDir, "update_cache.json")
	if err := os.Symlink(externalFile, cachePath); err != nil {
		t.Fatalf("symlink cache file: %v", err)
	}
	checker = NewChecker(DefaultConfig(), realDir)
	if err := checker.SaveCache(&UpdateCache{}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlinked file save error = %v", err)
	}
	if _, err := checker.LoadCache(); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlinked file load error = %v", err)
	}
	data, err := os.ReadFile(externalFile)
	if err != nil {
		t.Fatalf("read external file: %v", err)
	}
	if string(data) != "unchanged" {
		t.Fatalf("external file was modified: %q", data)
	}
}
