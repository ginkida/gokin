package context

import (
	"os"
	"strings"
	"testing"

	"gokin/internal/memory"

	"google.golang.org/genai"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// userMsg creates a user content with a single text part.
func userMsg(text string) *genai.Content {
	return genai.NewContentFromText(text, genai.RoleUser)
}

// modelMsg creates a model content with a single text part.
func modelMsg(text string) *genai.Content {
	return genai.NewContentFromText(text, genai.RoleModel)
}

// funcCallMsg creates a model content that contains one FunctionCall.
func funcCallMsg(name string, args map[string]any) *genai.Content {
	return &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: name, Args: args}},
		},
	}
}

// funcRespMsg creates a user content that contains one FunctionResponse.
func funcRespMsg(name string, resp map[string]any) *genai.Content {
	return &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{Name: name, Response: resp}},
		},
	}
}

// minHistory returns a small but valid history (>= 4 messages).
func minHistory() []*genai.Content {
	return []*genai.Content{
		userMsg("Please refactor the authentication module to use JWT tokens"),
		modelMsg("Sure, I will refactor the authentication module."),
		funcCallMsg("read", map[string]any{"file_path": "/src/auth.go"}),
		funcRespMsg("read", map[string]any{"content": "package auth", "success": true}),
	}
}

// ---------------------------------------------------------------------------
// TestSessionMemory_ShouldExtract
// ---------------------------------------------------------------------------

func TestSessionMemory_ShouldExtract_NotEnabled(t *testing.T) {
	cfg := DefaultSessionMemoryConfig()
	cfg.Enabled = false
	mgr := NewSessionMemoryManager(t.TempDir(), cfg)

	if mgr.ShouldExtract(999999) {
		t.Error("ShouldExtract must return false when Enabled is false")
	}
}

func TestSessionMemory_ShouldExtract_FirstExtractionWaitsForMinTokens(t *testing.T) {
	cfg := DefaultSessionMemoryConfig()
	cfg.MinTokensToInit = 10000
	mgr := NewSessionMemoryManager(t.TempDir(), cfg)

	if mgr.ShouldExtract(5000) {
		t.Error("ShouldExtract should be false when tokens < MinTokensToInit")
	}
	if mgr.ShouldExtract(9999) {
		t.Error("ShouldExtract should be false when tokens < MinTokensToInit")
	}
	if !mgr.ShouldExtract(10000) {
		t.Error("ShouldExtract should be true when tokens == MinTokensToInit")
	}
	if !mgr.ShouldExtract(20000) {
		t.Error("ShouldExtract should be true when tokens > MinTokensToInit")
	}
}

func TestSessionMemory_ShouldExtract_SubsequentByTokenDelta(t *testing.T) {
	cfg := DefaultSessionMemoryConfig()
	cfg.MinTokensToInit = 100
	cfg.MinTokensBetweenUpdates = 5000
	cfg.ToolCallsBetweenUpdates = 999 // disable tool-call trigger
	mgr := NewSessionMemoryManager(t.TempDir(), cfg)

	// Trigger first extraction to set initialized = true.
	mgr.Extract(minHistory(), 200)

	// Just after extraction, not enough delta yet.
	if mgr.ShouldExtract(200) {
		t.Error("ShouldExtract should be false when token delta is 0")
	}
	if mgr.ShouldExtract(5100) {
		t.Error("ShouldExtract should be false when delta < MinTokensBetweenUpdates")
	}
	if !mgr.ShouldExtract(5200) {
		t.Error("ShouldExtract should be true when delta >= MinTokensBetweenUpdates")
	}
}

func TestSessionMemory_ShouldExtract_SubsequentByToolCalls(t *testing.T) {
	cfg := DefaultSessionMemoryConfig()
	cfg.MinTokensToInit = 100
	cfg.MinTokensBetweenUpdates = 999999 // disable token trigger
	cfg.ToolCallsBetweenUpdates = 3
	mgr := NewSessionMemoryManager(t.TempDir(), cfg)

	mgr.Extract(minHistory(), 200)

	mgr.RecordToolCall()
	mgr.RecordToolCall()
	if mgr.ShouldExtract(200) {
		t.Error("ShouldExtract should be false with only 2 tool calls")
	}
	mgr.RecordToolCall()
	if !mgr.ShouldExtract(200) {
		t.Error("ShouldExtract should be true with 3 tool calls")
	}
}

