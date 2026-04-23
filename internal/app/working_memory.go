package app

import appcontext "gokin/internal/context"

func (a *App) updateWorkingMemoryFromTurn(userMessage, response string) {
	if a == nil || a.workingMemory == nil {
		return
	}

	turn := appcontext.WorkingMemoryTurn{
		UserMessage:  userMessage,
		Response:     response,
		ToolsUsed:    a.snapshotResponseToolsUsed(),
		TouchedPaths: a.snapshotResponseTouchedPaths(),
		Commands:     a.snapshotResponseCommands(),
	}
	if !a.workingMemory.UpdateFromTurn(turn) {
		return
	}

	if a.promptBuilder != nil {
		a.promptBuilder.Invalidate()
	}
	a.refreshSystemInstruction()
}
