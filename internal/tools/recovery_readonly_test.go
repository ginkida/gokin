package tools

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gokin/internal/permission"
	"google.golang.org/genai"
)

func TestExecutorRecoveryAllowsReadOnlyBashWhileMutationPending(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		for _, tc := range []struct {
			command string
			allowed bool
		}{
			{"git diff", true},
			{"git status --short", true},
			{"git log -1", true},
			{"ls", true},
			{"cat state.go", true},
			{"git diff && git status --short", true},
			{"git diff 2>/dev/null", true},
			{"git diff > saved.diff", false},
			{"git diff && touch changed", false},
			{"unknown-command", false},
			{"git diff --output=state.go", false},
			{"git diff -U --output=state.go", false},
			{"git log -n --output=state.go", false},
			{"git -c core.fsmonitor=./mutate status", false},
			{"go test ./...", false},
			{"printf ok\ntouch changed", false},
			{"find . '-exec' touch changed +", false},
			{"cat f | wc -l", true},
			{"grep -rn x .", true},
		} {
			t.Run(fmt.Sprintf("deferred=%t/%s", deferred, tc.command), func(t *testing.T) {
				registry := NewRegistry()
				bash := &checkpointCountingBashTool{}
				registry.MustRegister(bash)
				executor := NewExecutor(registry, nil, time.Second)
				ctx := t.Context()
				if deferred {
					ctx = ContextWithDeferredCatalog(ctx, registry, registry.GeminiTools())
				}
				call := func(id, command string) *genai.FunctionCall {
					args := map[string]any{"command": command}
					if deferred {
						return &genai.FunctionCall{ID: id, Name: "use_tool", Args: map[string]any{"name": "bash", "arguments": args}}
					}
					return &genai.FunctionCall{ID: id, Name: "bash", Args: args}
				}
				original := call("mutate", "gofmt -w state.go && go test ./internal/state/")
				first, err := executor.executeTools(ctx, []*genai.FunctionCall{original})
				if err != nil || first[0].Response["success"] != true {
					t.Fatal(first, err)
				}
				executor.SetSideEffectDedup(true)
				result, err := executor.executeTools(ctx, []*genai.FunctionCall{call("inspect", tc.command)})
				wantCalls := int32(1)
				if tc.allowed {
					wantCalls++
				}
				if err != nil || result[0].Response["success"] != tc.allowed || bash.calls.Load() != wantCalls {
					t.Fatalf("command=%q result=%+v err=%v executions=%d, want %d", tc.command, result, err, bash.calls.Load(), wantCalls)
				}
				if n := executor.GetCheckpointJournal().ReplayRemaining(); n != 1 {
					t.Fatalf("inspection consumed the pending mutation: %d", n)
				}
				replayed, err := executor.executeTools(ctx, []*genai.FunctionCall{original})
				if err != nil || replayed[0].Response["content"] != first[0].Response["content"] || bash.calls.Load() != wantCalls {
					t.Fatalf("mutation did not replay exactly once: %+v err=%v executions=%d", replayed, err, bash.calls.Load())
				}
			})
		}
	}
}

func TestCheckpointDeliveryStillRequiresExactRetainedPair(t *testing.T) {
	for _, variant := range []string{"delivered", "replayed_delivery", "changed_delivery", "changed_input", "different_raw_result", "not_retained", "cleared_delivery"} {
		t.Run(variant, func(t *testing.T) {
			journal := NewCheckpointJournal()
			call := &genai.FunctionCall{ID: "format", Name: "bash", Args: map[string]any{"command": "gofmt -w state.go"}}
			raw := NewSuccessResultWithData("full output", map[string]any{"stdout": "full stdout"})
			journal.Record(call, raw)
			journal.BeginReplay()
			if variant == "replayed_delivery" {
				copy := *call
				copy.ID = "regenerated"
				call = &copy
				if _, _, ok := journal.ConsumeReplay(call); !ok {
					t.Fatal("regenerated call did not replay")
				}
			}
			original := raw.ToMap()
			if variant == "different_raw_result" {
				original = NewSuccessResult("unrelated result").ToMap()
			}
			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			delivered := map[string]any{"success": true, "content": "[System: review]\ncompressed output"}
			journal.recordDelivery(call, sha256.Sum256(encoded), delivered)
			if variant == "cleared_delivery" {
				journal.Clear()
				journal.Record(call, raw)
				journal.BeginReplay()
			}
			history := checkpointResultHistory(call, raw)
			history[1].Parts[0].FunctionResponse.Response = delivered
			switch variant {
			case "changed_delivery":
				delivered["content"] = "forged outcome"
			case "changed_input":
				history[0].Parts[0].FunctionCall = &genai.FunctionCall{ID: call.ID, Name: call.Name, Args: map[string]any{"command": "touch other"}}
			case "not_retained":
				history = history[:1]
			}
			want := 0
			if variant == "delivered" || variant == "replayed_delivery" {
				want = 1
			}
			if n := journal.AcknowledgeResults(history); n != want || journal.ReplayRemaining() != 1-want {
				t.Fatalf("acknowledged=%d pending=%d, want %d and %d", n, journal.ReplayRemaining(), want, 1-want)
			}
			if want == 0 {
				replayed, _, ok := journal.ConsumeReplay(call)
				if !ok || replayed.Content != raw.Content || replayed.Data.(map[string]any)["stdout"] != "full stdout" {
					t.Fatalf("delivery binding changed the pending raw outcome: %+v", replayed)
				}
			}
		})
	}
}