// ---------------------------------------------------------------------------
// TestSessionMemory_RecordToolCall
// ---------------------------------------------------------------------------

func TestSessionMemory_RecordToolCall_IncrementsAndResetsAfterExtract(t *testing.T) {
	cfg := DefaultSessionMemoryConfig()
	cfg.MinTokensToInit = 1
	cfg.MinTokensBetweenUpdates = 999999
	cfg.ToolCallsBetweenUpdates = 2
	mgr := NewSessionMemoryManager(t.TempDir(), cfg)

	mgr.RecordToolCall()
	mgr.RecordToolCall()

	// After first Extract, toolCallsSinceUpdate should reset.
	mgr.Extract(minHistory(), 100)

	// Now we need 2 more tool calls to trigger again.
	if mgr.ShouldExtract(100) {
		t.Error("ShouldExtract should be false right after Extract (tool counter reset)")
	}
	mgr.RecordToolCall()
	if mgr.ShouldExtract(100) {
		t.Error("ShouldExtract should be false with only 1 tool call after reset")
	}
	mgr.RecordToolCall()
	if !mgr.ShouldExtract(100) {
		t.Error("ShouldExtract should be true with 2 tool calls after reset")
	}
}

// ---------------------------------------------------------------------------
// TestSessionMemory_Extract
// ---------------------------------------------------------------------------

func TestSessionMemory_Extract_SkipsShortHistory(t *testing.T) {
	mgr := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	shortHistory := []*genai.Content{
		userMsg("Hello"),
		modelMsg("Hi"),
	}
	mgr.Extract(shortHistory, 100)
	if mgr.GetContent() != "" {
		t.Error("Extract should produce no content for history < 4 messages")
	}
}

// TestSessionMemory_PersistsOwnerOnly: session memory captures
// recent files, errors, and decisions during the active session.
// Same sensitivity class as the chat history (also 0600). Pinned in
// tests so a future "default 0644 is fine" change can't silently
// regress.
func TestSessionMemory_PersistsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	mgr := NewSessionMemoryManager(dir, DefaultSessionMemoryConfig())

	history := []*genai.Content{
		userMsg("Please touch a file so we extract something"),
		modelMsg("On it."),
		funcCallMsg("read", map[string]any{"file_path": "/src/anything.go"}),
		funcRespMsg("read", map[string]any{"content": "package anything", "success": true}),
	}
	mgr.Extract(history, 5000)

	gokinDir := dir + "/.gokin"
	dirInfo, err := os.Stat(gokinDir)
	if err != nil {
		t.Fatalf("stat .gokin: %v", err)
	}
	if dirMode := dirInfo.Mode().Perm(); dirMode != 0700 {
		t.Errorf(".gokin dir mode = %o, want 0700 (owner-only)", dirMode)
	}

	fileInfo, err := os.Stat(gokinDir + "/.session-memory.md")
	if err != nil {
		t.Fatalf("stat session-memory file: %v", err)
	}
	if fileMode := fileInfo.Mode().Perm(); fileMode != 0600 {
		t.Errorf("session-memory file mode = %o, want 0600 (owner-only)", fileMode)
	}
}

func TestSessionMemory_Extract_BuildsCurrentStateSection(t *testing.T) {
	mgr := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	history := []*genai.Content{
		userMsg("Please refactor the authentication module to use JWT tokens"),
		modelMsg("Sure, I will start on that now."),
		funcCallMsg("read", map[string]any{"file_path": "/src/auth.go"}),
		funcRespMsg("read", map[string]any{"content": "package auth", "success": true}),
	}
	mgr.Extract(history, 5000)
	content := mgr.GetContent()

	if !strings.Contains(content, "## Current State") {
		t.Error("expected Current State section")
	}
	if !strings.Contains(content, "refactor the authentication module") {
		t.Error("expected user task text in Current State")
	}
}

func TestSessionMemory_Extract_BuildsFilesSection(t *testing.T) {
	mgr := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	history := []*genai.Content{
		userMsg("Please review and fix the authentication code"),
		modelMsg("Let me look at the files."),
		funcCallMsg("read", map[string]any{"file_path": "/src/auth.go"}),
		funcRespMsg("read", map[string]any{"content": "package auth"}),
		funcCallMsg("edit", map[string]any{"file_path": "/src/auth.go", "old_string": "a", "new_string": "b"}),
		funcRespMsg("edit", map[string]any{"success": true}),
	}
	mgr.Extract(history, 5000)
	content := mgr.GetContent()

	if !strings.Contains(content, "## Files and Functions") {
		t.Error("expected Files and Functions section")
	}
	if !strings.Contains(content, "/src/auth.go") {
		t.Error("expected file path in output")
	}
	if !strings.Contains(content, "edited") {
		t.Error("expected 'edited' action for file that was read then edited")
	}
}

