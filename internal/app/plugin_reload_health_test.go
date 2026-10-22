package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/hooks"
	"gokin/internal/mcp"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

// This test binary also supplies a stdio MCP fixture. A crash marker keeps
// reconnects failing until the parent has observed the health notification.
func TestPluginReloadHealthServerProcess(t *testing.T) {
	root := os.Getenv("GOKIN_TEST_MCP_HEALTH_ROOT")
	if root == "" {
		return
	}
	go func() {
		for {
			if _, err := os.Stat(filepath.Join(root, "crash")); err == nil {
				os.Exit(1)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request mcp.JSONRPCMessage
		if err := decoder.Decode(&request); err != nil {
			if err == io.EOF {
				os.Exit(0)
			}
			os.Exit(2)
		}
		if request.ID == nil {
			continue
		}
		var result any = map[string]any{}
		switch request.Method {
		case mcp.MethodInitialize:
			result = map[string]any{"protocolVersion": mcp.ProtocolVersion, "serverInfo": map[string]any{"name": "late", "version": "1"}}
		case mcp.MethodToolsList:
			result = map[string]any{"tools": []any{map[string]any{"name": "query", "inputSchema": map[string]any{"type": "object"}}}}
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			os.Exit(2)
		}
	}
}

func TestPluginReloadCreatedManagerMonitorsHealth(t *testing.T) {
	work, plugin, user := t.TempDir(), t.TempDir(), t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	authority := hooks.NewManager(false, work)
	authority.ConfigureClaudeManagedSettings(t.TempDir())
	authority.ConfigureClaudeSettings(work, user, false)
	authority.ConfigureClaudePlugins([]string{plugin})
	path := filepath.Join(plugin, ".claude-plugin", "plugin.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"name": "late", "mcpServers": map[string]any{"db": map[string]any{"command": executable, "args": []string{"-test.run=^TestPluginReloadHealthServerProcess$"}, "env": map[string]string{"GOKIN_TEST_MCP_HEALTH_ROOT": work}}}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.MCP.Enabled, cfg.Web.GLMSearch = false, false
	cfg.MCP.HealthCheckInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	program, model := newCapturingProgram(t)
	app := &App{ctx: ctx, config: cfg, workDir: work, registry: tools.NewRegistry(), hooksManager: authority, program: program}
	if _, err := app.ReloadClaudePlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager := app.GetMCPManager()
	if manager == nil {
		t.Fatal("reload did not create manager")
	}
	defer manager.Shutdown(t.Context())
	original, ok := manager.GetClient("plugin:late:db")
	if !ok {
		t.Fatal("late plugin did not connect")
	}
	crash := filepath.Join(work, "crash")
	if err := os.WriteFile(crash, nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	notified := false
	for time.Now().Before(deadline) {
		model.mu.Lock()
		for _, msg := range model.msgs {
			if status, ok := msg.(ui.StatusUpdateMsg); ok && strings.Contains(status.Message, `MCP server "plugin:late:db" disconnected`) {
				notified = true
			}
		}
		model.mu.Unlock()
		if notified {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !notified {
		t.Fatal("reload-created manager did not report crashed server health")
	}
	if err := os.Remove(crash); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		current, connected := manager.GetClient("plugin:late:db")
		if connected && current != original && current.IsInitialized() && manager.IsHealthy("plugin:late:db") && len(manager.GetTools()) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("health monitor did not reconnect late plugin")
}
