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

	"gokin/internal/cache"
	"gokin/internal/client"
	"gokin/internal/hooks"
	"gokin/internal/permission"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"google.golang.org/genai"
)

func installAgentHookFixture(t *testing.T, a *App, event string, once bool) {
	t.Helper()
	if tool, ok := a.registry.Get("read"); ok {
		if read, ok := tool.(*appHeadlessScriptedTool); ok {
			for range 60 {
				read.results = append(read.results, tools.NewSuccessResult("notes inspected"))
			}
		}
	}
	skill := tools.NewSkillToolWithCatalog(skills.NewCatalog(nil))
	a.registry.MustRegister(skill)
	registration, err := skill.HookScope().Prepare("agent-review", []hooks.ClaudeCommand{{Event: event, Handler: "agent", Prompt: "AGENT_HOOK_ACCEPTANCE: verify evidence $ARGUMENTS", Timeout: 10 * time.Second, Once: once}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
}

func TestAgentHookHeadlessToolContinuationSharedLimitsAndUsage(t *testing.T) {
	for _, test := range []struct {
		name, decision, kind      string
		cap, verifierCalls, reads int
	}{
		{"allow", `{"ok":true}`, "", 4, 2, 2},
		{"deny", `{"ok":false,"reason":"proof missing"}`, "", 4, 2, 1},
		{"turn-cap-before-verifier", `{"ok":true}`, "max_turns", 1, 0, 0},
		{"turn-cap-in-verifier", `{"ok":true}`, "max_turns", 2, 1, 1},
		{"invalid-decision", `{"ok":true,"impossible":false}`, "hook_stopped", 4, 2, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "requested.txt"}).EnqueueText("Inspected")
			verifier := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "proof.txt"}).EnqueueText(test.decision)
			a := newGoalTestApp(t, worker, verifier)
			installAgentHookFixture(t, a, "PreToolUse", false)
			result, err := a.RunHeadlessWithOptions(t.Context(), "inspect evidence", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: test.cap})
			if test.kind != "" {
				if err == nil || result.Error == nil || result.Error.Kind != test.kind {
					t.Fatalf("wrong agent-hook error: %+v %v", result, err)
				}
			} else if test.name == "allow" && err != nil {
				t.Fatalf("agent hook failed: %+v %v", result, err)
			}
			read, _ := a.registry.Get("read")
			if got := read.(*appHeadlessScriptedTool).CallCount(); got != test.reads || len(verifier.Calls()) != test.verifierCalls {
				t.Fatalf("verification recursed or crossed denied boundary: reads=%d verifierCalls=%d result=%+v", got, len(verifier.Calls()), result)
			}
			calls := len(worker.Calls()) + len(verifier.Calls())
			if result.Usage.InputTokens != 10*calls || result.Usage.ModelRounds != calls {
				t.Fatalf("verifier usage lost or duplicated: %+v calls=%d", result.Usage, calls)
			}
			if test.verifierCalls == 2 {
				continuation := verifier.Calls()[1]
				if continuation.Method != "SendFunctionResponse" || len(continuation.Responses) != 1 || continuation.Responses[0].Response["content"] != "notes inspected" || len(continuation.History) != 2 {
					t.Fatalf("lost isolated verifier history: %+v", continuation)
				}
			}
		})
	}
}

func TestAgentHookStopContinuesAndOnceDoesNotRearm(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("First result").EnqueueText("Verified final result")
	verifier := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "proof.txt"}).EnqueueText(`{"ok":false,"reason":"finish acceptance"}`)
	a := newGoalTestApp(t, worker, verifier)
	installAgentHookFixture(t, a, "Stop", true)
	result, err := a.RunHeadlessWithOptions(t.Context(), "finish acceptance", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 4})
	if err != nil || len(worker.Calls()) != 2 || len(verifier.Calls()) != 2 || result.Usage.ModelRounds != 4 {
		t.Fatalf("agent Stop continuation or once lost: %+v %v", result, err)
	}
}

