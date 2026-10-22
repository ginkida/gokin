package mcp

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
	"time"

	"gokin/internal/hooks"
	"gokin/internal/tools"
)

func writePluginMCPFile(t *testing.T, root, relative, body string) {
	t.Helper()
	file := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pluginMCPFixture(t *testing.T, manifest string) (*hooks.Manager, string, string, string) {
	t.Helper()
	canonical := func() string {
		p, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	plugin, user, work := canonical(), canonical(), canonical()
	writePluginMCPFile(t, plugin, ".claude-plugin/plugin.json", manifest)
	manager := hooks.NewManager(false, work)
	manager.ConfigureClaudeManagedSettings(t.TempDir())
	manager.ConfigureClaudeSettings(work, user, false)
	manager.ConfigureClaudePlugins([]string{plugin})
	return manager, plugin, user, work
}

func TestPluginMCPLoaderMergesSourcesAndExpandsValuesOnce(t *testing.T) {
	manifest := `{"name":"suite","userConfig":{"value":{"type":"string","default":"${HOME} !` + "`touch never`" + `"}},"mcpServers":["./config/mcp.json",{"db":{"command":"${CLAUDE_PLUGIN_ROOT}/server","args":["${user_config.value}"],"env":{"VALUE":"${user_config.value}","CLAUDE_PLUGIN_ROOT":"spoof"}}}]}`
	authority, plugin, _, work := pluginMCPFixture(t, manifest)
	writePluginMCPFile(t, plugin, ".mcp.json", `{"mcpServers":{"db":{"command":"old"},"legacy":{"command":"legacy"}}}`)
	writePluginMCPFile(t, plugin, "config/mcp.json", `{"db":{"command":"middle"},"remote":{"type":"http","url":"https://example.org/mcp"}}`)
	configs, err := LoadClaudePluginServers(authority, work)
	if err != nil || len(configs) != 3 {
		t.Fatalf("configs=%v %v", configs, err)
	}
	db := configs[0]
	if db.Name != "plugin:suite:db" || db.Command != filepath.Join(plugin, "server") || db.Args[0] != "${HOME} !`touch never`" || db.Env["VALUE"] != "${HOME} !`touch never`" || db.Env["CLAUDE_PLUGIN_ROOT"] != plugin || db.Env["CLAUDE_PLUGIN_OPTION_VALUE"] != "${HOME} !`touch never`" || db.WorkDir != work {
		t.Fatalf("expansion=%+v", db)
	}
	if _, err := os.Stat(db.Env["CLAUDE_PLUGIN_DATA"]); !os.IsNotExist(err) {
		t.Fatal("discovery created data directory")
	}
	if _, err := os.Stat(filepath.Join(work, "never")); !os.IsNotExist(err) {
		t.Fatal("discovery executed option data")
	}
}

func TestPluginMCPRejectsMalformedAndUnsupportedSources(t *testing.T) {
	for _, tc := range []struct{ name, declared, source string }{
		{"parent", `"./../outside.json"`, "{}"},
		{"absolute", `"/tmp/outside.json"`, "{}"},
		{"bundle", `"./server.mcpb"`, "{}"},
		{"bundle-url", `"https://example.org/server.mcpb"`, "{}"},
		{"invalid-json", `"./config.json"`, "broken"},
		{"duplicate", `"./config.json"`, `{"db":{"command":"first","command":"second"}}`},
		{"unsupported-transport", `{"db":{"type":"sse","url":"https://example.org/mcp"}}`, "{}"},
		{"foreign-field", `{"db":{"command":"node","permissionLevel":"low"}}`, "{}"},
		{"missing-option", `{"db":{"command":"${user_config.missing}"}}`, "{}"},
		{"insecure-url", `{"db":{"type":"http","url":"http://example.org/mcp"}}`, "{}"},
		{"conflicting-fields", `{"db":{"command":"node","url":"https://example.org/mcp"}}`, "{}"},
		{"null-definition", `{"db":null}`, "{}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authority, plugin, _, work := pluginMCPFixture(t, `{"name":"suite","mcpServers":`+tc.declared+`}`)
			writePluginMCPFile(t, plugin, "config.json", tc.source)
			if _, err := LoadClaudePluginServers(authority, work); err == nil {
				t.Fatal("invalid declaration loaded")
			}
		})
	}
	authority, plugin, _, work := pluginMCPFixture(t, `{"name":"suite","mcpServers":"./escape/config.json"}`)
	outside := t.TempDir()
	writePluginMCPFile(t, outside, "config.json", `{"db":{"command":"node"}}`)
	if err := os.Symlink(outside, filepath.Join(plugin, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClaudePluginServers(authority, work); err == nil {
		t.Fatal("MCP source escaped plugin root")
	}
}

func pluginHTTPServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request JSONRPCMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		if request.ID == nil {
			w.WriteHeader(202)
			return
		}
		var result any = map[string]any{}
		switch request.Method {
		case MethodInitialize:
			result = map[string]any{"protocolVersion": ProtocolVersion, "serverInfo": map[string]any{"name": "synthetic", "version": "1"}}
		case MethodToolsList:
			result = map[string]any{"tools": []any{map[string]any{"name": "query", "description": "Query synthetic data", "inputSchema": map[string]any{"type": "object"}}}}
		case MethodToolsCall:
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "answer-" + r.Header.Get("X-Version")}}}
		case MethodResourcesRead:
			result = map[string]any{"contents": []any{map[string]any{"uri": "fixture://data", "text": "fixture"}}}
		case MethodPromptsGet:
			result = map[string]any{"messages": []any{}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	return srv, calls
}

func TestPluginMCPHTTPConnectionSurvivesHandshakeAndReloadRevokesOldReferences(t *testing.T) {
	srv, calls := pluginHTTPServer(t)
	defer srv.Close()
	manifest := func(version string) string {
		return fmt.Sprintf(`{"name":"suite","mcpServers":{"db":{"type":"http","url":%q,"headers":{"X-Version":%q}}}}`, srv.URL, version)
	}
	authority, plugin, user, work := pluginMCPFixture(t, manifest("one"))
	configs, err := LoadClaudePluginServers(authority, work)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(configs)
	defer manager.Shutdown(t.Context())
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	if err := manager.ConnectAll(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	tool := manager.GetTools()[0].(*MCPTool)
	if tool.Name() != "mcp__plugin_suite_db__query" || tools.ClaudeHookToolName(tool) != tool.Name() {
		t.Fatalf("namespace=%s %s", tool.Name(), tools.ClaudeHookToolName(tool))
	}
	if result, err := tool.Execute(t.Context(), nil); err != nil || !result.Success || result.Content != "answer-one" {
		t.Fatalf("connection died with handshake: %+v %v", result, err)
	}
	first, _ := manager.GetClient("plugin:suite:db")
	if changed, err := manager.ReconcileClaudePlugins(t.Context(), authority, work); err != nil || len(changed) != 0 {
		t.Fatalf("unchanged reload=%v %v", changed, err)
	}
	if current, _ := manager.GetClient("plugin:suite:db"); current != first {
		t.Fatal("unchanged connection replaced")
	}
	writePluginMCPFile(t, plugin, ".claude-plugin/plugin.json", manifest("two"))
	before := calls.Load()
	if result, _ := tool.Execute(t.Context(), nil); result.HookStopReason == "" || result.Success || calls.Load() != before {
		t.Fatalf("old configuration reached server: %+v", result)
	}
	if changed, err := manager.ReconcileClaudePlugins(t.Context(), authority, work); err != nil || len(changed) != 1 {
		t.Fatalf("changed reload=%v %v", changed, err)
	}
	newTool := manager.GetTools()[0].(*MCPTool)
	if result, _ := newTool.Execute(t.Context(), nil); !result.Success || result.Content != "answer-two" {
		t.Fatalf("replacement=%+v", result)
	}
	client, _ := manager.GetClient("plugin:suite:db")
	writePluginMCPFile(t, user, "settings.json", `{"enabledPlugins":{"suite@inline":false}}`)
	before = calls.Load()
	if result, _ := newTool.Execute(t.Context(), nil); result.HookStopReason == "" || result.Success {
		t.Fatal("disabled tool remained callable")
	}
	if _, err := client.ReadResource(t.Context(), "fixture://data"); !tools.IsHookStopError(err) {
		t.Fatalf("resource bypass=%v", err)
	}
	if _, err := client.GetPrompt(t.Context(), "fixture", nil); !tools.IsHookStopError(err) {
		t.Fatalf("prompt bypass=%v", err)
	}
	if calls.Load() != before {
		t.Fatal("revocation contacted remote server")
	}
	manager.prunePluginConnections(authority, work)
	if len(manager.GetConnectedServers()) != 0 || len(manager.GetTools()) != 0 {
		t.Fatal("revocation retained connection or tools")
	}
	if _, err := manager.ReconcileClaudePlugins(t.Context(), authority, work); err != nil {
		t.Fatal(err)
	}
	if _, exists := manager.GetServerConfig("plugin:suite:db"); exists {
		t.Fatal("reload retained removed definition")
	}
}

func TestPluginMCPStdioHelper(t *testing.T) {
	if os.Getenv("GOKIN_PLUGIN_MCP_HELPER") != "1" {
		return
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request JSONRPCMessage
		if decoder.Decode(&request) != nil {
			os.Exit(0)
		}
		if request.ID == nil {
			continue
		}
		var result any = map[string]any{}
		switch request.Method {
		case MethodInitialize:
			result = map[string]any{"protocolVersion": ProtocolVersion, "serverInfo": map[string]any{"name": "helper", "version": "1"}}
		case MethodToolsList:
			result = map[string]any{"tools": []any{map[string]any{"name": "environment", "inputSchema": map[string]any{"type": "object"}}}}
		case MethodToolsCall:
			cwd, _ := os.Getwd()
			encoded, _ := json.Marshal(map[string]string{"value": os.Getenv("VALUE"), "root": os.Getenv("CLAUDE_PLUGIN_ROOT"), "data": os.Getenv("CLAUDE_PLUGIN_DATA"), "option": os.Getenv("CLAUDE_PLUGIN_OPTION_TEXT"), "cwd": cwd})
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(encoded)}}}
		}
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}) != nil {
			os.Exit(0)
		}
	}
}

