package app

import (
	"fmt"
	"strings"

	"gokin/internal/chat"
	"gokin/internal/logging"
	"google.golang.org/genai"
)

// prepareHeadlessRecovery runs only after acquiring the foreground slot. An
// exact scheduled request retains the normal checkpoint/claim path. Otherwise
// an explicit headless invocation acknowledges the interruption and starts its
// supplied prompt; it never dispatches the old executable recovery message or
// applies an old checkpoint generation to a different request.
func (a *App) prepareHeadlessRecovery(prompt string) (*chat.SerializedPendingRecovery, conversationLineage, error) {
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	lineage := conversationLineage{sessionID: a.session.GetID(), epoch: a.recoveryEpoch.Load()}
	recoveries, err := validatePendingRecoveryBatch(lineage.sessionID, a.session.GetPendingRecoveries())
	if err != nil {
		return nil, lineage, err
	}
	if recovery, err := a.pendingRecoveryForHeadlessPromptUnlocked(prompt, lineage.sessionID); err == nil {
		return recovery, lineage, nil
	}
	if len(recoveries) == 0 {
		return nil, lineage, nil
	}

	var note strings.Builder
	note.WriteString("[Interrupted session recovery acknowledged] The previous run was interrupted. Its queued recovery will not be replayed. Completed tool outcomes recorded before interruption:\n")
	for _, recovery := range recoveries {
		fmt.Fprintf(&note, "Recovery %s (%s):\n", recovery.ID, recovery.State)
		for _, checkpoint := range recovery.Checkpoints {
			fmt.Fprintf(&note, "- %s (%s): success=%t; %s\n", checkpoint.ToolName, checkpoint.CallID, checkpoint.ResultV2.Success, boundedRecoverySummary(checkpoint.ResultV2.Content))
		}
	}
	note.WriteString("Additional interrupted tools may have partially changed the workspace without a recorded result. Inspect the current files, diff and process state before continuing. Do not blindly repeat writes, shell commands, commits or external actions; verify their effects first. The new user request follows.")
	previousHistory := a.session.GetHistory()
	previousRecoveries := a.session.GetPendingRecoveries()
	mutate := func() (func(), error) {
		a.advanceConversationEpoch()
		for _, recovery := range previousRecoveries {
			a.session.RemovePendingRecovery(recovery.ID)
		}
		a.session.SetHistory(append(previousHistory, genai.NewContentFromText(note.String(), genai.RoleUser)))
		return func() {
			a.session.SetHistory(previousHistory)
			a.recoveryEpoch.Store(lineage.epoch)
			for _, recovery := range previousRecoveries {
				a.session.AddPendingRecovery(recovery, "")
			}
		}, nil
	}
	if a.sessionManager != nil {
		if err := a.sessionManager.ApplyAndSave(mutate); err != nil {
			return nil, lineage, fmt.Errorf("acknowledge interrupted headless recovery: %w", err)
		}
	} else {
		_, _ = mutate()
	}
	lineage.epoch = a.recoveryEpoch.Load()
	a.cancelRecoveryTimersForSession(lineage.sessionID)
	a.dropRecoveryPendingForSession(lineage.sessionID)
	for _, recovery := range recoveries {
		a.markRecoveryDispatched(recovery.SessionID, recovery.ID)
	}
	logging.Warn("headless resume acknowledged interrupted recovery; old requests will not be replayed", "session_id", lineage.sessionID, "recoveries", len(recoveries))
	return nil, lineage, nil
}

func boundedRecoverySummary(content string) string {
	runes := []rune(content)
	if len(runes) > 256 {
		return string(runes[:256]) + "…"
	}
	return content
}