func TestSessionMemory_Extract_BuildsWorkflowSection(t *testing.T) {
	mgr := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	history := []*genai.Content{
		userMsg("Search for all TODO comments in the codebase"),
		modelMsg("I will search the codebase now."),
		funcCallMsg("grep", map[string]any{"path": "/src", "pattern": "TODO"}),
		funcRespMsg("grep", map[string]any{"content": "found matches"}),
		funcCallMsg("grep", map[string]any{"path": "/tests", "pattern": "TODO"}),
		funcRespMsg("grep", map[string]any{"content": "found matches"}),
	}
	mgr.Extract(history, 5000)
	content := mgr.GetContent()

	if !strings.Contains(content, "## Workflow") {
		t.Error("expected Workflow section")
	}
	if !strings.Contains(content, "grep (2x)") {
		t.Error("expected grep counted twice in Workflow")
	}
}

func TestSessionMemory_Extract_BuildsErrorsSection(t *testing.T) {
	mgr := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	history := []*genai.Content{
		userMsg("Compile and run the test suite for the project"),
		modelMsg("Running tests now."),
		funcCallMsg("bash", map[string]any{"command": "go test ./..."}),
		funcRespMsg("bash", map[string]any{
			"content": "--- FAIL: TestAuth\nerror: invalid token\nFAIL",
		}),
	}
	mgr.Extract(history, 5000)
	content := mgr.GetContent()

	if !strings.Contains(content, "## Errors & Corrections") {
		t.Error("expected Errors & Corrections section")
	}
	if !strings.Contains(content, "error: invalid token") {
		t.Error("expected error line in output")
	}
}

func TestSessionMemory_Extract_PromotesDurableCommandsToProjectLearning(t *testing.T) {
	dir := t.TempDir()
	mgr := NewSessionMemoryManager(dir, DefaultSessionMemoryConfig())
	learning, err := memory.NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning() error = %v", err)
	}
	mgr.SetProjectLearning(learning)

	history := []*genai.Content{
		userMsg("Please format the UI package and verify it still builds and tests cleanly."),
		modelMsg("I will run the relevant commands and keep the project memory up to date."),
		funcCallMsg("bash", map[string]any{
			"command": "cd /Users/ginkida/github/gokin && gofmt -w internal/ui && go test ./internal/ui -count=1",
		}),
		funcRespMsg("bash", map[string]any{"success": true, "content": "ok\tinternal/ui\t0.211s"}),
		funcCallMsg("verify_code", map[string]any{"path": "."}),
		funcRespMsg("verify_code", map[string]any{
			"success": true,
			"content": "Verification successful (go build ./...):\n",
		}),
	}

	mgr.Extract(history, 5000)

	if got := learning.GetPreference("convention:format_command"); got != "gofmt -w internal/ui" {
		t.Fatalf("format command = %q, want %q", got, "gofmt -w internal/ui")
	}
	if got := learning.GetPreference("fact:test_command"); got != "go test ./internal/ui -count=1" {
		t.Fatalf("test command = %q, want %q", got, "go test ./internal/ui -count=1")
	}
	if got := learning.GetPreference("fact:build_command"); got != "go build ./..." {
		t.Fatalf("build command = %q, want %q", got, "go build ./...")
	}

	markdown, err := os.ReadFile(learning.MarkdownPath())
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", learning.MarkdownPath(), err)
	}
	content := string(markdown)
	for _, needle := range []string{
		"## Facts",
		"## Conventions",
		"go test ./internal/ui -count=1",
		"gofmt -w internal/ui",
		"go build ./...",
	} {
		if !strings.Contains(content, needle) {
			t.Fatalf("project memory markdown missing %q:\n%s", needle, content)
		}
	}
}

