package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
	"gokin/internal/testkit"
	"google.golang.org/genai"
)

func pluginExecutorFixture(t *testing.T) (*Executor, *hooks.Manager, string, string) {
	t.Helper()
	executor, manager, root, source := settingsExecutorFixture(t, "exit 0")
	plugin := t.TempDir()
	path := filepath.Join(plugin, "hooks", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, path); err != nil {
		t.Fatal(err)
	}
	manager.SetEnabled(false)
	manager.ConfigureClaudePlugins([]string{plugin})
	return executor, manager, root, path
}

func TestPluginInvalidConfigurationBlocksBeforeModelAndPendingConfigurationStopsTool(t *testing.T) {
	t.Run("before-model", func(t *testing.T) {
		executor, _, root, path := pluginExecutorFixture(t)
		if err := os.WriteFile(path, []byte(`{"PreToolUse":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		mock := testkit.NewMockClient().EnqueueText("must not request")
		executor.SetClient(mock)
		if _, _, err := executor.Execute(t.Context(), nil, "Write."); !IsHookStopError(err) || len(mock.Calls()) != 0 {
			t.Fatalf("invalid explicit plugin reached provider: %v calls=%d", err, len(mock.Calls()))
		}
		result, err := executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(root, "forbidden.txt"), "content": "forbidden"})
		if !IsHookStopError(err) && result.HookStopReason == "" {
			t.Fatalf("direct call bypassed plugin: %+v %v", result, err)
		}
		if _, err := os.Stat(filepath.Join(root, "forbidden.txt")); !os.IsNotExist(err) {
			t.Fatal("actual write bypassed failed plugin")
		}
	})
	t.Run("waiting-tool", func(t *testing.T) {
		executor, _, root, path := pluginExecutorFixture(t)
		executor.SetHandler(&ExecutionHandler{OnToolStart: func(string, map[string]any) {
			if err := os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"exit 2"}]}]}}`), 0o600); err != nil {
				t.Error(err)
			}
		}})
		result := executor.doExecuteTool(t.Context(), &genai.FunctionCall{ID: "pending", Name: "write", Args: map[string]any{"file_path": filepath.Join(root, "forbidden.txt"), "content": "forbidden"}})
		if !strings.Contains(result.HookStopReason, "configuration changed") {
			t.Fatalf("pending tool retained stale plugin checks: %+v", result)
		}
		if _, err := os.Stat(filepath.Join(root, "forbidden.txt")); !os.IsNotExist(err) {
			t.Fatal("tool ran under stale plugin checks")
		}
	})
}
