package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/hooks"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"google.golang.org/genai"
)

func installAsyncHookFixture(t *testing.T, a *App, command string, rewake bool) {
	t.Helper()
	skill := tools.NewSkillToolWithCatalog(skills.NewCatalog(nil))
	a.registry.MustRegister(skill)
	registration, err := skill.HookScope().Prepare("async-fixture", []hooks.ClaudeCommand{{Event: "PostToolUse", Matcher: "Read", Command: command, Async: !rewake, AsyncRewake: rewake, Once: true, Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	t.Cleanup(a.closeAsyncHooks)
}

func awaitAppAsync(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("async application condition did not become true")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAsyncHeadlessDeliveryIsPairedAndDoesNotSpendModelRounds(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "first"})
	worker.EnqueueScript(testkit.ResponseScript{DelayBeforeFirstChunk: 80 * time.Millisecond, Chunks: []client.ResponseChunk{{FunctionCalls: []*genai.FunctionCall{{Name: "read", Args: map[string]any{"file_path": "second"}}}}, {Done: true, InputTokens: 10, OutputTokens: 5}}}).EnqueueText("Done")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	installAsyncHookFixture(t, a, `while [ ! -f release ]; do sleep 0.01; done; printf '%s' '{"continue":false,"decision":"block","systemMessage":"ASYNC_SYSTEM","hookSpecificOutput":{"additionalContext":"ASYNC_EVIDENCE"}}'`, false)
	worker.OnSend = func(context.Context) {
		if len(worker.Calls()) == 2 {
			_ = os.WriteFile(filepath.Join(a.workDir, "release"), nil, 0600)
		}
	}
	result, err := a.RunHeadlessWithOptions(t.Context(), "read evidence", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 3})
	if err != nil || result.Usage.ModelRounds != 3 || result.Usage.InputTokens != 30 || len(worker.Calls()) != 3 {
		t.Fatalf("async result altered control or accounting: %+v %v", result, err)
	}
	last := worker.Calls()[2]
	text := fmt.Sprint(last.Responses[0].Response)
	if !strings.Contains(text, "ASYNC_SYSTEM") || !strings.Contains(text, "ASYNC_EVIDENCE") || strings.Contains(text, "continue") {
		t.Fatalf("async fields lost or control leaked: %+v", last)
	}
}

func TestAsyncHeadlessTeardownCancelsLateProcess(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "proof"}).EnqueueText("Done")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	installAsyncHookFixture(t, a, `sleep 1; printf leaked > leaked`, false)
	result, err := a.RunHeadlessWithOptions(t.Context(), "read evidence", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 2})
	if err != nil || result.Usage.ModelRounds != 2 {
		t.Fatalf("headless did not finish normally: %+v %v", result, err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(a.workDir, "leaked")); !os.IsNotExist(err) {
		t.Fatal("headless left async process running")
	}
}

func TestAsyncIdleDeliveryWaitsForNextPrompt(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "proof"}).EnqueueText("First complete").EnqueueText("Next prompt complete")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	installAsyncHookFixture(t, a, `while [ ! -f release ]; do sleep 0.01; done; printf '%s' '{"systemMessage":"IDLE_ASYNC_EVIDENCE"}'; touch finished`, false)
	a.processMessageWithContext(t.Context(), "read evidence")
	_ = os.WriteFile(filepath.Join(a.workDir, "release"), nil, 0600)
	awaitAppAsync(t, func() bool { _, err := os.Stat(filepath.Join(a.workDir, "finished")); return err == nil })
	time.Sleep(25 * time.Millisecond)
	if len(worker.Calls()) != 2 {
		t.Fatal("plain async hook woke idle session")
	}
	a.processMessageWithContext(t.Context(), "next user prompt")
	if len(worker.Calls()) != 3 || !strings.Contains(worker.Calls()[2].Message, "IDLE_ASYNC_EVIDENCE") {
		t.Fatalf("next prompt lost async evidence: %+v", worker.Calls())
	}
	if !strings.Contains(fmt.Sprint(a.session.GetHistory()[4].Parts[0].Text), "IDLE_ASYNC_EVIDENCE") {
		t.Fatal("delivered async evidence was not retained in conversation history")
	}
}

