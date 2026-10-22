package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gokin/internal/testkit"
	"gokin/internal/tools"
	"google.golang.org/genai"
)

type headlessBuildRepairTool struct {
	path string
	fix  bool
}

func (*headlessBuildRepairTool) Name() string                  { return "bash" }
func (*headlessBuildRepairTool) Description() string           { return "test repair command" }
func (*headlessBuildRepairTool) Validate(map[string]any) error { return nil }
func (t *headlessBuildRepairTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name()}
}
func (t *headlessBuildRepairTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	if t.fix {
		if err := os.WriteFile(t.path, []byte("package fixture\nvar Value = 1\n"), 0600); err != nil {
			return tools.NewErrorResult(err.Error()), nil
		}
	}
	return tools.NewSuccessResult("command exited zero"), nil
}

func TestHeadlessCompilerRefusalRequiresFreshVerification(t *testing.T) {
	for _, repaired := range []bool{false, true} {
		t.Run(map[bool]string{false: "still_broken", true: "repaired"}[repaired], func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			path := filepath.Join(root, "value.go")
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/fixture\n\ngo 1.22\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("package fixture\nvar Value = missing\n"), 0600); err != nil {
				t.Fatal(err)
			}
			mock := testkit.NewMockClient()
			edit := &appHeadlessScriptedTool{name: "edit"}
			for idx := range 5 { // Four actual calls, then the exhausted grace window.
				mock.EnqueueToolCall("edit", map[string]any{"file_path": path, "old_string": "old-" + strconv.Itoa(idx), "new_string": "new"})
				edit.results = append(edit.results, tools.NewSuccessResult("intermediate edit"))
			}
			mock.EnqueueToolCall("bash", map[string]any{"command": "repair build"}).EnqueueText("The build is fixed.")
			application, executor := newHeadlessPolicyTestApp(t, mock, edit)
			application.workDir = root
			executor.SetWorkDir(root)
			executor.SetDeltaCheckConfig(true, 10*time.Second, false, 2)
			if err := application.registry.Register(&headlessBuildRepairTool{path: path, fix: repaired}); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			result, err := application.RunHeadlessWithOptions(context.Background(), "fix the compiler error", HeadlessOptions{
				OutputFormat: HeadlessOutputJSON, Stdout: &output, Stderr: io.Discard,
			})
			if repaired && (err != nil || result.Status != "success") {
				t.Fatalf("repaired build kept a stale policy failure: %v, %s", err, output.String())
			}
			if !repaired && (err == nil || result.Status == "success") {
				t.Fatalf("model prose or a zero shell exit hid the broken build: %v, %s", err, output.String())
			}
			if edit.CallCount() != 4 {
				t.Fatalf("compiler barrier did not block its fifth mutation: calls=%d", edit.CallCount())
			}
		})
	}
}
