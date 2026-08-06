package app

import (
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/client"

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

func TestNextAutoResumeMessageAfterProgress_FirstRecoveryAnchorsPartialText(t *testing.T) {
	original := "finish the timeout fix"
	preAttempt := []*genai.Content{userContent("older task"), modelTextContent("older answer")}
	committed := append(append([]*genai.Content{}, preAttempt...),
		userContent(original),
		modelTextContent("I inspected the stream. The response accumulator is intact."),
	)

	got := nextAutoResumeMessageAfterProgress(original, preAttempt, committed, false)
	if got == original {
		t.Fatal("first auto-resume discarded partial progress and replayed the bare request")
	}
	if !strings.Contains(got, "The response accumulator is intact") {
		t.Fatalf("auto-resume payload lacks the latest partial anchor: %q", got)
	}
	if !strings.HasSuffix(got, "\n\n"+original) {
		t.Fatalf("auto-resume payload lost the original objective: %q", got)
	}
}

func TestNextAutoResumeMessageAfterProgress_RecoveryPayloadIdentityIsStable(t *testing.T) {
	persistedPayload := "[System note: previous response was interrupted. Continue from where you stopped.]\n\nfinish the timeout fix"
	preAttempt := []*genai.Content{userContent(persistedPayload)}
	committed := append(append([]*genai.Content{}, preAttempt...),
		modelTextContent("More progress from the recovery attempt."),
	)

	got := nextAutoResumeMessageAfterProgress(persistedPayload, preAttempt, committed, true)
	if got != persistedPayload {
		t.Fatalf("recovery payload identity changed across attempts:\n got: %q\nwant: %q", got, persistedPayload)
	}
}

func TestAnchoredAutoResumePayloadContinuesPersistedRetryBudget(t *testing.T) {
	original := "finish the timeout fix"
	preAttempt := []*genai.Content{userContent(original)}
	committed := append(append([]*genai.Content{}, preAttempt...),
		modelTextContent("Partial result before the timeout."),
	)
	payload := nextAutoResumeMessageAfterProgress(original, preAttempt, committed, false)
	if payload == original {
		t.Fatal("test setup did not produce an anchored recovery payload")
	}

	a := &App{autoResumeCount: make(map[string]int)}
	// This is what claimPendingRecovery restores after the first scheduled
	// attempt survives a process restart or wakes from its durable timer.
	a.seedPendingRecoveryBudget(chat.SerializedPendingRecovery{
		Message:            payload,
		Kind:               "auto_resume",
		Attempt:            1,
		AutoResumeAttempts: 1,
	})

	attempt, _, ok := a.scheduleAutoResume(
		payload, client.NewModelRoundTimeoutError(client.DefaultModelRoundTimeout))
	if !ok || attempt != 2 {
		t.Fatalf("anchored recovery resumed at attempt=%d ok=%v, want bounded attempt 2", attempt, ok)
	}
	if _, _, ok := a.scheduleAutoResume(
		payload, client.NewModelRoundTimeoutError(client.DefaultModelRoundTimeout)); ok {
		t.Fatal("anchored recovery reset its key and exceeded the two-attempt budget")
	}
}

func TestAnchoredAutoResumePayloadOwnsInProcessBudgetFromFirstAttempt(t *testing.T) {
	original := "finish the timeout fix"
	preAttempt := []*genai.Content{userContent(original)}
	committed := append(append([]*genai.Content{}, preAttempt...),
		modelTextContent("Partial result before the timeout."),
	)
	payload := nextAutoResumeMessageAfterProgress(original, preAttempt, committed, false)
	a := &App{autoResumeCount: make(map[string]int)}
	timeoutErr := client.NewModelRoundTimeoutError(client.DefaultModelRoundTimeout)

	first, _, ok := a.scheduleAutoResume(payload, timeoutErr)
	if !ok || first != 1 {
		t.Fatalf("first anchored schedule = (%d,%v), want (1,true)", first, ok)
	}
	second, _, ok := a.scheduleAutoResume(payload, timeoutErr)
	if !ok || second != 2 {
		t.Fatalf("second anchored schedule = (%d,%v), want (2,true)", second, ok)
	}
	if _, _, ok := a.scheduleAutoResume(payload, timeoutErr); ok {
		t.Fatal("in-process anchored payload exceeded the bounded retry budget")
	}
	if _, exists := a.autoResumeCount[rateLimitRetryKey(original)]; exists {
		t.Fatal("bare prompt received a separate counter from the executable anchored payload")
	}
}

// TestNextRetryMessageAfterProgress_CleanedNotLongerThanPreAttempt guards an
// edge case flagged by the round-5 adversarial diff review: if
// stripOrphanFunctionCalls ever shrinks `cleaned` to AT OR BELOW
// preAttempt's length (e.g. it stripped an orphan from the already-
// persisted prefix, not just this attempt's tail — a violation of the
// pre-existing "persisted history is orphan-free" invariant, not something
// this attempt itself causes), the function must NOT fall back to scanning
// the whole (now equal-or-shorter) cleaned slice — that could resurrect an
// OLDER, unrelated turn's content as if it were this attempt's progress.
func TestNextRetryMessageAfterProgress_CleanedNotLongerThanPreAttempt(t *testing.T) {
	original := "now do something else"
	preAttempt := []*genai.Content{
		userContent("earlier, unrelated task"),
		modelTextContent("I finished the earlier, unrelated task successfully."),
	}

	t.Run("equal length", func(t *testing.T) {
		cleaned := append([]*genai.Content{}, preAttempt...)
		got := nextRetryMessageAfterProgress(original, preAttempt, cleaned)
		if got != original {
			t.Fatalf("expected verbatim original, got: %q", got)
		}
	})

	t.Run("shorter than preAttempt", func(t *testing.T) {
		// Simulates stripOrphanFunctionCalls removing an entry from the
		// already-persisted prefix itself.
		cleaned := preAttempt[:1]
		got := nextRetryMessageAfterProgress(original, preAttempt, cleaned)
		if got != original {
			t.Fatalf("expected verbatim original, got: %q", got)
		}
		if strings.Contains(got, "unrelated task") {
			t.Fatalf("anchor leaked an older, unrelated turn's content: %q", got)
		}
	})
}
