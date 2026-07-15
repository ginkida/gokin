package tools

import (
	"context"
	"testing"

	"gokin/internal/memory"
)

func TestMemoryRememberReturnsCanonicalIDAfterSemanticDedup(t *testing.T) {
	store, err := memory.NewStore(t.TempDir(), t.TempDir(), 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tool := NewMemoryTool()
	tool.SetStore(store)

	remember := func() map[string]any {
		t.Helper()
		result, execErr := tool.Execute(context.Background(), map[string]any{
			"action":  "remember",
			"content": "Deploy keys rotate every ninety days",
			"key":     "deploy-key",
		})
		if execErr != nil || !result.Success {
			t.Fatalf("remember failed: err=%v result=%+v", execErr, result)
		}
		data, ok := result.Data.(map[string]any)
		if !ok {
			t.Fatalf("remember data has type %T, want map", result.Data)
		}
		return data
	}

	first := remember()
	second := remember()
	firstID, _ := first["id"].(string)
	secondID, _ := second["id"].(string)
	if firstID == "" || secondID != firstID {
		t.Fatalf("deduplicated remember returned stale ID: first=%q second=%q", firstID, secondID)
	}
	if _, ok := store.GetByID(secondID); !ok {
		t.Fatalf("returned canonical ID %q is not stored", secondID)
	}

	feedback, execErr := tool.Execute(context.Background(), map[string]any{
		"action":  "feedback",
		"id":      secondID,
		"success": true,
	})
	if execErr != nil || !feedback.Success {
		t.Fatalf("feedback for returned ID failed: err=%v result=%+v", execErr, feedback)
	}
}
