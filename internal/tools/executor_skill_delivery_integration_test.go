package tools_test

import (
	"context"
	"strings"
	"testing"
	"time"

	ctxmgr "gokin/internal/context"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

const (
	executorDeliveryMiddleSentinel = "<<EXECUTOR_DELIVERY_MIDDLE_SENTINEL>>"
	executorDeliveryTailSentinel   = "<<EXECUTOR_DELIVERY_TAIL_SENTINEL>>"
)

type executorDeliveryStaticTool struct {
	name    string
	content string
}

func (t *executorDeliveryStaticTool) Name() string        { return t.name }
func (t *executorDeliveryStaticTool) Description() string { return "test initial-delivery tool" }
func (t *executorDeliveryStaticTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name, Description: "test initial-delivery tool"}
}
func (t *executorDeliveryStaticTool) Validate(map[string]any) error { return nil }
func (t *executorDeliveryStaticTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	return tools.NewSuccessResult(t.content), nil
}

func executorDeliveryPayload(t *testing.T) string {
	t.Helper()

	const payloadBytes = 20 * 1024
	prefix := strings.Repeat("a", 12*1024)
	remaining := payloadBytes - len(prefix) - len(executorDeliveryMiddleSentinel) - len(executorDeliveryTailSentinel)
	if remaining < 0 {
		t.Fatal("test sentinels exceed payload size")
	}
	payload := prefix + executorDeliveryMiddleSentinel + strings.Repeat("b", remaining) + executorDeliveryTailSentinel
	if len(payload) != payloadBytes {
		t.Fatalf("payload length = %d, want %d", len(payload), payloadBytes)
	}
	if len(payload) > skills.MaxSkillBytes {
		t.Fatalf("payload length = %d, exceeds MaxSkillBytes = %d", len(payload), skills.MaxSkillBytes)
	}
	return payload
}

func executorHistoryResponseContent(t *testing.T, history []*genai.Content, toolName string) string {
	t.Helper()
	for _, content := range history {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil || part.FunctionResponse == nil || part.FunctionResponse.Name != toolName {
				continue
			}
			value, _ := part.FunctionResponse.Response["content"].(string)
			return value
		}
	}
	t.Fatalf("history has no %q FunctionResponse", toolName)
	return ""
}

func executorRecordedResponse(t *testing.T, calls []testkit.RecordedCall) *genai.FunctionResponse {
	t.Helper()
	for _, call := range calls {
		if call.Method == "SendFunctionResponse" && len(call.Responses) > 0 {
			return call.Responses[0]
		}
	}
	t.Fatal("model calls contain no SendFunctionResponse")
	return nil
}

func TestExecutorInitialSkillDeliveryPreservesFullPayload(t *testing.T) {
	payload := executorDeliveryPayload(t)
	oversized := strings.Repeat("x", skills.MaxRenderedSkillBytes+1)

	for _, tc := range []struct {
		name              string
		toolName          string
		content           string
		wantIntact        bool
		wantSentinelsGone bool
	}{
		{name: "skill", toolName: "skill", content: payload, wantIntact: true},
		{name: "oversized fake skill", toolName: "skill", content: oversized},
		{name: "ordinary large result", toolName: "read", content: payload, wantSentinelsGone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tools.NewRegistry()
			if err := registry.Register(&executorDeliveryStaticTool{name: tc.toolName, content: tc.content}); err != nil {
				t.Fatalf("Register() error = %v", err)
			}

			mock := testkit.NewMockClient().
				EnqueueToolCall(tc.toolName, map[string]any{}).
				EnqueueText("done")
			executor := tools.NewExecutor(registry, mock, time.Second)
			executor.EnablePreFlightChecks(false)
			executor.SetCompactor(ctxmgr.NewResultCompactor(10_000))
			executor.SetResponseCompressor(ctxmgr.NewResponseCompressor(10_000))

			history, finalText, err := executor.Execute(context.Background(), nil, "load instructions")
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if finalText != "done" {
				t.Fatalf("finalText = %q, want done", finalText)
			}

			response := executorRecordedResponse(t, mock.Calls())
			delivered, _ := response.Response["content"].(string)
			stored := executorHistoryResponseContent(t, history, tc.toolName)
			if tc.wantIntact {
				for location, got := range map[string]string{"model delivery": delivered, "stored history": stored} {
					if got != payload {
						t.Fatalf("%s changed skill payload: middle=%t tail=%t len=%d want=%d",
							location,
							strings.Contains(got, executorDeliveryMiddleSentinel),
							strings.Contains(got, executorDeliveryTailSentinel),
							len(got), len(payload))
					}
				}
				return
			}

			if delivered == tc.content || stored == tc.content {
				t.Fatalf("ordinary large result bypassed compaction: delivered=%d stored=%d original=%d",
					len(delivered), len(stored), len(tc.content))
			}
			if tc.wantSentinelsGone && (strings.Contains(delivered, executorDeliveryMiddleSentinel) || strings.Contains(delivered, executorDeliveryTailSentinel)) {
				t.Fatalf("ordinary model delivery retained sentinels after expected compaction: %q", delivered)
			}
		})
	}
}
