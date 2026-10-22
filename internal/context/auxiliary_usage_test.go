package context

import (
	"context"
	"sync"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/testkit"
	"google.golang.org/genai"
)

func TestHierarchicalSummaryAccountsEveryRequest(t *testing.T) {
	model := testkit.NewMockClient()
	for range 12 {
		model.EnqueueText("A sufficiently detailed summary preserving the acceptance decisions.")
	}
	messages := make([]*genai.Content, 110)
	for i := range messages {
		messages[i] = genai.NewContentFromText("Conversation decisions and acceptance evidence.", genai.RoleUser)
	}
	counter := &client.TokenUsageCounter{}
	ctx := client.ContextWithTokenUsageCounter(t.Context(), counter)
	if _, err := NewSummarizer(model).Summarize(ctx, messages); err != nil {
		t.Fatal(err)
	}
	input, _ := counter.Snapshot()
	if len(model.Calls()) < 2 || input != len(model.Calls())*10 {
		t.Fatalf("hierarchical stages escaped usage: calls=%d input=%d", len(model.Calls()), input)
	}
}

func TestBoundedAuxiliaryWorkJoinsBeforeReturning(t *testing.T) {
	for _, kind := range []string{"compaction", "session-memory"} {
		t.Run(kind, func(t *testing.T) {
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			model := testkit.NewMockClient().EnqueueText("A sufficiently detailed memory summary retaining acceptance evidence.")
			model.OnSend = func(ctx context.Context) {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			counter := &client.TokenUsageCounter{}
			ctx := client.ContextWithBoundedAuxiliaryRequests(client.ContextWithTokenUsageCounter(t.Context(), counter))
			var run func()
			if kind == "compaction" {
				session := chat.NewSession()
				for range 30 {
					session.AddUserMessage("Preserve the implementation and acceptance decisions.")
				}
				manager := NewContextManager(t.Context(), session, model, &config.ContextConfig{MaxInputTokens: 100000, EnableAutoSummary: true})
				manager.SetSummaryStrategy(SummaryStrategy{InitialMessageCount: 1, RecentMessageCount: 1, MinMessagesForSummary: 2, TargetRatio: .5})
				run = func() { manager.backgroundOptimize(ctx) }
			} else {
				manager := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
				manager.SetSummarizer(NewClientSessionSummarizer(model))
				manager.ExtractAsyncWithContext(ctx, longHistory(), 10000)
				manager.ExtractAsyncWithContext(ctx, longHistory(), 20000)
				run = func() { manager.ExtractAsyncWithContext(ctx, longHistory(), 30000) }
			}
			go func() { run(); close(done) }()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("auxiliary request did not start")
			}
			select {
			case <-done:
				t.Fatal("one-shot caller returned while auxiliary model work was running")
			default:
			}
			once.Do(func() { close(release) })
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("auxiliary work did not join")
			}
			input, _ := counter.Snapshot()
			if input != 10 || len(model.Calls()) != 1 {
				t.Fatalf("joined usage lost: input=%d calls=%d", input, len(model.Calls()))
			}
		})
	}
}
