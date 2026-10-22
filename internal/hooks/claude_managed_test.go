package hooks

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func managedTestManager(t *testing.T, enabled, trusted bool) (*Manager, string, string, string) {
	t.Helper()
	root, user, managed := t.TempDir(), t.TempDir(), t.TempDir()
	m := NewManager(enabled, root)
	m.ConfigureClaudeSettings(root, user, trusted)
	m.ConfigureClaudeManagedSettings(managed)
	return m, root, user, managed
}

func assertManagedMarker(t *testing.T, root, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if want == "" {
		if !os.IsNotExist(err) {
			t.Fatalf("excluded %s executed: %q %v", name, data, err)
		}
	} else if err != nil || string(data) != want {
		t.Fatalf("%s=%q want %q (%v)", name, data, want, err)
	}
}

func TestManagedHooksSurviveUserDisableNativeDisableAndUntrustedWorkspace(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		for _, trusted := range []bool{true, false} {
			t.Run(fmt.Sprintf("enabled=%t/trusted=%t", enabled, trusted), func(t *testing.T) {
				m, root, user, managed := managedTestManager(t, enabled, trusted)
				writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), settingsTestEvent("PreToolUse", "Write", "printf x >> mandatory"))
				lower := settingsTestEvent("PreToolUse", "Write", "printf x >> optional")
				lower["disableAllHooks"] = true
				writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), lower)
				writeClaudeSettingsTestFile(t, filepath.Join(root, ".claude", "settings.local.json"), lower)
				m.AddHook(&Hook{Type: PreTool, Enabled: true, Command: "printf x >> native"})
				results := m.RunPreTool(t.Context(), "write", nil)
				if len(results) != 1 || results[0].Error != nil || !m.IsEnabled() || m.SkillHooksAllowed() {
					t.Fatalf("mandatory hooks lost: %+v", results)
				}
				if !results[0].Hook.claudeManaged || !strings.HasPrefix(results[0].Hook.Source, "claude:managed:") {
					t.Fatalf("lost authority: %+v", results[0].Hook)
				}
				assertManagedMarker(t, root, "mandatory", "x")
				assertManagedMarker(t, root, "optional", "")
				assertManagedMarker(t, root, "native", "")
			})
		}
	}
}

func TestManagedOnlyIgnoresLowerFilesAndCannotBeOverriddenOrForged(t *testing.T) {
	m, root, user, managed := managedTestManager(t, true, true)
	policy := settingsTestEvent("PreToolUse", "Write", "printf x >> mandatory")
	policy["allowManagedHooksOnly"] = true
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), policy)
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"allowManagedHooksOnly": false, "disableAllHooks": true})
	project := filepath.Join(root, ".claude", "settings.json")
	writeClaudeSettingsTestFile(t, project, map[string]any{})
	if err := os.WriteFile(project, []byte("invalid ignored lower tier"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Public provenance strings cannot confer organizational authority.
	m.AddHook(&Hook{Type: PreTool, Enabled: true, Source: "claude:managed:forged", Command: "printf x >> forged"})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Error != nil || !m.ManagedHooksOnly() {
		t.Fatalf("lower tier bypassed managed policy: %+v", results)
	}
	assertManagedMarker(t, root, "mandatory", "x")
	assertManagedMarker(t, root, "forged", "")
	if err := m.SkillHookPolicyError(); err == nil || !strings.Contains(err.Error(), "allowManagedHooksOnly") {
		t.Fatalf("skill policy=%v", err)
	}
	// The same key in user settings has no policy authority.
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{})
	lower := settingsTestEvent("PreToolUse", "Write", "printf x >> optional")
	lower["allowManagedHooksOnly"] = true
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), lower)
	writeClaudeSettingsTestFile(t, project, map[string]any{})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 2 || !m.SkillHooksAllowed() || m.ManagedHooksOnly() {
		t.Fatalf("lower managed-only key gained authority: %+v", results)
	}
	assertManagedMarker(t, root, "optional", "x")
}

