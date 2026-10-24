package agent

import (
	"testing"

	"gokin/internal/client"
	"gokin/internal/testkit"
)

type budgetSnapshotClient struct {
	*testkit.MockClient
	resets int
}

func (c *budgetSnapshotClient) ResetConversationCache() { c.resets++ }

func TestInvocationBudgetClientForwardsSnapshotReset(t *testing.T) {
	base := &budgetSnapshotClient{MockClient: testkit.NewMockClient()}
	wrapped := newInvocationBudgetClient(base)
	client.ResetConversationCache(wrapped)
	if base.resets != 1 {
		t.Fatalf("budget wrapper did not reset its provider: %d", base.resets)
	}
}
