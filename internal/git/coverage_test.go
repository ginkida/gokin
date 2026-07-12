package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- IsGitRepo ---

func TestIsGitRepo_True(t *testing.T) {
	dir := setupGitRepo(t)
	if !IsGitRepo(dir) {
		t.Error("IsGitRepo should return true for a git repo")
	}
}

func TestIsGitRepo_False(t *testing.T) {
	dir := t.TempDir() // not a git repo
	if IsGitRepo(dir) {
		t.Error("IsGitRepo should return false for non-git dir")
	}
}

// --- GetCurrentBranch ---

func TestGetCurrentBranch_InRepo(t *testing.T) {
	dir := setupGitRepo(t)
	branch := GetCurrentBranch(dir)
	// git init creates "main" or "master" depending on version
	if branch == "" {
		t.Error("GetCurrentBranch should return non-empty branch name")
	}
}

func TestGetCurrentBranch_NotInRepo(t *testing.T) {
	dir := t.TempDir()
	branch := GetCurrentBranch(dir)
	if branch != "" {
		t.Errorf("GetCurrentBranch in non-git dir should return empty, got %q", branch)
	}
}

// --- GitContextProvider: GetContext ---

func TestGitContextProvider_GetContext(t *testing.T) {
	dir := setupGitRepo(t)
	p := NewGitContextProvider(dir)
	ctx := context.Background()

	gc, err := p.GetContext(ctx)
	if err != nil {
		t.Fatalf("GetContext: %v", err)
	}

	if gc.Branch == "" {
		t.Error("Branch should not be empty")
	}
	if len(gc.RecentCommits) == 0 {
		t.Error("should have at least 1 recent commit")
	}
	if gc.RecentCommits[0].Subject != "init" {
		t.Errorf("first commit subject = %q, want 'init'", gc.RecentCommits[0].Subject)
	}
	if gc.RecentCommits[0].Hash == "" {
		t.Error("commit hash should not be empty")
	}
}

func TestGitContextProvider_GetContext_NotARepo(t *testing.T) {
	dir := t.TempDir()
	p := NewGitContextProvider(dir)
	_, err := p.GetContext(context.Background())
	if err == nil {
		t.Error("expected error for non-git dir")
	}
}

func TestGitContextProvider_GetContext_CacheHit(t *testing.T) {
	dir := setupGitRepo(t)
	p := NewGitContextProvider(dir)

	gc1, err := p.GetContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Second call should return cached result
	gc2, err := p.GetContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Same pointer (cached)
	if gc1 != gc2 {
		t.Error("expected same cached pointer on second GetContext call")
	}
}

func TestGitContextProvider_GetContext_CacheExpiry(t *testing.T) {
	dir := setupGitRepo(t)
	p := NewGitContextProvider(dir)
	p.cacheTTL = 1 * time.Millisecond // very short TTL

	gc1, _ := p.GetContext(context.Background())
	time.Sleep(5 * time.Millisecond)

	gc2, _ := p.GetContext(context.Background())

	// Cache should have expired — different pointer (fresh fetch)
	if gc1 == gc2 {
		t.Error("expected different pointer after cache expiry")
	}
}

func TestGitContextProvider_InvalidateCache(t *testing.T) {
	dir := setupGitRepo(t)
	p := NewGitContextProvider(dir)

	gc1, _ := p.GetContext(context.Background())
	p.InvalidateCache()
	gc2, _ := p.GetContext(context.Background())

	if gc1 == gc2 {
		t.Error("expected different pointer after InvalidateCache")
	}
}

func TestGitContextProvider_SetMaxCommits(t *testing.T) {
	dir := setupGitRepo(t)
	p := NewGitContextProvider(dir)
	p.SetMaxCommits(1)

	gc, err := p.GetContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(gc.RecentCommits) > 1 {
		t.Errorf("expected at most 1 commit, got %d", len(gc.RecentCommits))
	}
}

// --- GitContextProvider: status parsing ---

