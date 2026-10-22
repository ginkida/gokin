package client

import (
	"context"
	"sync"
	"testing"
)

func TestTokenUsageCounterPreservesParentAcrossNestedAndDetachedWork(t *testing.T) {
	parent, nested, sibling := &TokenUsageCounter{}, &TokenUsageCounter{}, &TokenUsageCounter{}
	ctx, cancel := context.WithCancel(ContextWithTokenUsageCounter(t.Context(), parent))
	child := ContextWithTokenUsageCounter(ctx, nested)
	child = ContextWithTokenUsageCounter(child, parent)
	other := ContextWithTokenUsageCounter(ctx, sibling)
	RecordContextTokenUsage(ctx, 5, 2)
	cancel()
	child = context.WithoutCancel(child)
	var workers sync.WaitGroup
	for range 20 {
		workers.Add(1)
		go func() { defer workers.Done(); RecordContextTokenUsage(child, 3, 1) }()
	}
	RecordContextTokenUsage(other, 7, 4)
	workers.Wait()
	for counter, want := range map[*TokenUsageCounter][2]int{parent: {72, 26}, nested: {60, 20}, sibling: {7, 4}} {
		input, output := counter.Snapshot()
		if input != want[0] || output != want[1] {
			t.Fatalf("counter=%p usage=%d/%d want=%v", counter, input, output, want)
		}
	}
}