func TestExecutorRecoveryInspectionStillEnforcesPermissions(t *testing.T) {
	registry := NewRegistry()
	bash := &checkpointCountingBashTool{}
	registry.MustRegister(bash)
	executor := NewExecutor(registry, nil, time.Second)
	executor.GetCheckpointJournal().Record(&genai.FunctionCall{ID: "pending", Name: "bash", Args: map[string]any{"command": "touch state.go"}}, NewSuccessResult("written"))
	executor.SetSideEffectDedup(true)
	rules := permission.DefaultRules()
	rules.SetPolicy("bash", permission.LevelDeny)
	executor.SetPermissions(permission.NewManager(rules, true))
	result := executor.executeTool(t.Context(), &genai.FunctionCall{ID: "inspect", Name: "bash", Args: map[string]any{"command": "git diff"}})
	if result.PolicyBlock == nil || result.PolicyBlock.Kind != PolicyBlockPermission || bash.calls.Load() != 0 || executor.GetCheckpointJournal().ReplayRemaining() != 1 {
		t.Fatalf("inspection bypassed permission policy: %+v", result)
	}
}

func TestExecutorRecoveryRejectsInspectionRewrittenToMutation(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		for _, command := range []string{"touch changed", "git diff --output=state.go", "git -c core.fsmonitor=./mutate status", "go test ./...", "printf ok\ntouch changed", "find . '-exec' touch changed +"} {
			t.Run(fmt.Sprintf("deferred=%t/%s", deferred, command), func(t *testing.T) {
				executor, _, _, source := settingsExecutorFixture(t, "exit 0")
				bash := &checkpointCountingBashTool{}
				if err := executor.registry.Register(bash); err != nil {
					t.Fatal(err)
				}
				payload, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "updatedInput": map[string]any{"command": command}}})
				if err != nil {
					t.Fatal(err)
				}
				hookCommand := "printf '%s' '" + strings.ReplaceAll(string(payload), "'", "'\\''") + "'"
				settings, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": hookCommand}}}}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(source, settings, 0600); err != nil {
					t.Fatal(err)
				}
				executor.GetCheckpointJournal().Record(&genai.FunctionCall{ID: "pending", Name: "bash", Args: map[string]any{"command": "touch state.go"}}, NewSuccessResult("written"))
				executor.SetSideEffectDedup(true)
				args := map[string]any{"command": "git diff"}
				call := &genai.FunctionCall{ID: "inspect", Name: "bash", Args: args}
				ctx := t.Context()
				if deferred {
					ctx = ContextWithDeferredCatalog(ctx, executor.registry, executor.registry.GeminiTools())
					call = &genai.FunctionCall{ID: call.ID, Name: "use_tool", Args: map[string]any{"name": "bash", "arguments": args}}
				}
				results, err := executor.executeTools(ctx, []*genai.FunctionCall{call})
				if err != nil || len(results) != 1 || results[0].Response["success"] != false || !strings.Contains(fmt.Sprint(results[0].Response["error"]), "Exactly-once recovery blocked") || bash.calls.Load() != 0 || executor.GetCheckpointJournal().ReplayRemaining() != 1 || args["command"] != "git diff" {
					t.Fatalf("hook rewrite bypassed recovery: %+v err=%v executions=%d", results, err, bash.calls.Load())
				}
			})
		}
	}
}
