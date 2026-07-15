package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/memory"
	"gokin/internal/testkit"
)

func TestRelevantMemoryTravelsWithCurrentTurn(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.NewStore(t.TempDir(), dir, 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Add(memory.NewEntry(
		"Authentication tests require ./scripts/start-issuer.sh",
		memory.MemoryProject,
	).WithKey("auth-test-issuer")); err != nil {
		t.Fatalf("Add relevant memory: %v", err)
	}
	if err := store.Add(memory.NewEntry(
		"The dashboard accent color is violet",
		memory.MemoryProject,
	).WithKey("dashboard-color")); err != nil {
		t.Fatalf("Add unrelated memory: %v", err)
	}

	mock := testkit.NewMockClient()
	app := &App{workDir: dir, memoryStore: store, client: mock}
	app.memoryAutoInject.Store(true)
	app.updateRelevantMemoryForTurn("fix the authentication integration tests")
	app.pushTurnContext()

	turnContext := mock.TurnContext()
	if !strings.Contains(turnContext, "start-issuer.sh") {
		t.Fatalf("current-query memory missing from turn context:\n%s", turnContext)
	}
	if strings.Contains(turnContext, "violet") {
		t.Fatalf("unrelated memory leaked into turn context:\n%s", turnContext)
	}
}

func TestRelevantMemoryClearsWhenQueryOrAutoInjectChanges(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.NewStore(t.TempDir(), dir, 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Add(memory.NewEntry(
		"Authentication uses the local issuer",
		memory.MemoryProject,
	).WithKey("auth-issuer")); err != nil {
		t.Fatal(err)
	}

	mock := testkit.NewMockClient()
	app := &App{workDir: dir, memoryStore: store, client: mock}
	app.memoryAutoInject.Store(true)
	app.updateRelevantMemoryForTurn("authentication issuer")
	app.pushTurnContext()
	if !strings.Contains(mock.TurnContext(), "local issuer") {
		t.Fatal("test setup did not inject relevant memory")
	}

	app.updateRelevantMemoryForTurn("please continue")
	app.pushTurnContext()
	if strings.Contains(mock.TurnContext(), "local issuer") {
		t.Fatalf("previous query memory survived an unrelated turn:\n%s", mock.TurnContext())
	}

	app.updateRelevantMemoryForTurn("authentication issuer")
	app.memoryAutoInject.Store(false)
	app.pushTurnContext()
	if strings.Contains(mock.TurnContext(), "local issuer") {
		t.Fatalf("disabled auto-inject still exposed retrieved memory:\n%s", mock.TurnContext())
	}
}

func TestRelevantMemoryDoesNotChangeCachedSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.NewStore(t.TempDir(), dir, 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Add(memory.NewEntry("Auth tests need Redis", memory.MemoryProject).WithKey("auth-tests")); err != nil {
		t.Fatal(err)
	}

	mock := testkit.NewMockClient()
	app := &App{workDir: dir, memoryStore: store, client: mock}
	app.memoryAutoInject.Store(true)
	app.updateRelevantMemoryForTurn("auth tests")
	first := app.relevantMemorySnapshot()
	app.updateRelevantMemoryForTurn("render dashboard")
	second := app.relevantMemorySnapshot()
	if first == "" || second != "" {
		t.Fatalf("query-aware snapshots not updated as expected: first=%q second=%q", first, second)
	}
	// The implementation has no PromptBuilder dependency: retrieval is delivered
	// exclusively through SetTurnContext, preserving GLM's cached system prefix.
	app.pushTurnContext()
	if strings.Contains(mock.SystemInstruction(), "Auth tests need Redis") {
		t.Fatal("query-aware memory leaked into the system instruction")
	}
}

func TestClearConversationRemovesAllSessionMemoryBeforePromptRebuild(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.NewStore(t.TempDir(), dir, 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Add(memory.NewEntry("old task uses port 8123", memory.MemorySession).WithKey("old-task")); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(memory.NewEntry("project tests use go test ./...", memory.MemoryProject).WithKey("project-tests")); err != nil {
		t.Fatal(err)
	}

	sessionDir := filepath.Join(dir, ".gokin")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionMemoryPath := filepath.Join(sessionDir, ".session-memory.md")
	if err := os.WriteFile(sessionMemoryPath, []byte("## Session Memory\nOld task: repair port 8123"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionMemory := appcontext.NewSessionMemoryManager(dir, appcontext.DefaultSessionMemoryConfig())
	sessionMemory.LoadFromDisk()

	promptBuilder := appcontext.NewPromptBuilder(dir, &appcontext.ProjectInfo{})
	promptBuilder.SetMemoryStore(store)
	before := promptBuilder.Build()
	if !strings.Contains(before, "old task uses port 8123") {
		t.Fatalf("test setup: session keyed memory missing from prompt:\n%s", before)
	}

	mock := testkit.NewMockClient()
	app := &App{
		config:              config.DefaultConfig(),
		workDir:             dir,
		client:              mock,
		session:             chat.NewSession(),
		promptBuilder:       promptBuilder,
		sessionMemory:       sessionMemory,
		memoryStore:         store,
		rateLimitRetryCount: make(map[string]int),
		autoResumeCount:     make(map[string]int),
	}
	app.memoryAutoInject.Store(true)
	app.setRelevantMemoryContext("## Relevant Memory for This Turn\nold task uses port 8123")
	app.ClearConversation()

	if got := sessionMemory.GetContent(); got != "" {
		t.Fatalf("automatic session memory survived /clear: %q", got)
	}
	if _, err := os.Stat(sessionMemoryPath); !os.IsNotExist(err) {
		t.Fatalf("session memory file survived /clear: err=%v", err)
	}
	if _, ok := store.Get("old-task"); ok {
		t.Fatal("keyed scope=session memory survived /clear")
	}
	if _, ok := store.Get("project-tests"); !ok {
		t.Fatal("durable project memory was removed by /clear")
	}
	if got := mock.SystemInstruction(); strings.Contains(got, "port 8123") || !strings.Contains(got, "go test ./...") {
		t.Fatalf("rebuilt prompt has wrong memory boundary:\n%s", got)
	}
	if got := mock.TurnContext(); strings.Contains(got, "port 8123") || strings.Contains(got, "Session Memory") {
		t.Fatalf("cleared session state remained in turn context:\n%s", got)
	}
}
