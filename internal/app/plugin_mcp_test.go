package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gokin/internal/commands"
	"gokin/internal/config"
	"gokin/internal/hooks"
	"gokin/internal/mcp"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestBuilderPluginMCPAndReloadReplaceActiveWrapper(t *testing.T) {
	for _, bare := range []bool{false, true} {
		t.Run(fmt.Sprintf("bare=%v", bare), func(t *testing.T) {
			work, plugin, user := t.TempDir(), t.TempDir(), t.TempDir()
			calls := &atomic.Int32{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var msg mcp.JSONRPCMessage
				if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				calls.Add(1)
				if msg.ID == nil {
					w.WriteHeader(202)
					return
				}
				var result any = map[string]any{}
				switch msg.Method {
				case mcp.MethodInitialize:
					result = map[string]any{"protocolVersion": mcp.ProtocolVersion, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
				case mcp.MethodToolsList:
					result = map[string]any{"tools": []any{map[string]any{"name": "query", "inputSchema": map[string]any{"type": "object"}}}}
				case mcp.MethodToolsCall:
					result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "version-" + r.Header.Get("X-Version")}}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
			}))
			defer srv.Close()
			writeManifest := func(version string) {
				path := filepath.Join(plugin, ".claude-plugin", "plugin.json")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				data := fmt.Sprintf(`{"name":"suite","mcpServers":{"db":{"type":"http","url":%q,"headers":{"X-Version":%q}}}}`, srv.URL, version)
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writeManifest("one")
			authority := hooks.NewManager(false, work)
			authority.ConfigureClaudeManagedSettings(t.TempDir())
			authority.ConfigureClaudeSettings(work, user, false)
			authority.ConfigureClaudePlugins([]string{plugin})
			cfg := config.DefaultConfig()
			cfg.Bare = bare
			cfg.MCP.Enabled = false
			cfg.Web.GLMSearch = false
			registry := tools.NewRegistry()
			native := tools.NewReadTool(work)
			registry.MustRegister(native)
			mock := testkit.NewMockClient()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			builder := &Builder{ctx: ctx, cfg: cfg, workDir: work, registry: registry, mainClient: mock, hooksManager: authority, options: BuildOptions{Bare: bare}}
			if err := builder.initMCP(); err != nil {
				t.Fatal(err)
			}
			if bare {
				if builder.mcpManager != nil || calls.Load() != 0 || len(registry.Names()) != 1 {
					t.Fatal("bare loaded plugin MCP")
				}
				return
			}
			if builder.mcpManager == nil {
				t.Fatal("selected plugin MCP did not initialize without native MCP")
			}
			defer builder.mcpManager.Shutdown(t.Context())
			name := "mcp__plugin_suite_db__query"
			first, ok := registry.Get(name)
			if !ok {
				t.Fatal("plugin tool not registered")
			}
			if result, _ := first.Execute(t.Context(), nil); !result.Success || result.Content != "version-one" {
				t.Fatalf("initial=%+v", result)
			}
			app := &App{ctx: ctx, config: cfg, workDir: work, registry: registry, client: mock, mcpManager: builder.mcpManager, hooksManager: authority}
			writeManifest("two")
			output, err := (&commands.ReloadPluginsCommand{}).Execute(t.Context(), nil, app)
			if err != nil || !strings.Contains(output, "Reloaded 1 plugins") {
				t.Fatalf("reload=%q %v", output, err)
			}
			second, _ := registry.Get(name)
			if second == first {
				t.Fatal("app retained closed wrapper under unchanged tool name")
			}
			if result, _ := second.Execute(t.Context(), nil); !result.Success || result.Content != "version-two" {
				t.Fatalf("replacement=%+v", result)
			}
			if current, _ := registry.Get("read"); current != native {
				t.Fatal("plugin reload changed native tool")
			}
			if err := os.WriteFile(filepath.Join(user, "settings.json"), []byte(`{"enabledPlugins":{"suite@inline":false}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := app.ReloadClaudePlugins(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, exists := registry.Get(name); exists || len(builder.mcpManager.GetConnectedServers()) != 0 {
				t.Fatal("disabled plugin remained registered")
			}
		})
	}
}

func TestMCPAdminInspectionFollowsManagerCreatedAfterBoot(t *testing.T) {
	config := config.DefaultConfig()
	config.MCP.Enabled = false
	registry := tools.NewRegistry()
	admin := tools.NewMCPAdminTool()
	registry.MustRegister(admin)
	builder := &Builder{ctx: t.Context(), cfg: config, registry: registry}
	if err := builder.initMCP(); err != nil {
		t.Fatal(err)
	}
	initial, err := admin.Execute(t.Context(), map[string]any{"action": "list"})
	if err != nil || !strings.Contains(strings.ToLower(initial.Content), "disabled") {
		t.Fatalf("initial status=%+v %v", initial, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg mcp.JSONRPCMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{}
		switch msg.Method {
		case mcp.MethodInitialize:
			result = map[string]any{"protocolVersion": mcp.ProtocolVersion, "serverInfo": map[string]any{"name": "late", "version": "1"}}
		case mcp.MethodResourcesList:
			result = map[string]any{"resources": []any{map[string]any{"uri": "fixture://late", "name": "late-resource"}}}
		case mcp.MethodResourcesRead:
			result = map[string]any{"contents": []any{map[string]any{"uri": "fixture://late", "text": "late-resource-content"}}}
		case mcp.MethodPromptsList:
			result = map[string]any{"prompts": []any{map[string]any{"name": "late-prompt"}}}
		case mcp.MethodPromptsGet:
			result = map[string]any{"messages": []any{map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": "late-prompt-content"}}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}))
	defer srv.Close()
	manager := mcp.NewManager([]*mcp.ServerConfig{{Name: "late-plugin-server", Transport: "http", URL: srv.URL, AutoConnect: true}})
	defer manager.Shutdown(t.Context())
	if err := manager.ConnectAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	builder.cachedApp = &App{mcpManager: manager}
	updated, err := admin.Execute(t.Context(), map[string]any{"action": "list"})
	if err != nil || !strings.Contains(updated.Content, "late-plugin-server") {
		t.Fatalf("stale builder manager=%+v %v", updated, err)
	}
	for _, check := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "resources"}, "late-resource"},
		{map[string]any{"action": "resources", "uri": "fixture://late"}, "late-resource-content"},
		{map[string]any{"action": "prompts"}, "late-prompt"},
		{map[string]any{"action": "prompts", "name": "late-prompt"}, "late-prompt-content"},
	} {
		result, err := admin.Execute(t.Context(), check.args)
		if err != nil || !result.Success || !strings.Contains(result.Content, check.want) {
			t.Fatalf("late manager action=%v result=%+v err=%v", check.args, result, err)
		}
	}
}
