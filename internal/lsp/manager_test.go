package lsp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/lsp"
	"gokin/internal/testkit"
)

func TestLSPFixtureHelper(t *testing.T) { testkit.RunLSPFixture(t) }

func setup(t *testing.T, env map[string]string) (string, string, *hooks.Manager, *lsp.Manager) {
	t.Helper()
	work, plugin, user := t.TempDir(), t.TempDir(), t.TempDir()
	work, _ = filepath.EvalSymlinks(work)
	if err := os.WriteFile(filepath.Join(work, "source.py"), []byte("a😀b\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if env == nil {
		env = map[string]string{}
	}
	env["GOKIN_LSP_FIXTURE"] = "1"
	env["GOKIN_LSP_LOG"] = filepath.Join(work, "rpc.jsonl")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	definition := map[string]any{"command": binary, "args": []string{"-test.run=^TestLSPFixtureHelper$"}, "extensionToLanguage": map[string]string{".py": "python"}, "env": env, "settings": map[string]any{"fixture": map[string]any{"option": "value"}}}
	writeManifest(t, plugin, definition)
	authority := hooks.NewManager(false, work)
	authority.ConfigureClaudeManagedSettings(t.TempDir())
	authority.ConfigureClaudeSettings(work, user, false)
	authority.ConfigureClaudePlugins([]string{plugin})
	manager, err := lsp.NewManager(t.Context(), authority, work)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close() })
	return work, plugin, authority, manager
}
func writeManifest(t *testing.T, plugin string, definition map[string]any) {
	t.Helper()
	dir := filepath.Join(plugin, ".claude-plugin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"name": "suite", "lspServers": map[string]any{"python": definition}})
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
func calls(t *testing.T, work string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(work, "rpc.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var item map[string]any
		if json.Unmarshal([]byte(line), &item) != nil {
			t.Fatal(string(data))
		}
		out = append(out, item)
	}
	return out
}
func countStarts(t *testing.T, work string) int {
	n := 0
	for _, item := range calls(t, work) {
		if item["event"] == "start" {
			n++
		}
	}
	return n
}

func TestLSPNavigationSynchronizationAndDiagnostics(t *testing.T) {
	work, _, _, manager := setup(t, map[string]string{"LITERAL_OPTION": "${HOME}; literal"})
	if status := manager.Status(); len(status) != 1 || status[0].Running {
		t.Fatal("discovery started a server", status)
	}
	if _, err := os.Stat(filepath.Join(work, "rpc.jsonl")); !os.IsNotExist(err) {
		t.Fatal("discovery spawned process")
	}
	ctx, cancel := context.WithCancel(t.Context())
	result, err := manager.Query(ctx, lsp.Request{Operation: "hover", File: "source.py", Line: 1, Character: 3})
	cancel()
	if err != nil || !strings.Contains(string(result.Value), `"character":3`) {
		t.Fatalf("UTF16 query=%s %v", result.Value, err)
	}
	if starts := countStarts(t, work); starts != 1 {
		t.Fatalf("starts=%d", starts)
	}
	if err := os.WriteFile(filepath.Join(work, "source.py"), []byte("TYPE_ERROR\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := manager.Diagnose(t.Context(), "source.py")
	if err != nil || !report.Available || report.Version != 2 || len(report.Diagnostics) != 1 || report.Diagnostics[0].Message != "fixture type mismatch" {
		t.Fatalf("diagnostics=%+v %v", report, err)
	}
	if err := os.WriteFile(filepath.Join(work, "source.py"), []byte("clean\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err = manager.Diagnose(t.Context(), "source.py")
	if err != nil || !report.Available || len(report.Diagnostics) != 0 {
		t.Fatalf("clean=%+v %v", report, err)
	}
	foundRange, foundSettings := false, false
	for _, item := range calls(t, work) {
		if item["event"] == "start" && item["option"] != "${HOME}; literal" {
			t.Fatalf("expanded inserted env: %+v", item)
		}
		if item["method"] == "textDocument/didChange" {
			data, _ := json.Marshal(item)
			if strings.Contains(string(data), `"range"`) {
				foundRange = true
			}
		}
		if item["method"] == "" && item["result"] != nil {
			data, _ := json.Marshal(item["result"])
			foundSettings = strings.Contains(string(data), "value")
		}
	}
	if !foundRange || !foundSettings {
		t.Fatalf("incremental/settings=%v/%v", foundRange, foundSettings)
	}
	for _, operation := range []string{"goToDefinition", "findReferences", "documentSymbol", "workspaceSymbol", "goToImplementation", "prepareCallHierarchy", "incomingCalls", "outgoingCalls"} {
		if _, err := manager.Query(t.Context(), lsp.Request{Operation: operation, File: "source.py", Line: 1, Character: 1, Query: "clean"}); err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
	}
	if starts := countStarts(t, work); starts != 1 {
		t.Fatalf("query cancellation killed managed lifetime: starts=%d", starts)
	}
}

func TestLSPPullDiagnosticsAndMissingPublish(t *testing.T) {
	t.Run("pull", func(t *testing.T) {
		work, _, _, manager := setup(t, map[string]string{"GOKIN_LSP_PULL": "1", "GOKIN_LSP_NO_PUSH": "1"})
		os.WriteFile(filepath.Join(work, "source.py"), []byte("TYPE_ERROR"), 0600)
		report, err := manager.Diagnose(t.Context(), "source.py")
		if err != nil || !report.Available || len(report.Diagnostics) != 1 {
			t.Fatalf("pull=%+v %v", report, err)
		}
	})
	t.Run("silent", func(t *testing.T) {
		_, _, _, manager := setup(t, map[string]string{"GOKIN_LSP_NO_PUSH": "1"})
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		report, err := manager.Diagnose(ctx, "source.py")
		if err == nil || report.Available {
			t.Fatalf("silent server counted clean: %+v %v", report, err)
		}
	})
}

func TestLSPReloadRevocationAndIsolatedWorkspace(t *testing.T) {
	work, plugin, authority, manager := setup(t, map[string]string{"GOKIN_LSP_VERSION": "one"})
	request := lsp.Request{Operation: "hover", File: "source.py", Line: 1, Character: 1}
	if _, err := manager.Query(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if changed, err := manager.Reload(t.Context()); err != nil || changed != 0 {
		t.Fatalf("unchanged=%d %v", changed, err)
	}
	path := filepath.Join(plugin, ".claude-plugin", "plugin.json")
	data, _ := os.ReadFile(path)
	data = []byte(strings.Replace(string(data), `"one"`, `"two"`, 1))
	os.WriteFile(path, data, 0600)
	if _, err := manager.Query(t.Context(), request); !errors.Is(err, lsp.ErrPolicyChanged) {
		t.Fatalf("changed source executed: %v", err)
	}
	if changed, err := manager.Reload(t.Context()); err != nil || changed != 1 {
		t.Fatalf("replacement=%d %v", changed, err)
	}
	result, err := manager.Query(t.Context(), request)
	if err != nil || !strings.Contains(string(result.Value), "two") {
		t.Fatalf("replacement query=%s %v", result.Value, err)
	}
	if starts := countStarts(t, work); starts != 2 {
		t.Fatalf("starts=%d", starts)
	}
	isolated := t.TempDir()
	os.WriteFile(filepath.Join(isolated, "source.py"), []byte("isolated"), 0600)
	fork, err := manager.ForWorkDir(isolated)
	if err != nil {
		t.Fatal(err)
	}
	if fork == manager {
		t.Fatal("isolated index shared parent")
	}
	if _, err := fork.Query(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Query(t.Context(), lsp.Request{Operation: "hover", File: filepath.Join(isolated, "source.py"), Line: 1, Character: 1}); err == nil {
		t.Fatal("foreground accepted isolated source")
	}
	authority.ConfigureClaudePlugins(nil)
	if _, err := manager.Query(t.Context(), request); !errors.Is(err, lsp.ErrPolicyChanged) {
		t.Fatalf("revoked source executed: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for manager.Status()[0].Running && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if manager.Status()[0].Running {
		t.Fatal("policy watcher left revoked process alive")
	}
	manager.Close()
	if status := manager.Status(); len(status) != 0 {
		t.Fatalf("closed manager status=%+v", status)
	}
	if _, err := fork.Query(t.Context(), request); !errors.Is(err, lsp.ErrClosed) {
		t.Fatalf("fork survived owner shutdown: %v", err)
	}
}

func TestLSPCrashRestartPolicy(t *testing.T) {
	work, _, _, manager := setup(t, map[string]string{"GOKIN_LSP_CRASH_ONCE": filepath.Join(t.TempDir(), "crash")})
	request := lsp.Request{Operation: "hover", File: "source.py", Line: 1, Character: 1}
	if _, err := manager.Query(t.Context(), request); err == nil {
		t.Fatal("crash hidden as no match")
	}
	if _, err := manager.Query(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if starts := countStarts(t, work); starts != 2 {
		t.Fatalf("starts=%d", starts)
	}
}

func TestLSPRequestTimeoutKeepsConnectionAndRestartLimits(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		work, _, _, manager := setup(t, map[string]string{"GOKIN_LSP_HANG_METHOD": "textDocument/hover"})
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		if _, err := manager.Query(ctx, lsp.Request{Operation: "hover", File: "source.py", Line: 1, Character: 1}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout=%v", err)
		}
		if _, err := manager.Query(t.Context(), lsp.Request{Operation: "documentSymbol", File: "source.py"}); err != nil {
			t.Fatal(err)
		}
		if countStarts(t, work) != 1 {
			t.Fatal("cancelled request restarted healthy process")
		}
	})
	for _, field := range []string{`"restartOnCrash":false,`, `"maxRestarts":0,`} {
		t.Run(field, func(t *testing.T) {
			work, plugin, _, manager := setup(t, map[string]string{"GOKIN_LSP_CRASH_ONCE": filepath.Join(t.TempDir(), "crash")})
			path := filepath.Join(plugin, ".claude-plugin", "plugin.json")
			data, _ := os.ReadFile(path)
			data = []byte(strings.Replace(string(data), `"command":`, field+`"command":`, 1))
			os.WriteFile(path, data, 0600)
			if _, err := manager.Reload(t.Context()); err != nil {
				t.Fatal(err)
			}
			request := lsp.Request{Operation: "hover", File: "source.py", Line: 1, Character: 1}
			if _, err := manager.Query(t.Context(), request); err == nil {
				t.Fatal("crash hidden")
			}
			if _, err := manager.Query(t.Context(), request); err == nil {
				t.Fatal("restart restriction ignored")
			}
			if countStarts(t, work) != 1 {
				t.Fatal("restricted server restarted")
			}
			if changed, err := manager.Reload(t.Context()); err != nil || changed != 1 {
				t.Fatalf("manual recovery=%d %v", changed, err)
			}
			if _, err := manager.Query(t.Context(), request); err != nil {
				t.Fatalf("explicit reload did not reset stopped server: %v", err)
			}
		})
	}
}

func TestLSPRealGoplsDefinitionAndTypeDiagnostics(t *testing.T) {
	binary, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not installed")
	}
	work, plugin, user := t.TempDir(), t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.test/lspfixture\n\ngo 1.25\n"), 0600)
	source := "package fixture\nfunc value() int { return 1 }\nfunc caller() int { return value() }\n"
	os.WriteFile(filepath.Join(work, "source.go"), []byte(source), 0600)
	writeManifest(t, plugin, map[string]any{"command": binary, "args": []string{"serve"}, "extensionToLanguage": map[string]string{".go": "go"}, "env": map[string]string{"GOPROXY": "off", "GOTOOLCHAIN": "local"}})
	authority := hooks.NewManager(false, work)
	authority.ConfigureClaudeManagedSettings(t.TempDir())
	authority.ConfigureClaudeSettings(work, user, false)
	authority.ConfigureClaudePlugins([]string{plugin})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	manager, err := lsp.NewManager(ctx, authority, work)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	result, err := manager.Query(ctx, lsp.Request{Operation: "goToDefinition", File: "source.go", Line: 3, Character: 28})
	if err != nil || !strings.Contains(string(result.Value), `"line":1`) {
		t.Fatalf("gopls definition=%s %v", result.Value, err)
	}
	os.WriteFile(filepath.Join(work, "source.go"), []byte(strings.Replace(source, "return 1", "return \"wrong\"", 1)), 0600)
	report, err := manager.Diagnose(ctx, "source.go")
	if err != nil || !report.Available || len(report.Diagnostics) == 0 {
		t.Fatalf("gopls diagnostics=%+v %v", report, err)
	}
}
