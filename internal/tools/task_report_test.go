package tools

import (
	"strings"
	"testing"
)

// TestWriteSubAgentOutput pins the v0.98.x #8 fix: a sub-agent's transcript is
// folded back into the parent context capped to the tail (the conclusion),
// instead of dumping the full multi-turn narration and bloating the parent.
func TestWriteSubAgentOutput(t *testing.T) {
	t.Run("short output passes through whole", func(t *testing.T) {
		var b strings.Builder
		writeSubAgentOutput(&b, "all done: fixed the bug", "/tmp/out.txt")
		got := b.String()
		if !strings.Contains(got, "all done: fixed the bug") {
			t.Errorf("short output should pass through whole; got %q", got)
		}
		if strings.Contains(got, "truncated") {
			t.Errorf("short output must not be marked truncated; got %q", got)
		}
	})

	t.Run("empty output writes nothing", func(t *testing.T) {
		var b strings.Builder
		writeSubAgentOutput(&b, "", "/tmp/out.txt")
		if b.String() != "" {
			t.Errorf("empty output should write nothing; got %q", b.String())
		}
	})

	t.Run("long output keeps tail + marker + file pointer", func(t *testing.T) {
		dropMarker := "DROP_ME_HEAD_SENTINEL"
		conclusion := "CONCLUSION: shipped the fix and verified with go test"
		// Sentinel sits at the very start, beyond the tail window → must be dropped.
		full := dropMarker + strings.Repeat("A", maxSubAgentReportChars) + conclusion
		var b strings.Builder
		writeSubAgentOutput(&b, full, "/tmp/agent-out.txt")
		got := b.String()
		if !strings.Contains(got, "truncated") {
			t.Error("long output must be marked truncated")
		}
		if !strings.Contains(got, conclusion) {
			t.Error("the conclusion (tail) must survive truncation")
		}
		if !strings.Contains(got, "/tmp/agent-out.txt") {
			t.Error("truncated output must point at the OutputFile for the full transcript")
		}
		if strings.Contains(got, dropMarker) {
			t.Error("the discarded head (beyond the tail window) must not appear in parent-facing output")
		}
		if len([]rune(got)) > maxSubAgentReportChars+400 {
			t.Errorf("output not bounded: %d runes", len([]rune(got)))
		}
	})

	t.Run("long output without OutputFile still truncates", func(t *testing.T) {
		var b strings.Builder
		writeSubAgentOutput(&b, strings.Repeat("B", maxSubAgentReportChars+500), "")
		got := b.String()
		if !strings.Contains(got, "truncated") {
			t.Error("must truncate even without an OutputFile pointer")
		}
		if strings.Contains(got, "full output in") {
			t.Error("must not claim a file pointer when OutputFile is empty")
		}
	})
}
