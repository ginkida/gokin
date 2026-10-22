package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/commands"
	"gokin/internal/config"
	"gokin/internal/hooks"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestLSPFixtureHelper(t *testing.T) { testkit.RunLSPFixture(t) }

func TestPluginLSPRuntimeReloadAndForegroundDiagnostics(t *testing.T) {
	work, plugin, user := t.TempDir(), t.TempDir(), t.TempDir()
	work, _ = filepath.EvalSymlinks(work)
	dir := filepath.Join(plugin, ".claude-plugin")
	os.MkdirAll(dir, 0700)
	manifest := filepath.Join(dir, "plugin.json")
	os.WriteFile(manifest, []byte(`{"name":"suite"}`), 0600)
	authority := hooks.NewManager(false, work)
	authority.ConfigureClaudeManagedSettings(t.TempDir())
	authority.ConfigureClaudeSettings(work, user, false)
	authority.ConfigureClaudePlugins([]string{plugin})
	cfg := config.DefaultConfig()
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewWriteTool(work))
	mock := testkit.NewMockClient()
	builder := &Builder{ctx: t.Context(), cfg: cfg, registry: registry, mainClient: mock, hooksManager: authority, workDir: work}
	if err := builder.initLSP(); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("lsp"); ok || builder.lspManager != nil {
		t.Fatal("unconfigured LSP was advertised")
	}
	binary, _ := os.Executable()
	write := func(version string) {
		data, _ := json.Marshal(map[string]any{"name": "suite", "lspServers": map[string]any{"python": map[string]any{"command": binary, "args": []string{"-test.run=^TestLSPFixtureHelper$"}, "extensionToLanguage": map[string]string{".py": "python"}, "env": map[string]string{"GOKIN_LSP_FIXTURE": "1", "GOKIN_LSP_VERSION": version}}}})
		if err := os.WriteFile(manifest, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("one")
	app := &App{ctx: t.Context(), config: cfg, registry: registry, client: mock, hooksManager: authority, workDir: work}
	output, err := (&commands.ReloadPluginsCommand{}).Execute(t.Context(), nil, app)
	if err != nil || !strings.Contains(output, "1 LSP servers updated") {
		t.Fatalf("reload=%q %v", output, err)
	}
	defer app.lspManager.Close()
	if _, ok := registry.Get("lsp"); !ok || app.mcpManager != nil {
		t.Fatal("LSP reload changed disabled MCP or failed registration")
	}
	status, err := (&commands.LSPCommand{}).Execute(t.Context(), nil, app)
	if err != nil || !strings.Contains(status, "idle") {
		t.Fatalf("status=%q %v", status, err)
	}
	mock.EnqueueToolCall("write", map[string]any{"file_path": filepath.Join(work, "changed.py"), "content": "TYPE_ERROR"}).EnqueueText("done")
	executor := tools.NewExecutor(registry, mock, 30*time.Second)
	executor.SetWorkDir(work)
	if _, _, err := executor.Execute(t.Context(), nil, "create a source file"); err != nil {
		t.Fatal(err)
	}
	found := false
	var responses []map[string]any
	for _, call := range mock.Calls() {
		for _, response := range call.Responses {
			responses = append(responses, response.Response)
			if content, ok := response.Response["content"].(string); ok && strings.Contains(content, "fixture type mismatch") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("foreground model did not receive post-write LSP diagnostics; responses=%+v", responses)
	}
	tool, _ := registry.Get("lsp")
	args := map[string]any{"operation": "hover", "filePath": "changed.py", "line": 1, "character": 1}
	result, _ := tool.Execute(t.Context(), args)
	if !result.Success || !strings.Contains(result.Content, "one") {
		t.Fatalf("query=%+v", result)
	}
	write("two")
	if _, err := app.ReloadClaudePlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, _ = tool.Execute(t.Context(), args)
	if !result.Success || !strings.Contains(result.Content, "two") {
		t.Fatalf("reload wrapper=%+v", result)
	}
	authority.ConfigureClaudePlugins(nil)
	if _, err := app.ReloadClaudePlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("lsp"); ok {
		t.Fatal("removed LSP plugin left declaration")
	}
	cfg.Bare = true
	bare := &Builder{ctx: t.Context(), cfg: cfg, registry: tools.NewRegistry(), hooksManager: authority, workDir: work}
	if err := bare.initLSP(); err != nil || bare.lspManager != nil {
		t.Fatalf("bare LSP=%v", err)
	}
}
