package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/commands"
	"gokin/internal/config"
	"gokin/internal/hooks"
	"gokin/internal/mcp"
	"gokin/internal/plugins"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"gokin/internal/ui"
	"google.golang.org/genai"
)

func writeCommandRefreshJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func appCommandRefreshFixture(t *testing.T) (*App, *plugins.Manager, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command fixture")
	}
	work, user, managed, market, producer := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	work, _ = filepath.EvalSymlinks(work)
	producer, _ = filepath.EvalSymlinks(producer)
	for _, key := range []string{"DISABLE_UPDATES", "DISABLE_AUTOUPDATER", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "FORCE_AUTOUPDATE_PLUGINS"} {
		t.Setenv(key, "")
	}
	writeCommandRefreshJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), map[string]any{"name": "suite"})
	counter := filepath.Join(t.TempDir(), "runs")
	command := "printf x >> '" + counter + "'; printf '%s\\n' '" + producer + "'"
	writeCommandRefreshJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "team", "owner": map[string]string{"name": "Test"}, "plugins": []any{map[string]any{"name": "suite", "source": plugins.Source{Kind: "command", Command: command}}}})
	installer, err := plugins.NewManager(plugins.Options{UserDir: user, ManagedDir: managed, WorkDir: work, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(command))
	if _, err := installer.Install(t.Context(), "suite", plugins.InstallOptions{AcceptCommands: []string{hex.EncodeToString(sum[:])}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(counter, nil, 0600); err != nil {
		t.Fatal(err)
	}
	authority := hooks.NewManager(false, work)
	authority.ConfigureClaudeManagedSettings(managed)
	authority.ConfigureClaudeSettings(work, user, true)
	authority.ConfigureClaudePlugins(nil)
	if err := authority.PinClaudePluginInstalls(); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.MCP.Enabled = false
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewReadTool(work))
	updater := newPluginCommandUpdater(installer)
	updater.wait = func(context.Context) bool { return true }
	a := &App{ctx: ctx, cancel: cancel, config: cfg, workDir: work, hooksManager: authority, registry: registry, client: testkit.NewMockClient(), pluginCommandUpdater: updater}
	return a, installer, producer, counter
}

func TestCommandRefreshBackgroundReloadsOnceAtIdle(t *testing.T) {
	a, _, producer, counter := appCommandRefreshFixture(t)
	program, capture := newCapturingProgram(t)
	a.program = program
	selected, err := a.hooksManager.ClaudePlugins()
	if err != nil || len(selected) != 1 {
		t.Fatalf("old=%+v %v", selected, err)
	}
	old := selected[0]
	if err := os.WriteFile(filepath.Join(producer, "new.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	a.processing = true
	a.startPluginCommandRefresh()
	a.startPluginCommandRefresh()
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, _ := os.ReadFile(counter)
		if len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("startup command did not run without user input")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-a.pluginCommandUpdater.done:
		t.Fatal("reloaded during foreground turn")
	default:
	}
	if err := old.CheckSelected(a.hooksManager); err != nil {
		t.Fatal("old runtime changed during foreground", err)
	}
	a.mu.Lock()
	a.processing = false
	a.mu.Unlock()
	select {
	case <-a.pluginCommandUpdater.done:
	case <-time.After(3 * time.Second):
		t.Fatal("command refresh failed to reload at idle")
	}
	if err := old.CheckSelected(a.hooksManager); err == nil {
		t.Fatal("new output was not activated")
	}
	data, _ := os.ReadFile(counter)
	if string(data) != "x" {
		t.Fatalf("duplicate refresh=%q", data)
	}
	notified := false
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		capture.mu.Lock()
		for _, msg := range capture.msgs {
			if notice, ok := msg.(ui.StatusUpdateMsg); ok && strings.Contains(notice.Message, "Command-source plugins reloaded") {
				notified = true
			}
		}
		capture.mu.Unlock()
		if notified {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !notified {
		t.Fatal("automatic reload was not announced")
	}
}

func TestCommandRefreshSchedulerCancellationAndExcludedModes(t *testing.T) {
	for _, mode := range []string{"bare", "headless", "shutdown", "embedding-off", "cancel-wait", "cancel-run"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started := make(chan struct{})
			updater := &pluginAutoUpdater{done: make(chan struct{}), wait: func(ctx context.Context) bool {
				if mode == "cancel-wait" {
					close(started)
					<-ctx.Done()
					return false
				}
				return true
			}, run: func(ctx context.Context) (plugins.StartupReport, error) {
				close(started)
				<-ctx.Done()
				return plugins.StartupReport{}, ctx.Err()
			}}
			a := &App{ctx: ctx, config: &config.Config{}, pluginCommandUpdater: updater}
			switch mode {
			case "bare":
				a.config.Bare = true
			case "headless":
				a.headlessDirect = true
			case "shutdown":
				a.shuttingDown = true
			case "embedding-off":
				a.pluginCommandUpdater = nil
			}
			a.startPluginCommandRefresh()
			if mode != "cancel-wait" && mode != "cancel-run" {
				if updater.started.Load() {
					t.Fatal("excluded mode scheduled command")
				}
				return
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("refresh not started")
			}
			cancel()
			select {
			case <-updater.done:
			case <-time.After(time.Second):
				t.Fatal("session cancellation did not stop refresh")
			}
		})
	}
}

func TestPluginReloadCacheGuardForLSPAndForceCommand(t *testing.T) {
	a, installer, producer, _ := appCommandRefreshFixture(t)
	selected, _ := a.hooksManager.ClaudePlugins()
	old := selected[0]
	a.session = chat.NewSession()
	a.session.SetHistory([]*genai.Content{genai.NewContentFromText("existing conversation", genai.RoleUser)})
	writeCommandRefreshJSON(t, filepath.Join(producer, ".lsp.json"), map[string]any{"python": map[string]any{"command": "NOT_STARTED", "extensionToLanguage": map[string]string{".py": "python"}}})
	if _, err := installer.Install(t.Context(), "suite", plugins.InstallOptions{Update: true}); err != nil {
		t.Fatal(err)
	}
	message, err := a.ReloadClaudePlugins(t.Context())
	if err != nil || !strings.Contains(message, "/reload-plugins --force") || !strings.Contains(message, "LSP tool") {
		t.Fatalf("guard=%q %v", message, err)
	}
	if a.lspManager != nil {
		t.Fatal("declined reload created a language server manager")
	}
	if err := old.CheckSelected(a.hooksManager); err != nil {
		t.Fatal("declined reload changed installed-version pin", err)
	}
	message, err = (&commands.ReloadPluginsCommand{}).Execute(t.Context(), []string{"--force"}, a)
	if err != nil || !strings.Contains(message, "Reloaded 1 plugins") {
		t.Fatalf("force=%q %v", message, err)
	}
	if _, ok := a.registry.Get("lsp"); !ok {
		t.Fatal("forced reload did not add LSP tool")
	}
	defer a.lspManager.Close()
	if err := old.CheckSelected(a.hooksManager); err == nil {
		t.Fatal("forced reload retained old root")
	}
}

func TestPluginReloadCacheGuardMCPAdditionAndRemoval(t *testing.T) {
	a, installer, producer, _ := appCommandRefreshFixture(t)
	a.config.MCP.Enabled = true
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var msg mcp.JSONRPCMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if msg.ID == nil {
			w.WriteHeader(202)
			return
		}
		var result any = map[string]any{}
		switch msg.Method {
		case mcp.MethodInitialize:
			result = map[string]any{"protocolVersion": mcp.ProtocolVersion, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
		case mcp.MethodToolsList:
			result = map[string]any{"tools": []any{map[string]any{"name": "probe", "inputSchema": map[string]any{"type": "object"}}}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}))
	defer server.Close()
	a.session = chat.NewSession()
	a.session.SetHistory([]*genai.Content{genai.NewContentFromText("existing", genai.RoleUser)})
	path := filepath.Join(producer, ".mcp.json")
	writeCommandRefreshJSON(t, path, map[string]any{"db": map[string]any{"type": "http", "url": server.URL}})
	if _, err := installer.Install(t.Context(), "suite", plugins.InstallOptions{Update: true}); err != nil {
		t.Fatal(err)
	}
	message, err := a.ReloadClaudePlugins(t.Context())
	if err != nil || !strings.Contains(message, "MCP plugin:suite:db") || calls.Load() != 0 || a.mcpManager != nil {
		t.Fatalf("addition=%q %v calls=%d", message, err, calls.Load())
	}
	if _, err := a.ReloadClaudePluginsWithForce(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	defer a.mcpManager.Shutdown(t.Context())
	if len(a.mcpManager.GetConnectedServers()) != 1 {
		t.Fatal("force did not connect MCP")
	}
	// Same server name may be refreshed without adding/removing prompt tools.
	if err := os.WriteFile(filepath.Join(producer, "other.txt"), []byte("new copy"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install(t.Context(), "suite", plugins.InstallOptions{Update: true}); err != nil {
		t.Fatal(err)
	}
	if message, err := a.ReloadClaudePlugins(t.Context()); err != nil || !strings.Contains(message, "Reloaded 1 plugins") {
		t.Fatalf("same-name refresh=%q %v", message, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install(t.Context(), "suite", plugins.InstallOptions{Update: true}); err != nil {
		t.Fatal(err)
	}
	message, err = a.ReloadClaudePlugins(t.Context())
	if err != nil || !strings.Contains(message, "--force") || len(a.mcpManager.GetConnectedServers()) != 1 {
		t.Fatalf("removal=%q %v", message, err)
	}
	if _, err := (&commands.ReloadPluginsCommand{}).Execute(t.Context(), []string{"force"}, a); err != nil {
		t.Fatal(err)
	}
	if len(a.mcpManager.GetConnectedServers()) != 0 {
		t.Fatal("force did not remove MCP")
	}
}

func TestCommandRefreshBackgroundPromptsForCacheCost(t *testing.T) {
	a, _, producer, _ := appCommandRefreshFixture(t)
	program, capture := newCapturingProgram(t)
	a.program = program
	a.session = chat.NewSession()
	a.session.SetHistory([]*genai.Content{genai.NewContentFromText("existing", genai.RoleUser)})
	selected, _ := a.hooksManager.ClaudePlugins()
	old := selected[0]
	writeCommandRefreshJSON(t, filepath.Join(producer, ".lsp.json"), map[string]any{"python": map[string]any{"command": "NOT_STARTED", "extensionToLanguage": map[string]string{".py": "python"}}})
	a.startPluginCommandRefresh()
	select {
	case <-a.pluginCommandUpdater.done:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not finish")
	}
	if err := old.CheckSelected(a.hooksManager); err != nil {
		t.Fatal("cache guard changed runtime pin", err)
	}
	if a.lspManager != nil {
		t.Fatal("background refresh started new runtime despite cache guard")
	}
	deadline := time.Now().Add(time.Second)
	notified := false
	for time.Now().Before(deadline) {
		capture.mu.Lock()
		for _, msg := range capture.msgs {
			if notice, ok := msg.(ui.StatusUpdateMsg); ok && notice.Type == ui.StatusWarning && strings.Contains(notice.Message, "--force") {
				notified = true
			}
		}
		capture.mu.Unlock()
		if notified {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !notified {
		t.Fatal("cache-cost confirmation not shown")
	}
}

func TestBuilderNoninteractiveRefreshesAcceptedCommandsBeforeDiscovery(t *testing.T) {
	a, _, producer, counter := appCommandRefreshFixture(t)
	old, _ := a.hooksManager.ClaudePlugins()
	if err := os.WriteFile(filepath.Join(producer, "new.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	user, managed := a.hooksManager.ClaudeSettingsDirectories()
	authority := hooks.NewManager(false, a.workDir)
	authority.ConfigureClaudeManagedSettings(managed)
	authority.ConfigureClaudeSettings(a.workDir, user, true)
	authority.ConfigureClaudePlugins(nil)
	b := &Builder{ctx: t.Context(), cfg: a.config, workDir: a.workDir, hooksManager: authority, options: BuildOptions{NonInteractive: true, SyncDeclaredPlugins: true}}
	b.syncStartupPlugins(user, true)
	if b.pluginCommandUpdater != nil {
		t.Fatal("headless builder scheduled interactive worker")
	}
	selected, err := authority.ClaudePlugins()
	if err != nil || len(selected) != 1 || selected[0].Root() == old[0].Root() {
		t.Fatalf("headless source not refreshed=%+v %v", selected, err)
	}
	data, _ := os.ReadFile(counter)
	if string(data) != "x" {
		t.Fatalf("headless runs=%q", data)
	}
}

func TestRenamedPluginReloadHonorsCacheGuardAndLiveDisable(t *testing.T) {
	a, installer, producer, _ := appCommandRefreshFixture(t)
	a.config.MCP.Enabled = true
	a.session = chat.NewSession()
	a.session.SetHistory([]*genai.Content{genai.NewContentFromText("existing", genai.RoleUser)})
	selected, err := a.hooksManager.ClaudePlugins()
	if err != nil {
		t.Fatal(err)
	}
	old := selected[0]
	rows, err := installer.List()
	if err != nil {
		t.Fatal(err)
	}
	source := rows[0].Source
	markets, err := installer.MarketplaceList()
	if err != nil {
		t.Fatal(err)
	}
	writeCommandRefreshJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), map[string]any{"name": "renamed"})
	writeCommandRefreshJSON(t, filepath.Join(producer, ".mcp.json"), map[string]any{"db": map[string]any{"type": "http", "url": "http://127.0.0.1:1"}})
	writeCommandRefreshJSON(t, filepath.Join(markets["team"].Source.Path, ".claude-plugin", "marketplace.json"), map[string]any{"name": "team", "owner": map[string]string{"name": "Test"}, "renames": map[string]string{"suite": "renamed"}, "plugins": []any{map[string]any{"name": "renamed", "source": source}}})
	if _, err := installer.UpdateMarketplace(t.Context(), "team", nil); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(source.Command))
	if _, err := installer.Install(t.Context(), "renamed@team", plugins.InstallOptions{AcceptCommands: []string{hex.EncodeToString(sum[:])}}); err != nil {
		t.Fatal(err)
	}
	message, err := a.ReloadClaudePlugins(t.Context())
	if err != nil || !strings.Contains(message, "--force") {
		t.Fatalf("rename cache guard=%q %v", message, err)
	}
	if a.mcpManager != nil {
		t.Fatal("declined rename reload attempted MCP connection")
	}
	if err := old.CheckSelected(a.hooksManager); err != nil {
		t.Fatal("rename switched pending version before force", err)
	}
	if _, err := installer.SetEnabled(t.Context(), "renamed@team", "user", false); err != nil {
		t.Fatal(err)
	}
	if err := old.CheckSelected(a.hooksManager); err == nil {
		t.Fatal("live disable did not revoke renamed pinned plugin")
	}
}
