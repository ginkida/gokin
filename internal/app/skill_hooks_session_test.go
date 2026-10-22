package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/hooks"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestHeadlessSkillHookStopIsTerminalAndRetainsCompletedToolResults(t *testing.T) {
	for _, scenario := range []struct {
		event  string
		cancel bool
	}{
		{"PreToolUse", false}, {"PreToolUse", true}, {"PostToolUse", false}, {"PostToolUse", true},
	} {
		t.Run(fmt.Sprintf("%s_cancel_%t", scenario.event, scenario.cancel), func(t *testing.T) {
			cancelOnHook := scenario.cancel
			post := scenario.event == "PostToolUse"
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			mock := testkit.NewMockClient().EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueToolCall("write", map[string]any{"file_path": "artifact.txt"}).EnqueueText("Must not request this.")
			write := &appHeadlessScriptedTool{name: "write", results: []tools.ToolResult{tools.NewSuccessResult("completed write")}}
			application, executor := newHeadlessPolicyTestApp(t, mock, write)
			root := application.workDir
			path := filepath.Join(root, "guard", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("---\ndescription: Review code\nhooks:\n  "+scenario.event+":\n    - matcher: Write\n      hooks:\n        - type: command\n          command: \"printf '%s' '{\\\"continue\\\":false,\\\"stopReason\\\":\\\"HTTP 429 rate limit timeout is only the hook explanation\\\"}'\"\n---\nReview code."), 0o600); err != nil {
				t.Fatal(err)
			}
			skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), root)
			application.registry.MustRegister(skill)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			application.hooksManager = hooks.NewManager(true, root)
			if cancelOnHook {
				application.hooksManager.SetHandler(func(*hooks.Hook, string, error) { cancel() })
			}
			executor.SetHooks(application.hooksManager)
			executor.SetWorkDir(root)
			manager, err := chat.NewSessionManager(application.session, chat.DefaultSessionManagerConfig())
			if err != nil {
				t.Fatal(err)
			}
			application.sessionManager = manager
			var stdout bytes.Buffer
			result, err := application.RunHeadlessWithOptions(ctx, "Review with the guard skill.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: io.Discard})
			wantWrites := 0
			if post {
				wantWrites = 1
			}
			wantKind := "hook_stopped"
			if cancelOnHook {
				wantKind = "cancelled"
			}
			if err == nil || result.Error == nil || result.Error.Kind != wantKind || len(mock.Calls()) != 2 || write.CallCount() != wantWrites {
				t.Fatalf("result=%+v err=%v model calls=%d writes=%d", result, err, len(mock.Calls()), write.CallCount())
			}
			if result.Usage.TotalTokens == 0 {
				t.Fatal("hook stop lost already billed usage")
			}
			decodeSingleHeadlessResult(t, stdout.Bytes())
			paired := false
			for _, message := range application.session.GetHistory() {
				for _, part := range message.Parts {
					if response := part.FunctionResponse; response != nil && response.Name == "write" {
						paired = true
						if reason, _ := response.Response["hook_stop_reason"].(string); !strings.Contains(reason, "HTTP 429") {
							t.Fatalf("paired result lost reason: %v", response.Response)
						}
					}
				}
			}
			if !paired {
				t.Fatal("terminal save lost the completed write result")
			}
			checkpoints := application.session.GetState().ToolCheckpoints
			restored := deserializeToolCheckpoints(checkpoints)
			retainedStop := false
			for _, checkpoint := range restored {
				if checkpoint.ToolName == "write" && strings.Contains(checkpoint.Result.HookStopReason, "HTTP 429") {
					retainedStop = true
				}
			}
			if post && !retainedStop {
				t.Fatalf("durable checkpoint lost stop control: %+v", checkpoints)
			}
			if len(application.rateLimitRetryCount) != 0 || isAutoResumableError(&tools.HookExecutionStoppedError{Reason: "timeout"}) {
				t.Fatal("hook reason scheduled automatic provider recovery")
			}
		})
	}
}

func TestSwitchSessionSkillHooksClearOnlyAfterSuccessfulPublication(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	application := newSessionLeaseTestApp(t, "hooks-current", "glm", root, "current history")
	skill := tools.NewSkillToolWithCatalog(skills.NewCatalog(nil))
	registry := tools.NewRegistry()
	registry.MustRegister(skill)
	application.registry = registry
	registration, err := skill.HookScope().Prepare("current", []hooks.ClaudeCommand{{Event: "PreToolUse", Command: "exit 2", Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	foreign := saveSessionLeaseFixture(t, "hooks-foreign", "deepseek", root, "foreign history")
	if _, err := application.SwitchSession(t.Context(), foreign, false); err == nil || len(tools.SkillHooks(registry)) != 1 {
		t.Fatalf("rejected switch removed hooks: err=%v hooks=%d", err, len(tools.SkillHooks(registry)))
	}
	same := application.session.GetState()
	if _, err := application.SwitchSession(t.Context(), same, false); err != nil || len(tools.SkillHooks(registry)) != 1 {
		t.Fatalf("same-session switch removed hooks: err=%v", err)
	}
	accepted := saveSessionLeaseFixture(t, "hooks-target", "glm", root, "target history")
	if _, err := application.SwitchSession(t.Context(), accepted, false); err != nil {
		t.Fatal(err)
	}
	if len(tools.SkillHooks(registry)) != 0 {
		t.Fatal("previous conversation's executable hooks survived switch")
	}
}
