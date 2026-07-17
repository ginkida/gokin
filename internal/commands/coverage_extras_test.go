package commands

import (
	"context"
	"os"
	"testing"

	"gokin/internal/config"
)

// --- Aliases() (0% → 100%) ---

func TestAliases_NilHandler(t *testing.T) {
	var h *Handler
	if got := h.Aliases(); got != nil {
		t.Errorf("nil handler Aliases() = %v, want nil", got)
	}
}

func TestAliases_ReturnsCopy(t *testing.T) {
	h := NewHandler()
	aliases := h.Aliases()
	if aliases == nil {
		t.Fatal("expected non-nil aliases from default handler")
	}
	if len(aliases) == 0 {
		t.Fatal("expected default aliases to be non-empty")
	}
	// Verify it's a copy — mutating the returned map shouldn't affect the handler
	original := aliases["p"]
	aliases["p"] = "mutated"
	if h.ResolveAlias("p") == "mutated" {
		t.Fatal("Aliases() should return a copy, not the internal map")
	}
	_ = original
}

// --- PwdCommand.Execute (0% → 100%) ---

func TestPwdCommand_Execute(t *testing.T) {
	app := &fakeAppForMCP{workDir: "/test/workdir"}
	out, err := (&PwdCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("PwdCommand.Execute: %v", err)
	}
	if out != "/test/workdir" {
		t.Errorf("output = %q, want /test/workdir", out)
	}
}

// --- ConfigCommand.Execute (0% → 100%) ---

func TestConfigCommand_Execute_NilConfig(t *testing.T) {
	app := &fakeAppForMCP{cfg: nil}
	out, err := (&ConfigCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("ConfigCommand.Execute: %v", err)
	}
	if out != "Configuration not available." {
		t.Errorf("output = %q, want 'Configuration not available.'", out)
	}
}

func TestConfigCommand_Execute_WithConfig(t *testing.T) {
	app := &fakeAppForMCP{cfg: &config.Config{}}
	app.cfg.Model.Provider = "glm"
	out, err := (&ConfigCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("ConfigCommand.Execute: %v", err)
	}
	if out == "Configuration not available." {
		t.Error("expected actual config output, not 'not available'")
	}
}

// --- ClearTodosCommand.Execute (0% → 100%) ---

func TestClearTodosCommand_Execute_NoTodoTool(t *testing.T) {
	app := &fakeAppForMCP{}
	out, err := (&ClearTodosCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("ClearTodosCommand.Execute: %v", err)
	}
	if out != "Todo tool not available." {
		t.Errorf("output = %q, want 'Todo tool not available.'", out)
	}
}

// --- SessionsCommand.Execute (7.3% → higher) ---

func TestSessionsCommand_Execute_NoHistoryManager(t *testing.T) {
	app := &fakeAppForMCP{}
	out, err := (&SessionsCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("SessionsCommand.Execute: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output")
	}
}

// --- PermissionsCommand.Execute (41.2% → higher) ---

func TestPermissionsCommand_Execute_NoConfig(t *testing.T) {
	app := &fakeAppForMCP{cfg: nil}
	out, err := (&PermissionsCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("PermissionsCommand.Execute: %v", err)
	}
	if out != "Config not available" {
		t.Errorf("output = %q, want 'Config not available'", out)
	}
}

func TestPermissionsCommand_Execute_StatusOn(t *testing.T) {
	app := &fakeAppForMCP{cfg: &config.Config{}}
	app.cfg.Permission.Enabled = true
	out, err := (&PermissionsCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("PermissionsCommand.Execute: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty status output")
	}
}

func TestPermissionsCommand_Execute_StatusOff(t *testing.T) {
	app := &fakeAppForMCP{cfg: &config.Config{}}
	app.cfg.Permission.Enabled = false
	out, err := (&PermissionsCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("PermissionsCommand.Execute: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty status output")
	}
}

func TestPermissionsCommand_Execute_TurnOn(t *testing.T) {
	app := &fakeAppForMCP{cfg: &config.Config{}}
	app.cfg.Permission.Enabled = false
	out, err := (&PermissionsCommand{}).Execute(context.Background(), []string{"on"}, app)
	if err != nil {
		t.Fatalf("PermissionsCommand.Execute: %v", err)
	}
	if !app.cfg.Permission.Enabled {
		t.Error("expected Permission.Enabled = true after 'on'")
	}
	_ = out
}

func TestPermissionsCommand_Execute_TurnOff(t *testing.T) {
	app := &fakeAppForMCP{cfg: &config.Config{}}
	app.cfg.Permission.Enabled = true
	out, err := (&PermissionsCommand{}).Execute(context.Background(), []string{"off"}, app)
	if err != nil {
		t.Fatalf("PermissionsCommand.Execute: %v", err)
	}
	if app.cfg.Permission.Enabled {
		t.Error("expected Permission.Enabled = false after 'off'")
	}
	_ = out
}

func TestPermissionsCommand_Execute_InvalidArg(t *testing.T) {
	app := &fakeAppForMCP{cfg: &config.Config{}}
	out, err := (&PermissionsCommand{}).Execute(context.Background(), []string{"bogus"}, app)
	if err != nil {
		t.Fatalf("PermissionsCommand.Execute: %v", err)
	}
	if out != "/permissions on | off" {
		t.Errorf("output = %q, want '/permissions on | off'", out)
	}
}

