package context

import (
	"context"

	"google.golang.org/genai"
)

type CompactionCommitter func(ctx context.Context, history []*genai.Content, version int64, carryAt int, reason string) ([]*genai.Content, bool, error)

func (m *ContextManager) SetCompactionCommitter(commit CompactionCommitter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compactionCommitter = commit
}

func (m *ContextManager) commitCompaction(ctx context.Context, history []*genai.Content, version int64, carryAt int, reason string) ([]*genai.Content, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	m.mu.RLock()
	commit := m.compactionCommitter
	m.mu.RUnlock()
	if commit != nil {
		return commit(ctx, history, version, carryAt, reason)
	}
	committed, ok := m.session.SetHistoryIfVersionWithSkillCarry(history, version, carryAt)
	return committed, ok, nil
}
