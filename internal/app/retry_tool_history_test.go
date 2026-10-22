package app

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"google.golang.org/genai"
)

func TestHeadlessRetryContinuesAfterRetainedToolResult(t *testing.T) {
	for _, firstSuccess := range []bool{false, true} {
		t.Run(map[bool]string{false: "correct_failed_edit", true: "continue_after_success"}[firstSuccess], func(t *testing.T) {
			first := tools.NewErrorResult("old_string not found")
			if firstSuccess {
				first = tools.NewSuccessResult("first edit applied")
			}
			mock := testkit.NewMockClient()
			for idx, text := range []string{"old", "corrected"} {
				id := []string{"original-call", "corrected-call"}[idx]
				mock.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{
					{FunctionCalls: []*genai.FunctionCall{{ID: id, Name: "edit", Args: map[string]any{"file_path": "helper.py", "old_string": text}}}},
					{Done: true, FinishReason: genai.FinishReasonStop},
				}})
				if idx == 0 {
					mock.EnqueueError(&client.HTTPError{StatusCode: 429, Message: "rate limited after tool result"})
				} else {
					mock.EnqueueText("Completed and verified.")
				}
			}
			tool := &appHeadlessScriptedTool{name: "edit", results: []tools.ToolResult{first, tools.NewSuccessResult("corrected edit applied")}}
			application, _ := newHeadlessPolicyTestApp(t, mock, tool)
			var output bytes.Buffer
			result, err := application.RunHeadlessWithOptions(context.Background(), "fix and verify", HeadlessOptions{
				OutputFormat: HeadlessOutputJSON, Stdout: &output, Stderr: io.Discard,
			})
			if err != nil || result.Status != "success" {
				t.Fatalf("retry failed despite a retained tool outcome: status=%s error=%v result=%s", result.Status, err, output.String())
			}
			if tool.CallCount() != 2 {
				t.Fatalf("actual edits=%d, want the original and corrected calls exactly once", tool.CallCount())
			}
		})
	}
}

func TestRetainedToolHistoryAcknowledgesOnlyCurrentAttempt(t *testing.T) {
	for _, oldOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "new_result", true: "old_turn_with_reused_id"}[oldOnly], func(t *testing.T) {
			executor := tools.NewExecutor(tools.NewRegistry(), nil, time.Second)
			call := &genai.FunctionCall{ID: "reused-id", Name: "edit", Args: map[string]any{"file_path": "helper.py"}}
			result := tools.NewErrorResult("read-before-edit")
			executor.GetCheckpointJournal().Record(call, result)
			executor.SetSideEffectDedup(true)
			pair := []*genai.Content{
				{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: call}}},
				{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
					ID: call.ID, Name: call.Name, Response: result.ToMap(),
				}}}},
			}
			before := []*genai.Content{userContent("earlier task")}
			if oldOnly {
				before = append(before, pair...)
			}
			retained := append(append([]*genai.Content{}, before...), userContent("current task"))
			if !oldOnly {
				retained = append(retained, pair...)
			}
			application := &App{session: chat.NewSession(), executor: executor}
			application.setRetainedToolHistory(before, retained)
			want := 0
			if oldOnly {
				want = 1
			}
			if got := executor.GetCheckpointJournal().ReplayRemaining(); got != want {
				t.Fatalf("pending effects=%d, want %d", got, want)
			}
		})
	}
}