func TestManagedDisablePrecedenceAndLiveEnableToggle(t *testing.T) {
	m, root, user, managed := managedTestManager(t, true, true)
	lower := settingsTestEvent("PreToolUse", "Write", "printf x >> optional")
	lower["disableAllHooks"] = true
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), lower)
	path := filepath.Join(managed, "managed-settings.json")
	policy := settingsTestEvent("PreToolUse", "Write", "printf x >> mandatory")
	policy["disableAllHooks"] = false
	writeClaudeSettingsTestFile(t, path, policy)
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 2 || !m.SkillHooksAllowed() {
		t.Fatalf("managed false lost precedence: %+v", results)
	}
	revision := m.ExecutionRevision()
	m.SetEnabled(false)
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || m.SkillHooksAllowed() {
		t.Fatalf("native flag disabled mandatory or retained optional: %+v", results)
	}
	if err := m.CheckExecutionRevision(revision); err == nil {
		t.Fatal("pending tool retained stale policy")
	}
	m.SetEnabled(true)
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 2 {
		t.Fatalf("enable did not reload ordinary settings: %+v", results)
	}
	policy["disableAllHooks"] = true
	writeClaudeSettingsTestFile(t, path, policy)
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 0 || m.IsEnabled() || !m.ClaudeHooksDisabled() {
		t.Fatalf("managed disable did not disable all: %+v", results)
	}
	assertManagedMarker(t, root, "mandatory", "xxx")
	assertManagedMarker(t, root, "optional", "xx")
}

