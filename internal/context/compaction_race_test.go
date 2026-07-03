package context

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/testkit"

	"google.golang.org/genai"
)

// versionBumpingClient simulates a foreground turn committing NEW history to the
// session DURING background summarization: on the summarizer's SendMessage call
// it appends a message (bumping the session version) before returning the canned
// summary. Deterministic — the version always changes mid-compaction.
type versionBumpingClient struct {
	*testkit.MockClient
	sess   *chat.Session
	bumped bool
}

func (c *versionBumpingClient) SendMessage(ctx context.Context, message string) (*client.StreamingResponse, error) {
	if !c.bumped {
		c.bumped = true
		h := append([]*genai.Content{}, c.sess.GetHistory()...)
		h = append(h, genai.NewContentFromText("CONCURRENT TURN MESSAGE", genai.RoleModel))
		c.sess.SetHistory(h) // bumps session version
	}
	return c.MockClient.SendMessage(ctx, message)
}

// Background IncrementalCompact must NOT overwrite a turn that committed
// concurrently during summarization. The optimistic SetHistoryIfVersion commit
// skips on version mismatch, so the just-finished turn's message survives.
// (This is a lost-update the -race detector can't catch — both writers hold
// s.mu — so it needs an ordering test.)
func TestIncrementalCompact_SkipsCommitWhenHistoryChangedDuringSummarize(t *testing.T) {
	sess := chat.NewSession()
	var hist []*genai.Content
	for i := 0; i < 80; i++ {
		role := genai.RoleUser
		if i%2 == 1 {
			role = genai.RoleModel
		}
		hist = append(hist, genai.NewContentFromText(fmt.Sprintf("msg%d", i), genai.Role(role)))
	}
	sess.SetHistory(hist)

	mc := testkit.NewMockClient()
	mc.EnqueueText("User implemented the backup feature; touched internal/cmd/backup.go and added tests.")
	summarizer := NewSummarizer(&versionBumpingClient{MockClient: mc, sess: sess})

	m := &ContextManager{
		session:         sess,
		tokenCounter:    &TokenCounter{limits: TokenLimits{MaxInputTokens: 100000}},
		keyFiles:        map[string]bool{},
		summaryStrategy: DefaultSummaryStrategy(),
		summarizer:      summarizer,
		config:          &config.ContextConfig{EnableAutoSummary: true},
	}

	if err := m.IncrementalCompact(context.Background()); err != nil {
		t.Fatalf("IncrementalCompact returned error: %v", err)
	}

	final := sess.GetHistory()
	found := false
	for _, c := range final {
		if len(c.Parts) > 0 && strings.Contains(c.Parts[0].Text, "CONCURRENT TURN MESSAGE") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the concurrent turn's message was wiped by a stale compaction commit (history len=%d)", len(final))
	}
}