func TestAgentHookCannotWidenCapabilitiesOrBypassPermissionDenials(t *testing.T) {
	for _, gate := range []string{"capability", "permission"} {
		t.Run(gate, func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "requested.txt"}).EnqueueText("Inspected")
			verifier := testkit.NewMockClient().EnqueueToolCall("write", map[string]any{"file_path": "forbidden.txt", "content": "changed"}).EnqueueText(`{"ok":true}`)
			a := newGoalTestApp(t, worker, verifier)
			write := &appHeadlessScriptedTool{name: "write", results: []tools.ToolResult{tools.NewSuccessResult("must not execute")}}
			a.registry.MustRegister(write)
			installAgentHookFixture(t, a, "PreToolUse", false)
			ctx := t.Context()
			if gate == "capability" {
				ctx = tools.ContextWithToolCapabilityCeiling(ctx, []string{"read"})
			} else {
				manager := permission.NewManager(nil, true)
				manager.SetRunToolRules(nil, []string{"write"})
				a.executor.SetPermissions(manager)
			}
			result, err := a.RunHeadlessWithOptions(ctx, "inspect evidence", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 4})
			if gate == "capability" && err != nil || gate == "permission" && (err != nil || result.Status != "success" || len(result.PermissionDenials) != 1) || write.CallCount() != 0 || len(verifier.Calls()) != 2 || verifier.Calls()[1].Responses[0].Response["success"] != false {
				t.Fatalf("agent hook bypassed %s: %+v %v", gate, result, err)
			}
		})
	}
}

func TestAgentHookFiftyRoundLimitAndFatalProviderCause(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "requested.txt"})
	verifier := testkit.NewMockClient()
	for range 50 {
		verifier.EnqueueToolCall("read", map[string]any{"file_path": "proof.txt"})
	}
	a := newGoalTestApp(t, worker, verifier)
	installAgentHookFixture(t, a, "PreToolUse", false)
	result, err := a.RunHeadlessWithOptions(t.Context(), "inspect evidence", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 60})
	read, _ := a.registry.Get("read")
	if err == nil || result.Error == nil || result.Error.Kind != "hook_stopped" || !strings.Contains(result.Error.Message, "50-turn") || len(verifier.Calls()) != 50 || read.(*appHeadlessScriptedTool).CallCount() != 49 || result.Usage.ModelRounds != 51 {
		t.Fatalf("agent hook exceeded verifier bound: %+v %v calls=%d reads=%d error=%+v", result, err, len(verifier.Calls()), read.(*appHeadlessScriptedTool).CallCount(), result.Error)
	}
	worker = testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "requested.txt"})
	verifier = testkit.NewMockClient().EnqueueStartupError(&client.TerminalProviderError{Code: "1113", Status: 400, Message: "insufficient balance"})
	a = newGoalTestApp(t, worker, verifier)
	installAgentHookFixture(t, a, "PreToolUse", false)
	result, err = a.RunHeadlessWithOptions(t.Context(), "/goal inspect evidence", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 4})
	if err == nil || result.Goal == nil || result.Goal.Status != "cleared" {
		t.Fatalf("agent hook lost terminal provider cause: %+v %v", result, err)
	}
}

func TestAgentHookBashUsesCancellationAndKeepsNativeChecks(t *testing.T) {
	for _, mode := range []string{"check", "cancel", "native-deny"} {
		t.Run(mode, func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "requested.txt"}).EnqueueText("Inspected")
			command := "printf verified > verifier-proof"
			if mode == "cancel" {
				command = "sleep 3; printf leaked > verifier-proof"
			}
			verifier := testkit.NewMockClient().EnqueueToolCall("bash", map[string]any{"command": command}).EnqueueText(`{"ok":true}`)
			a := newGoalTestApp(t, worker, verifier)
			bash := tools.NewBashTool(a.workDir)
			bash.SetSandboxEnabled(false)
			a.registry.MustRegister(bash)
			installAgentHookFixture(t, a, "PreToolUse", false)
			if mode == "native-deny" {
				a.hooksManager.AddHook(&hooks.Hook{Name: "deny-verifier-shell", Type: hooks.PreTool, ToolName: "bash", Command: "exit 1", Enabled: true, FailOnError: true})
			}
			ctx := t.Context()
			if mode == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
			}
			result, err := a.RunHeadlessWithOptions(ctx, "inspect evidence", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 4})
			content, fileErr := os.ReadFile(filepath.Join(a.workDir, "verifier-proof"))
			if mode == "check" {
				if err != nil || string(content) != "verified" {
					t.Fatalf("verifier could not run test: %+v %v %v", result, err, fileErr)
				}
			} else if !os.IsNotExist(fileErr) {
				t.Fatalf("cancelled or denied verifier wrote file: %s", content)
			}
			if mode == "native-deny" && verifier.Calls()[1].Responses[0].Response["success"] != false {
				t.Fatal("verifier lost native policy denial")
			}
		})
	}
}