func TestManagedDropInsOrderingDedupRemovalAndEmptyFile(t *testing.T) {
	m, root, user, managed := managedTestManager(t, true, false)
	base := filepath.Join(managed, "managed-settings.json")
	if err := os.WriteFile(base, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(managed, "managed-settings.d")
	first := settingsTestEvent("PreToolUse", "Write", "printf x >> duplicate")
	first["allowManagedHooksOnly"] = true
	writeClaudeSettingsTestFile(t, filepath.Join(dir, "10-base.json"), first)
	second := settingsTestEvent("PreToolUse", "Edit|Write", "printf x >> duplicate")
	second["allowManagedHooksOnly"] = false
	writeClaudeSettingsTestFile(t, filepath.Join(dir, "20-final.json"), second)
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), settingsTestEvent("PreToolUse", "Write", "printf x >> optional"))
	for _, ignored := range []string{".hidden.json", "README.txt"} {
		if err := os.WriteFile(filepath.Join(dir, ignored), []byte("malformed"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 2 || m.ManagedHooksOnly() {
		t.Fatalf("alphabetical merge/dedup failed: %+v", results)
	}
	assertManagedMarker(t, root, "duplicate", "x")
	assertManagedMarker(t, root, "optional", "x")
	writeClaudeSettingsTestFile(t, filepath.Join(dir, "30-remove.json"), map[string]any{"hooks": map[string]any{"PreToolUse": nil}})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Hook.claudeManaged {
		t.Fatalf("event null did not remove earlier managed checks: %+v", results)
	}
	writeClaudeSettingsTestFile(t, filepath.Join(dir, "40-readd.json"), settingsTestEvent("PreToolUse", "Write", "printf x >> later"))
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 2 {
		t.Fatalf("re-add after removal failed: %+v", results)
	}
	writeClaudeSettingsTestFile(t, filepath.Join(dir, "50-clear.json"), map[string]any{"hooks": nil, "allowManagedHooksOnly": nil})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 {
		t.Fatalf("hooks null did not remove whole managed block: %+v", results)
	}
	// A drop-in remains authoritative without a base file.
	if err := os.Remove(base); err != nil {
		t.Fatal(err)
	}
	writeClaudeSettingsTestFile(t, filepath.Join(dir, "60-readd.json"), settingsTestEvent("PreToolUse", "Write", "printf x >> last"))
	m.SetEnabled(false)
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || !results[0].Hook.claudeManaged {
		t.Fatalf("drop-in without base lost enforcement: %+v", results)
	}
	assertManagedMarker(t, root, "last", "x")
}

func TestManagedInvalidSettingsFailClosedEvenWhenOrdinaryHooksDisabledAndRepair(t *testing.T) {
	invalid := []string{"[]", "broken", `{"hooks":{},"hooks":{}}`, `{"allowManagedHooksOnly":"true"}`, `{"disableAllHooks":42}`, `{"hooks":{"PreToolUse":[{"hooks":[{"type":"http","url":"file:///etc/passwd"}]}]}}`, `{"hooks":{"UnsupportedEvent":[]}}`}
	for _, input := range invalid {
		t.Run(input, func(t *testing.T) {
			m, _, _, managed := managedTestManager(t, false, false)
			path := filepath.Join(managed, "managed-settings.json")
			writeClaudeSettingsTestFile(t, path, settingsTestEvent("PreToolUse", "Write", "printf checked"))
			good, _ := os.ReadFile(path)
			if err := m.CheckReady(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := m.CheckReady(); err == nil {
				t.Fatal("disabled ordinary hooks bypassed malformed managed file")
			}
			if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].StopReason == "" {
				t.Fatalf("invalid mandatory hook silently skipped: %+v", results)
			}
			if err := os.WriteFile(path, good, 0o600); err != nil {
				t.Fatal(err)
			}
			if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Error != nil || results[0].Output != "checked" {
				t.Fatalf("identical repair failed: %+v", results)
			}
		})
	}
	// An invalid newly discovered policy cannot inherit the old absent state.
	m, _, _, managed := managedTestManager(t, false, false)
	if err := m.CheckReady(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managed, "managed-settings.json"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckReady(); err == nil {
		t.Fatal("new malformed policy ignored")
	}
}

func TestManagedUnreadableTypeAndBoundedSourceSet(t *testing.T) {
	m, _, _, managed := managedTestManager(t, false, false)
	path := filepath.Join(managed, "managed-settings.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckReady(); err == nil {
		t.Fatal("directory masquerading as policy accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeClaudeSettingsTestFile(t, path, settingsTestEvent("PreToolUse", "Write", "printf checked"))
	if err := m.CheckReady(); err != nil {
		t.Fatalf("read failure did not recover: %v", err)
	}
	dir := filepath.Join(managed, "managed-settings.d")
	for i := 0; i < maxManagedSettingsFiles; i++ {
		writeClaudeSettingsTestFile(t, filepath.Join(dir, fmt.Sprintf("%03d.json", i)), map[string]any{})
	}
	if err := m.CheckReady(); err == nil || !strings.Contains(err.Error(), "256 files") {
		t.Fatalf("source set unbounded: %v", err)
	}
}

func TestManagedPolicyConcurrentDiagnosticsAndExecution(t *testing.T) {
	m, _, _, managed := managedTestManager(t, false, false)
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), settingsTestEvent("PreToolUse", "Write", "exit 0"))
	if err := m.CheckReady(); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 20; j++ {
				if j%2 == 0 {
					m.GetHooks()
				} else {
					m.RunPreTool(t.Context(), "write", nil)
				}
			}
		}()
	}
	workers.Wait()
}

func TestManagedPolicyNullRestoresLowerTierWithoutRemovingManagedHooks(t *testing.T) {
	m, _, user, managed := managedTestManager(t, true, false)
	lower := settingsTestEvent("PreToolUse", "Write", "exit 0")
	lower["disableAllHooks"] = true
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), lower)
	base := settingsTestEvent("PreToolUse", "Write", "printf mandatory")
	base["disableAllHooks"], base["allowManagedHooksOnly"] = true, true
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), base)
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 0 {
		t.Fatalf("managed disable lost: %+v", results)
	}
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.d", "final.json"), map[string]any{"disableAllHooks": nil, "allowManagedHooksOnly": nil})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Output != "mandatory" || m.ManagedHooksOnly() || m.SkillHooksAllowed() {
		t.Fatalf("scalar null did not restore lower disable authority: %+v", results)
	}
	lower["disableAllHooks"] = false
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), lower)
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 2 || !m.SkillHooksAllowed() {
		t.Fatalf("scalar null retained stale managed-only value: %+v", results)
	}
}