func TestAsyncRewakeHonorsForegroundAndConversationBoundaries(t *testing.T) {
	for _, boundary := range []string{"idle", "cancel", "epoch", "busy-user", "headless", "revoked"} {
		t.Run(boundary, func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "proof"}).EnqueueText("First complete").EnqueueText("Handled background failure")
			a := newGoalTestApp(t, worker, testkit.NewMockClient())
			a.running = true // Automatic rewakes belong to an interactive runtime.
			installAsyncHookFixture(t, a, `while [ ! -f release ]; do sleep 0.01; done; printf 'REWAKE_FAILURE' >&2; exit 2`, true)
			a.processMessageWithContext(t.Context(), "read evidence")
			switch boundary {
			case "cancel":
				if !a.cancelIdleAsyncHooks() || a.hasPendingAsyncHooks() {
					t.Fatal("idle cancellation missed active async hooks")
				}
			case "epoch":
				a.sessionLeaseMu.Lock()
				a.advanceConversationEpoch()
				a.sessionLeaseMu.Unlock()
			case "busy-user", "revoked":
				a.mu.Lock()
				a.processing = true
				a.mu.Unlock()
			case "headless":
				a.mu.Lock()
				a.headlessRunActive = true
				a.mu.Unlock()
			}
			_ = os.WriteFile(filepath.Join(a.workDir, "release"), nil, 0600)
			if boundary == "revoked" {
				a.asyncHooksMu.Lock()
				scope := a.asyncHooksScope
				a.asyncHooksMu.Unlock()
				awaitAppAsync(t, scope.HasRewake)
				a.hooksManager.SetEnabled(false)
				a.mu.Lock()
				a.processing = false
				a.mu.Unlock()
				a.tryWakeAsyncHooks()
			}
			if boundary == "idle" {
				awaitAppAsync(t, func() bool { return len(worker.Calls()) == 3 })
				awaitAppAsync(t, func() bool { a.mu.Lock(); defer a.mu.Unlock(); return !a.processing })
				if !strings.Contains(worker.Calls()[2].Message, "REWAKE_FAILURE") {
					t.Fatal("rewake lost stderr")
				}
			} else {
				time.Sleep(80 * time.Millisecond)
				if len(worker.Calls()) != 2 {
					t.Fatalf("async rewake crossed %s boundary", boundary)
				}
				if boundary == "busy-user" {
					a.mu.Lock()
					a.processing = false
					a.mu.Unlock()
					a.tryWakeAsyncHooks()
					awaitAppAsync(t, func() bool { return len(worker.Calls()) == 3 })
					awaitAppAsync(t, func() bool { a.mu.Lock(); defer a.mu.Unlock(); return !a.processing })
				}
				if boundary == "headless" {
					a.mu.Lock()
					a.headlessRunActive = false
					a.mu.Unlock()
				}
			}
		})
	}
}

func TestAsyncRewakeRechecksAuthorityAfterDeliveredResult(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "proof"}).EnqueueText("First complete").EnqueueToolCall("read", map[string]any{"file_path": "follow-up"}).EnqueueText("Must not request")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	a.running = true
	installAsyncHookFixture(t, a, `while [ ! -f release ]; do sleep 0.01; done; printf 'REWAKE_FAILURE' >&2; exit 2`, true)
	a.processMessageWithContext(t.Context(), "read evidence")
	worker.OnSend = func(context.Context) {
		if len(worker.Calls()) == 3 {
			a.hooksManager.SetEnabled(false)
		}
	}
	_ = os.WriteFile(filepath.Join(a.workDir, "release"), nil, 0600)
	awaitAppAsync(t, func() bool { return len(worker.Calls()) == 3 })
	awaitAppAsync(t, func() bool { a.mu.Lock(); defer a.mu.Unlock(); return !a.processing })
	if len(worker.Calls()) != 3 {
		t.Fatal("revoked automatic turn made another provider request")
	}
}
