package tools

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
	"gokin/internal/lsp"
	"gokin/internal/permission"
	"gokin/internal/testkit"
	"google.golang.org/genai"
)

func TestLSPFixtureHelper(t *testing.T) { testkit.RunLSPFixture(t) }
func lspToolFixture(t *testing.T) (string, *LSPTool, *Registry) {
	t.Helper()
	work, plugin, user := t.TempDir(), t.TempDir(), t.TempDir()
	work = canonicalToolWorkDir(work)
	os.WriteFile(filepath.Join(work, "source.py"), []byte("source"), 0600)
	os.WriteFile(filepath.Join(work, "secret.py"), []byte("secret"), 0600)
	binary, _ := os.Executable()
	definition := map[string]any{"command": binary, "args": []string{"-test.run=^TestLSPFixtureHelper$"}, "extensionToLanguage": map[string]string{".py": "python"}, "env": map[string]string{"GOKIN_LSP_FIXTURE": "1", "GOKIN_LSP_EXTRA_URI": (&url.URL{Scheme: "file", Path: filepath.Join(work, "secret.py")}).String()}}
	data, _ := json.Marshal(map[string]any{"name": "suite", "lspServers": map[string]any{"python": definition}})
	os.MkdirAll(filepath.Join(plugin, ".claude-plugin"), 0700)
	os.WriteFile(filepath.Join(plugin, ".claude-plugin", "plugin.json"), data, 0600)
	authority := hooks.NewManager(false, work)
	authority.ConfigureClaudeManagedSettings(t.TempDir())
	authority.ConfigureClaudeSettings(work, user, false)
	authority.ConfigureClaudePlugins([]string{plugin})
	manager, err := lsp.NewManager(t.Context(), authority, work)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close() })
	tool := NewLSPTool(work)
	tool.SetManager(manager)
	registry := NewRegistry()
	registry.MustRegister(tool)
	return work, tool, registry
}
func TestLSPToolReadRulesFilterReturnedLocationsAndInput(t *testing.T) {
	work, tool, registry := lspToolFixture(t)
	permissions := permission.NewManager(nil, true)
	denies, err := permission.ParseTemporaryToolDenyList("Read(secret.py)")
	if err != nil {
		t.Fatal(err)
	}
	permissions.SetRunToolRules(nil, denies)
	ctx := ContextWithLSPReadPolicy(t.Context(), permissions, registry, work)
	args := map[string]any{"operation": "hover", "filePath": "source.py", "line": 1, "character": 1}
	result, err := tool.Execute(ctx, args)
	if err != nil || !result.Success || strings.Contains(result.Content, "secret.py") || !strings.Contains(result.Content, `"omitted_locations": 1`) {
		t.Fatalf("filtered=%+v %v", result, err)
	}
	args["filePath"] = "secret.py"
	result, err = tool.Execute(ctx, args)
	if err != nil || result.Success || result.PolicyBlock == nil {
		t.Fatalf("denied input=%+v %v", result, err)
	}
	args["filePath"] = filepath.Join(t.TempDir(), "outside.py")
	result, _ = tool.Execute(ctx, args)
	if result.Success {
		t.Fatal("outside source accepted")
	}
	if ClaudeHookToolName(tool) != "LSP" || !IsReadOnlyForPlanMode("lsp") {
		t.Fatal("LSP hook/plan identity missing")
	}
}
func TestLSPDiagnosticsFollowActualMutationAndRespectDenies(t *testing.T) {
	work, tool, registry := lspToolFixture(t)
	os.WriteFile(filepath.Join(work, "source.py"), []byte("TYPE_ERROR"), 0600)
	call := &genai.FunctionCall{Name: "batch", Args: map[string]any{"file_path": "secret.py"}}
	result := NewSuccessResultWithData("written", map[string]any{"changed": true, "written_paths": []string{filepath.Join(work, "source.py")}})
	AppendLSPDiagnostics(t.Context(), registry, call, &result)
	if !strings.Contains(result.Content, "fixture type mismatch") {
		t.Fatalf("diagnostics missed actual destination: %+v", result)
	}
	permissions := permission.NewManager(nil, true)
	denies, err := permission.ParseTemporaryToolDenyList("LSP")
	if err != nil {
		t.Fatal(err)
	}
	permissions.SetRunToolRules(nil, denies)
	ctx := ContextWithLSPReadPolicy(t.Context(), permissions, registry, work)
	denied := NewSuccessResult("written")
	AppendLSPDiagnostics(ctx, registry, &genai.FunctionCall{Name: "write", Args: map[string]any{"file_path": "source.py"}}, &denied)
	if denied.Content != "written" {
		t.Fatal("denied LSP still diagnosed", denied.Content)
	}
	noop := NewSuccessResultWithData("no changes", map[string]any{"changed": false, "written_paths": []string{filepath.Join(work, "source.py")}})
	AppendLSPDiagnostics(t.Context(), registry, call, &noop)
	if noop.Content != "no changes" {
		t.Fatal("no-op diagnosed")
	}
	clone := CloneToolForWorkDir(tool, "").(*LSPTool)
	if clone == tool || clone.Manager() != tool.Manager() {
		t.Fatal("shared-workspace clone lost scoped provider")
	}
	isolated := t.TempDir()
	os.WriteFile(filepath.Join(isolated, "source.py"), []byte("clean"), 0600)
	fork := CloneToolForWorkDir(tool, isolated).(*LSPTool)
	if fork.Manager() == nil || fork.Manager() == tool.Manager() {
		t.Fatal("isolated agent reused foreground provider")
	}
	got, err := fork.Execute(t.Context(), map[string]any{"operation": "hover", "filePath": "source.py", "line": 1, "character": 1})
	if err != nil || !got.Success {
		t.Fatalf("isolated LSP=%+v %v", got, err)
	}
}
