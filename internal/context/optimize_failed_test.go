package context

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/config"
	"gokin/internal/testkit"

	"google.golang.org/genai"
)

// --- OnOptimizeFailed / #7-#8 quota-burning-background-LLM-calls fixes. ---
// Every background compaction path only Warn-logged a failure — invisible to
// the user, and on a quota-limited provider (GLM's 5h cap) a degraded
// endpoint could silently spend real API calls against the cap with zero
// on-screen signal. These tests pin that a failure now fires OnOptimizeFailed.

// bigHistory builds a history LONGER than IncrementalCompact's hardcoded
// preserveCount (min(50, len(history))) — with exactly 17 messages (the
// package's longHistory()), preserveCount==len(history) and IncrementalCompact
// returns nil immediately (nothing old enough to compact), never reaching the
// summarizer at all.
func bigHistory(n int) []*genai.Content {
	h := make([]*genai.Content, 0, n)
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			h = append(h, userMsg("continuing the task"))
		} else {
			h = append(h, modelMsg("working on it"))
		}
	}
	return h
}

// TestTryAutoCompact_NotifiesStartAndFailure pins BOTH halves of the fix:
// tryAutoCompact was the ONE compaction trigger (of four) that never called
// notifyOptimizeStart, and its IncrementalCompact failure was Warn-log only.
func TestTryAutoCompact_NotifiesStartAndFailure(t *testing.T) {
	mc := testkit.NewMockClient()
	mc.EnqueueStartupError(context.DeadlineExceeded)

	sess := chat.NewSession()
	sess.SetHistory(bigHistory(60))

	m := NewContextManager(context.Background(), sess, mc, &config.ContextConfig{
		EnableAutoSummary:    true,
		AutoCompactThreshold: 0.5,
	})
	m.summaryStrategy = SummaryStrategy{MinMessagesForSummary: 2, RecentMessageCount: 2, TargetRatio: 0.5}

	var startReasons []string
	var failReasons []string
	m.OnOptimizeStart = func(reason string) { startReasons = append(startReasons, reason) }
	m.OnOptimizeFailed = func(reason string, err error) { failReasons = append(failReasons, reason) }

	m.tokenCounter.limits = TokenLimits{MaxInputTokens: 100}
	// 90 / 100 = 90% — well over the 0.5 threshold.
	m.tryAutoCompact(context.Background(), 90)

	if len(startReasons) != 1 {
		t.Fatalf("tryAutoCompact must fire OnOptimizeStart, got %v", startReasons)
	}
	if len(failReasons) != 1 || !strings.Contains(failReasons[0], "auto-compact") {
		t.Fatalf("a failing IncrementalCompact must fire OnOptimizeFailed, got %v", failReasons)
	}
}

// TestBackgroundOptimize_NotifiesFailureOnGenuineError pins that a genuine
// OptimizeContext failure (not the ErrHistoryTooShort/ErrNothingToSummarize
// "nothing to do" sentinels) fires OnOptimizeFailed.
func TestBackgroundOptimize_NotifiesFailureOnGenuineError(t *testing.T) {
	mc := testkit.NewMockClient()
	mc.EnqueueStartupError(context.DeadlineExceeded)

	sess := chat.NewSession()
	sess.SetHistory(longHistory())

	m := NewContextManager(context.Background(), sess, mc, &config.ContextConfig{EnableAutoSummary: true})
	m.summaryStrategy = SummaryStrategy{MinMessagesForSummary: 2, RecentMessageCount: 2, TargetRatio: 0.5}

	var failErr error
	failed := false
	m.OnOptimizeFailed = func(reason string, err error) {
		failed = true
		failErr = err
	}

	m.backgroundOptimize(context.Background())
	waitForOptimizeDone(m)

	if !failed {
		t.Fatal("backgroundOptimize did not report the genuine failure via OnOptimizeFailed")
	}
	if failErr == nil {
		t.Fatal("OnOptimizeFailed fired with a nil error")
	}
}

// TestBackgroundOptimize_SentinelErrorsDoNotNotifyFailure guards against a
// false alarm: ErrHistoryTooShort/ErrNothingToSummarize are normal "nothing
// to do" outcomes (the same sentinel-error convention /compact uses), not
// failures worth surfacing to the user.
func TestBackgroundOptimize_SentinelErrorsDoNotNotifyFailure(t *testing.T) {
	mc := testkit.NewMockClient()
	sess := chat.NewSession()
	sess.SetHistory(longHistory())

	m := NewContextManager(context.Background(), sess, mc, &config.ContextConfig{EnableAutoSummary: true})
	// MinMessagesForSummary above the history length -> ErrHistoryTooShort,
	// a legitimate no-op, never reaching the summarizer.
	m.summaryStrategy = SummaryStrategy{MinMessagesForSummary: 1000, RecentMessageCount: 2, TargetRatio: 0.5}

	failed := false
	m.OnOptimizeFailed = func(string, error) { failed = true }

	m.backgroundOptimize(context.Background())
	waitForOptimizeDone(m)

	if failed {
		t.Fatal("a short-history no-op must NOT fire OnOptimizeFailed")
	}
}

func waitForOptimizeDone(m *ContextManager) {
	m.mu.Lock()
	done := m.summarizeDone
	m.mu.Unlock()
	if done != nil {
		<-done
	}
}