func TestSessionMemory_Extract_SetsInitializedAndTokens(t *testing.T) {
	cfg := DefaultSessionMemoryConfig()
	cfg.MinTokensToInit = 100
	cfg.MinTokensBetweenUpdates = 5000
	mgr := NewSessionMemoryManager(t.TempDir(), cfg)

	if mgr.ShouldExtract(50) {
		t.Error("not initialized, below threshold")
	}
	mgr.Extract(minHistory(), 300)

	// After Extract, ShouldExtract should use delta logic.
	// 300 + 5000 = 5300 needed.
	if mgr.ShouldExtract(4000) {
		t.Error("delta 3700 < 5000, should be false")
	}
	if !mgr.ShouldExtract(5300) {
		t.Error("delta 5000 >= 5000, should be true")
	}
}

// ---------------------------------------------------------------------------
// TestSessionMemory_extractCurrentTask
// ---------------------------------------------------------------------------

func TestSessionMemory_extractCurrentTask_FindsLastSubstantiveMessage(t *testing.T) {
	history := []*genai.Content{
		userMsg("Please write a comprehensive unit test for the parser module"),
		modelMsg("On it."),
		userMsg("ok"),
		modelMsg("Done."),
	}
	task := extractCurrentTask(history)
	if !strings.Contains(task, "comprehensive unit test for the parser") {
		t.Errorf("expected first user message as task, got %q", task)
	}
}

func TestSessionMemory_extractCurrentTask_SkipsShortMessages(t *testing.T) {
	history := []*genai.Content{
		userMsg("yes"),
		modelMsg("ok"),
		userMsg("no"),
		modelMsg("alright"),
	}
	task := extractCurrentTask(history)
	if task != "" {
		t.Errorf("expected empty task for all short messages, got %q", task)
	}
}

func TestSessionMemory_extractCurrentTask_TruncatesLongMessages(t *testing.T) {
	long := strings.Repeat("x", 300)
	history := []*genai.Content{
		userMsg(long),
		modelMsg("ok"),
	}
	task := extractCurrentTask(history)
	if len(task) > 210 { // 200 + "..."
		t.Errorf("expected truncation to ~203 chars, got len %d", len(task))
	}
	if !strings.HasSuffix(task, "...") {
		t.Error("expected truncated task to end with '...'")
	}
}

func TestSessionMemory_extractCurrentTask_PicksLatestSubstantiveMessage(t *testing.T) {
	history := []*genai.Content{
		userMsg("First task: implement the login feature"),
		modelMsg("Working on login."),
		userMsg("Second task: now fix the logout bug too"),
		modelMsg("Fixing logout."),
		userMsg("ok"),
	}
	task := extractCurrentTask(history)
	if !strings.Contains(task, "fix the logout bug") {
		t.Errorf("expected latest substantive message, got %q", task)
	}
}

// ---------------------------------------------------------------------------
// TestSessionMemory_extractFileActivity
// ---------------------------------------------------------------------------

func TestSessionMemory_extractFileActivity_ClassifiesReadWriteEditGrep(t *testing.T) {
	history := []*genai.Content{
		funcCallMsg("read", map[string]any{"file_path": "/a.go"}),
		funcCallMsg("write", map[string]any{"file_path": "/b.go"}),
		funcCallMsg("edit", map[string]any{"file_path": "/c.go"}),
		funcCallMsg("grep", map[string]any{"path": "/d.go"}),
		funcCallMsg("glob", map[string]any{"path": "/e.go"}),
	}

	files := extractFileActivity(history)
	lookup := make(map[string]string)
	for _, f := range files {
		lookup[f.Path] = f.Action
	}

	tests := []struct {
		path   string
		action string
	}{
		{"/a.go", "read"},
		{"/b.go", "created"},
		{"/c.go", "edited"},
		{"/d.go", "searched"},
		{"/e.go", "searched"},
	}
	for _, tc := range tests {
		if got := lookup[tc.path]; got != tc.action {
			t.Errorf("file %s: action = %q, want %q", tc.path, got, tc.action)
		}
	}
}

func TestSessionMemory_extractFileActivity_WriteOverridesRead(t *testing.T) {
	history := []*genai.Content{
		funcCallMsg("read", map[string]any{"file_path": "/x.go"}),
		funcCallMsg("write", map[string]any{"file_path": "/x.go"}),
	}
	files := extractFileActivity(history)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Action != "created" {
		t.Errorf("write should override read, got %q", files[0].Action)
	}
}

func TestSessionMemory_extractFileActivity_EditOverridesRead(t *testing.T) {
	history := []*genai.Content{
		funcCallMsg("read", map[string]any{"file_path": "/x.go"}),
		funcCallMsg("edit", map[string]any{"file_path": "/x.go"}),
	}
	files := extractFileActivity(history)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Action != "edited" {
		t.Errorf("edit should override read, got %q", files[0].Action)
	}
}

