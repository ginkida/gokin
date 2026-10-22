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

func managedExecutorFixture(t *testing.T) (*Executor, *hooks.Manager, string, string) {
	t.Helper()
	executor, manager, root, userPath := settingsExecutorFixture(t, "exit 0")
	dir := t.TempDir()
	path := filepath.Join(dir, "managed-settings.json")
	if err := os.Rename(userPath, path); err != nil {
		t.Fatal(err)
	}
	manager.ConfigureClaudeManagedSettings(dir)
	manager.SetEnabled(false)
	return executor, manager, root, path
}

func TestManagedSettingsInvalidBeforeModelAndChangedBeforeToolEvenWhenDisabled(t *testing.T) {
	t.Run("before-model", func(t *testing.T) {
		executor, _, root, path := managedExecutorFixture(t)
		if err := os.WriteFile(path, []byte(`{"allowManagedHooksOnly":"true"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		mock := testkit.NewMockClient().EnqueueText("must not request")
		executor.SetClient(mock)
		if _, _, err := executor.Execute(t.Context(), nil, "Write."); !IsHookStopError(err) || len(mock.Calls()) != 0 {
			t.Fatalf("invalid managed policy reached model: %v calls=%d", err, len(mock.Calls()))
		}
		result, err := executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(root, "forbidden.txt"), "content": "forbidden"})
		if !IsHookStopError(err) && result.HookStopReason == "" {
			t.Fatalf("direct call bypassed malformed policy: %+v %v", result, err)
		}
		if _, err := os.Stat(filepath.Join(root, "forbidden.txt")); !os.IsNotExist(err) {
			t.Fatal("actual tool ran")
		}
	})
	t.Run("waiting-tool", func(t *testing.T) {
		executor, _, root, path := managedExecutorFixture(t)
		executor.SetHandler(&ExecutionHandler{OnToolStart: func(string, map[string]any) {
			if err := os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"exit 2"}]}]}}`), 0o600); err != nil {
				t.Error(err)
			}
		}})
		result := executor.doExecuteTool(t.Context(), &genai.FunctionCall{ID: "pending", Name: "write", Args: map[string]any{"file_path": filepath.Join(root, "forbidden.txt"), "content": "forbidden"}})
		if !strings.Contains(result.HookStopReason, "configuration changed") {
			t.Fatalf("pending call bypassed updated policy: %+v", result)
		}
		if _, err := os.Stat(filepath.Join(root, "forbidden.txt")); !os.IsNotExist(err) {
			t.Fatal("tool ran under old managed checks")
		}
	})
}

func TestManagedOnlyRejectsSkillActivationRestoreAndInheritedChecksThenRecovers(t *testing.T) {
	executor, skill, root := hookSkillFixture(t, "Write", "exit 0", "", "Review.")
	loadGuardSkill(t, executor)
	refs, _ := skill.InvocationLedger().HookActivations()
	managed := t.TempDir()
	path := filepath.Join(managed, "managed-settings.json")
	executor.hooks.ConfigureClaudeManagedSettings(managed)
	child := NewRegistry() // A delegated registry without Skill retains required parent checks.
	ctx := ContextWithDelegatedSkillHooks(executor.SkillInvocationContext(t.Context()), child, executor.registry, executor.hooks)
	if err := os.WriteFile(path, []byte(`{"allowManagedHooksOnly":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SkillHooksError(ctx); !IsHookStopError(err) || !strings.Contains(err.Error(), "allowManagedHooksOnly") {
		t.Fatalf("inherited scope bypassed policy: %v", err)
	}
	if err := ValidateSkillHookRestore(executor.registry, executor.hooks, refs); err == nil || !strings.Contains(err.Error(), "allowManagedHooksOnly") {
		t.Fatalf("restore bypassed policy: %v", err)
	}
	mock := testkit.NewMockClient().EnqueueText("must not request")
	executor.SetClient(mock)
	if _, _, err := executor.Execute(t.Context(), nil, "Continue."); !IsHookStopError(err) || len(mock.Calls()) != 0 {
		t.Fatalf("required skill silently skipped: %v calls=%d", err, len(mock.Calls()))
	}
	if result, err := executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(root, "forbidden.txt"), "content": "forbidden"}); !IsHookStopError(err) && result.HookStopReason == "" {
		t.Fatalf("active skill check bypassed: %+v %v", result, err)
	}
	if err := os.WriteFile(path, []byte(`{"allowManagedHooksOnly":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSkillHooks(executor.registry, executor.hooks); err != nil {
		t.Fatalf("policy recovery failed: %v", err)
	}
	if err := SkillHooksError(ctx); err != nil {
		t.Fatalf("inherited scope did not recover: %v", err)
	}
	// A new activation is rejected before hooks or instructions are published.
	fresh, freshSkill, _ := hookSkillFixture(t, "Write", "exit 0", "", "Review.")
	fresh.hooks.ConfigureClaudeManagedSettings(managed)
	if err := os.WriteFile(path, []byte(`{"allowManagedHooksOnly":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := fresh.InvokeTool(t.Context(), "skill", map[string]any{"name": "guard"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "allowManagedHooksOnly") || len(freshSkill.HookScope().Hooks()) != 0 || len(freshSkill.InvocationLedger().SnapshotNewestFirst()) != 0 {
		t.Fatalf("blocked skill published state: %+v", result)
	}
}
