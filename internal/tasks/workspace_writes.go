package tasks

import (
	"context"

	"gokin/internal/workspace"
)

type writeCompletionKey struct{}

type WriteCompletion func(success bool) workspace.Delta

// ContextWithWriteCompletion binds a bash-owned observer before Start launches
// the process. Task.Done is published only after this evidence is retained.
func ContextWithWriteCompletion(ctx context.Context, complete WriteCompletion) context.Context {
	return context.WithValue(ctx, writeCompletionKey{}, complete)
}

func completeWorkspaceWrites(complete WriteCompletion, success bool) (delta workspace.Delta) {
	defer func() {
		if recover() != nil {
			delta = workspace.Delta{Partial: true, Warning: "background workspace observer panicked"}
		}
	}()
	return complete(success)
}