func TestSessionMemory_extractFileActivity_SearchDoesNotOverrideRead(t *testing.T) {
	history := []*genai.Content{
		funcCallMsg("read", map[string]any{"file_path": "/x.go"}),
		funcCallMsg("grep", map[string]any{"path": "/x.go"}),
	}
	files := extractFileActivity(history)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Action != "read" {
		t.Errorf("grep should not override read, got %q", files[0].Action)
	}
}

func TestSessionMemory_extractFileActivity_LimitsTo40Files(t *testing.T) {
	var history []*genai.Content
	for i := 0; i < 50; i++ {
		path := "/file" + strings.Repeat("0", 3-len(itoa(i))) + itoa(i) + ".go"
		history = append(history, funcCallMsg("read", map[string]any{"file_path": path}))
	}
	files := extractFileActivity(history)
	if len(files) > 40 {
		t.Errorf("expected at most 40 files, got %d", len(files))
	}
}

// itoa is a minimal int-to-string helper to avoid importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func TestSessionMemory_extractFileActivity_SkipsEmptyPaths(t *testing.T) {
	history := []*genai.Content{
		funcCallMsg("read", map[string]any{}),
		funcCallMsg("edit", map[string]any{"file_path": ""}),
	}
	files := extractFileActivity(history)
	if len(files) != 0 {
		t.Errorf("expected 0 files when no paths given, got %d", len(files))
	}
}

func TestSessionMemory_extractFileActivity_FallsBackToPathArg(t *testing.T) {
	history := []*genai.Content{
		funcCallMsg("grep", map[string]any{"path": "/search/dir"}),
	}
	files := extractFileActivity(history)
	if len(files) != 1 || files[0].Path != "/search/dir" {
		t.Errorf("expected fallback to 'path' arg, got %v", files)
	}
}

func TestSessionMemory_extractFileActivity_SortedByPath(t *testing.T) {
	history := []*genai.Content{
		funcCallMsg("read", map[string]any{"file_path": "/z.go"}),
		funcCallMsg("read", map[string]any{"file_path": "/a.go"}),
		funcCallMsg("read", map[string]any{"file_path": "/m.go"}),
	}
	files := extractFileActivity(history)
	for i := 1; i < len(files); i++ {
		if files[i-1].Path > files[i].Path {
			t.Errorf("files not sorted: %s > %s", files[i-1].Path, files[i].Path)
		}
	}
}

// ---------------------------------------------------------------------------
// TestSessionMemory_extractToolUsage
// ---------------------------------------------------------------------------

func TestSessionMemory_extractToolUsage_CountsTools(t *testing.T) {
	history := []*genai.Content{
		funcCallMsg("read", map[string]any{"file_path": "/a.go"}),
		funcCallMsg("read", map[string]any{"file_path": "/b.go"}),
		funcCallMsg("write", map[string]any{"file_path": "/c.go"}),
		funcCallMsg("edit", map[string]any{"file_path": "/d.go"}),
		funcCallMsg("edit", map[string]any{"file_path": "/e.go"}),
		funcCallMsg("edit", map[string]any{"file_path": "/f.go"}),
	}
	counts := extractToolUsage(history)

	if counts["read"] != 2 {
		t.Errorf("read count = %d, want 2", counts["read"])
	}
	if counts["write"] != 1 {
		t.Errorf("write count = %d, want 1", counts["write"])
	}
	if counts["edit"] != 3 {
		t.Errorf("edit count = %d, want 3", counts["edit"])
	}
}

func TestSessionMemory_extractToolUsage_IgnoresNonFunctionParts(t *testing.T) {
	history := []*genai.Content{
		userMsg("hello world, this is a long user message"),
		modelMsg("here is my response to the user query"),
	}
	counts := extractToolUsage(history)
	if len(counts) != 0 {
		t.Errorf("expected no tool counts for plain text, got %v", counts)
	}
}

func TestSessionMemory_extractToolUsage_EmptyHistory(t *testing.T) {
	counts := extractToolUsage(nil)
	if len(counts) != 0 {
		t.Errorf("expected empty map, got %v", counts)
	}
}

// ---------------------------------------------------------------------------
// TestSessionMemory_extractErrors
// ---------------------------------------------------------------------------