func TestAgentHookReadsStayIndependentOfForegroundCacheAndDedup(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(fmt.Sprint(warm), func(t *testing.T) {
			worker := testkit.NewMockClient()
			verifier := testkit.NewMockClient()
			a := newGoalTestApp(t, worker, verifier)
			canonical, pathErr := filepath.EvalSymlinks(a.workDir)
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			a.workDir = canonical
			proof := filepath.Join(a.workDir, "proof.txt")
			if err := os.WriteFile(proof, []byte("ORIGINAL_PROOF"), 0600); err != nil {
				t.Fatal(err)
			}
			a.registry.Unregister("read")
			a.registry.MustRegister(tools.NewReadTool(a.workDir))
			a.executor.SetWorkDir(a.workDir)
			a.executor.SetReadTracker(tools.NewFileReadTracker())
			a.executor.SetToolCache(tools.NewToolResultCache(tools.DefaultCacheConfig()))
			want := "ORIGINAL_PROOF"
			if warm {
				if result, err := a.executor.InvokeTool(t.Context(), "read", map[string]any{"file_path": proof}); err != nil || !result.Success {
					t.Fatalf("warm read: %+v %v", result, err)
				}
				want = "FRESH_VERIFIER_PROOF_AFTER_EXTERNAL_CHANGE"
				if err := os.WriteFile(proof, []byte(want), 0600); err != nil {
					t.Fatal(err)
				}
			}
			worker.EnqueueToolCall("read", map[string]any{"file_path": proof}).EnqueueText("Inspected")
			verifier.EnqueueToolCall("read", map[string]any{"file_path": proof}).EnqueueText(`{"ok":true}`)
			installAgentHookFixture(t, a, "PreToolUse", false)
			result, err := a.RunHeadlessWithOptions(t.Context(), "inspect proof", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 4})
			if err != nil {
				t.Fatalf("isolated verifier failed: %+v %v", result, err)
			}
			for _, calls := range [][]testkit.RecordedCall{worker.Calls(), verifier.Calls()} {
				text := fmt.Sprint(calls[1].Responses[0].Response)
				if !strings.Contains(text, want) || strings.Contains(text, "already in context above") {
					t.Fatalf("independent history lost file bytes: %s", text)
				}
			}
		})
	}
}

func TestAgentHookReadBeforeEditUsesVerifierEvidence(t *testing.T) {
	worker, verifier := testkit.NewMockClient(), testkit.NewMockClient()
	a := newGoalTestApp(t, worker, verifier)
	canonical, pathErr := filepath.EvalSymlinks(a.workDir)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	a.workDir = canonical
	proof := filepath.Join(a.workDir, "proof.txt")
	if err := os.WriteFile(proof, []byte("OLD_PROOF"), 0600); err != nil {
		t.Fatal(err)
	}
	a.registry.Unregister("read")
	a.registry.MustRegister(tools.NewReadTool(a.workDir))
	tracker := tools.NewFileReadTracker()
	a.executor.SetReadTracker(tracker)
	a.executor.SetWorkDir(a.workDir)
	edit := tools.NewEditTool(a.workDir)
	edit.SetReadTracker(tracker)
	edit.SetRequireReadBeforeEdit(true)
	a.registry.MustRegister(edit)
	worker.EnqueueToolCall("read", map[string]any{"file_path": proof}).EnqueueText("Inspected")
	verifier.EnqueueToolCall("read", map[string]any{"file_path": proof}).EnqueueToolCall("edit", map[string]any{"file_path": proof, "old_string": "OLD_PROOF", "new_string": "VERIFIED_PROOF"}).EnqueueToolCall("read", map[string]any{"file_path": proof}).EnqueueText(`{"ok":true}`)
	installAgentHookFixture(t, a, "PreToolUse", false)
	result, err := a.RunHeadlessWithOptions(t.Context(), "verify requested correction", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 6})
	content, fileErr := os.ReadFile(proof)
	if err != nil || fileErr != nil || string(content) != "VERIFIED_PROOF" || verifier.Calls()[2].Responses[0].Response["success"] != true {
		t.Fatalf("verifier lost read-before-edit evidence: %+v %v file=%s", result, err, content)
	}
}

