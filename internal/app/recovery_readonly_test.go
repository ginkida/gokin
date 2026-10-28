package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"
	appcontext "gokin/internal/context"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"gokin/internal/toolschema"
	"google.golang.org/genai"
)

func TestHeadlessGoTestReportsGeneratedFile(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(fmt.Sprintf("deferred=%t", deferred), func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			for name, content := range map[string]string{
				"go.mod":           "module example\n\ngo 1.25.0\n",
				"generate_test.go": "package example\nimport (\"os\"; \"testing\")\nfunc TestGenerate(t *testing.T) { if err := os.WriteFile(\"generated.txt\", []byte(\"generated\"), 0600); err != nil { t.Fatal(err) } }\n",
			} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			mock := testkit.NewMockClient()
			name, args := "bash", map[string]any{"command": "go test -count=1 ./..."}
			if deferred {
				name, args = "use_tool", map[string]any{"name": name, "arguments": args}
			}
			mock.EnqueueToolCall(name, args).EnqueueText("Completed.")
			bash := tools.NewBashTool(root)
			bash.SetSandboxEnabled(false)
			a, executor := newHeadlessPolicyTestApp(t, mock, bash)
			a.workDir = root
			executor.SetWorkDir(root)
			executor.SetToolTimeout(30 * time.Second)
			a.session.SetToolSchemaProfile(toolschema.New(deferred))
			result, err := a.RunHeadlessWithOptions(t.Context(), "Run the tests", HeadlessOptions{
				PermissionMode: "bypassPermissions", NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard,
			})
			if err != nil || result.Status != "success" || result.FilesChangedPartial || !reflect.DeepEqual(result.FilesChanged, []string{"generated.txt"}) {
				t.Fatalf("go test writes missing from files_changed: result=%+v err=%v", result, err)
			}
			if data, err := os.ReadFile(filepath.Join(root, "generated.txt")); err != nil || string(data) != "generated" {
				t.Fatalf("fixture did not really write: %q err=%v", data, err)
			}
		})
	}
}

func TestHeadlessRecoveryAcknowledgesDeliveredBashResult(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		for _, delivery := range []string{"plain", "notification", "compressed_content", "compressed_data"} {
			for _, failure := range []string{"stream_fault", "empty_response", "output_guard"} {
				t.Run(fmt.Sprintf("deferred=%t/%s/%s", deferred, delivery, failure), func(t *testing.T) {
					mock := testkit.NewMockClient()
					enqueue := func(id, name string, args map[string]any) {
						if deferred {
							args = map[string]any{"name": name, "arguments": args}
							name = "use_tool"
						}
						mock.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{
							{FunctionCalls: []*genai.FunctionCall{{ID: id, Name: name, Args: args}}},
							{Done: true},
						}})
					}
					// The eval's ten-call shape: glob, five reads, three edits,
					// then formatting/tests. Only the last result is transformed.
					for i, name := range []string{"glob", "read", "read", "read", "read", "read", "edit", "edit", "edit", "bash"} {
						args := map[string]any{"file_path": fmt.Sprintf("state%d.go", i)}
						if name == "glob" {
							args = map[string]any{"pattern": "**/*.go"}
						}
						if name == "bash" {
							args = map[string]any{"command": "gofmt -w internal/state/*.go && go test ./internal/state/"}
						}
						if name == "edit" {
							args["old_string"], args["new_string"] = "before", "after"
						}
						enqueue(fmt.Sprintf("call-%d", i), name, args)
					}
					bashResult := tools.NewSuccessResult("ok internal/state")
					switch delivery {
					case "compressed_content":
						bashResult.Content = strings.Repeat("test output\n", 100)
					case "compressed_data":
						bashResult.Data = map[string]any{"stdout": strings.Repeat("test output\n", 100), "workspace_observed": true, "written_paths": []string{"internal/state/state.go"}}
					}
					switch failure {
					case "stream_fault":
						mock.EnqueueError(&client.HTTPError{StatusCode: 429, Message: "rate limited after tests"})
					case "empty_response":
						mock.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{{Done: true}}})
					case "output_guard":
						mock.EnqueueError(&client.DegenerateOutputError{Model: "mock-model", Tool: "bash", SizeLimit: true})
					}
					enqueue("inspect", "bash", map[string]any{"command": "git diff"})
					// A new mutation also has to proceed, so allowing inspection
					// alone cannot hide a stale verification checkpoint.
					enqueue("continue", "edit", map[string]any{"file_path": "followup.go", "old_string": "before", "new_string": "after"})
					mock.EnqueueText("Completed.")
					bash := &appHeadlessScriptedTool{name: "bash", results: []tools.ToolResult{bashResult, tools.NewSuccessResult("diff")}}
					a, executor := newHeadlessPolicyTestApp(t, mock, bash)
					a.session.SetToolSchemaProfile(toolschema.New(deferred))
					for name, count := range map[string]int{"glob": 1, "read": 5, "edit": 4} {
						results := make([]tools.ToolResult, count)
						for i := range results {
							results[i] = tools.NewSuccessResult("completed")
						}
						a.registry.MustRegister(&appHeadlessScriptedTool{name: name, results: results})
					}
					executor.SetResponseCompressor(appcontext.NewResponseCompressor(128))
					mock.OnSend = func(context.Context) {
						if len(mock.Calls()) == 10 && delivery == "notification" {
							executor.AddPendingNotification("Review the diff after verification.")
						}
						if len(mock.Calls()) == 12 && failure != "output_guard" {
							if n := executor.GetCheckpointJournal().ReplayRemaining(); n != 0 {
								t.Errorf("retry retained %d acknowledged checkpoints: %+v", n, executor.GetCheckpointJournal().Entries())
							}
						}
					}
					result, err := a.RunHeadlessWithOptions(t.Context(), "Thread the state enum and verify", HeadlessOptions{
						PermissionMode: "bypassPermissions", NoSelfReview: true,
						OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard,
					})
					if err != nil || result.Status != "success" || bash.CallCount() != 2 || len(mock.Calls()) != 14 {
						t.Fatalf("status=%s err=%v bash=%d requests=%d", result.Status, err, bash.CallCount(), len(mock.Calls()))
					}
				})
			}
		}
	}
}
