package commands

import (
	"errors"
	"strings"
	"testing"

	appcontext "gokin/internal/context"
)

// TestFormatCompactionResult covers the message-rendering contract for
// /compact. Regression for v0.80.5: the prior version reported "Context
// compacted successfully" whenever ForceSummarize returned nil, even when
// the summarizer was a no-op (history too short, nothing left to compact,
// summarizer not configured). Now each case has its own message so the
// user can act on it.
func TestFormatCompactionResult(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		tokensBefore int
		tokensAfter  int
		pctAfter     float64
		wantContains string
	}{
		{
			name:         "summarizer_unavailable",
			err:          appcontext.ErrSummarizerUnavailable,
			wantContains: "summarizer is not configured",
		},
		{
			name:         "history_too_short",
			err:          appcontext.ErrHistoryTooShort,
			wantContains: "too short to compact",
		},
		{
			name:         "nothing_to_summarize",
			err:          appcontext.ErrNothingToSummarize,
			wantContains: "every message is pinned",
		},
		{
			name:         "real_failure",
			err:          errors.New("network timeout"),
			wantContains: "Compaction failed: network timeout",
		},
		{
			name:         "successful_compaction_freed_tokens",
			err:          nil,
			tokensBefore: 50000,
			tokensAfter:  20000,
			pctAfter:     0.4,
			wantContains: "freed 30k",
		},
		{
			name:         "successful_compaction_no_token_count",
			err:          nil,
			tokensBefore: 0,
			tokensAfter:  0,
			wantContains: "Context compacted successfully",
		},
		{
			name:         "compaction_ran_but_no_tokens_freed",
			err:          nil,
			tokensBefore: 30000,
			tokensAfter:  30000, // same — summarizer no-op
			wantContains: "freed no tokens",
		},
		{
			name:         "compaction_grew_the_context",
			err:          nil,
			tokensBefore: 10000,
			tokensAfter:  12000, // verbose summary made it bigger
			wantContains: "freed no tokens",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatCompactionResult(tc.err, tc.tokensBefore, tc.tokensAfter, tc.pctAfter)
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("expected message to contain %q, got %q", tc.wantContains, got)
			}
		})
	}
}

// TestFormatCompactionResult_NoOpSentinelsAreNotErrors pins that the three
// no-op sentinels are framed as informational ("No-op: ...") not as
// failures. Previously they would have been logged as silent success which
// looked exactly like a no-op compaction with 0 tokens freed — confusing.
func TestFormatCompactionResult_NoOpSentinelsAreNotErrors(t *testing.T) {
	sentinels := []error{
		appcontext.ErrSummarizerUnavailable,
		appcontext.ErrHistoryTooShort,
		appcontext.ErrNothingToSummarize,
	}
	for _, sentinel := range sentinels {
		got := formatCompactionResult(sentinel, 0, 0, 0)
		if !strings.HasPrefix(got, "No-op:") {
			t.Errorf("sentinel %q should produce 'No-op:' prefix, got %q", sentinel, got)
		}
		if strings.Contains(got, "failed") {
			t.Errorf("sentinel %q should not be framed as failure, got %q", sentinel, got)
		}
	}
}

// TestFormatCompactionResult_RealFailureIsNotANoOp pins the inverse: a
// non-sentinel error must still surface as a real failure ("Compaction
// failed:") so users don't confuse a network timeout or quota issue with
// the benign no-op cases above.
func TestFormatCompactionResult_RealFailureIsNotANoOp(t *testing.T) {
	got := formatCompactionResult(errors.New("rate limit exceeded"), 0, 0, 0)
	if !strings.HasPrefix(got, "Compaction failed:") {
		t.Errorf("real error should produce 'Compaction failed:' prefix, got %q", got)
	}
	if strings.HasPrefix(got, "No-op:") {
		t.Errorf("real error should not be framed as no-op, got %q", got)
	}
}

// TestLongRunningCommands pins which commands set LongRunning=true. If you
// remove a command from this list, justify it: the LongRunning hint is
// what makes the TUI distinguish "command in flight" from "model thinking
// about a regular message", which was the original bug for /compact.
func TestLongRunningCommands(t *testing.T) {
	wantLongRunning := map[string]string{
		"compact": "Compacting context",
		"pr":      "Creating PR",
		"update":  "Checking for updates",
		"mcp":     "Talking to MCP server",
	}

	for name, expectedLabelPrefix := range wantLongRunning {
		t.Run(name, func(t *testing.T) {
			h := NewHandler()
			cmd, ok := h.GetCommand(name)
			if !ok {
				t.Fatalf("command /%s not registered", name)
			}
			provider, ok := cmd.(MetadataProvider)
			if !ok {
				t.Fatalf("command /%s does not implement MetadataProvider", name)
			}
			meta := provider.GetMetadata()
			if !meta.LongRunning {
				t.Errorf("command /%s should be marked LongRunning=true (regression: was a slow op without progress hint)", name)
			}
			if !strings.HasPrefix(meta.LongRunningLabel, expectedLabelPrefix) {
				t.Errorf("command /%s LongRunningLabel = %q, expected prefix %q",
					name, meta.LongRunningLabel, expectedLabelPrefix)
			}
		})
	}
}
