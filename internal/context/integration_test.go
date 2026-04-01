package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// TestSmoke_SessionMemoryLifecycle verifies the full session memory lifecycle:
// creation, extraction, disk persistence, reload, and cleanup.
func TestSmoke_SessionMemoryLifecycle(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultSessionMemoryConfig()
	cfg.MinTokensToInit = 100 // Low threshold for test

	sm := NewSessionMemoryManager(dir, cfg)

	// Initially empty
	if sm.GetContent() != "" {
		t.Error("expected empty content initially")
	}
	if sm.ShouldExtract(50) {
		t.Error("should not extract before MinTokensToInit")
	}
	if !sm.ShouldExtract(200) {
		t.Error("should extract after MinTokensToInit")
	}

	// Build test history
	history := []*genai.Content{
		genai.NewContentFromText("system init", genai.RoleUser),
		genai.NewContentFromText("understood", genai.RoleModel),
		genai.NewContentFromText("Read the file internal/app/app.go and explain the architecture", genai.RoleUser),
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": "/app/app.go"}}},
		}},
		{Role: genai.RoleUser, Parts: []*genai.Part{
			genai.NewPartFromFunctionResponse("read", map[string]any{"content": "package app\n\ntype App struct{}", "success": true}),
		}},
		genai.NewContentFromText("The app uses a builder pattern...", genai.RoleModel),
	}

	// Extract
	sm.Extract(history, 200)

	content := sm.GetContent()
	if content == "" {
		t.Fatal("expected non-empty content after extraction")
	}
	if !strings.Contains(content, "# Session Memory") {
		t.Error("missing Session Memory header")
	}
	if !strings.Contains(content, "/app/app.go") {
		t.Error("missing file reference")
	}

	// Verify disk persistence
	memFile := filepath.Join(dir, ".gokin", ".session-memory.md")
	data, err := os.ReadFile(memFile)
	if err != nil {
		t.Fatalf("session memory not written to disk: %v", err)
	}
	if string(data) != content {
		t.Error("disk content doesn't match in-memory content")
	}

	// Reload from disk
	sm2 := NewSessionMemoryManager(dir, cfg)
	sm2.LoadFromDisk()
	if sm2.GetContent() != content {
		t.Error("reloaded content doesn't match original")
	}

	// Clear
	sm.Clear()
	if sm.GetContent() != "" {
		t.Error("content should be empty after Clear")
	}
	if _, err := os.Stat(memFile); !os.IsNotExist(err) {
		t.Error("disk file should be removed after Clear")
	}
}

// TestSmoke_GitignoreIntegration verifies gitignore auto-creation in a simulated project.
func TestSmoke_GitignoreIntegration(t *testing.T) {
	dir := t.TempDir()

	// Create .git directory to simulate git repo
	os.Mkdir(filepath.Join(dir, ".git"), 0755)

	EnsureGokinGitignore(dir)

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("gitignore not created: %v", err)
	}

	required := []string{
		".gokin/.session-memory.md",
		".gokin/task-output/",
		"GOKIN.local.md",
	}
	for _, entry := range required {
		if !strings.Contains(string(content), entry) {
			t.Errorf("missing gitignore entry: %s", entry)
		}
	}

	// Idempotency
	EnsureGokinGitignore(dir)
	content2, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(content) != string(content2) {
		t.Error("gitignore should be idempotent")
	}
}

// TestSmoke_MultiLayerInstructions verifies that instructions load from multiple layers.
func TestSmoke_MultiLayerInstructions(t *testing.T) {
	dir := t.TempDir()

	// Create project-level instruction
	os.WriteFile(filepath.Join(dir, "GOKIN.md"), []byte("# Project Rules\nUse gofmt."), 0644)

	// Create local override
	os.WriteFile(filepath.Join(dir, "GOKIN.local.md"), []byte("# Local Override\nDebug mode on."), 0644)

	pm := NewProjectMemory(dir)
	if err := pm.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	instr := pm.GetInstructions()
	if !strings.Contains(instr, "Project Rules") {
		t.Error("missing project layer")
	}
	if !strings.Contains(instr, "Local Override") {
		t.Error("missing local layer")
	}
}

// TestSmoke_PromptBuilderSmartInjection verifies that questions get lighter prompts.
func TestSmoke_PromptBuilderSmartInjection(t *testing.T) {
	pb := NewPromptBuilder("/tmp/test", &ProjectInfo{Type: ProjectTypeGo, Name: "test"})
	pb.SetPlanAutoDetect(true)
	pb.SetDetectedContext("framework: gin, test: go test")
	pb.SetToolHints("prefer grep over bash grep")

	// Question-only prompt should skip planning, detected context, tool hints
	pb.SetLastMessage("What does this function do?")
	questionPrompt := pb.Build()

	pb.SetLastMessage("Refactor the auth module to use JWT")
	actionPrompt := pb.Build()

	if len(questionPrompt) >= len(actionPrompt) {
		t.Errorf("question prompt (%d chars) should be shorter than action prompt (%d chars)",
			len(questionPrompt), len(actionPrompt))
	}

	if strings.Contains(questionPrompt, "AUTOMATIC PLANNING PROTOCOL") {
		t.Error("question prompt should not contain planning protocol")
	}
	if !strings.Contains(actionPrompt, "AUTOMATIC PLANNING PROTOCOL") {
		t.Error("action prompt should contain planning protocol")
	}

	if strings.Contains(questionPrompt, "Tool Usage Hints") {
		t.Error("question prompt should not contain tool hints")
	}
}

// TestSmoke_SessionMemoryToolCallThreshold verifies tool call counting triggers extraction.
func TestSmoke_SessionMemoryToolCallThreshold(t *testing.T) {
	dir := t.TempDir()
	cfg := SessionMemoryConfig{
		Enabled:                 true,
		MinTokensToInit:         100,
		MinTokensBetweenUpdates: 99999, // High token threshold
		ToolCallsBetweenUpdates: 2,     // Low tool call threshold
	}

	sm := NewSessionMemoryManager(dir, cfg)

	// Force initialization
	history := []*genai.Content{
		genai.NewContentFromText("init", genai.RoleUser),
		genai.NewContentFromText("ok", genai.RoleModel),
		genai.NewContentFromText("do something long enough to pass", genai.RoleUser),
		genai.NewContentFromText("done with the task", genai.RoleModel),
	}
	sm.Extract(history, 200)

	// After extraction, should not extract again (token delta too small)
	if sm.ShouldExtract(200) {
		t.Error("should not extract immediately after extraction")
	}

	// Record tool calls
	sm.RecordToolCall()
	if sm.ShouldExtract(200) {
		t.Error("1 tool call < threshold of 2")
	}

	sm.RecordToolCall()
	if !sm.ShouldExtract(200) {
		t.Error("2 tool calls should trigger extraction")
	}
}
