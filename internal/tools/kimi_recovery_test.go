package tools

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestBuildKimiToolErrorRecoveryNotification_ReadBeforeEdit(t *testing.T) {
	got := buildKimiToolErrorRecoveryNotification("kimi-for-coding", []*genai.FunctionResponse{{
		Name: "edit",
		Response: map[string]any{
			"success": false,
			"error":   "read-before-edit: call the read tool on internal/app.go first",
		},
	}})
	if !strings.Contains(got, "read-before-edit blocked") {
		t.Fatalf("notification = %q", got)
	}
	if !strings.Contains(got, "Do not issue another mutation first") {
		t.Fatalf("notification lacks mutation guard: %q", got)
	}
}

func TestBuildKimiToolErrorRecoveryNotification_EditMatchFailure(t *testing.T) {
	got := buildKimiToolErrorRecoveryNotification("kimi-k2.6", []*genai.FunctionResponse{{
		Name: "edit",
		Response: map[string]any{
			"success": false,
			"error":   "old_string not found in file: internal/app.go",
		},
	}})
	if !strings.Contains(got, "edit matching failed") {
		t.Fatalf("notification = %q", got)
	}
	if !strings.Contains(got, "line_start/line_end") {
		t.Fatalf("notification lacks line-range recovery: %q", got)
	}
}

func TestBuildKimiToolErrorRecoveryNotification_NonKimiSkips(t *testing.T) {
	got := buildKimiToolErrorRecoveryNotification("glm-5.1", []*genai.FunctionResponse{{
		Name: "edit",
		Response: map[string]any{
			"success": false,
			"error":   "read-before-edit: call read first",
		},
	}})
	if got != "" {
		t.Fatalf("non-Kimi notification = %q, want empty", got)
	}
}

func TestBuildKimiToolErrorRecoveryNotification_SuccessSkips(t *testing.T) {
	got := buildKimiToolErrorRecoveryNotification("kimi-for-coding", []*genai.FunctionResponse{{
		Name: "edit",
		Response: map[string]any{
			"success": true,
			"content": "edited",
		},
	}})
	if got != "" {
		t.Fatalf("success notification = %q, want empty", got)
	}
}

func TestAppendNotificationToFunctionResults_AppendsToContent(t *testing.T) {
	results := []*genai.FunctionResponse{{
		Name: "read",
		Response: map[string]any{
			"success": true,
			"content": "file contents",
		},
	}}

	appendNotificationToFunctionResults(results, "[System: reuse results]")

	content, _ := results[0].Response["content"].(string)
	if !strings.Contains(content, "file contents") || !strings.Contains(content, "[System: reuse results]") {
		t.Fatalf("content = %q, want original content plus notification", content)
	}
}

func TestAppendNotificationToFunctionResults_AppendsToErrorWhenPresent(t *testing.T) {
	results := []*genai.FunctionResponse{{
		Name: "edit",
		Response: map[string]any{
			"success": false,
			"error":   "old_string not found",
			"content": "ignored by Anthropic converter when error exists",
		},
	}}

	appendNotificationToFunctionResults(results, "[System: re-read target]")

	errMsg, _ := results[0].Response["error"].(string)
	if !strings.Contains(errMsg, "old_string not found") || !strings.Contains(errMsg, "[System: re-read target]") {
		t.Fatalf("error = %q, want original error plus notification", errMsg)
	}
}
