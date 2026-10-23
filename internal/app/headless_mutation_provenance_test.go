package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/testkit"
	"gokin/internal/tools"
)

type mutationPresentationCompactor struct{}

func (mutationPresentationCompactor) CompactForType(_ string, r tools.ToolResult) tools.ToolResult {
	return tools.NewSuccessResult(r.Content)
}

func TestHeadlessFilesChangedSurvivesExecutorRedaction(t *testing.T) {
	for _, name := range []string{"copy", "batch", "refactor"} {
		for _, compact := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/compact=%v", name, compact), func(t *testing.T) {
				model := testkit.NewMockClient()
				a, e := newHeadlessPolicyTestApp(t, model, &appHeadlessScriptedTool{name: "unused"})
				a.workDir = testkit.ResolvedTempDir(t)
				e.SetWorkDir(a.workDir)
				e.SetDeltaCheckConfig(false, 0, false, 0)
				if compact {
					e.SetCompactor(mutationPresentationCompactor{})
				}
				relative := "testdata/sk-abcdefghijklmnopqrstuvwx.txt"
				target := filepath.Join(a.workDir, filepath.FromSlash(relative))
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				var tool tools.Tool
				var args map[string]any
				want := "after"
				switch name {
				case "copy":
					source := filepath.Join(a.workDir, "source.txt")
					if err := os.WriteFile(source, []byte(want), 0600); err != nil {
						t.Fatal(err)
					}
					tool = tools.NewCopyTool(a.workDir)
					args = map[string]any{"source": source, "destination": target}
				case "batch":
					tool = tools.NewBatchTool(a.workDir)
					args = map[string]any{"operation": "replace", "pattern": "testdata/*.txt", "search": "before", "replacement": want}
				case "refactor":
					refactor := tools.NewRefactorTool()
					refactor.SetWorkDir(a.workDir)
					tool = refactor
					args = map[string]any{"operation": "rename", "file_path": target, "pattern": "testdata/*.txt", "old_name": "before", "new_name": want}
				}
				if name != "copy" {
					if err := os.WriteFile(target, []byte("before"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				a.registry.MustRegister(tool)
				model.EnqueueToolCall(name, args).EnqueueText("Updated.")
				result, err := a.RunHeadlessWithOptions(t.Context(), "Apply the change.", HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard, NoSelfReview: true})
				if err != nil || result.Status != "success" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				data, err := os.ReadFile(target)
				if err != nil || string(data) != want {
					t.Fatalf("mutation missing: %q %v", data, err)
				}
				calls := model.Calls()
				if len(calls) != 2 || len(calls[1].Responses) != 1 {
					t.Fatalf("missing real executor continuation: %v", calls)
				}
				response := calls[1].Responses[0].Response
				encoded, _ := json.Marshal(response)
				if response["success"] != true || !strings.Contains(string(encoded), "[REDACTED]") || strings.Contains(string(encoded), "sk-abcdefghijklmnopqrstuvwx") {
					t.Fatalf("real executor/redactor was bypassed: %s", encoded)
				}
				if !reflect.DeepEqual(result.FilesChanged, []string{relative}) {
					t.Fatalf("files_changed=%v want=%s", result.FilesChanged, relative)
				}
			})
		}
	}
}
