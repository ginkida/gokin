package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
	"gokin/internal/lsp"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"google.golang.org/genai"
)

func TestLSPFixtureHelper(t *testing.T) { testkit.RunLSPFixture(t) }

func TestAgentMutationReturnsPluginLSPDiagnostics(t *testing.T) {
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plugin, user := t.TempDir(), t.TempDir()
	binary, _ := os.Executable()
	manifest, _ := json.Marshal(map[string]any{"name": "suite", "lspServers": map[string]any{"python": map[string]any{"command": binary, "args": []string{"-test.run=^TestLSPFixtureHelper$"}, "extensionToLanguage": map[string]string{".py": "python"}, "env": map[string]string{"GOKIN_LSP_FIXTURE": "1"}}}})
	os.MkdirAll(filepath.Join(plugin, ".claude-plugin"), 0700)
	os.WriteFile(filepath.Join(plugin, ".claude-plugin", "plugin.json"), manifest, 0600)
	authority := hooks.NewManager(false, work)
	authority.ConfigureClaudeManagedSettings(t.TempDir())
	authority.ConfigureClaudeSettings(work, user, false)
	authority.ConfigureClaudePlugins([]string{plugin})
	manager, err := lsp.NewManager(t.Context(), authority, work)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewWriteTool(work))
	tool := tools.NewLSPTool(work)
	tool.SetManager(manager)
	registry.MustRegister(tool)
	agent := NewAgent(AgentTypeGeneral, testkit.NewMockClient(), registry, work, 3, "", nil, nil)
	agent.reflector = nil
	agent.SetHooks(authority)
	result := agent.executeToolWithReflection(t.Context(), &genai.FunctionCall{ID: "lsp-write", Name: "write", Args: map[string]any{"file_path": filepath.Join(work, "source.py"), "content": "TYPE_ERROR"}})
	content, _ := result.Response.Response["content"].(string)
	if success, _ := result.Response.Response["success"].(bool); !success || !strings.Contains(content, "fixture type mismatch") {
		t.Fatalf("agent result=%+v", result.Response.Response)
	}
	for _, typ := range []AgentType{AgentTypeExplore, AgentTypePlan} {
		found := false
		for _, name := range typ.AllowedTools() {
			if name == "lsp" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s lacks LSP navigation", typ)
		}
	}
}
