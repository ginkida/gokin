package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/testkit"
	"google.golang.org/genai"
)

func TestExecutorRelativeReadsAuthorizeEditAndInvalidateAliases(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, relativeRead := range []bool{false, true} {
			t.Run(fmtReadPathCase(cached, relativeRead), func(t *testing.T) {
				root := testkit.ResolvedTempDir(t)
				t.Chdir(root)
				path := filepath.Join(root, "helper.py")
				if err := os.WriteFile(path, []byte("value = 1\n"), 0600); err != nil {
					t.Fatal(err)
				}
				tracker := NewFileReadTracker()
				registry := NewRegistry()
				reader, editor := NewReadTool(root), NewEditTool(root)
				editor.SetReadTracker(tracker)
				editor.SetRequireReadBeforeEdit(true)
				for _, tool := range []Tool{reader, editor} {
					if err := registry.Register(tool); err != nil {
						t.Fatal(err)
					}
				}
				executor := NewExecutor(registry, nil, time.Second)
				executor.SetWorkDir(root)
				executor.SetReadTracker(tracker)
				executor.EnablePreFlightChecks(false)
				if cached {
					executor.SetToolCache(NewToolResultCache(DefaultCacheConfig()))
				}
				readPath, editPath := path, "helper.py"
				if relativeRead {
					readPath, editPath = editPath, readPath
				}
				read := &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": readPath}}
				for range 2 { // The second read also exercises the result cache.
					if result := executor.doExecuteTool(context.Background(), read); !result.Success {
						t.Fatalf("read: %+v", result)
					}
				}
				if !tracker.HasBeenRead(path) {
					t.Fatal("successful read was not recorded under its validated path")
				}
				edit := &genai.FunctionCall{Name: "edit", Args: map[string]any{
					"file_path": editPath, "old_string": "value = 1", "new_string": "value = 2",
				}}
				if result := executor.doExecuteTool(context.Background(), edit); !result.Success {
					t.Fatalf("edit after reading an alias was rejected: %+v", result)
				}
				if tracker.HasBeenRead(path) {
					t.Fatal("edit did not invalidate the canonical read record")
				}
				edit.Args["old_string"], edit.Args["new_string"] = "value = 2", "value = 3"
				result := executor.doExecuteTool(context.Background(), edit)
				if result.Success || !strings.Contains(result.Error, "read-before-edit") {
					t.Fatalf("second mutation without a fresh read bypassed the guard: %+v", result)
				}
				if data, err := os.ReadFile(path); err != nil || string(data) != "value = 2\n" {
					t.Fatalf("file = %q, %v", data, err)
				}
			})
		}
	}
}

func fmtReadPathCase(cached, relative bool) string {
	name := "absolute_read"
	if relative {
		name = "relative_read"
	}
	if cached {
		name += "/cached"
	}
	return name
}
