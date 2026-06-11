package app

import (
	"strings"

	appcontext "gokin/internal/context"
)

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

	// Roadmap #7: working memory is delivered as per-turn context OUTSIDE the
	// cached prefix (client.SetTurnContext → appended to the latest user
	// message at request-build time). The previous Invalidate() +
	// refreshSystemInstruction() here re-billed the whole system+tools prefix
	// on every working-memory change for caching providers.
	a.pushTurnContext()
}

// turnContextContent assembles everything that travels as ephemeral per-turn
// context instead of living in the cached system prefix: session memory
// (mutates every ~5K tokens) and working memory (mutates every turn).
func (a *App) turnContextContent() string {
	if a == nil {
		return ""
	}
	var parts []string
	if a.sessionMemory != nil {
		if sm := a.sessionMemory.GetContent(); sm != "" {
			parts = append(parts, sm)
		}
	}
	if a.workingMemory != nil {
		if wm := a.workingMemory.GetContent(); wm != "" {
			parts = append(parts, wm)
		}
	}
	return strings.Join(parts, "\n\n")
}

// pushTurnContext delivers the current session+working memory snapshot to the
// active client as ephemeral per-turn context. Safe to call with a nil client.
func (a *App) pushTurnContext() {
	if a == nil || a.client == nil {
		return
	}
	a.client.SetTurnContext(a.turnContextContent())
}