func TestGitContextProvider_StatusStagedModified(t *testing.T) {
	dir := setupGitRepo(t)

	// Modify existing file (staged)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# modified"), 0644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", dir, "add", "README.md").Run()

	// Create untracked file
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}

	// Modify staged file again (now also modified in work tree)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# modified again"), 0644); err != nil {
		t.Fatal(err)
	}

	p := NewGitContextProvider(dir)
	gc, err := p.GetContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(gc.StagedFiles) == 0 {
		t.Error("expected at least 1 staged file")
	}
	if len(gc.ModifiedFiles) == 0 {
		t.Error("expected at least 1 modified file")
	}
	if len(gc.UntrackedFiles) == 0 {
		t.Error("expected at least 1 untracked file")
	}
}

func TestGitContextProvider_Stashes(t *testing.T) {
	dir := setupGitRepo(t)

	// Create a stash
	if err := os.WriteFile(filepath.Join(dir, "stash.txt"), []byte("stash me"), 0644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", dir, "add", "stash.txt").Run()
	exec.Command("git", "-C", dir, "stash").Run()

	p := NewGitContextProvider(dir)
	gc, err := p.GetContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if gc.Stashes < 1 {
		t.Errorf("expected ≥1 stash, got %d", gc.Stashes)
	}
}

// --- GitContext: FormatForPrompt ---

func TestGitContext_FormatForPrompt_NilSafe(t *testing.T) {
	var gc *GitContext
	if s := gc.FormatForPrompt(); s != "" {
		t.Errorf("nil FormatForPrompt should return empty, got %q", s)
	}
}

func TestGitContext_FormatForPrompt_BranchOnly(t *testing.T) {
	gc := &GitContext{Branch: "main"}
	s := gc.FormatForPrompt()
	if !strings.Contains(s, "main") {
		t.Errorf("expected branch name in output: %s", s)
	}
}

func TestGitContext_FormatForPrompt_WithRemote(t *testing.T) {
	gc := &GitContext{
		Branch:       "feature",
		RemoteBranch: "origin/feature",
		AheadBehind:  AheadBehind{Ahead: 2, Behind: 1},
	}
	s := gc.FormatForPrompt()
	if !strings.Contains(s, "feature") {
		t.Error("missing branch")
	}
	if !strings.Contains(s, "origin/feature") {
		t.Error("missing remote")
	}
	if !strings.Contains(s, "2 ahead") {
		t.Error("missing ahead count")
	}
	if !strings.Contains(s, "1 behind") {
		t.Error("missing behind count")
	}
}

func TestGitContext_FormatForPrompt_WithChanges(t *testing.T) {
	gc := &GitContext{
		Branch:         "main",
		StagedFiles:    []string{"a.go", "b.go"},
		ModifiedFiles:  []string{"c.go"},
		UntrackedFiles: []string{"d.go"},
		Stashes:        2,
	}
	s := gc.FormatForPrompt()
	if !strings.Contains(s, "Staged: 2") {
		t.Error("missing staged count")
	}
	if !strings.Contains(s, "Modified: 1") {
		t.Error("missing modified count")
	}
	if !strings.Contains(s, "Untracked: 1") {
		t.Error("missing untracked count")
	}
	// FormatForPrompt renders stash count as "**Stashes:** N"
	if !strings.Contains(s, "Stashes") || !strings.Contains(s, "2") {
		t.Error("missing stash info")
	}
}

func TestGitContext_FormatForPrompt_WithCommits(t *testing.T) {
	gc := &GitContext{
		Branch: "main",
		RecentCommits: []CommitInfo{
			{Hash: "abcd1234", Author: "Alice", Subject: "fix bug", Date: time.Now().Add(-30 * time.Second)},
		},
	}
	s := gc.FormatForPrompt()
	if !strings.Contains(s, "abcd1234") {
		t.Error("missing commit hash")
	}
	if !strings.Contains(s, "fix bug") {
		t.Error("missing commit subject")
	}
	if !strings.Contains(s, "Alice") {
		t.Error("missing author")
	}
	// 30s ago → "just now" (< 1 minute)
	if !strings.Contains(s, "just now") {
		t.Error("missing relative time 'just now'")
	}
}

// --- GitContext: FormatCompact ---

func TestGitContext_FormatCompact_NilSafe(t *testing.T) {
	var gc *GitContext
	if s := gc.FormatCompact(); s != "" {
		t.Errorf("nil FormatCompact should return empty, got %q", s)
	}
}

func TestGitContext_FormatCompact_BranchOnly(t *testing.T) {
	gc := &GitContext{Branch: "main"}
	s := gc.FormatCompact()
	if s != "main" {
		t.Errorf("FormatCompact branch-only = %q, want 'main'", s)
	}
}

func TestGitContext_FormatCompact_WithChanges(t *testing.T) {
	gc := &GitContext{
		Branch:        "main",
		StagedFiles:   []string{"a.go"},
		ModifiedFiles: []string{"b.go", "c.go"},
	}
	s := gc.FormatCompact()
	if !strings.Contains(s, "3 changes") {
		t.Errorf("expected '3 changes' in compact: %s", s)
	}
}

func TestGitContext_FormatCompact_AheadBehind(t *testing.T) {
	gc := &GitContext{
		Branch:      "main",
		AheadBehind: AheadBehind{Ahead: 2, Behind: 3},
	}
	s := gc.FormatCompact()
	if !strings.Contains(s, "↑2") {
		t.Errorf("expected ↑2 in compact: %s", s)
	}
	if !strings.Contains(s, "↓3") {
		t.Errorf("expected ↓3 in compact: %s", s)
	}
}

// --- GitContext: IsClean ---

func TestGitContext_IsClean_True(t *testing.T) {
	gc := &GitContext{Branch: "main"}
	if !gc.IsClean() {
		t.Error("expected IsClean=true for empty working tree")
	}
}

func TestGitContext_IsClean_False(t *testing.T) {
	gc := &GitContext{Branch: "main", StagedFiles: []string{"a.go"}}
	if gc.IsClean() {
		t.Error("expected IsClean=false with staged files")
	}
}

// --- formatTimeAgo ---

func TestFormatTimeAgo(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"seconds", 30 * time.Second, "just now"},
		{"1 minute", 1 * time.Minute, "1 minute ago"},
		{"minutes", 5 * time.Minute, "5 minutes ago"},
		{"1 hour", 1 * time.Hour, "1 hour ago"},
		{"hours", 3 * time.Hour, "3 hours ago"},
		{"1 day", 25 * time.Hour, "1 day ago"},
		{"days", 3 * 24 * time.Hour, "3 days ago"},
		{"1 week", 8 * 24 * time.Hour, "1 week ago"},
		{"weeks", 3 * 7 * 24 * time.Hour, "3 weeks ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTimeAgo(time.Now().Add(-tt.d))
			if got != tt.want {
				t.Errorf("formatTimeAgo(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// --- limitList ---

func TestLimitList_NoTruncation(t *testing.T) {
	list := []string{"a", "b", "c"}
	got := limitList(list, 5)
	if len(got) != 3 {
		t.Errorf("expected 3 items, got %d", len(got))
	}
}

func TestLimitList_Truncation(t *testing.T) {
	list := []string{"a", "b", "c", "d", "e"}
	got := limitList(list, 3)
	if len(got) != 3 || got[2] != "c" {
		t.Errorf("expected first 3 items [a,b,c], got %v", got)
	}
}

// --- parseBlameOutput ---

func TestParseBlameOutput(t *testing.T) {
	// git blame porcelain output: hash line has 40 hex chars + space + line nums.
	// Use real 40-char SHAs (38 chars would fail isHex check at line[:40]).
	hash1 := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	hash2 := "b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3"
	output := hash1 + " 10 10 1\n" +
		"author Alice\n" +
		"\tfunc main() {}\n" +
		hash2 + " 11 11 1\n" +
		"author Bob\n" +
		"\tfunc other() {}\n"
	result := parseBlameOutput(output)
	if len(result) != 2 {
		t.Fatalf("expected 2 blame entries, got %d", len(result))
	}
	if result[0].Author != "Alice" {
		t.Errorf("first author = %q, want Alice", result[0].Author)
	}
	if result[0].Line != 10 {
		t.Errorf("first line = %d, want 10", result[0].Line)
	}
	if result[0].Text != "func main() {}" {
		t.Errorf("first text = %q", result[0].Text)
	}
	if result[1].Author != "Bob" {
		t.Errorf("second author = %q, want Bob", result[1].Author)
	}
}

func TestParseBlameOutput_Empty(t *testing.T) {
	result := parseBlameOutput("")
	if len(result) != 0 {
		t.Errorf("expected 0 entries for empty output, got %d", len(result))
	}
}

// --- isHex ---

func TestIsHex(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"0123456789abcdef", true},
		{"ABCDEF", true},
		{"0x1G", false},
		{"", true}, // empty string vacuously true
		{"hello", false},
	}
	for _, tt := range tests {
		if got := isHex(tt.s); got != tt.want {
			t.Errorf("isHex(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

// --- GetBlameContext (integration) ---

func TestGitContextProvider_GetBlameContext(t *testing.T) {
	dir := setupGitRepo(t)
	p := NewGitContextProvider(dir)

	blame, err := p.GetBlameContext(context.Background(), "README.md", 1, 1)
	if err != nil {
		t.Fatalf("GetBlameContext: %v", err)
	}
	if len(blame) == 0 {
		t.Fatal("expected at least 1 blame entry")
	}
	if blame[0].Author == "" {
		t.Error("blame author should not be empty")
	}
}

func TestGitContextProvider_GetBlameContext_NonExistentFile(t *testing.T) {
	dir := setupGitRepo(t)
	p := NewGitContextProvider(dir)

	_, err := p.GetBlameContext(context.Background(), "nonexistent.go", 1, 1)
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

// --- GitIgnore: Load + IsIgnored ---

func TestGitIgnore_LoadAndMatch(t *testing.T) {
	dir := t.TempDir()

	// Create .gitignore
	content := "*.log\nbuild/\n!important.log\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	g := NewGitIgnore(dir)
	if err := g.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !g.IsLoaded() {
		t.Error("IsLoaded should be true after Load")
	}

	// *.log ignored
	if !g.IsIgnored(filepath.Join(dir, "test.log")) {
		t.Error("test.log should be ignored")
	}

	// important.log NOT ignored (negated)
	if g.IsIgnored(filepath.Join(dir, "important.log")) {
		t.Error("important.log should NOT be ignored (negated)")
	}

	// build/ directory ignored
	if err := os.Mkdir(filepath.Join(dir, "build"), 0755); err != nil {
		t.Fatal(err)
	}
	if !g.IsIgnored(filepath.Join(dir, "build", "output.go")) {
		t.Error("build/output.go should be ignored (under build/)")
	}

	// regular .go file not ignored
	if g.IsIgnored(filepath.Join(dir, "main.go")) {
		t.Error("main.go should NOT be ignored")
	}
}

func TestGitIgnore_NotLoaded(t *testing.T) {
	g := NewGitIgnore(t.TempDir())
	// Load not called — IsIgnored returns false
	if g.IsIgnored("anything") {
		t.Error("IsIgnored should return false when not loaded")
	}
}

func TestGitIgnore_InvalidateCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644); err != nil {
		t.Fatal(err)
	}

	g := NewGitIgnore(dir)
	if err := g.Load(); err != nil {
		t.Fatal(err)
	}

	// First call caches
	g.IsIgnored(filepath.Join(dir, "test.log"))

	// Invalidate
	g.InvalidateCache()

	// Should still work after cache clear
	if !g.IsIgnored(filepath.Join(dir, "test.log")) {
		t.Error("test.log should be ignored after cache invalidation")
	}
}

func TestGitIgnore_Reload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644); err != nil {
		t.Fatal(err)
	}

	g := NewGitIgnore(dir)
	if err := g.Load(); err != nil {
		t.Fatal(err)
	}

	if !g.IsIgnored(filepath.Join(dir, "test.log")) {
		t.Error("test.log should be ignored")
	}

	// Rewrite .gitignore to NOT ignore *.log
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.tmp\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := g.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if g.IsIgnored(filepath.Join(dir, "test.log")) {
		t.Error("test.log should NOT be ignored after reload")
	}
	if !g.IsIgnored(filepath.Join(dir, "test.tmp")) {
		t.Error("test.tmp should be ignored after reload")
	}
}

func TestGitIgnore_AddPattern(t *testing.T) {
	g := NewGitIgnore(t.TempDir())
	g.loaded = true // IsIgnored returns false if not loaded
	g.AddPattern("*.secret")

	if !g.IsIgnored(filepath.Join(g.workDir, "api.secret")) {
		t.Error("api.secret should be ignored after AddPattern")
	}
}

func TestGitIgnore_NestedGitignore(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Root .gitignore
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Nested .gitignore
	if err := os.WriteFile(filepath.Join(subDir, ".gitignore"), []byte("*.tmp\n"), 0644); err != nil {
		t.Fatal(err)
	}

	g := NewGitIgnore(dir)
	if err := g.Load(); err != nil {
		t.Fatal(err)
	}

	// Root pattern applies to sub
	if !g.IsIgnored(filepath.Join(subDir, "debug.log")) {
		t.Error("sub/debug.log should be ignored by root pattern")
	}
	// Nested pattern applies locally
	if !g.IsIgnored(filepath.Join(subDir, "cache.tmp")) {
		t.Error("sub/cache.tmp should be ignored by nested pattern")
	}
}

func TestGitIgnore_GitDirIgnored(t *testing.T) {
	dir := setupGitRepo(t)
	g := NewGitIgnore(dir)
	if err := g.Load(); err != nil {
		t.Fatal(err)
	}

	// .git directory should always be ignored
	if !g.IsIgnored(filepath.Join(dir, ".git", "config")) {
		t.Error(".git/config should be ignored")
	}
}

func TestGitIgnore_CacheEviction(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0644); err != nil {
		t.Fatal(err)
	}

	g := NewGitIgnore(dir)
	g.maxCacheSize = 3 // tiny cache to trigger eviction
	if err := g.Load(); err != nil {
		t.Fatal(err)
	}

	// Fill cache beyond capacity
	for i := range 5 {
		g.IsIgnored(filepath.Join(dir, "file"+string(rune('0'+i))+".log"))
	}

	// Cache should not exceed maxCacheSize
	g.mu.RLock()
	cacheLen := len(g.resultCache)
	g.mu.RUnlock()

	if cacheLen > g.maxCacheSize {
		t.Errorf("cache size %d exceeds max %d", cacheLen, g.maxCacheSize)
	}
}

// --- Worktree: ListWorktrees (additional coverage) ---

func TestListWorktrees_NotARepo(t *testing.T) {
	dir := t.TempDir()
	trees := ListWorktrees(dir)
	if trees != nil {
		t.Errorf("ListWorktrees in non-repo should return nil, got %v", trees)
	}
}

// --- getHead ---

func TestGetHead_InRepo(t *testing.T) {
	dir := setupGitRepo(t)
	head := getHead(dir)
	if head == "" {
		t.Error("getHead should return non-empty SHA")
	}
}

func TestGetHead_NotInRepo(t *testing.T) {
	dir := t.TempDir()
	head := getHead(dir)
	if head != "" {
		t.Errorf("getHead in non-repo should return empty, got %q", head)
	}
}
