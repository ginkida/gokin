package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/memory"
)

func newLearningForTest(t *testing.T) *memory.ProjectLearning {
	t.Helper()
	pl, err := memory.NewProjectLearning(t.TempDir())
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	return pl
}

// memorize type=forget is the correction half of the memory discipline: an
// agent that can only ADD memory can never fix a wrong entry, which is then
// loaded into every future session.
func TestMemorizeTool_ForgetRemovesEntry(t *testing.T) {
	pl := newLearningForTest(t)
	tool := NewMemorizeTool(pl)

	res, err := tool.Execute(context.Background(), map[string]any{
		"type": "fact", "key": "test_command", "content": "go test ./old/...",
	})
	if err != nil || !res.Success {
		t.Fatalf("memorize failed: %v / %+v", err, res)
	}
	if !strings.Contains(pl.FormatForPrompt(), "go test ./old/...") {
		t.Fatal("fact not stored")
	}

	// forget needs no content — Validate must accept that.
	if verr := tool.Validate(map[string]any{"type": "forget", "key": "test_command"}); verr != nil {
		t.Fatalf("forget without content must validate: %v", verr)
	}

	res, err = tool.Execute(context.Background(), map[string]any{
		"type": "forget", "key": "test_command",
	})
	if err != nil || !res.Success {
		t.Fatalf("forget failed: %v / %+v", err, res)
	}
	if !strings.Contains(res.Content, "Forgot") {
		t.Fatalf("forget result should say Forgot: %q", res.Content)
	}
	if strings.Contains(pl.FormatForPrompt(), "go test ./old/...") {
		t.Fatal("forgotten entry still renders into the prompt")
	}
	// The write actually mutated the store files — written_paths must be
	// declared so the executor invalidates the read-dedup cache (the same
	// out-of-band-writer rule memorize already follows for saves).
	data, ok := res.Data.(map[string]any)
	if !ok || data["written_paths"] == nil {
		t.Fatalf("forget must declare written_paths, got %+v", res.Data)
	}
}

func TestMemorizeTool_ForgetMissingKeyIsAnError(t *testing.T) {
	tool := NewMemorizeTool(newLearningForTest(t))
	res, err := tool.Execute(context.Background(), map[string]any{
		"type": "forget", "key": "never-existed",
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res.Success {
		t.Fatalf("forgetting a missing key must be an honest error, got success: %+v", res)
	}
}

// TestMemorizeTool_DeclarationDelegatesToSharedSchema pins the single-schema
// rule: MemorizeTool.Declaration() must serve the SAME schema as the lazy
// registry's MemorizeToolDeclaration — the two hand-maintained copies had
// already drifted once before being unified.
func TestMemorizeTool_DeclarationDelegatesToSharedSchema(t *testing.T) {
	tool := NewMemorizeTool(nil)
	if !reflect.DeepEqual(tool.Declaration(), MemorizeToolDeclaration()) {
		t.Fatal("MemorizeTool.Declaration must delegate to MemorizeToolDeclaration (schemas drifted)")
	}
	var hasForget bool
	for _, v := range tool.Declaration().Parameters.Properties["type"].Enum {
		if v == "forget" {
			hasForget = true
		}
	}
	if !hasForget {
		t.Fatal("schema must offer type=forget")
	}
}
