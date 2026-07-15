package app

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/memory"
	"gokin/internal/tools"
)

func TestRelevantGlobalMemoryRequiresExplicitOptIn(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.NewStore(t.TempDir(), dir, 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Add(memory.NewEntry(
		"GLOBAL-AUTH-SENTINEL uses the shared hardware token",
		memory.MemoryGlobal,
	).WithKey("global-auth-device")); err != nil {
		t.Fatal(err)
	}

	app := &App{workDir: dir, memoryStore: store}
	app.memoryAutoInject.Store(true)
	app.updateRelevantMemoryForTurn("global auth device hardware token")
	if got := app.relevantMemorySnapshot(); strings.Contains(got, "GLOBAL-AUTH-SENTINEL") {
		t.Fatalf("default per-turn retrieval leaked global memory:\n%s", got)
	}

	app.memoryAllowGlobal.Store(true)
	app.updateRelevantMemoryForTurn("global auth device hardware token")
	if got := app.relevantMemorySnapshot(); !strings.Contains(got, "GLOBAL-AUTH-SENTINEL") {
		t.Fatalf("opted-in per-turn retrieval omitted global memory:\n%s", got)
	}
}

func TestApplyConfigUpdatesLiveGlobalMemoryPolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	workDir := t.TempDir()
	store, err := memory.NewStore(t.TempDir(), workDir, 100)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.DefaultRegistry(workDir)
	registered, ok := registry.Get("memory")
	if !ok {
		t.Fatal("memory tool missing")
	}
	memoryTool := registered.(*tools.MemoryTool)
	memoryTool.SetStore(store)
	// Clone before either config transition: it must follow the shared live
	// policy rather than retaining a stale boot-time value.
	agentClone := tools.CloneToolForWorkDir(memoryTool, "").(*tools.MemoryTool)

	cfg := config.DefaultConfig()
	cfg.API.GLMKey = "test-key-that-is-long-enough-1234567890"
	cfg.API.ActiveProvider = "glm"
	cfg.API.Backend = "glm"
	cfg.Model.Provider = "glm"
	cfg.Model.Name = "glm-5.2"
	cfg.Memory.AllowGlobal = true
	promptBuilder := appcontext.NewPromptBuilder(workDir, &appcontext.ProjectInfo{})
	app := &App{
		config:        config.DefaultConfig(),
		ctx:           context.Background(),
		registry:      registry,
		memoryStore:   store,
		promptBuilder: promptBuilder,
	}

	if err := app.ApplyConfig(cfg); err != nil {
		t.Fatalf("enable global memory: %v", err)
	}
	if !app.memoryAllowGlobal.Load() {
		t.Fatal("App did not publish enabled global-memory policy")
	}
	result, err := agentClone.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"scope":   "global",
		"key":     "live-global-enable",
		"content": "global access enabled live",
	})
	if err != nil || !result.Success {
		t.Fatalf("pre-existing clone did not receive enable: result=%+v err=%v", result, err)
	}

	revoked := cfg.Clone()
	revoked.Memory.AllowGlobal = false
	if err := app.ApplyConfig(revoked); err != nil {
		t.Fatalf("revoke global memory: %v", err)
	}
	if app.memoryAllowGlobal.Load() {
		t.Fatal("App retained global-memory access after revocation")
	}
	result, err = agentClone.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"scope":   "global",
		"key":     "live-global-revoked",
		"content": "must be blocked",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.PolicyBlock == nil || result.PolicyBlock.Kind != tools.PolicyBlockPermission {
		t.Fatalf("pre-existing clone retained access after revocation: %+v", result)
	}
}