func TestAgentHookSearchDoesNotReuseForegroundNegativeCache(t *testing.T) {
	worker, verifier := testkit.NewMockClient(), testkit.NewMockClient()
	a := newGoalTestApp(t, worker, verifier)
	canonical, err := filepath.EvalSymlinks(a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	a.workDir = canonical
	glob := tools.NewGlobTool(a.workDir)
	glob.SetCache(cache.NewSearchCache(10, time.Minute))
	a.registry.MustRegister(glob)
	args := map[string]any{"pattern": "*.txt", "path": a.workDir}
	if result, err := glob.Execute(t.Context(), args); err != nil || !strings.Contains(result.Content, "no matches") {
		t.Fatalf("warm glob: %+v %v", result, err)
	}
	proof := filepath.Join(a.workDir, "proof.txt")
	if err := os.WriteFile(proof, []byte("new proof"), 0600); err != nil {
		t.Fatal(err)
	}
	worker.EnqueueToolCall("read", map[string]any{"file_path": proof}).EnqueueText("Inspected")
	verifier.EnqueueToolCall("glob", args).EnqueueText(`{"ok":true}`)
	installAgentHookFixture(t, a, "PreToolUse", false)
	result, err := a.RunHeadlessWithOptions(t.Context(), "verify proof", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 4})
	if err != nil || !strings.Contains(fmt.Sprint(verifier.Calls()[1].Responses[0].Response), "proof.txt") {
		t.Fatalf("verifier reused stale search cache: %+v %v calls=%+v", result, err, verifier.Calls())
	}
}

func TestStrictPermissionStopsNestedHookBeforeOtherCalls(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "requested.txt"}).EnqueueText("Finished")
	verifier := testkit.NewMockClient().EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{{FunctionCalls: []*genai.FunctionCall{
		{ID: "denied", Name: "write", Args: map[string]any{"file_path": filepath.Join(root, "denied.txt"), "content": "denied"}},
		{ID: "allowed", Name: "write", Args: map[string]any{"file_path": filepath.Join(root, "allowed.txt"), "content": "allowed"}},
	}}}}).EnqueueText(`{"ok":true}`)
	a := newGoalTestApp(t, worker, verifier)
	a.workDir = root
	a.executor.SetWorkDir(root)
	a.registry.MustRegister(tools.NewWriteTool(a.workDir))
	installAgentHookFixture(t, a, "PreToolUse", false)
	manager := permission.NewManager(nil, true)
	manager.SetRunToolRules([]string{"write"}, []string{"write(**/denied.txt)"})
	for path, want := range map[string]bool{"denied.txt": false, "allowed.txt": true} {
		response, err := manager.Check(permission.ContextWithWorkDir(t.Context(), root), "write", map[string]any{"file_path": filepath.Join(root, path), "content": "fixture"})
		if err != nil || response.Allowed != want {
			t.Fatalf("invalid permission fixture for %s: %+v %v", path, response, err)
		}
	}
	a.executor.SetPermissions(manager)
	result, err := a.RunHeadlessWithOptions(t.Context(), "inspect evidence", HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard, StrictPermissions: true, NoSelfReview: true})
	if err == nil || result.Status != "policy_blocked" || len(result.PermissionDenials) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(verifier.Calls()) != 1 || len(worker.Calls()) != 1 {
		t.Errorf("requests after denial: verifier=%d worker=%d", len(verifier.Calls()), len(worker.Calls()))
	}
	if _, err := os.Stat(filepath.Join(a.workDir, "allowed.txt")); !os.IsNotExist(err) {
		t.Errorf("write after first denial: %v", err)
	}
	read, _ := a.registry.Get("read")
	if read.(*appHeadlessScriptedTool).CallCount() != 0 {
		t.Error("enclosing tool executed after denial")
	}
}