func TestManagedCombinedByteAndHandlerLimits(t *testing.T) {
	t.Run("bytes", func(t *testing.T) {
		m, _, _, managed := managedTestManager(t, false, false)
		dir := filepath.Join(managed, "managed-settings.d")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		data := bytes.Repeat([]byte(" "), maxClaudeSettingsBytes)
		for i := 0; i < 17; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d.json", i)), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.CheckReady(); err == nil || !strings.Contains(err.Error(), "16 MiB in total") {
			t.Fatalf("combined read budget unenforced: %v", err)
		}
	})
	t.Run("handlers", func(t *testing.T) {
		m, _, _, managed := managedTestManager(t, false, false)
		var commands []any
		for i := 0; i < 129; i++ {
			commands = append(commands, map[string]any{"type": "command", "command": fmt.Sprintf("printf %d", i)})
		}
		file := map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"hooks": commands}}}}
		writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), file)
		writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.d", "final.json"), file)
		if err := m.CheckReady(); err == nil || !strings.Contains(err.Error(), "256 handlers") {
			t.Fatalf("combined handler budget unenforced: %v", err)
		}
	})
}

func TestManagedAndOrdinaryAliasedPathKeepSeparateAuthority(t *testing.T) {
	m, _, _, managed := managedTestManager(t, true, false)
	dir := filepath.Join(managed, "managed-settings.d")
	shared := settingsTestEvent("PreToolUse", "Write", "printf mandatory")
	shared["hooks"].(map[string]any)["PostToolUse"] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "exit 0"}}}}
	writeClaudeSettingsTestFile(t, filepath.Join(dir, "settings.json"), shared)
	writeClaudeSettingsTestFile(t, filepath.Join(dir, "z-final.json"), map[string]any{"hooks": map[string]any{"PostToolUse": nil}})
	// Even an alternate user directory pointing at managed drop-ins must not
	// replace the organizational registration with a lower-tier scope entry.
	m.ConfigureClaudeSettings("", dir, false)
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || !results[0].Hook.claudeManaged {
		t.Fatalf("lower aliased path replaced mandatory authority: %+v", results)
	}
	all := m.GetHooks()
	managedCount := 0
	for _, hook := range all {
		if hook.claudeManaged {
			managedCount++
		}
	}
	if managedCount != 1 || len(all) != 3 {
		t.Fatalf("source identity collided: managed=%d total=%d", managedCount, len(all))
	}
	m.SetEnabled(false)
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || !results[0].Hook.claudeManaged {
		t.Fatalf("native toggle lost aliased mandatory registration: %+v", results)
	}
}

func TestManagedPolicyCannotSuppressRequiredSkillStopAfterResponse(t *testing.T) {
	m, _, _, managed := managedTestManager(t, true, false)
	scope := &Scope{}
	registration, err := scope.Prepare("guard", []ClaudeCommand{{Event: "Stop", Command: "printf required", Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	ctx := ContextWithScope(t.Context(), scope)
	if results := m.RunStopInDir(ctx, "", "answer"); len(results) != 1 || results[0].Output != "required" {
		t.Fatalf("initial Stop=%+v", results)
	}
	path := filepath.Join(managed, "managed-settings.json")
	writeClaudeSettingsTestFile(t, path, map[string]any{"allowManagedHooksOnly": true})
	if results := m.RunStopInDir(ctx, "", "answer"); len(results) != 1 || !strings.Contains(results[0].StopReason, "allowManagedHooksOnly") {
		t.Fatalf("required Stop silently skipped: %+v", results)
	}
	writeClaudeSettingsTestFile(t, path, map[string]any{"allowManagedHooksOnly": false})
	if results := m.RunStopInDir(ctx, "", "answer"); len(results) != 1 || results[0].Output != "required" {
		t.Fatalf("required Stop did not recover: %+v", results)
	}
}

func TestManagedRemovedHandlersAreNotCompiledButMalformedJSONStillFails(t *testing.T) {
	m, _, _, managed := managedTestManager(t, false, false)
	base := filepath.Join(managed, "managed-settings.json")
	writeClaudeSettingsTestFile(t, base, map[string]any{"hooks": map[string]any{"SessionStart": "removed unsupported schema"}})
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.d", "final.json"), map[string]any{"hooks": map[string]any{"SessionStart": nil, "PreToolUse": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "printf checked"}}}}}})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Error != nil || results[0].Output != "checked" {
		t.Fatalf("removed handler still compiled: %+v", results)
	}
	if err := os.WriteFile(base, []byte("invalid JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckReady(); err == nil {
		t.Fatal("null removal bypassed source JSON validation")
	}
}
