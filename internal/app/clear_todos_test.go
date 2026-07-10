package app

import (
	"context"
	"testing"

	"gokin/internal/tools"
)

// TestClearTodosForNewConversation pins the "/clear = clean slate" fix for the
// todo list: ClearConversation never reset the foreground TodoTool, so the
// Ctrl+T panel and the "Ctrl+T tasks N" status-bar hint kept showing the
// PREVIOUS conversation's tasks in the fresh one.
func TestClearTodosForNewConversation(t *testing.T) {
	reg := tools.NewRegistry()
	todo := tools.NewTodoTool()
	reg.MustRegister(todo)

	// Seed items the way the agent does — through Execute.
	_, err := todo.Execute(context.Background(), map[string]any{
		"todos": []any{
			map[string]any{"content": "old task", "status": "pending"},
		},
	})
	if err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	if len(todo.GetItems()) == 0 {
		t.Fatal("precondition: todo tool must hold items")
	}

	a := &App{registry: reg}
	a.clearTodosForNewConversation()

	if got := len(todo.GetItems()); got != 0 {
		t.Fatalf("todo items must be cleared for the new conversation, still %d", got)
	}
}

// TestClearTodosForNewConversation_NilRegistryNoPanic guards the minimal-App path.
func TestClearTodosForNewConversation_NilRegistryNoPanic(t *testing.T) {
	a := &App{}
	a.clearTodosForNewConversation()
}
