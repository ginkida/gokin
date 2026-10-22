package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeClaudeSettingsTestFile(t *testing.T, path string, settings any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func settingsTestEvent(event, matcher, command string) map[string]any {
	return map[string]any{"hooks": map[string]any{event: []any{map[string]any{"matcher": matcher,
		"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 2}}}}}}
}

func TestClaudeSettingsMergeDeduplicateAndRefreshWithoutSkill(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	manager := NewManager(true, root)
	manager.ConfigureClaudeSettings(root, user, true)
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), settingsTestEvent("PreToolUse", "Write", "printf x >> duplicated"))
	writeClaudeSettingsTestFile(t, filepath.Join(root, ".claude", "settings.json"), settingsTestEvent("PreToolUse", "Edit, Write", "printf x >> duplicated"))
	localPath := filepath.Join(root, ".claude", "settings.local.json")
	writeClaudeSettingsTestFile(t, localPath, settingsTestEvent("PreToolUse", "Write", "printf x >> local"))
	for i := 0; i < 2; i++ {
		if results := manager.RunPreTool(t.Context(), "write", nil); len(results) != 2 || results[0].Error != nil || results[1].Error != nil {
			t.Fatalf("settings did not merge/deduplicate: %+v", results)
		}
	}
	for _, filename := range []string{"duplicated", "local"} {
		data, err := os.ReadFile(filepath.Join(root, filename))
		if err != nil || string(data) != "xx" {
			t.Fatalf("%s: %q %v", filename, data, err)
		}
	}
	all := manager.GetHooks()
	if len(all) != 3 || !strings.Contains(all[0].Source, "claude:user") || !strings.Contains(all[2].Source, "claude:local") {
		t.Fatalf("source diagnostics: %+v", all)
	}
	writeClaudeSettingsTestFile(t, localPath, settingsTestEvent("PreToolUse", "Write", "printf edited >&2; exit 2"))
	if result, blocked := Blocked(manager.RunPreTool(t.Context(), "write", nil)); !blocked || result.BlockReason != "edited" {
		t.Fatalf("edit not applied: %+v %t", result, blocked)
	}
	manager.ConfigureClaudeSettings(root, user, false)
	if results := manager.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Error != nil {
		t.Fatalf("revoked trust retained project hooks or lost user hooks: %+v", results)
	}
}

func TestClaudeSettingsParallelHandlersAndOnceIgnored(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	manager := NewManager(true, root)
	manager.ConfigureClaudeSettings(root, user, true)
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"hooks": map[string]any{"PreToolUse": []any{
		map[string]any{"matcher": "Bash", "hooks": []any{
			map[string]any{"type": "command", "command": "while [ ! -f second-ready ]; do sleep 0.01; done; printf x >> first", "timeout": 2, "once": true},
			map[string]any{"type": "command", "command": "touch second-ready; printf x >> second", "timeout": 2},
		}},
	}}})
	for i := 0; i < 2; i++ {
		if results := manager.RunPreTool(t.Context(), "bash", nil); len(results) != 2 || results[0].Error != nil || results[1].Error != nil {
			t.Fatalf("settings handlers did not run in parallel: %+v", results)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "first"))
	if err != nil || string(data) != "xx" {
		t.Fatalf("settings once incorrectly consumed: %q %v", data, err)
	}
}

func TestClaudeSettingsDisablePrecedenceAndUntrustedProjectIsolation(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	manager := NewManager(true, root)
	manager.ConfigureClaudeSettings(root, user, true)
	userConfig := settingsTestEvent("PreToolUse", "Write", "printf enabled")
	userConfig["disableAllHooks"] = true
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), userConfig)
	if results := manager.RunPreTool(t.Context(), "write", nil); len(results) != 0 || manager.IsEnabled() {
		t.Fatalf("disableAllHooks not honored: %+v", results)
	}
	projectPath := filepath.Join(root, ".claude", "settings.json")
	writeClaudeSettingsTestFile(t, projectPath, map[string]any{"disableAllHooks": false})
	if results := manager.RunPreTool(t.Context(), "write", nil); len(results) != 1 || !manager.IsEnabled() {
		t.Fatalf("project false did not override user true: %+v", results)
	}
	writeClaudeSettingsTestFile(t, filepath.Join(root, ".claude", "settings.local.json"), map[string]any{"disableAllHooks": true})
	if results := manager.RunPreTool(t.Context(), "write", nil); len(results) != 0 || manager.IsEnabled() {
		t.Fatal("local precedence lost")
	}
	if err := os.WriteFile(projectPath, []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.ConfigureClaudeSettings(root, user, false)
	if err := manager.CheckReady(); err != nil || manager.IsEnabled() {
		t.Fatalf("untrusted project affected hook policy: %v", err)
	}
}

