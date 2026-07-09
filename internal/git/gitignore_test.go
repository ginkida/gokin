package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseLine(t *testing.T) {
	g := NewGitIgnore("/tmp")

	tests := []struct {
		line     string
		wantNil  bool
		negation bool
		dirOnly  bool
		anchored bool
		pattern  string
	}{
		{"", true, false, false, false, ""},
		{"# comment", true, false, false, false, ""},
		{"*.log", false, false, false, false, "*.log"},
		{"!important.log", false, true, false, false, "important.log"},
		{"build/", false, false, true, false, "build"},
		{"/root-only", false, false, false, true, "root-only"},
		{"dir/subdir", false, false, false, true, "dir/subdir"},
		{"  ", true, false, false, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			p := g.parseLine(tt.line, "/tmp")
			if tt.wantNil {
				if p != nil {
					t.Errorf("expected nil for %q", tt.line)
				}
				return
			}
			if p == nil {
				t.Fatalf("unexpected nil for %q", tt.line)
			}
			if p.negation != tt.negation {
				t.Errorf("negation = %v, want %v", p.negation, tt.negation)
			}
			if p.dirOnly != tt.dirOnly {
				t.Errorf("dirOnly = %v, want %v", p.dirOnly, tt.dirOnly)
			}
			if p.anchored != tt.anchored {
				t.Errorf("anchored = %v, want %v", p.anchored, tt.anchored)
			}
			if p.pattern != tt.pattern {
				t.Errorf("pattern = %q, want %q", p.pattern, tt.pattern)
			}
		})
	}
}

func TestNewGitIgnore(t *testing.T) {
	g := NewGitIgnore("/tmp/test")
	if g.workDir != "/tmp/test" {
		t.Errorf("workDir = %q", g.workDir)
	}
	if g.IsLoaded() {
		t.Error("should not be loaded before Load()")
	}
}

func TestGitIgnoreLoad(t *testing.T) {
	dir := t.TempDir()

	// Create a .gitignore
	gitignore := filepath.Join(dir, ".gitignore")
	os.WriteFile(gitignore, []byte("*.log\nbuild/\n"), 0644)

	// Create .git dir so it looks like a repo
	os.Mkdir(filepath.Join(dir, ".git"), 0755)

	g := NewGitIgnore(dir)
	if err := g.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !g.IsLoaded() {
		t.Error("should be loaded after Load()")
	}
}

func TestGitIgnoreIsIgnored(t *testing.T) {
	dir := t.TempDir()

	// Create .gitignore
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\nbuild/\n!important.log\n"), 0644)
	os.Mkdir(filepath.Join(dir, ".git"), 0755)

	// Create test files
	os.WriteFile(filepath.Join(dir, "test.log"), []byte("log"), 0644)
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("go"), 0644)
	os.WriteFile(filepath.Join(dir, "important.log"), []byte("keep"), 0644)
	os.MkdirAll(filepath.Join(dir, "build"), 0755)

	g := NewGitIgnore(dir)
	g.Load()

	// *.log should be ignored
	if !g.IsIgnored(filepath.Join(dir, "test.log")) {
		t.Error("test.log should be ignored")
	}

	// .go files should not be ignored
	if g.IsIgnored(filepath.Join(dir, "test.go")) {
		t.Error("test.go should not be ignored")
	}

	// !important.log negation should un-ignore
	if g.IsIgnored(filepath.Join(dir, "important.log")) {
		t.Error("important.log should NOT be ignored (negation)")
	}

	// build/ dir should be ignored
	if !g.IsIgnored(filepath.Join(dir, "build")) {
		t.Error("build/ should be ignored")
	}

	// .git is always ignored
	if !g.IsIgnored(filepath.Join(dir, ".git")) {
		t.Error(".git should always be ignored")
	}
}

func TestGitIgnoreNotLoaded(t *testing.T) {
	g := NewGitIgnore("/nonexistent")
	// Before Load(), nothing is ignored
	if g.IsIgnored("/nonexistent/test.log") {
		t.Error("before Load, nothing should be ignored")
	}
}

func TestGitIgnoreAddPattern(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret"), 0644)

	g := NewGitIgnore(dir)
	g.Load()

	// Not ignored yet
	if g.IsIgnored(filepath.Join(dir, "secret.txt")) {
		t.Error("secret.txt should not be ignored before AddPattern")
	}

	g.InvalidateCache()
	g.AddPattern("secret.txt")

	if !g.IsIgnored(filepath.Join(dir, "secret.txt")) {
		t.Error("secret.txt should be ignored after AddPattern")
	}
}