// --- SandboxCommand.Execute (41.2% → higher) ---

func TestSandboxCommand_Execute_NoConfig(t *testing.T) {
	app := &fakeAppForMCP{cfg: nil}
	out, err := (&SandboxCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("SandboxCommand.Execute: %v", err)
	}
	if out != "Config not available" {
		t.Errorf("output = %q, want 'Config not available'", out)
	}
}

func TestSandboxCommand_Execute_Status(t *testing.T) {
	app := &fakeAppForMCP{cfg: &config.Config{}}
	out, err := (&SandboxCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("SandboxCommand.Execute: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty status output")
	}
}

func TestSandboxCommand_Execute_TurnOn(t *testing.T) {
	app := &fakeAppForMCP{cfg: &config.Config{}}
	out, err := (&SandboxCommand{}).Execute(context.Background(), []string{"on"}, app)
	if err != nil {
		t.Fatalf("SandboxCommand.Execute: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output")
	}
}

func TestSandboxCommand_Execute_TurnOff(t *testing.T) {
	app := &fakeAppForMCP{cfg: &config.Config{}}
	out, err := (&SandboxCommand{}).Execute(context.Background(), []string{"off"}, app)
	if err != nil {
		t.Fatalf("SandboxCommand.Execute: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output")
	}
}

func TestSandboxCommand_Execute_InvalidArg(t *testing.T) {
	app := &fakeAppForMCP{cfg: &config.Config{}}
	out, err := (&SandboxCommand{}).Execute(context.Background(), []string{"bogus"}, app)
	if err != nil {
		t.Fatalf("SandboxCommand.Execute: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty usage output for invalid arg")
	}
}

// --- BrowseCommand.Execute (0% → higher) ---

func TestBrowseCommand_Execute(t *testing.T) {
	app := &fakeAppForMCP{workDir: "/test/dir"}
	out, err := (&BrowseCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("BrowseCommand.Execute: %v", err)
	}
	// BrowseCommand may return an error message about no UI, but shouldn't panic
	_ = out
}

// --- parseOnlyFlag (50% → 100%) ---

func TestParseOnlyFlag_WithFlag(t *testing.T) {
	got, msg := parseOnlyFlag([]string{"--all"}, "--all", "/test [flags]")
	if msg != "" {
		t.Errorf("expected empty msg, got %q", msg)
	}
	if !got {
		t.Error("expected true for --all flag present")
	}
}

func TestParseOnlyFlag_WithoutFlag(t *testing.T) {
	got, msg := parseOnlyFlag(nil, "--all", "/test [flags]")
	if msg != "" {
		t.Errorf("expected empty msg, got %q", msg)
	}
	if got {
		t.Error("expected false for no args")
	}
}

func TestParseOnlyFlag_UnknownArg(t *testing.T) {
	got, msg := parseOnlyFlag([]string{"--bogus"}, "--all", "/test [flags]")
	if msg == "" {
		t.Error("expected error message for unknown flag")
	}
	if got {
		t.Error("expected false when unknown flag present")
	}
}

// --- ResolveAlias nil handler (80% → 100%) ---

func TestResolveAlias_NilHandler(t *testing.T) {
	var h *Handler
	if got := h.ResolveAlias("p"); got != "p" {
		t.Errorf("nil handler ResolveAlias = %q, want 'p'", got)
	}
}

func TestResolveAlias_NilAliasesMap(t *testing.T) {
	h := &Handler{aliases: nil}
	if got := h.ResolveAlias("p"); got != "p" {
		t.Errorf("nil aliases ResolveAlias = %q, want 'p'", got)
	}
}

// --- LoadAliasesFromFile malformed entries (81% → higher) ---

func TestLoadAliasesFromFile_MalformedEntries(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/aliases.yaml"
	// Mix of valid + malformed entries
	content := "p: plan\n" +
		"bad key: cmd\n" + // key with space → skipped
		"empty: \n" + // empty value → skipped
		"slash/key: cmd\n" + // key with slash → skipped
		"good: undo\n" // valid
	if err := writeTestFile(path, content); err != nil {
		t.Fatal(err)
	}
	h := NewHandler()
	if err := h.LoadAliasesFromFile(path); err != nil {
		t.Fatalf("LoadAliasesFromFile: %v", err)
	}
	// Valid entries should be present
	if got := h.ResolveAlias("good"); got != "undo" {
		t.Errorf("ResolveAlias(good) = %q, want undo", got)
	}
	// Malformed entries should NOT be present
	if got := h.ResolveAlias("bad key"); got != "bad key" {
		t.Errorf("malformed key with space should not resolve, got %q", got)
	}
}

func TestLoadAliasesFromFile_BadYAML(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/aliases.yaml"
	content := "{{invalid yaml"
	if err := writeTestFile(path, content); err != nil {
		t.Fatal(err)
	}
	h := NewHandler()
	if err := h.LoadAliasesFromFile(path); err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

// --- helper ---

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}
