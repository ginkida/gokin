package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gokin/internal/chat"
	appcontext "gokin/internal/context"
	"gokin/internal/ui"

	"google.golang.org/genai"
)

// SummarizeAtPoint condenses only the selected interval. It keeps the current
// invocation ledger, hook consumption, scratchpad and retry generations; this
// is a context change within the same conversation, not a rewind.
func (a *App) SummarizeAtPoint(ctx context.Context, id, mode, instructions string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(instructions) > 4096 {
		return "", fmt.Errorf("summary guidance exceeds 4096 bytes")
	}
	store, err := a.turnRewindStore()
	if err != nil {
		return "", err
	}
	initial := a.session.GetState()
	epoch := a.recoveryEpoch.Load()
	plan, err := store.PlanSummary(id, mode, initial)
	if err != nil {
		return "", err
	}
	messages := make([]*genai.Content, len(plan.Messages))
	for i, item := range plan.Messages {
		message, err := chat.DeserializeContent(item)
		if err != nil {
			return "", err
		}
		messages[i] = message
	}
	// A point belongs before a user prompt. Refuse any malformed persisted
	// boundary rather than cutting a tool call away from its response.
	combined := append(append(append([]chat.SerializedContent{}, plan.Before...), plan.Messages...), plan.After...)
	contents := make([]*genai.Content, len(combined))
	for i, item := range combined {
		contents[i], err = chat.DeserializeContent(item)
		if err != nil {
			return "", err
		}
	}
	start, end := len(plan.Before), len(plan.Before)+len(plan.Messages)
	if appcontext.AdjustBoundaryForToolPairs(contents, start) != start || appcontext.AdjustBoundaryForToolPairs(contents, end) != end {
		return "", fmt.Errorf("summary boundary splits a tool call and its response")
	}
	a.safeSendToProgram(ui.StatusUpdateMsg{Type: ui.StatusInfo, Message: "Summarizing the selected conversation range…"})
	summarizer := appcontext.NewSummarizer(a.GetMainClient())
	summary, err := summarizer.SummarizeWithInstructions(ctx, messages, instructions)
	if err != nil {
		return "", err
	}
	var text strings.Builder
	for _, part := range summary.Parts {
		if part != nil {
			text.WriteString(part.Text)
		}
	}
	marker := chat.SerializeContent(genai.NewContentFromText(fmt.Sprintf("[Summarized conversation: %s]\n%s\nOriginal messages: use read with file_path gokin://summary/%s (offset/limit supported).\n[End of summarized conversation]", plan.ID, text.String(), plan.ID), genai.RoleUser))
	history := append(append(append([]chat.SerializedContent{}, plan.Before...), marker), plan.After...)
	// Include active skill carry in the candidate before checking savings. A
	// summarized skill load may have to be reattached to preserve its workflow.
	candidate := a.session.GetState()
	candidate.History = history
	candidate.TokenCounts = make([]int, len(history))
	candidate.TotalTokens = 0
	probe := chat.NewSession()
	if err := probe.RestoreFromState(candidate); err != nil {
		return "", err
	}
	history = probe.GetState().History
	// Include marker and archive reference overhead when judging whether the
	// operation actually frees context. A longer summary never replaces text.
	oldJSON, _ := json.Marshal(initial.History)
	newJSON, _ := json.Marshal(history)
	if len(newJSON) >= len(oldJSON) {
		return "", fmt.Errorf("summary would not reduce the current context; conversation kept unchanged")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	if !plan.Matches(a.session.GetState()) || a.recoveryEpoch.Load() != epoch {
		return "", fmt.Errorf("conversation changed while the summary was being generated")
	}
	if a.sessionLease == nil {
		lease, err := chat.AcquireSessionWriterLease(initial.ID)
		if err != nil {
			return "", err
		}
		a.sessionLease = lease
	}
	if !a.sessionLease.IsActive() || a.sessionLease.SessionID() != initial.ID {
		return "", fmt.Errorf("summary session ownership changed")
	}
	if err := store.SaveSummary(plan, marker); err != nil {
		return "", err
	}
	err = a.sessionManager.ApplyAndSave(func() (func(), error) {
		before := a.session.GetState()
		if !plan.Matches(before) || a.recoveryEpoch.Load() != epoch {
			return nil, fmt.Errorf("conversation changed before summary publication")
		}
		state := a.session.GetState()
		state.History = history
		state.TokenCounts = make([]int, len(history))
		state.TotalTokens = 0
		state.Version = before.Version + 1
		probe := chat.NewSession()
		if err := probe.RestoreFromState(state); err != nil {
			return nil, err
		}
		if err := a.session.RestoreFromState(state); err != nil {
			return nil, err
		}
		return func() { _ = a.session.RestoreFromState(before) }, nil
	})
	if err != nil {
		return "", err
	}
	// The session keeps the same ledger instance during RestoreFromState, so
	// executable skill hooks and their once state remain bound to the owner.
	if mode == "summarize-from" {
		a.safeSendToProgram(ui.RewindPromptMsg{Prompt: plan.Prompt})
	} else {
		a.safeSendToProgram(ui.RewindPromptMsg{Prompt: ""})
	}
	a.safeSendToProgram(ui.StreamTextMsg("\nSummarized conversation\n"))
	a.RefreshTokenCount()
	return fmt.Sprintf("Summarized %d message(s). Original messages preserved as %s.", len(plan.Messages), plan.ID), nil
}

func (a *App) ReadSummaryTranscript(ctx context.Context, id string) (string, error) {
	text, err := a.ReadSummaryResource(ctx, id)
	if err != nil {
		return "", err
	}
	if len(text) > 1<<20 {
		return "", fmt.Errorf("transcript is too large to display at once; use read with gokin://summary/%s and offset/limit", id)
	}
	return text, nil
}

func (a *App) ReadSummaryResource(ctx context.Context, id string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	store, err := a.turnRewindStore()
	if err != nil {
		return "", err
	}
	archive, err := store.Summary(id)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(archive.Original, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
