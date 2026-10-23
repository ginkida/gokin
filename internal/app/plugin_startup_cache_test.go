package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/mcp"
	"gokin/internal/plugins"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func writeHeadlessFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHeadlessBuilderDoesNotFetchMarketplaceDuringStartup(t *testing.T) {
	user, work := t.TempDir(), testkit.ResolvedTempDir(t)
	t.Setenv("CLAUDE_CONFIG_DIR", user)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); <-r.Context().Done() }))
	defer server.Close()
	settings, _ := json.Marshal(map[string]any{"enabledPlugins": map[string]bool{"suite@acme": true}, "extraKnownMarketplaces": map[string]any{"acme": map[string]any{"source": map[string]string{"source": "url", "url": server.URL}}}})
	writeHeadlessFixture(t, filepath.Join(user, "settings.json"), string(settings))
	cfg := config.DefaultConfig()
	cfg.Session.Enabled = false
	b := NewBuilderWithOptions(cfg, work, BuildOptions{NonInteractive: true, SyncDeclaredPlugins: true})
	defer b.cancel()
	b.mainClient = testkit.NewMockClient()
	b.registry = tools.NewRegistry()
	b.executor = tools.NewExecutor(b.registry, b.mainClient, time.Second)
	b.configDirErr = errors.New("no persistence")
	started := time.Now()
	if err := b.initManagers(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second || requests.Load() != 0 {
		t.Fatalf("startup blocked/contacted server: duration=%s requests=%d", elapsed, requests.Load())
	}
	if selected, err := b.hooksManager.ClaudePlugins(); err != nil || len(selected) != 0 {
		t.Fatalf("uncached enabled plugin blocked startup: %v %v", selected, err)
	}
	if err := b.initMCP(); err != nil {
		t.Fatal(err)
	}
	if b.pluginStartupRefresh == nil {
		t.Fatal("background refresh was not scheduled")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := b.pluginStartupRefresh(ctx); err == nil && requests.Load() == 0 {
		t.Fatal("background refresh did not attempt download")
	}
	if ctx.Err() == nil {
		t.Fatal("blackhole refresh did not respect cancellation")
	}
}

func TestNoPluginsPreservesNativeSkillsSettingsHooksAndMCP(t *testing.T) {
	for _, mode := range []string{"flag", "env"} {
		t.Run(mode, func(t *testing.T) {
			user, work := t.TempDir(), testkit.ResolvedTempDir(t)
			t.Setenv("CLAUDE_CONFIG_DIR", user)
			t.Setenv("GOKIN_NO_PLUGINS", map[bool]string{true: "1", false: ""}[mode == "env"])
			// Broken plugin state must never be inspected with plugins disabled.
			writeHeadlessFixture(t, filepath.Join(user, "plugins", "installed_plugins.json"), `not JSON`)
			writeHeadlessFixture(t, filepath.Join(user, "settings.json"), `{"hooks":{"PreToolUse":[{"matcher":"Read","hooks":[{"type":"command","command":"printf native-hook"}]}]}}`)
			writeHeadlessFixture(t, filepath.Join(work, ".gokin", "skills", "native", "SKILL.md"), "---\nname: native\ndescription: Native workflow\n---\nNative instructions.")
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var message mcp.JSONRPCMessage
				if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if message.ID == nil {
					w.WriteHeader(202)
					return
				}
				var result any = map[string]any{}
				switch message.Method {
				case mcp.MethodInitialize:
					result = map[string]any{"protocolVersion": mcp.ProtocolVersion, "serverInfo": map[string]string{"name": "native", "version": "1"}}
				case mcp.MethodToolsList:
					result = map[string]any{"tools": []any{map[string]any{"name": "query", "inputSchema": map[string]string{"type": "object"}}}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
			}))
			defer server.Close()
			cfg := config.DefaultConfig()
			cfg.Session.Enabled = false
			cfg.Hooks.Enabled = true
			cfg.MCP.Enabled = true
			cfg.Web.GLMSearch = false
			cfg.MCP.Servers = []config.MCPServerConfig{{Name: "native", Transport: "http", URL: server.URL, AutoConnect: true, PermissionLevel: "low"}}
			b := NewBuilderWithOptions(cfg, work, BuildOptions{NonInteractive: true, SyncDeclaredPlugins: true, NoPlugins: mode == "flag"})
			defer b.cancel()
			b.mainClient = testkit.NewMockClient()
			b.registry = tools.NewRegistry()
			skill := tools.NewSkillTool(work)
			b.registry.MustRegister(skill)
			b.executor = tools.NewExecutor(b.registry, b.mainClient, time.Second)
			b.configDirErr = errors.New("no persistence")
			if err := b.initManagers(); err != nil {
				t.Fatal(err)
			}
			if b.pluginStartupRefresh != nil || b.pluginAutoUpdater != nil || b.pluginCommandUpdater != nil {
				t.Fatal("disabled plugins scheduled background work")
			}
			selected, err := b.hooksManager.ClaudePlugins()
			if err != nil || len(selected) != 0 {
				t.Fatalf("plugin discovery was active: %+v %v", selected, err)
			}
			result, err := skill.Execute(t.Context(), map[string]any{"name": "native"})
			if err != nil || !result.Success {
				t.Fatalf("native skill not available: %+v %v", result, err)
			}
			hookResults := b.hooksManager.RunPreTool(t.Context(), "read", map[string]any{"file_path": "native.txt"})
			if len(hookResults) != 1 || hookResults[0].Output != "native-hook" {
				t.Fatalf("native settings hooks not available: %+v", hookResults)
			}
			if err := b.initMCP(); err != nil {
				t.Fatal(err)
			}
			defer b.mcpManager.Shutdown(t.Context())
			if len(b.mcpManager.GetConnectedServers()) != 1 || calls.Load() < 2 {
				t.Fatal("native MCP did not connect")
			}
		})
	}
}

func TestPluginStartupRefreshReturnsImmediatelyAndJoinsOnClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	app := &App{ctx: ctx}
	entered := make(chan struct{})
	finished := make(chan struct{})
	app.startPluginStartupRefresh(func(ctx context.Context) (plugins.StartupReport, error) {
		close(entered)
		<-ctx.Done()
		close(finished)
		return plugins.StartupReport{}, ctx.Err()
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("background refresh did not start")
	}
	app.ClosePluginStartupRefresh()
	select {
	case <-finished:
	default:
		t.Fatal("closing headless runtime did not join cancelled refresh")
	}
}
