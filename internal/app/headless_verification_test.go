package app

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"gokin/internal/permission"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestHeadlessVerificationReceiptUsesToolOutcomes(t *testing.T) {
	for _, test := range []struct {
		name, status string
		toolResult   tools.ToolResult
		call, deny   bool
	}{
		{name: "model claims alone", status: "not_run"},
		{name: "check passed", status: "passed", call: true, toolResult: tools.NewSuccessResult("tests passed")},
		{name: "check failed", status: "failed", call: true, toolResult: tools.NewErrorResult("tests failed")},
		{name: "check denied", status: "denied", call: true, deny: true},
		{name: "check skipped", status: "not_run", call: true, toolResult: tools.ToolResult{Success: true, VerificationSkipped: true}},
	} {
		for _, format := range []HeadlessOutputFormat{HeadlessOutputJSON, HeadlessOutputStreamJSON} {
			t.Run(test.name+"/"+string(format), func(t *testing.T) {
				mock := testkit.NewMockClient()
				if test.call {
					mock.EnqueueToolCall("run_tests", map[string]any{"path": "."})
				}
				mock.EnqueueText("Everything is verified and all tests passed.")
				a, exec := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "run_tests", results: []tools.ToolResult{test.toolResult}})
				if test.deny {
					rules := permission.DefaultRules()
					rules.SetPolicy("run_tests", permission.LevelDeny)
					exec.SetPermissions(permission.NewManager(rules, true))
				}
				var out bytes.Buffer
				result, err := a.RunHeadlessWithOptions(t.Context(), "Run tests and analyze failures", HeadlessOptions{OutputFormat: format, Stdout: &out, Stderr: io.Discard})
				if err != nil || result.Status != "success" || result.Verification.Status != test.status {
					t.Fatalf("receipt=%+v err=%v", result, err)
				}
				frames := decodeHeadlessJSONL(t, out.Bytes())
				var decoded HeadlessResult
				if err := json.Unmarshal(frames[len(frames)-1], &decoded); err != nil || decoded.Verification.Status != test.status {
					t.Fatalf("wire receipt=%+v err=%v", decoded, err)
				}
			})
		}
	}
}
