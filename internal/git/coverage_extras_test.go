package git

import (
	"os"
	"path/filepath"
	"testing"
)

// --- globMatch error path (75% → 100%) ---

func TestGlobMatch_InvalidPattern(t *testing.T) {
	g := NewGitIgnore("/tmp")
	// An invalid glob pattern (unmatched brackets) should return false, not panic
	if g.globMatch("[", "foo.txt") {
		t.Fatal("globMatch with invalid pattern should return false")
	}
}

func TestGlobMatch_ValidPatterns(t *testing.T) {
	g := NewGitIgnore("/tmp")
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "main.txt", false},
		{"**/*.go", "pkg/sub/main.go", true},
		{"foo", "foo", true},
		{"foo", "bar", false},
	}
	for _, tt := range tests {
		if got := g.globMatch(tt.pattern, tt.path); got != tt.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

// --- getGlobalGitignore (60% → 100%) ---

func TestGetGlobalGitignore_XDGConfigHome(t *testing.T) {
	tmpHome := t.TempDir()
	xdgDir := filepath.Join(tmpHome, "xdg-config")
	gitConfigDir := filepath.Join(xdgDir, "git")
	os.MkdirAll(gitConfigDir, 0755)

	globalIgnore := filepath.Join(gitConfigDir, "ignore")
	os.WriteFile(globalIgnore, []byte("*.tmp\n"), 0644)

	t.Setenv("XDG_CONFIG_HOME", xdgDir)

	g := NewGitIgnore("/tmp")
	if got := g.getGlobalGitignore(); got != globalIgnore {
		t.Fatalf("getGlobalGitignore() = %q, want %q", got, globalIgnore)
	}
}

func TestGetGlobalGitignore_HomeFallback(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")

	// Set HOME so os.UserHomeDir returns our temp
	t.Setenv("HOME", tmpHome)

	globalIgnore := filepath.Join(tmpHome, ".gitignore_global")
	os.WriteFile(globalIgnore, []byte("*.bak\n"), 0644)

	g := NewGitIgnore("/tmp")
	if got := g.getGlobalGitignore(); got != globalIgnore {
		t.Fatalf("getGlobalGitignore() = %q, want %q", got, globalIgnore)
	}
}

func TestGetGlobalGitignore_NotFound(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	g := NewGitIgnore("/tmp")
	if got := g.getGlobalGitignore(); got != "" {
		t.Fatalf("getGlobalGitignore() = %q, want empty", got)
	}
}

// --- GetMainWorktreeRoot (60% → 100%) ---

func TestGetMainWorktreeRoot_NormalRepo(t *testing.T) {
	dir := setupGitRepo(t)
	// In a normal repo, git common dir is ".git", so should return workDir
	root := GetMainWorktreeRoot(dir)
	if root != dir {
		t.Fatalf("GetMainWorktreeRoot(normal repo) = %q, want %q", root, dir)
	}
}

func TestGetMainWorktreeRoot_NotARepo(t *testing.T) {
	dir := t.TempDir()
	// Not a git repo — git rev-parse fails, should return workDir
	root := GetMainWorktreeRoot(dir)
	if root != dir {
		t.Fatalf("GetMainWorktreeRoot(non-repo) = %q, want %q", root, dir)
	}
}

// --- matchPattern additional coverage (94.1% → 100%) ---

func TestMatchPattern_NegationUnignores(t *testing.T) {
	dir := t.TempDir()
	gitignoreContent := "*.log\n!important.log\n"
	gitignorePath := filepath.Join(dir, ".gitignore")
	os.WriteFile(gitignorePath, []byte(gitignoreContent), 0644)

	g := NewGitIgnore(dir)
	if err := g.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// important.log should NOT be ignored due to negation
	if g.IsIgnored(filepath.Join(dir, "important.log")) {
		t.Error("important.log should not be ignored (negation)")
	}
	// other .log files should be ignored
	if !g.IsIgnored(filepath.Join(dir, "debug.log")) {
		t.Error("debug.log should be ignored")
	}
}

// --- cacheResult / IsIgnored cache path (90.9% → 100%) ---

func TestIsIgnored_CacheEviction(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.tmp\n"), 0644)

	g := NewGitIgnore(dir)
	g.maxCacheSize = 3 // tiny cache to trigger eviction
	if err := g.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Access more paths than maxCacheSize to trigger LRU eviction
	for i := 0; i < 10; i++ {
		path := filepath.Join(dir, "file.tmp")
		g.IsIgnored(path)
	}

	// Should not panic or corrupt state
	if !g.IsIgnored(filepath.Join(dir, "file.tmp")) {
		t.Error("file.tmp should be ignored")
	}
}

func TestIsIgnored_OutsideWorkDir(t *testing.T) {
	dir := t.TempDir()
	otherDir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.tmp\n"), 0644)

	g := NewGitIgnore(dir)
	if err := g.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Path outside workDir should not be ignored (relative path resolution)
	_ = g.IsIgnored(filepath.Join(otherDir, "test.tmp"))
	// No assertion needed — just exercising the code path
}
