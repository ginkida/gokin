package client

import (
	"context"
	"sync"
)

// TokenUsageCounter attributes nested model work without deriving it from
// process-wide totals that may also include unrelated background requests.
type TokenUsageCounter struct {
	mu            sync.Mutex
	input, output int
}
type tokenUsageCounterKey struct{}
type tokenUsageCheckpointKey struct{}

func ContextWithTokenUsageCounter(ctx context.Context, counter *TokenUsageCounter) context.Context {
	if counter == nil {
		return ctx
	}
	parents, _ := ctx.Value(tokenUsageCounterKey{}).([]*TokenUsageCounter)
	for _, parent := range parents {
		if parent == counter {
			return ctx
		}
	}
	counters := append(append([]*TokenUsageCounter(nil), parents...), counter)
	return context.WithValue(ctx, tokenUsageCounterKey{}, counters)
}
func RecordContextTokenUsage(ctx context.Context, input, output int) {
	if ctx == nil {
		return
	}
	counters, _ := ctx.Value(tokenUsageCounterKey{}).([]*TokenUsageCounter)
	for _, counter := range counters {
		counter.mu.Lock()
		counter.input += max(input, 0)
		counter.output += max(output, 0)
		counter.mu.Unlock()
	}
}

// ContextWithTokenUsageCheckpoint schedules no work itself. The owner provides
// a durable accounting boundary that descendant runners call after a run ends,
// outside agent state locks and before delivering completion notifications.
func ContextWithTokenUsageCheckpoint(ctx context.Context, checkpoint func()) context.Context {
	return context.WithValue(ctx, tokenUsageCheckpointKey{}, checkpoint)
}

func CheckpointContextTokenUsage(ctx context.Context) {
	if ctx == nil {
		return
	}
	if checkpoint, ok := ctx.Value(tokenUsageCheckpointKey{}).(func()); ok && checkpoint != nil {
		checkpoint()
	}
}
func (counter *TokenUsageCounter) Snapshot() (int, int) {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.input, counter.output
}