func TestClaudeSettingsInvalidFileCannotSilentlyRemoveChecksAndCanRecover(t *testing.T) {
	invalid := []string{
		`{"hooks":{"PreToolUse":[{"hooks":[{"type":"unsupported-handler","prompt":"check"}]}]}}`,
		`{"hooks":{"UnsupportedEvent":[]}}`,
		`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"exit 0","unknownControl":true}]}]}}`,
		`{"hooks":{},"hooks":{}}`, `{"disableAllHooks":"false"}`,
		`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"exit 0","command":"exit 2"}]}]}}`,
		`[]`, `{}`, // The empty object is replaced with an oversized file below.
		`{"hooks":{"PreToolUse":[{"matcher":42,"hooks":[{"type":"command","command":"exit 0"}]}]}}`,
		`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":123}]}]}}`,
		`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"exit 0","args":null}]}]}}`,
		`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"exit 0","args":[42]}]}]}}`,
		`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"exit 0","timeout":"10"}]}]}}`,
	}
	for _, input := range invalid {
		t.Run(input, func(t *testing.T) {
			root, user := t.TempDir(), t.TempDir()
			manager := NewManager(true, root)
			manager.ConfigureClaudeSettings(root, user, true)
			path := filepath.Join(user, "settings.json")
			writeClaudeSettingsTestFile(t, path, settingsTestEvent("PreToolUse", "Write", "printf checked"))
			if err := manager.CheckReady(); err != nil {
				t.Fatal(err)
			}
			if input == `{}` {
				input = strings.Repeat(" ", maxClaudeSettingsBytes+1)
			}
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := manager.CheckReady(); err == nil {
				t.Fatal("invalid settings accepted")
			}
			results := manager.RunPreTool(t.Context(), "write", nil)
			if len(results) != 1 || results[0].StopReason == "" || results[0].Error == nil {
				t.Fatalf("invalid settings did not stop execution: %+v", results)
			}
			writeClaudeSettingsTestFile(t, path, settingsTestEvent("PreToolUse", "Write", "printf checked"))
			if results := manager.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Output != "checked" {
				t.Fatalf("repair did not recover: %+v", results)
			}
		})
	}
}

func TestClaudeSettingsReadFailureRecoversWhenOriginalFileReturns(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	manager := NewManager(true, root)
	manager.ConfigureClaudeSettings(root, user, false)
	path := filepath.Join(user, "settings.json")
	writeClaudeSettingsTestFile(t, path, settingsTestEvent("PreToolUse", "Write", "printf restored"))
	if err := manager.CheckReady(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.CheckReady(); err == nil {
		t.Fatal("nonregular settings path accepted")
	}
	if err := os.Rename(path, path+".directory"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	if results := manager.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Output != "restored" {
		t.Fatalf("original file did not recover: %+v", results)
	}
}

func TestClaudeSettingsStopAndSubagentStopHaveSeparateScopeAndMatcher(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	manager := NewManager(true, root)
	manager.ConfigureClaudeSettings(root, user, true)
	settings := settingsTestEvent("Stop", "", "printf main")
	settings["hooks"].(map[string]any)["SubagentStop"] = settingsTestEvent("SubagentStop", "general-purpose", "printf child")["hooks"].(map[string]any)["SubagentStop"]
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), settings)
	if results := manager.RunStop(t.Context(), "Done."); len(results) != 1 || results[0].Output != "main" {
		t.Fatalf("foreground stop mismatch: %+v", results)
	}
	ctx := ContextWithRuntimeInfo(t.Context(), RuntimeInfo{AgentID: "child-id", AgentType: "general-purpose"})
	if results := manager.RunStop(ctx, "Done."); len(results) != 1 || results[0].Output != "child" {
		t.Fatalf("child stop mismatch: %+v", results)
	}
}

func TestClaudeMatcherExactListsAndRegex(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		match         bool
	}{
		{"Bash", "Bash", true}, {"Bash", "MyBash", false}, {"Edit", "NotebookEdit", false},
		{"Edit|Write", "Write", true}, {"Edit, Write", "Write", true}, {"Edit, Write", "NotebookEdit", false},
		{"Edit.*", "NotebookEdit", true}, {"^Edit$", "NotebookEdit", false}, {"mcp__memory__.*", "mcp__memory__read", true},
	} {
		matcher, err := compileClaudeMatcher(tc.pattern)
		if err != nil || matcher.MatchString(tc.name) != tc.match {
			t.Fatalf("%q on %q: %v", tc.pattern, tc.name, err)
		}
	}
}