func TestSessionMemory_extractErrors_FindsErrorPatterns(t *testing.T) {
	history := []*genai.Content{
		funcRespMsg("bash", map[string]any{
			"content": "error: undefined variable x",
		}),
		funcRespMsg("bash", map[string]any{
			"content": "Build succeeded",
		}),
		funcRespMsg("bash", map[string]any{
			"error": "permission denied: /etc/shadow",
		}),
	}
	errors := extractErrors(history)

	found := strings.Join(errors, " | ")
	if !strings.Contains(found, "error: undefined variable x") {
		t.Errorf("expected 'error: undefined variable x', got %v", errors)
	}
	if !strings.Contains(found, "permission denied") {
		t.Errorf("expected 'permission denied', got %v", errors)
	}
	if len(errors) != 2 {
		t.Errorf("expected 2 errors, got %d: %v", len(errors), errors)
	}
}

func TestSessionMemory_extractErrors_DeduplicatesErrors(t *testing.T) {
	history := []*genai.Content{
		funcRespMsg("bash", map[string]any{"content": "error: foo"}),
		funcRespMsg("bash", map[string]any{"content": "error: foo"}),
		funcRespMsg("bash", map[string]any{"content": "error: foo"}),
	}
	errors := extractErrors(history)
	if len(errors) != 1 {
		t.Errorf("expected 1 deduplicated error, got %d", len(errors))
	}
}

func TestSessionMemory_extractErrors_LimitsTo20(t *testing.T) {
	var history []*genai.Content
	for i := 0; i < 30; i++ {
		history = append(history, funcRespMsg("bash", map[string]any{
			"content": "error: unique failure " + itoa(i),
		}))
	}
	errors := extractErrors(history)
	if len(errors) > 20 {
		t.Errorf("expected at most 20 errors, got %d", len(errors))
	}
}

func TestSessionMemory_extractErrors_VariousPatterns(t *testing.T) {
	tests := []struct {
		name    string
		content string
		errKey  string
		want    string
	}{
		{"error colon", "error: something broke", "", "error: something broke"},
		{"Error colon", "Error: capital form", "", "Error: capital form"},
		{"failed colon", "failed: to compile module", "", "failed: to compile module"},
		{"panic colon", "panic: runtime error", "", "panic: runtime error"},
		{"no such file", "open /foo: no such file or directory", "", "no such file or directory"},
		{"compilation failed", "compilation failed for main.go", "", "compilation failed"},
		{"test failed", "test failed in TestAuth", "", "test failed"},
		{"syntax error", "syntax error at line 42", "", "syntax error"},
		{"error in error key", "", "Error: from error field", "Error: from error field"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := map[string]any{}
			if tc.content != "" {
				resp["content"] = tc.content
			}
			if tc.errKey != "" {
				resp["error"] = tc.errKey
			}
			history := []*genai.Content{funcRespMsg("bash", resp)}
			errors := extractErrors(history)
			if len(errors) == 0 {
				t.Fatalf("expected at least one error for pattern %q", tc.name)
			}
			if !strings.Contains(errors[0], tc.want) {
				t.Errorf("expected error containing %q, got %q", tc.want, errors[0])
			}
		})
	}
}

func TestSessionMemory_extractErrors_NoErrorsInCleanOutput(t *testing.T) {
	history := []*genai.Content{
		funcRespMsg("bash", map[string]any{"content": "PASS\nok  gokin/internal/context 0.5s"}),
		funcRespMsg("read", map[string]any{"content": "package main\nfunc main() {}"}),
	}
	errors := extractErrors(history)
	if len(errors) != 0 {
		t.Errorf("expected no errors in clean output, got %v", errors)
	}
}

func TestSessionMemory_extractErrors_SkipsNilResponse(t *testing.T) {
	history := []*genai.Content{
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{FunctionResponse: &genai.FunctionResponse{Name: "bash", Response: nil}},
			},
		},
	}
	errors := extractErrors(history)
	if len(errors) != 0 {
		t.Errorf("expected no errors with nil response, got %v", errors)
	}
}

func TestSessionMemory_extractErrors_TruncatesLongLines(t *testing.T) {
	longErr := "error: " + strings.Repeat("a", 300)
	history := []*genai.Content{
		funcRespMsg("bash", map[string]any{"content": longErr}),
	}
	errors := extractErrors(history)
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errors))
	}
	if len(errors[0]) > 160 { // 150 + "..."
		t.Errorf("expected error line truncated to ~153 chars, got len %d", len(errors[0]))
	}
	if !strings.HasSuffix(errors[0], "...") {
		t.Error("expected truncated error line to end with '...'")
	}
}

