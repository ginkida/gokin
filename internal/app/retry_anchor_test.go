package app

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

func userContent(text string) *genai.Content {
	return genai.NewContentFromText(text, genai.RoleUser)
}

func modelTextContent(text string) *genai.Content {
	return genai.NewContentFromText(text, genai.RoleModel)
}

func modelToolCallContent(name string, args map[string]any) *genai.Content {
	return &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: name, Args: args}},
		},
	}
}

// TestNextRetryMessageAfterProgress_UnifiesAnchorAcrossBranches (round 5)
// pins the fix for the "stale retry anchor" bug in the app-level retry loop
// (message_processor.go). Previously retryMessage was set independently per
// retry-decision branch:
//
//	overload branch: retryMessage = originalMessage        (always reset)
//	partial branch:  retryMessage = continuation-anchored  (always wrapped)
//	plain branch:    retryMessage left untouched            (stale carry-over)
//
// A branch transition mid-retry-loop (partial stall -> overload -> plain)
// could desync the anchor from what was actually persisted: an overload
// reset discarded a still-valid partial-stall continuation, and a plain
// failure kept a stale anchor from several iterations back. The fix bases
// the anchor purely on "did THIS attempt make real progress" — a function of
// history, not of which branch fired — so it can't desync from what's
// persisted regardless of which failure classification hits next.
func TestNextRetryMessageAfterProgress_UnifiesAnchorAcrossBranches(t *testing.T) {
	original := "please refactor the payment module"
	preAttempt := []*genai.Content{userContent("earlier turn"), modelTextContent("earlier reply")}

	// This attempt made real progress: it called a tool before failing
	// (e.g. a partial stream stall after a tool_use round, or — the bug
	// scenario — an OVERLOAD that hit AFTER a tool call round succeeded).
	// The old code's overload branch would unconditionally discard this by
	// resetting retryMessage to the bare original. The fix must not.
	cleaned := append(append([]*genai.Content{}, preAttempt...),
		userContent(original),
		modelToolCallContent("read", map[string]any{"file_path": "payments.go"}),
	)

	got := nextRetryMessageAfterProgress(original, preAttempt, cleaned)

	if got == original {
		t.Fatal("expected the anchor to preserve this attempt's progress, got the bare original message — this is the exact overload-branch bug (discarding a valid continuation anchor)")
	}
	if !strings.Contains(got, "read") {
		t.Fatalf("expected the anchor to reference the tool call made this attempt, got: %q", got)
	}
	if !strings.HasSuffix(got, "\n\n"+original) {
		t.Fatalf("expected the anchor to still end with the original message, got: %q", got)
	}
}

// TestNextRetryMessageAfterProgress_NoProgressReturnsOriginalVerbatim proves
// the fix doesn't over-correct: Executor.Execute always appends the user's
// message to history before any Send* call, so len(cleaned) > len(preAttempt)
// is true even on an immediate zero-content failure. Without scoping to the
// newly-appended portion, every plain retry would get needlessly wrapped in
// interruption boilerplate. A genuinely empty attempt must resend the
// original message unchanged, matching the old (correct-in-this-case) plain
// branch's first-iteration behavior.
func TestNextRetryMessageAfterProgress_NoProgressReturnsOriginalVerbatim(t *testing.T) {
	original := "list the files in this repo"
	preAttempt := []*genai.Content{}
	// Executor.Execute appended the user's own turn, then failed immediately
	// with zero model content — no text, no tool call.
	cleaned := []*genai.Content{userContent(original)}

	got := nextRetryMessageAfterProgress(original, preAttempt, cleaned)

	if got != original {
		t.Fatalf("expected verbatim original for a zero-progress attempt, got: %q", got)
	}
}

// TestNextRetryMessageAfterProgress_DoesNotMisattributeOlderSessionTurn
// guards the other failure mode a naive len(cleaned) > len(preAttempt) check
// would hit: preAttempt already contains an OLDER, unrelated model turn from
// earlier in the session. If this attempt made no new progress, the anchor
// must not resurrect that older turn's content as if it were "the
// interrupted response" — it must fall back to the bare original message.
func TestNextRetryMessageAfterProgress_DoesNotMisattributeOlderSessionTurn(t *testing.T) {
	original := "now do something unrelated"
	preAttempt := []*genai.Content{
		userContent("earlier, unrelated task"),
		modelTextContent("I finished the earlier, unrelated task successfully."),
	}
	// This attempt only appended its own (failed, contentless) user turn.
	cleaned := append(append([]*genai.Content{}, preAttempt...), userContent(original))

	got := nextRetryMessageAfterProgress(original, preAttempt, cleaned)

	if got != original {
		t.Fatalf("expected verbatim original (no misattribution of the older turn), got: %q", got)
	}
	if strings.Contains(got, "unrelated task") {
		t.Fatalf("anchor leaked an older, unrelated session turn's content: %q", got)
	}
}

// TestNextRetryMessageAfterProgress_TextProgressAnchorsLastSentence covers
// the ordinary partial-stall case: this attempt streamed real text before
// failing. The anchor should reference that text, not the generic
// "interrupted" fallback used when nothing decodable is found.
func TestNextRetryMessageAfterProgress_TextProgressAnchorsLastSentence(t *testing.T) {
	original := "write a summary of the changes"
	preAttempt := []*genai.Content{}
	cleaned := []*genai.Content{
		userContent(original),
		modelTextContent("Here is the summary. The build passes."),
	}

	got := nextRetryMessageAfterProgress(original, preAttempt, cleaned)

	if got == original {
		t.Fatal("expected a continuation anchor for real text progress, got the bare original")
	}
	if !strings.Contains(got, "The build passes") {
		t.Fatalf("expected the anchor to reference the last complete sentence, got: %q", got)
	}
}
