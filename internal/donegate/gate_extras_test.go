package donegate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/tools"

	"google.golang.org/genai"
)

// ========== shellQuote ==========

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "/tmp/dir", "'/tmp/dir'"},
		{"empty", "", "''"},
		{"with_space", "/tmp/my dir", "'/tmp/my dir'"},
		{"with_single_quote", "/tmp/it's", `'/tmp/it'\''s'`},
		{"with_multiple_quotes", "a'b'c", `'a'\''b'\''c'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellQuote(tt.input)
			if got != tt.want {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ========== summarizeDoneGateCommand ==========

func TestSummarizeDoneGateCommand(t *testing.T) {
	// Short command — returned as-is
	short := "go test ./..."
	if got := summarizeDoneGateCommand(short); got != short {
		t.Errorf("short command: got %q, want %q", got, short)
	}
}

func TestSummarizeDoneGateCommand_Empty(t *testing.T) {
	if got := summarizeDoneGateCommand(""); got != "" {
		t.Errorf("empty command: got %q, want empty", got)
	}
}

func TestSummarizeDoneGateCommand_Whitespace(t *testing.T) {
	if got := summarizeDoneGateCommand("   "); got != "" {
		t.Errorf("whitespace command: got %q, want empty", got)
	}
}

func TestSummarizeDoneGateCommand_LongTruncated(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := summarizeDoneGateCommand(long)
	if len(got) > 90 {
		t.Errorf("long command not truncated: len=%d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("long command should end with '...', got %q", got)
	}
}

func TestSummarizeDoneGateCommand_CollapsesWhitespace(t *testing.T) {
	got := summarizeDoneGateCommand("go    test   ./...")
	if got != "go test ./..." {
		t.Errorf("whitespace not collapsed: got %q", got)
	}
}

// ========== IsMutationTool ==========

func TestIsMutationTool(t *testing.T) {
	mutationTools := []string{"write", "edit", "move", "copy", "delete", "mkdir", "batch", "refactor"}
	for _, name := range mutationTools {
		if !IsMutationTool(name) {
			t.Errorf("IsMutationTool(%q) = false, want true", name)
		}
	}
}

func TestIsMutationTool_NonMutation(t *testing.T) {
	nonMutation := []string{"read", "grep", "glob", "bash", "git_diff", "git_status", "list_dir", ""}
	for _, name := range nonMutation {
		if IsMutationTool(name) {
			t.Errorf("IsMutationTool(%q) = true, want false", name)
		}
	}
}

// ========== ExtractTouchedPaths ==========

func TestExtractTouchedPaths(t *testing.T) {
	args := map[string]any{
		"file_path":   "/src/main.go",
		"path":        "/src/util.go",
		"source":      "/old/file.go",
		"destination": "/new/file.go",
		"new_path":    "/another/file.go",
	}
	paths := ExtractTouchedPaths(args)
	if len(paths) != 5 {
		t.Fatalf("expected 5 paths, got %d: %v", len(paths), paths)
	}
	for _, p := range paths {
		if p == "" {
			t.Error("got empty path")
		}
	}
}

func TestExtractTouchedPaths_NilArgs(t *testing.T) {
	paths := ExtractTouchedPaths(nil)
	if paths != nil {
		t.Errorf("nil args should return nil, got %v", paths)
	}
}

func TestExtractTouchedPaths_EmptyArgs(t *testing.T) {
	paths := ExtractTouchedPaths(map[string]any{})
	if paths != nil {
		t.Errorf("empty args should return nil, got %v", paths)
	}
}

func TestExtractTouchedPaths_WhitespaceOnly(t *testing.T) {
	args := map[string]any{
		"file_path": "   ",
		"path":      "",
	}
	paths := ExtractTouchedPaths(args)
	if paths != nil {
		t.Errorf("whitespace-only paths should return nil, got %v", paths)
	}
}

func TestExtractTouchedPaths_NonStringArgs(t *testing.T) {
	args := map[string]any{
		"file_path": 123,
		"path":      true,
	}
	paths := ExtractTouchedPaths(args)
	if paths != nil {
		t.Errorf("non-string args should return nil, got %v", paths)
	}
}

func TestExtractTouchedPaths_PartialMatch(t *testing.T) {
	args := map[string]any{
		"file_path": "/src/main.go",
		"unknown":   "ignored",
	}
	paths := ExtractTouchedPaths(args)
	if len(paths) != 1 {
		t.Errorf("expected 1 path, got %d", len(paths))
	}
	if paths[0] != "/src/main.go" {
		t.Errorf("path = %q, want /src/main.go", paths[0])
	}
}

// ========== ShouldEnforce ==========

func TestShouldEnforce_NoTools(t *testing.T) {
	if ShouldEnforce("fix the bug", nil) {
		t.Error("should not enforce with no tools")
	}
	if ShouldEnforce("fix the bug", []string{}) {
		t.Error("should not enforce with empty tools")
	}
}

func TestShouldEnforce_MutationTool(t *testing.T) {
	if !ShouldEnforce("fix the bug", []string{"edit"}) {
		t.Error("edit should trigger enforcement")
	}
	if !ShouldEnforce("fix the bug", []string{"write"}) {
		t.Error("write should trigger enforcement")
	}
}

func TestShouldEnforce_BashWithCodingTask(t *testing.T) {
	if !ShouldEnforce("fix the bug", []string{"bash"}) {
		t.Error("bash + coding task should trigger enforcement")
	}
}

func TestShouldEnforce_BashWithoutCodingTask(t *testing.T) {
	if ShouldEnforce("hello world", []string{"bash"}) {
		t.Error("bash without coding task should not trigger enforcement")
	}
}

func TestShouldEnforce_ReadOnlyTools(t *testing.T) {
	if ShouldEnforce("fix the bug", []string{"read", "grep", "glob"}) {
		t.Error("read-only tools should not trigger enforcement")
	}
}

// ========== Passed ==========

func TestPassed_AllSuccess(t *testing.T) {
	results := []Result{
		{Name: "a", Success: true},
		{Name: "b", Success: true},
	}
	if !Passed(results) {
		t.Error("all-success results should pass")
	}
}

func TestPassed_OneFailed(t *testing.T) {
	results := []Result{
		{Name: "a", Success: true},
		{Name: "b", Success: false},
	}
	if Passed(results) {
		t.Error("one failed result should not pass")
	}
}

func TestPassed_Empty(t *testing.T) {
	if !Passed([]Result{}) {
		t.Error("empty results should pass (vacuously true)")
	}
}

func TestPassed_AllFailed(t *testing.T) {
	results := []Result{
		{Name: "a", Success: false},
		{Name: "b", Success: false},
	}
	if Passed(results) {
		t.Error("all-failed results should not pass")
	}
}

// ========== looksLikeCodingTask ==========

func TestLooksLikeCodingTask_English(t *testing.T) {
	keywords := []string{"implement", "fix", "refactor", "update", "change", "bug", "build", "lint", "compile"}
	for _, kw := range keywords {
		if !looksLikeCodingTask("please " + kw + " this") {
			t.Errorf("looksLikeCodingTask should be true for keyword %q", kw)
		}
	}
}

func TestLooksLikeCodingTask_Russian(t *testing.T) {
	keywords := []string{"доработ", "исправ", "рефактор", "обнов", "помен", "ошиб", "сборк", "линт", "код"}
	for _, kw := range keywords {
		if !looksLikeCodingTask("пожалуйста " + kw + " это") {
			t.Errorf("looksLikeCodingTask should be true for Russian keyword %q", kw)
		}
	}
}

func TestLooksLikeCodingTask_Empty(t *testing.T) {
	if looksLikeCodingTask("") {
		t.Error("empty string should not look like coding task")
	}
}

func TestLooksLikeCodingTask_Whitespace(t *testing.T) {
	if looksLikeCodingTask("   ") {
		t.Error("whitespace should not look like coding task")
	}
}

func TestLooksLikeCodingTask_NonCoding(t *testing.T) {
	if looksLikeCodingTask("hello world, how are you?") {
		t.Error("non-coding message should not look like coding task")
	}
}

// ========== doneGateCheckEvidence ==========

func TestDoneGateCheckEvidence(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		wantHas string
	}{
		{"verify_code", "go vet", "verify code"},
		{"git_diff_check", "git diff --check", "git diff --check"},
		{"git_unmerged_paths", "git diff --name-only", "git unmerged paths"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := doneGateCheckEvidence(tt.name, tt.cmd)
			if !strings.Contains(got, tt.wantHas) {
				t.Errorf("evidence for %q = %q, want to contain %q", tt.name, got, tt.wantHas)
			}
		})
	}
}

func TestDoneGateCheckEvidence_RunTestsPrefix(t *testing.T) {
	got := doneGateCheckEvidence("run_tests@go@./...", "go test ./...")
	if !strings.Contains(got, "run tests") {
		t.Errorf("evidence should contain 'run tests', got %q", got)
	}
}

func TestDoneGateCheckEvidence_VetPrefix(t *testing.T) {
	got := doneGateCheckEvidence("go_vet@./internal", "go vet ./internal")
	if !strings.Contains(got, "go vet") {
		t.Errorf("evidence should contain 'go vet', got %q", got)
	}
}

func TestDoneGateCheckEvidence_FallbackToCommand(t *testing.T) {
	got := doneGateCheckEvidence("unknown_check", "echo hello")
	if got != "echo hello" {
		t.Errorf("unknown check should fall back to command, got %q", got)
	}
}

// ========== doneGateCheckDisplayName ==========

func TestDoneGateCheckDisplayName(t *testing.T) {
	check := Check{Name: "my_check", Evidence: "go vet"}
	got := doneGateCheckDisplayName(check)
	if got != "go vet" {
		t.Errorf("displayName = %q, want 'go vet'", got)
	}
}

func TestDoneGateCheckDisplayName_EmptyEvidence(t *testing.T) {
	check := Check{Name: "my_check", Evidence: ""}
	got := doneGateCheckDisplayName(check)
	if got != "my check" {
		t.Errorf("displayName = %q, want 'my check' (underscores→spaces)", got)
	}
}

func TestDoneGateCheckDisplayName_EmptyAll(t *testing.T) {
	check := Check{Name: "", Evidence: ""}
	got := doneGateCheckDisplayName(check)
	if got != "verification" {
		t.Errorf("displayName = %q, want 'verification'", got)
	}
}

// ========== ResultDisplayName ==========

func TestResultDisplayName(t *testing.T) {
	r := Result{DisplayName: "go vet", Name: "check_1"}
	if got := ResultDisplayName(r); got != "go vet" {
		t.Errorf("got %q, want 'go vet'", got)
	}
}

func TestResultDisplayName_FallbackToName(t *testing.T) {
	r := Result{DisplayName: "", Name: "my_check"}
	got := ResultDisplayName(r)
	if got != "my check" {
		t.Errorf("got %q, want 'my check'", got)
	}
}

func TestResultDisplayName_Empty(t *testing.T) {
	r := Result{}
	if got := ResultDisplayName(r); got != "verification" {
		t.Errorf("got %q, want 'verification'", got)
	}
}

// ========== truncateDoneGateText ==========

func TestTruncateDoneGateText_Short(t *testing.T) {
	s := "short text"
	if got := truncateDoneGateText(s); got != s {
		t.Errorf("short text should not be truncated, got %q", got)
	}
}

func TestTruncateDoneGateText_Long(t *testing.T) {
	long := strings.Repeat("x", doneGateOutputLimit+100)
	got := truncateDoneGateText(long)
	if len(got) > doneGateOutputLimit+10 {
		t.Errorf("long text not truncated: len=%d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated text should end with '...'")
	}
}

func TestTruncateDoneGateText_Whitespace(t *testing.T) {
	if got := truncateDoneGateText("  "); got != "" {
		t.Errorf("whitespace should be trimmed to empty, got %q", got)
	}
}

// ========== BuildFixPrompt ==========

func TestBuildFixPrompt(t *testing.T) {
	failed := []Result{
		{Name: "go_vet", DisplayName: "go vet", Success: false, Error: "syntax error"},
		{Name: "run_tests", DisplayName: "run tests", Success: false, Content: "test output"},
	}
	prompt := BuildFixPrompt("fix the bug", failed, 1, 3)

	if !strings.Contains(prompt, "fix the bug") {
		t.Error("prompt should contain user message")
	}
	if !strings.Contains(prompt, "Auto-fix attempt 1/3") {
		t.Error("prompt should contain attempt counter")
	}
	if !strings.Contains(prompt, "go vet") {
		t.Error("prompt should contain failed check name")
	}
	if !strings.Contains(prompt, "syntax error") {
		t.Error("prompt should contain error")
	}
	if !strings.Contains(prompt, "test output") {
		t.Error("prompt should contain content")
	}
}

// ========== dirExists ==========

func TestDirExists_True(t *testing.T) {
	dir := t.TempDir()
	if !dirExists(dir) {
		t.Error("existing dir should return true")
	}
}

func TestDirExists_False(t *testing.T) {
	if dirExists("/nonexistent/path/that/does/not/exist") {
		t.Error("nonexistent dir should return false")
	}
}

func TestDirExists_Empty(t *testing.T) {
	if dirExists("") {
		t.Error("empty path should return false")
	}
}

// ========== readFileHead ==========

func TestReadFileHead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "line one\nline two\nline three\n"
	os.WriteFile(path, []byte(content), 0644)

	// readFileHead reads maxChars RUNES, not lines. With maxChars=20 we get
	// "line one\nline two\nline" (20 runes) — at least 2 lines.
	got := readFileHead(path, 20)
	if len(got) == 0 {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(got, "line one") {
		t.Errorf("should contain 'line one', got %q", got)
	}
}

func TestReadFileHead_Nonexistent(t *testing.T) {
	got := readFileHead("/nonexistent/file.txt", 10)
	if got != "" {
		t.Errorf("nonexistent file should return empty, got %q", got)
	}
}

func TestReadFileHead_FullFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.txt")
	os.WriteFile(path, []byte("only line\n"), 0644)

	got := readFileHead(path, 10)
	if !strings.Contains(got, "only line") {
		t.Errorf("should contain file content, got %q", got)
	}
}

// ========== isGitWorkTree ==========

func TestIsGitWorkTree_OutsideGit(t *testing.T) {
	dir := t.TempDir()
	if isGitWorkTree(dir) {
		t.Error("temp dir should not be a git work tree")
	}
}

// ========== readNodeScripts ==========

func TestReadNodeScripts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "package.json")
	pkg := map[string]any{
		"scripts": map[string]any{
			"test":  "jest",
			"build": "webpack",
			"lint":  "eslint",
		},
	}
	data, _ := json.Marshal(pkg)
	os.WriteFile(path, data, 0644)

	scripts := readNodeScripts(path)
	if len(scripts) != 3 {
		t.Fatalf("expected 3 scripts, got %d", len(scripts))
	}
	if scripts["test"] != "jest" {
		t.Errorf("test script = %q, want 'jest'", scripts["test"])
	}
}

func TestReadNodeScripts_Nonexistent(t *testing.T) {
	scripts := readNodeScripts("/nonexistent/package.json")
	if len(scripts) != 0 {
		t.Errorf("nonexistent file should return empty map, got %d", len(scripts))
	}
}

func TestReadNodeScripts_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "package.json")
	os.WriteFile(path, []byte("{invalid json}"), 0644)

	scripts := readNodeScripts(path)
	if len(scripts) != 0 {
		t.Errorf("invalid JSON should return empty map, got %d", len(scripts))
	}
}

func TestReadNodeScripts_NoScripts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "package.json")
	os.WriteFile(path, []byte(`{"name": "test"}`), 0644)

	scripts := readNodeScripts(path)
	if len(scripts) != 0 {
		t.Errorf("package without scripts should return empty map, got %d", len(scripts))
	}
}

func TestReadNodeScripts_EmptyScriptValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "package.json")
	pkg := map[string]any{
		"scripts": map[string]any{
			"test":  "jest",
			"empty": "",
		},
	}
	data, _ := json.Marshal(pkg)
	os.WriteFile(path, data, 0644)

	scripts := readNodeScripts(path)
	if len(scripts) != 1 {
		t.Errorf("empty script value should be skipped, got %d scripts", len(scripts))
	}
	if _, ok := scripts["test"]; !ok {
		t.Error("test script should be present")
	}
}

// ========== readComposerScripts ==========

func TestReadComposerScripts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "composer.json")
	pkg := map[string]any{
		"scripts": map[string]any{
			"test":  "phpunit",
			"lint":  "phpcs",
			"build": "compile",
		},
	}
	data, _ := json.Marshal(pkg)
	os.WriteFile(path, data, 0644)

	scripts := readComposerScripts(path)
	if len(scripts) != 3 {
		t.Fatalf("expected 3 scripts, got %d", len(scripts))
	}
	if scripts["test"] != "phpunit" {
		t.Errorf("test = %q, want 'phpunit'", scripts["test"])
	}
}

func TestReadComposerScripts_Nonexistent(t *testing.T) {
	scripts := readComposerScripts("/nonexistent/composer.json")
	if len(scripts) != 0 {
		t.Errorf("nonexistent should return empty, got %d", len(scripts))
	}
}

func TestReadComposerScripts_NonStringValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "composer.json")
	pkg := map[string]any{
		"scripts": map[string]any{
			"test":  "phpunit",
			"array": []string{"a", "b"}, // non-string should be skipped
		},
	}
	data, _ := json.Marshal(pkg)
	os.WriteFile(path, data, 0644)

	scripts := readComposerScripts(path)
	if len(scripts) != 1 {
		t.Errorf("non-string value should be skipped, got %d", len(scripts))
	}
}

// ========== detectNodeRunner ==========

func TestDetectNodeRunner_NPM(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0644)
	runner := detectNodeRunner(dir, dir)
	if runner != "npm" {
		t.Errorf("runner = %q, want 'npm'", runner)
	}
}

func TestDetectNodeRunner_PNPM(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte(""), 0644)
	runner := detectNodeRunner(dir, dir)
	if runner != "pnpm" {
		t.Errorf("runner = %q, want 'pnpm'", runner)
	}
}

func TestDetectNodeRunner_Yarn(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "yarn.lock"), []byte(""), 0644)
	runner := detectNodeRunner(dir, dir)
	if runner != "yarn" {
		t.Errorf("runner = %q, want 'yarn'", runner)
	}
}

func TestDetectNodeRunner_Bun(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bun.lockb"), []byte(""), 0644)
	runner := detectNodeRunner(dir, dir)
	if runner != "bun" {
		t.Errorf("runner = %q, want 'bun'", runner)
	}
}

func TestDetectNodeRunner_NoLockFile(t *testing.T) {
	dir := t.TempDir()
	runner := detectNodeRunner(dir, dir)
	if runner != "npm" {
		t.Errorf("default runner = %q, want 'npm'", runner)
	}
}

func TestDetectNodeRunner_FindsInParentDir(t *testing.T) {
	parent := t.TempDir()
	os.WriteFile(filepath.Join(parent, "yarn.lock"), []byte(""), 0644)
	child := filepath.Join(parent, "subdir")
	os.MkdirAll(child, 0755)

	runner := detectNodeRunner(parent, child)
	if runner != "yarn" {
		t.Errorf("runner = %q, want 'yarn' (found in parent)", runner)
	}
}

// ========== nodeScriptAliases ==========

func TestNodeScriptAliases(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"test", []string{"test", "tests", "t"}},
		{"lint", []string{"lint", "l"}},
		{"build", []string{"build"}},
		{"unknown", nil},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := nodeScriptAliases(tt.input)
			if len(got) == 0 && len(tt.want) > 0 {
				t.Errorf("nodeScriptAliases(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ========== composerScriptAliases ==========

func TestComposerScriptAliases(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"test", []string{"test", "tests"}},
		{"lint", []string{"lint"}},
		{"static", []string{"static"}},
		{"unknown", nil},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := composerScriptAliases(tt.input)
			if len(got) == 0 && len(tt.want) > 0 {
				t.Errorf("composerScriptAliases(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ========== isNoopNodeTestScript ==========

func TestIsNoopNodeTestScript(t *testing.T) {
	noops := []string{
		"echo \"Error: no test specified\" && exit 1",
		"echo \"no tests\" && exit 0",
		"",
		"   ",
	}
	for _, s := range noops {
		if !isNoopNodeTestScript(s) {
			t.Errorf("isNoopNodeTestScript(%q) = false, want true", s)
		}
	}
}

func TestIsNoopNodeTestScript_RealTest(t *testing.T) {
	reals := []string{
		"jest",
		"mocha",
		"vitest",
		"node --test",
	}
	for _, s := range reals {
		if isNoopNodeTestScript(s) {
			t.Errorf("isNoopNodeTestScript(%q) = true, want false", s)
		}
	}
}

// ========== RunChecks ==========

func TestRunChecks_AllPass(t *testing.T) {
	checks := []Check{
		{
			Name: "check1",
			Run: func(ctx context.Context) (tools.ToolResult, error) {
				return tools.NewSuccessResult("ok"), nil
			},
		},
		{
			Name: "check2",
			Run: func(ctx context.Context) (tools.ToolResult, error) {
				return tools.NewSuccessResult("ok"), nil
			},
		},
	}
	results := RunChecks(context.Background(), checks, 10*time.Second, nil, nil)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !Passed(results) {
		t.Error("all checks should pass")
	}
}

func TestRunChecks_OneFails(t *testing.T) {
	checks := []Check{
		{
			Name: "pass_check",
			Run: func(ctx context.Context) (tools.ToolResult, error) {
				return tools.NewSuccessResult("ok"), nil
			},
		},
		{
			Name: "fail_check",
			Run: func(ctx context.Context) (tools.ToolResult, error) {
				return tools.NewErrorResult("something went wrong"), nil
			},
		},
	}
	results := RunChecks(context.Background(), checks, 10*time.Second, nil, nil)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if Passed(results) {
		t.Error("should not pass when one check fails")
	}
}

func TestRunChecks_WithError(t *testing.T) {
	checks := []Check{
		{
			Name: "error_check",
			Run: func(ctx context.Context) (tools.ToolResult, error) {
				return tools.ToolResult{}, context.DeadlineExceeded
			},
		},
	}
	results := RunChecks(context.Background(), checks, 10*time.Second, nil, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Success {
		t.Error("error check should not be success")
	}
}

func TestRunChecks_WithProgressCallback(t *testing.T) {
	var progressCalls []string
	checks := []Check{
		{Name: "a", Run: func(ctx context.Context) (tools.ToolResult, error) {
			return tools.NewSuccessResult("ok"), nil
		}},
		{Name: "b", Run: func(ctx context.Context) (tools.ToolResult, error) {
			return tools.NewSuccessResult("ok"), nil
		}},
	}
	RunChecks(context.Background(), checks, 10*time.Second,
		func(current, total int, name string) {
			progressCalls = append(progressCalls, name)
		}, nil)
	if len(progressCalls) != 2 {
		t.Errorf("expected 2 progress calls, got %d", len(progressCalls))
	}
}

func TestRunChecks_WithEvidenceCallback(t *testing.T) {
	var evidenceCalls []string
	checks := []Check{
		{Name: "a", Evidence: "go vet", Run: func(ctx context.Context) (tools.ToolResult, error) {
			return tools.NewSuccessResult("ok"), nil
		}},
	}
	RunChecks(context.Background(), checks, 10*time.Second, nil,
		func(evidence string) {
			evidenceCalls = append(evidenceCalls, evidence)
		})
	if len(evidenceCalls) != 1 {
		t.Errorf("expected 1 evidence call, got %d", len(evidenceCalls))
	}
}

func TestRunChecks_Empty(t *testing.T) {
	results := RunChecks(context.Background(), nil, 10*time.Second, nil, nil)
	if len(results) != 0 {
		t.Errorf("empty checks should return empty results, got %d", len(results))
	}
}

// ========== newBashCheck ==========

func TestNewBashCheck(t *testing.T) {
	stub := &doneGateStubTool{name: "bash"}
	check := newBashCheck(stub, "test_check", "echo hello")

	if check.Name != "test_check" {
		t.Errorf("Name = %q, want 'test_check'", check.Name)
	}
	if check.Run == nil {
		t.Fatal("Run should not be nil")
	}
	if check.Evidence == "" {
		t.Error("Evidence should not be empty")
	}

	// Execute the check
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("check.Run failed: %v", err)
	}
	_ = result
}

// ========== newBashCheckWithDir ==========

func TestNewBashCheckWithDir_EmptyDir(t *testing.T) {
	stub := &doneGateStubTool{name: "bash"}
	check := newBashCheckWithDir(stub, "test", "", "echo hello")
	if check.Name != "test" {
		t.Errorf("Name = %q, want 'test'", check.Name)
	}
}

func TestNewBashCheckWithDir_DotDir(t *testing.T) {
	stub := &doneGateStubTool{name: "bash"}
	check := newBashCheckWithDir(stub, "test", ".", "echo hello")
	if check.Name != "test" {
		t.Errorf("Name = %q, want 'test'", check.Name)
	}
}

func TestNewBashCheckWithDir_RealDir(t *testing.T) {
	stub := &doneGateStubTool{name: "bash"}
	check := newBashCheckWithDir(stub, "test", "/tmp", "echo hello")
	if check.Name != "test" {
		t.Errorf("Name = %q, want 'test'", check.Name)
	}
	// Evidence should contain the wrapped command with cd
	if !strings.Contains(check.Evidence, "cd") {
		t.Errorf("evidence should contain 'cd' for dir-based check, got %q", check.Evidence)
	}
}

// ========== newBashCheckWithTimeout ==========

func TestNewBashCheckWithTimeout(t *testing.T) {
	stub := &doneGateStubTool{name: "bash"}
	check := newBashCheckWithTimeout(stub, "fast_check", "", "echo hello", 5*time.Second)

	if check.Name != "fast_check" {
		t.Errorf("Name = %q, want 'fast_check'", check.Name)
	}
	if check.Run == nil {
		t.Fatal("Run should not be nil")
	}

	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("check.Run failed: %v", err)
	}
	_ = result
}

func TestNewBashCheckWithTimeout_WithDir(t *testing.T) {
	stub := &doneGateStubTool{name: "bash"}
	check := newBashCheckWithTimeout(stub, "check", "/tmp", "pwd", 5*time.Second)

	if !strings.Contains(check.Evidence, "cd") {
		t.Errorf("evidence should contain 'cd' for dir, got %q", check.Evidence)
	}
}

// ========== DetectProfile ==========

func TestDetectProfile_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	profile := DetectProfile(dir)
	if profile.WorkDir != dir {
		t.Errorf("WorkDir = %q, want %q", profile.WorkDir, dir)
	}
}

func TestDetectProfile_GoModule(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0644)

	profile := DetectProfile(dir)
	if len(profile.GoModules) == 0 {
		t.Error("expected Go module to be detected")
	}
}

func TestDetectProfile_NodeProject(t *testing.T) {
	dir := t.TempDir()
	pkg := map[string]any{
		"name":    "test",
		"version": "1.0.0",
		"scripts": map[string]any{"test": "jest"},
	}
	data, _ := json.Marshal(pkg)
	os.WriteFile(filepath.Join(dir, "package.json"), data, 0644)

	profile := DetectProfile(dir)
	if len(profile.NodeProjects) == 0 {
		t.Error("expected Node project to be detected")
	}
}

// ========== NormalizeTouchedPaths ==========

func TestNormalizeTouchedPaths_Nil(t *testing.T) {
	if got := NormalizeTouchedPaths("/work", nil); got != nil {
		t.Errorf("nil paths should return nil, got %v", got)
	}
}

func TestNormalizeTouchedPaths_Empty(t *testing.T) {
	if got := NormalizeTouchedPaths("/work", []string{}); got != nil {
		t.Errorf("empty paths should return nil, got %v", got)
	}
}

func TestNormalizeTouchedPaths_Relative(t *testing.T) {
	// Relative paths are kept relative (cleaned + slash-normalized)
	got := NormalizeTouchedPaths("/work", []string{"src/main.go"})
	if len(got) != 1 {
		t.Fatalf("expected 1 path, got %d", len(got))
	}
	if got[0] != "src/main.go" {
		t.Errorf("path = %q, want 'src/main.go'", got[0])
	}
}

func TestNormalizeTouchedPaths_AbsoluteInsideWorkDir(t *testing.T) {
	// Absolute path inside workDir → converted to relative
	got := NormalizeTouchedPaths("/work", []string{"/work/src/main.go"})
	if len(got) != 1 {
		t.Fatalf("expected 1 path, got %d: %v", len(got), got)
	}
	if got[0] != "src/main.go" {
		t.Errorf("path = %q, want 'src/main.go'", got[0])
	}
}

func TestNormalizeTouchedPaths_AbsoluteOutsideWorkDir(t *testing.T) {
	// Absolute path OUTSIDE workDir → Rel returns ../... → skipped
	got := NormalizeTouchedPaths("/work", []string{"/other/main.go"})
	if len(got) != 0 {
		t.Errorf("path outside workDir should be skipped, got %v", got)
	}
}

func TestNormalizeTouchedPaths_Deduplicates(t *testing.T) {
	got := NormalizeTouchedPaths("/work", []string{"main.go", "main.go", "main.go"})
	if len(got) != 1 {
		t.Errorf("expected dedup to 1 path, got %d: %v", len(got), got)
	}
}

// ========== compactDoneGateFailureDetail ==========

func TestCompactDoneGateFailureDetail_WithError(t *testing.T) {
	r := Result{Error: "some error message"}
	got := compactDoneGateFailureDetail(r)
	if !strings.Contains(got, "some error message") {
		t.Errorf("should contain error, got %q", got)
	}
}

func TestCompactDoneGateFailureDetail_WithContent(t *testing.T) {
	r := Result{Content: "some content"}
	got := compactDoneGateFailureDetail(r)
	if !strings.Contains(got, "some content") {
		t.Errorf("should contain content, got %q", got)
	}
}

func TestCompactDoneGateFailureDetail_Empty(t *testing.T) {
	r := Result{}
	got := compactDoneGateFailureDetail(r)
	if got != "" {
		t.Errorf("empty result should return empty, got %q", got)
	}
}

// ========== shouldRunDoneGateToolTests ==========

func TestShouldRunDoneGateToolTests_NoTestsTargets(t *testing.T) {
	profile := Profile{WorkDir: "/tmp"}
	if shouldRunDoneGateToolTests("fix bug", []string{"edit"}, profile) {
		t.Error("should not run tests with no test targets")
	}
}

func TestShouldRunDoneGateToolTests_NoTestsKeyword(t *testing.T) {
	profile := Profile{WorkDir: "/tmp", GoModules: []string{"/tmp"}}
	if shouldRunDoneGateToolTests("без тест", []string{"edit"}, profile) {
		t.Error("should not run tests with 'без тест' keyword")
	}
	if shouldRunDoneGateToolTests("no tests please", []string{"edit"}, profile) {
		t.Error("should not run tests with 'no tests' keyword")
	}
}

func TestShouldRunDoneGateToolTests_RunTestsToolUsed(t *testing.T) {
	profile := Profile{
		WorkDir:      "/tmp",
		GoModules:    []string{"/tmp"},
		TouchedPaths: []string{"/tmp/main.go"},
	}
	if !shouldRunDoneGateToolTests("fix bug", []string{"run_tests"}, profile) {
		t.Error("should run tests when run_tests tool was used")
	}
}

// ========== stub tool (re-export for this file) ==========

// Ensure the stub tool from gate_test.go satisfies the tools.Tool interface.
var _ tools.Tool = (*doneGateStubTool)(nil)

// Ensure genai import is used (for consistency with gate_test.go).
var _ = genai.TypeObject