func TestGitIgnoreInvalidateCache(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644)
	os.WriteFile(filepath.Join(dir, "test.log"), []byte("log"), 0644)

	g := NewGitIgnore(dir)
	g.Load()

	// Populate cache
	g.IsIgnored(filepath.Join(dir, "test.log"))

	g.InvalidateCache()

	// After invalidation, cache should be empty but still functional
	if !g.IsIgnored(filepath.Join(dir, "test.log")) {
		t.Error("after InvalidateCache, IsIgnored should still work")
	}
}

func TestGitIgnoreNoGitignoreFile(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0755)

	g := NewGitIgnore(dir)
	err := g.Load()
	if err != nil {
		t.Fatalf("Load without .gitignore should not error: %v", err)
	}

	// Only .git should be ignored
	if !g.IsIgnored(filepath.Join(dir, ".git")) {
		t.Error(".git should always be ignored")
	}
}

func TestGitIgnoreNestedGitignore(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0755)

	// Root .gitignore
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.tmp\n"), 0644)

	// Nested directory with its own .gitignore
	subDir := filepath.Join(dir, "sub")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, ".gitignore"), []byte("*.dat\n"), 0644)

	// Create files
	os.WriteFile(filepath.Join(dir, "root.tmp"), []byte("tmp"), 0644)
	os.WriteFile(filepath.Join(subDir, "sub.dat"), []byte("dat"), 0644)
	os.WriteFile(filepath.Join(subDir, "sub.go"), []byte("go"), 0644)

	g := NewGitIgnore(dir)
	g.Load()

	if !g.IsIgnored(filepath.Join(dir, "root.tmp")) {
		t.Error("root.tmp should be ignored by root .gitignore")
	}
	if !g.IsIgnored(filepath.Join(subDir, "sub.dat")) {
		t.Error("sub.dat should be ignored by nested .gitignore")
	}
	if g.IsIgnored(filepath.Join(subDir, "sub.go")) {
		t.Error("sub.go should not be ignored")
	}
}

// TestGitIgnore_DirOnlyPatternIgnoresFilesUnderneath pins the round-10 fix:
// dirOnly patterns ("build/", "node_modules/", the always-added ".git") used
// to early-return false for FILES before the containment forms could apply —
// so every file INSIDE an ignored directory came back not-ignored and
// vendored trees leaked into grep/glob/check_impact results.
func TestGitIgnore_DirOnlyPatternIgnoresFilesUnderneath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\nbuild/\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0755)
	os.MkdirAll(filepath.Join(dir, "build"), 0755)
	os.MkdirAll(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "build", "out.bin"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("x"), 0644)

	g := NewGitIgnore(dir)
	if err := g.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, p := range []string{
		filepath.Join(dir, "node_modules", "pkg", "index.js"),
		filepath.Join(dir, "build", "out.bin"),
	} {
		if !g.IsIgnored(p) {
			t.Errorf("file under a dirOnly-ignored directory must be ignored: %s", p)
		}
	}
	// Directories themselves still match.
	if !g.IsIgnored(filepath.Join(dir, "node_modules")) {
		t.Error("the ignored directory itself must match")
	}
	// Ordinary source files stay visible.
	if g.IsIgnored(filepath.Join(dir, "src", "main.go")) {
		t.Error("src/main.go must NOT be ignored")
	}
	// A plain FILE literally named "build" must NOT be ignored by the dirOnly
	// "build/" pattern — git only ignores the DIRECTORY and what's under it.
	// (doublestar's trailing "/**" matches zero segments, so this was the
	// over-match regression the round-10 fix had to guard against.)
	os.WriteFile(filepath.Join(dir, "build_file_not_dir"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "src", "build"), []byte("x"), 0644)
	if err := g.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if g.IsIgnored(filepath.Join(dir, "src", "build")) {
		t.Error("a plain file named 'build' must NOT be ignored by the dirOnly 'build/' pattern")
	}
	// Files under .git (always-added dirOnly pattern) are ignored too.
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("x"), 0644)
	if err := g.Load(); err != nil { // reload so the always-added .git pattern set is fresh
		t.Fatalf("reload: %v", err)
	}
	if !g.IsIgnored(filepath.Join(dir, ".git", "config")) {
		t.Error(".git/config must be ignored via the always-added .git pattern")
	}
}

// TestGitIgnore_NegatedDirOnlyDoesNotReincludeFiles pins the round-10 review
// finding: a negated dirOnly pattern ("!logs/") must NOT re-include a file that
// an earlier pattern ("*.log") ignored — git cannot re-include content under a
// directory, and the containment change must not flip an ignored file visible.
func TestGitIgnore_NegatedDirOnlyDoesNotReincludeFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n!logs/\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "logs"), 0755)
	os.WriteFile(filepath.Join(dir, "logs", "app.log"), []byte("x"), 0644)

	g := NewGitIgnore(dir)
	if err := g.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !g.IsIgnored(filepath.Join(dir, "logs", "app.log")) {
		t.Error("logs/app.log matched *.log and a negated dir pattern must not re-include it")
	}
}