// ---------------------------------------------------------------------------
// TestSessionMemory_GetContent_Clear
// ---------------------------------------------------------------------------

func TestSessionMemory_GetContent_EmptyByDefault(t *testing.T) {
	mgr := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	if mgr.GetContent() != "" {
		t.Error("expected empty content by default")
	}
}

func TestSessionMemory_GetContent_AfterExtract(t *testing.T) {
	mgr := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	mgr.Extract(minHistory(), 5000)
	content := mgr.GetContent()
	if content == "" {
		t.Error("expected non-empty content after Extract")
	}
	if !strings.Contains(content, "# Session Memory") {
		t.Error("expected Session Memory header")
	}
}

func TestSessionMemory_Clear_ResetsEverything(t *testing.T) {
	cfg := DefaultSessionMemoryConfig()
	cfg.MinTokensToInit = 100
	cfg.MinTokensBetweenUpdates = 5000
	mgr := NewSessionMemoryManager(t.TempDir(), cfg)

	// Extract to set state.
	mgr.Extract(minHistory(), 500)
	mgr.RecordToolCall()

	if mgr.GetContent() == "" {
		t.Fatal("expected non-empty content before Clear")
	}

	mgr.Clear()

	if mgr.GetContent() != "" {
		t.Error("expected empty content after Clear")
	}

	// After Clear, should behave like a fresh manager: uses MinTokensToInit again.
	if mgr.ShouldExtract(50) {
		t.Error("after Clear, ShouldExtract should use MinTokensToInit threshold again")
	}
	if !mgr.ShouldExtract(100) {
		t.Error("after Clear, ShouldExtract should trigger at MinTokensToInit")
	}
}

func TestSessionMemory_Clear_WritesToDisk(t *testing.T) {
	dir := t.TempDir()
	mgr := NewSessionMemoryManager(dir, DefaultSessionMemoryConfig())

	// Extract writes to disk.
	mgr.Extract(minHistory(), 5000)

	// Clear should remove the file; LoadFromDisk should find nothing.
	mgr.Clear()

	mgr2 := NewSessionMemoryManager(dir, DefaultSessionMemoryConfig())
	mgr2.LoadFromDisk()
	if mgr2.GetContent() != "" {
		t.Error("expected no content after Clear removed the file")
	}
}

// ---------------------------------------------------------------------------
// TestSessionMemory_LoadFromDisk
// ---------------------------------------------------------------------------

func TestSessionMemory_LoadFromDisk_RestoresContent(t *testing.T) {
	dir := t.TempDir()
	mgr := NewSessionMemoryManager(dir, DefaultSessionMemoryConfig())
	mgr.Extract(minHistory(), 5000)
	saved := mgr.GetContent()

	mgr2 := NewSessionMemoryManager(dir, DefaultSessionMemoryConfig())
	mgr2.LoadFromDisk()
	if mgr2.GetContent() != saved {
		t.Errorf("LoadFromDisk content mismatch:\ngot:  %q\nwant: %q", mgr2.GetContent(), saved)
	}
}

func TestSessionMemory_LoadFromDisk_NoFileIsNoop(t *testing.T) {
	mgr := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	mgr.LoadFromDisk() // should not panic
	if mgr.GetContent() != "" {
		t.Error("expected empty content when no file exists")
	}
}

// ---------------------------------------------------------------------------
// TestSessionMemory_DefaultConfig
// ---------------------------------------------------------------------------

func TestSessionMemory_DefaultConfig(t *testing.T) {
	cfg := DefaultSessionMemoryConfig()
	if !cfg.Enabled {
		t.Error("default should be enabled")
	}
	if cfg.MinTokensToInit != 10000 {
		t.Errorf("MinTokensToInit = %d, want 10000", cfg.MinTokensToInit)
	}
	if cfg.MinTokensBetweenUpdates != 5000 {
		t.Errorf("MinTokensBetweenUpdates = %d, want 5000", cfg.MinTokensBetweenUpdates)
	}
	if cfg.ToolCallsBetweenUpdates != 3 {
		t.Errorf("ToolCallsBetweenUpdates = %d, want 3", cfg.ToolCallsBetweenUpdates)
	}
}
