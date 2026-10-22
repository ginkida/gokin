package app

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"gokin/internal/chat"
	"gokin/internal/ui"
)

func (a *App) ShowSessionPicker(ctx context.Context, sessions []chat.SessionInfo, note string) bool {
	a.mu.Lock()
	interactive := a.running && !a.headlessDirect && a.tui != nil
	a.mu.Unlock()
	if !interactive || ctx.Err() != nil || !a.hasProgram() {
		return false
	}
	var choices []ui.SessionChoice
	current := ""
	if a.session != nil {
		current = a.session.GetID()
	}
	workDir := a.GetWorkDir()
	for _, info := range sessions {
		// Match the exact project ownership enforced by SwitchSession. Unknown
		// legacy ownership remains accessible through the explicit --force path.
		if workDir == "" || info.WorkDir == "" || filepath.Clean(info.WorkDir) != filepath.Clean(workDir) || chat.ValidateSessionID(info.ID) != nil {
			continue
		}
		if info.ID == current && info.MessageCount == 0 {
			continue
		}
		choices = append(choices, ui.SessionChoice{
			ID: info.ID, Summary: info.Summary, Current: info.ID == current,
			Detail: fmt.Sprintf("%d messages · %s", info.MessageCount, humanizeAge(time.Since(info.LastActive))),
		})
	}
	if len(choices) == 0 {
		return false
	}
	a.safeSendToProgram(ui.OpenSessionPickerMsg{Choices: choices, Note: note})
	return true
}