func TestPluginMCPStdioEnvironmentAndDataAreOwnedAndLiteral(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"name":"suite","userConfig":{"text":{"type":"string","default":"${HOME} !literal"}},"mcpServers":{"local":{"command":%q,"args":["-test.run=^TestPluginMCPStdioHelper$"],"env":{"GOKIN_PLUGIN_MCP_HELPER":"1","VALUE":"${user_config.text}","CLAUDE_PLUGIN_ROOT":"spoof"}}}}`, executable)
	authority, plugin, _, work := pluginMCPFixture(t, manifest)
	configs, err := LoadClaudePluginServers(authority, work)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(configs)
	defer manager.Shutdown(t.Context())
	if err := manager.ConnectAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := manager.GetTools()[0].Execute(t.Context(), nil)
	if err != nil || !result.Success {
		t.Fatalf("stdio=%+v %v", result, err)
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(result.Content), &values); err != nil {
		t.Fatal(err)
	}
	if values["root"] != plugin || values["cwd"] != work || values["value"] != "${HOME} !literal" || values["option"] != "${HOME} !literal" {
		t.Fatalf("environment=%v", values)
	}
	if info, err := os.Stat(values["data"]); err != nil || !info.IsDir() {
		t.Fatal("plugin data directory unavailable")
	}
}

func TestPluginMCPHTTPCallerCancellationBoundsSend(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release }))
	defer srv.Close()
	defer close(release)
	transport, err := NewHTTPTransport(t.Context(), srv.URL, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- transport.SendContext(ctx, &JSONRPCMessage{ID: 1, Method: MethodInitialize}) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled HTTP request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP Send ignored caller cancellation")
	}
}

func TestPluginMCPAliasesKeepCanonicalIdentity(t *testing.T) {
	config := &ServerConfig{Name: "plugin:suite:db", pluginToolPrefix: "mcp__plugin_suite_db__"}
	client := &Client{config: config}
	first := NewMCPTool(client, config.Name, "ignored", &ToolInfo{Name: strings.Repeat("x", 80) + "one"})
	second := NewMCPTool(client, config.Name, "ignored", &ToolInfo{Name: strings.Repeat("x", 80) + "two"})
	if len(first.Name()) > 64 || first.Name() == second.Name() || first.Declaration().Name != first.Name() || tools.ClaudeHookToolName(first) != "mcp__plugin_suite_db__"+strings.Repeat("x", 80)+"one" {
		t.Fatal("alias lost source identity or collided")
	}
}

func TestPluginMCPPolicyWatcherRetiresDisabledConnection(t *testing.T) {
	srv, _ := pluginHTTPServer(t)
	defer srv.Close()
	authority, _, user, work := pluginMCPFixture(t, fmt.Sprintf(`{"name":"suite","mcpServers":{"db":{"type":"http","url":%q}}}`, srv.URL))
	configs, err := LoadClaudePluginServers(authority, work)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(configs)
	defer manager.Shutdown(t.Context())
	if err := manager.ConnectAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager.StartPluginPolicyWatch(t.Context(), authority, work)
	writePluginMCPFile(t, user, "settings.json", `{"enabledPlugins":{"suite@inline":false}}`)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for len(manager.GetConnectedServers()) != 0 || len(manager.GetTools()) != 0 {
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("policy watcher retained revoked connection")
		}
	}
}

func TestInstalledPluginMCPRespectsDisabledDefaultBeforeParsing(t *testing.T) {
	authority, plugin, user, work := pluginMCPFixture(t, `{"name":"suite","mcpServers":"./missing.json"}`)
	registry := fmt.Sprintf(`{"version":2,"plugins":{"suite@market":[{"scope":"user","installPath":%q}]}}`, plugin)
	writePluginMCPFile(t, user, "plugins/installed_plugins.json", registry)
	authority.ConfigureClaudePlugins(nil)
	if configs, err := LoadClaudePluginServers(authority, work, false); err != nil || len(configs) != 0 {
		t.Fatalf("disabled installed component was parsed: %v %v", configs, err)
	}
	if _, err := LoadClaudePluginServers(authority, work, true); err == nil {
		t.Fatal("enabled installed component ignored missing source")
	}
	writePluginMCPFile(t, plugin, "missing.json", `{"db":{"command":"node"}}`)
	configs, err := LoadClaudePluginServers(authority, work, true)
	if err != nil || len(configs) != 1 || configs[0].PluginExplicitlyEnabled() {
		t.Fatalf("installed activation=%v %v", configs, err)
	}
}
