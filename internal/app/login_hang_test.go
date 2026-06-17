package app

import (
	"context"
	"testing"
	"time"

	"google.golang.org/genai"

	"gokin/internal/chat"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/testkit"
)

// blockingCountClient blocks CountTokens until the ctx is cancelled — modeling
// the slow/unreachable count_tokens endpoint that froze /login. It embeds
// MockClient for the rest of the client.Client surface.
type blockingCountClient struct {
	*testkit.MockClient
}

func (b *blockingCountClient) CountTokens(ctx context.Context, contents []*genai.Content) (*genai.CountTokensResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestRefreshTokenCount_BoundedByTimeout pins the /login hang fix: a count_tokens
// round-trip that never returns must not block refreshTokenCount indefinitely.
// Before the fix it inherited a.ctx (no deadline) and stalled up to the 120s
// transport timeout; now a per-call timeout bounds it and UpdateTokenCount falls
// back to a local estimate.
func TestRefreshTokenCount_BoundedByTimeout(t *testing.T) {
	mock := testkit.NewMockClient().WithModel("glm-5.2")
	blocking := &blockingCountClient{MockClient: mock.(*testkit.MockClient)}

	cm := appcontext.NewContextManager(context.Background(), chat.NewSession(), blocking, &config.ContextConfig{})

	a := &App{
		ctx:                 context.Background(),
		config:              &config.Config{}, // sendTokenUsageUpdate reads config.UI.ShowTokenUsage
		contextManager:      cm,
		tokenRefreshTimeout: 200 * time.Millisecond,
	}

	done := make(chan struct{})
	go func() {
		a.refreshTokenCount()
		close(done)
	}()

	select {
	case <-done:
		// returned within the bound — good
	case <-time.After(3 * time.Second):
		t.Fatal("refreshTokenCount did not honor its timeout — count_tokens block was not bounded (the /login hang)")
	}
}
