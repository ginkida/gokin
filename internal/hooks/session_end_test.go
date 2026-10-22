package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionEndSchemaDefaultAndExplicitTimeoutFingerprints(t *testing.T) {
	definitions, err := parseClaudeTest(t, `SessionEnd: [{matcher: clear, hooks: [{type: command, command: echo cleanup}]}]`)
	if err != nil || len(definitions) != 1 || definitions[0].Timeout != 1500*time.Millisecond || definitions[0].TimeoutExplicit {
		t.Fatalf("end schema: %+v %v", definitions, err)
	}
	explicit, err := parseClaudeTest(t, `SessionEnd: [{matcher: clear, hooks: [{type: command, command: echo cleanup, timeout: 1.5}]}]`)
	if err != nil || !explicit[0].TimeoutExplicit || CommandFingerprint(definitions) == CommandFingerprint(explicit) {
		t.Fatal("end default/explicit budget distinction was lost", err)
	}
	for _, handler := range []string{"prompt", "agent"} {
		if _, err := parseClaudeTest(t, `SessionEnd: [{hooks: [{type: `+handler+`, prompt: verify}]}]`); err == nil {
			t.Fatal("SessionEnd accepted model handler")
		}
	}
}

func TestSessionEndSettingsBudgetAndEnvironmentOverride(t *testing.T) {
	root, user, plugin := t.TempDir(), t.TempDir(), t.TempDir()
	m := NewManager(true, root)
	m.ConfigureClaudeSettings(root, user, true)
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), settingsTestEvent("SessionEnd", "clear", "exit 0")) // explicit two seconds
	writeClaudeSettingsTestFile(t, filepath.Join(plugin, "hooks/hooks.json"), map[string]any{"hooks": map[string]any{"SessionEnd": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "exit 0", "timeout": 20}}}}}})
	m.ConfigureClaudePlugins([]string{plugin})
	for _, test := range []struct {
		env, reason string
		budget, per time.Duration
	}{
		{"", "clear", 2 * time.Second, 1500 * time.Millisecond},
		{"", "resume", 1500 * time.Millisecond, 1500 * time.Millisecond},
		{"250", "clear", 250 * time.Millisecond, 250 * time.Millisecond},
		{"90000", "clear", 60 * time.Second, 60 * time.Second},
		{"-1", "clear", 2 * time.Second, 1500 * time.Millisecond},
	} {
		t.Setenv("CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS", test.env)
		budget, per := m.SessionEndBudget(test.reason)
		if budget != test.budget || per != test.per {
			t.Fatalf("budget for %+v: %v %v", test, budget, per)
		}
	}
}

func TestSessionEndRunsAfterCancellationDiscardsDecisionsAndCancelsProcessTree(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	m := NewManager(true, root)
	m.ConfigureClaudeSettings(root, user, true)
	t.Setenv("CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS", "100")
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"hooks": map[string]any{"SessionEnd": []any{map[string]any{"matcher": "clear", "hooks": []any{
		map[string]any{"type": "command", "command": `cat > end.json; printf '%s' '{"continue":false,"decision":"block","systemMessage":"ignored","hookSpecificOutput":{"additionalContext":"ignored"}}'`},
		map[string]any{"type": "command", "command": `touch started; sleep 2; printf leaked > leaked`},
	}}}}})
	ctx, cancel := context.WithCancel(ContextWithRuntimeInfo(t.Context(), RuntimeInfo{SessionID: "old-session", ToolNameOverride: "Bash", AgentID: "old-child"}))
	cancel()
	results := m.RunSessionEnd(ctx, root, "clear")
	if len(results) != 2 {
		t.Fatalf("end did not run: %+v", results)
	}
	for _, result := range results {
		if result.StopReason != "" || result.BlockReason != "" || result.AdditionalContext != "" || result.Output != "" {
			t.Fatalf("end output controlled termination: %+v", result)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "end.json"))
	var input map[string]any
	_ = json.Unmarshal(data, &input)
	if err != nil || input["session_id"] != "old-session" || input["reason"] != "clear" || input["tool_name"] != nil || input["agent_id"] != nil {
		t.Fatalf("wrong end identity: %s %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "started")); err != nil {
		t.Fatal("cancelled parent suppressed cleanup", err)
	}
	if _, err := os.Stat(filepath.Join(root, "leaked")); !os.IsNotExist(err) {
		t.Fatal("timed-out end command left side effects")
	}
}

func TestSessionEndCapturedSkillScopeSurvivesLiveReplacementWithoutConsumingNewOwner(t *testing.T) {
	root := t.TempDir()
	m := NewManager(true, root)
	owned := &Scope{}
	registration, err := owned.Prepare("guard", []ClaudeCommand{{Event: "SessionEnd", Matcher: "clear", Command: "printf completed >> end-runs", Once: true, Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	captured := owned.CaptureEvent(SessionEnd)
	owned.Clear()
	next, err := owned.Prepare("guard", []ClaudeCommand{{Event: "SessionEnd", Command: "printf wrong >> next-runs", Once: true, Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	next.Commit()
	results := m.RunSessionEnd(ContextWithScope(t.Context(), captured), root, "clear")
	if len(results) != 1 || results[0].Error != nil || !registration.Consumed()[0] || next.Consumed()[0] {
		t.Fatalf("captured end lost/moved once ownership: %+v", results)
	}
	data, _ := os.ReadFile(filepath.Join(root, "end-runs"))
	if strings.TrimSpace(string(data)) != "completed" || len(captured.ConsumedEntries()["skill:guard"]) != 1 {
		t.Fatal("captured cleanup was lost")
	}
}

func TestSessionEndHTTPAndMCPRunAfterParentCancellationWithoutControlOutput(t *testing.T) {
	for _, kind := range []string{"http", "mcp_tool"} {
		t.Run(kind, func(t *testing.T) {
			root, user := t.TempDir(), t.TempDir()
			m := NewManager(true, root)
			m.ConfigureClaudeSettings(root, user, true)
			input := make(chan map[string]any, 1)
			handler := map[string]any{"type": kind}
			body := `{"continue":false,"decision":"block","reason":"must be ignored","systemMessage":"ignored"}`
			if kind == "http" {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var payload map[string]any
					_ = json.NewDecoder(r.Body).Decode(&payload)
					input <- payload
					fmt.Fprint(w, body)
				}))
				defer server.Close()
				handler["url"] = server.URL
			} else {
				handler["server"], handler["tool"], handler["input"] = "fixture", "cleanup", map[string]any{"reason": "${reason}", "session_id": "${session_id}"}
				m.SetMCPHookInvoker(func(ctx context.Context, server, tool string, payload map[string]any) (MCPHookOutput, error) {
					input <- payload
					return MCPHookOutput{Text: body}, ctx.Err()
				})
			}
			writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"hooks": map[string]any{"SessionEnd": []any{map[string]any{"matcher": "resume", "hooks": []any{handler}}}}})
			ctx, cancel := context.WithCancel(ContextWithRuntimeInfo(t.Context(), RuntimeInfo{SessionID: "ending"}))
			cancel()
			results := m.RunSessionEnd(ctx, root, "resume")
			if len(results) != 1 || results[0].Error != nil || results[0].Output != "" || results[0].StopReason != "" {
				t.Fatalf("end handler failed or controlled output: %+v", results)
			}
			payload := <-input
			if payload["reason"] != "resume" || payload["session_id"] != "ending" {
				t.Fatalf("end payload lost: %+v", payload)
			}
		})
	}
}
