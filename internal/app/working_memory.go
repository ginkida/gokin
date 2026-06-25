package app

import (
	"fmt"
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
	// Discuss-mode stance FIRST when the user is analyzing (discussGate = analysis
	// & not-yet-confirmed): tell the model to propose-not-implement. Reflects the
	// live per-turn atomic flags; travels in turn-context, never the cached prefix.
	if a.discussGate() {
		parts = append(parts, discussStanceBanner)
	}
	// Directory-access awareness so the model always knows the capability
	// exists and sees the live granted-dirs list. Dynamic (updates on
	// grant/revoke/clear), and lives in turn context — it NEVER touches the
	// byte-stable cached prefix (v0.88.0 rule).
	if dirCtx := a.directoryAccessContext(); dirCtx != "" {
		parts = append(parts, dirCtx)
	}
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

// directoryAccessContext tells the model which directories it can reach and how
// to get more — so it uses the /add-dir capability instead of guessing or
// silently failing on out-of-workspace paths. Always present (the model must
// learn the capability before it ever hits a denial), compact, and dynamic (the
// granted list updates as grants change).
func (a *App) directoryAccessContext() string {
	// Read the cached effective-dirs snapshot under grantedDirsMu ONLY. This must
	// NOT take a.mu: turnContextContent is sometimes called from a caller that
	// already holds a.mu, and re-acquiring it here would self-deadlock.
	a.grantedDirsMu.Lock()
	dirs := append([]string(nil), a.dirCtxSnapshot...)
	a.grantedDirsMu.Unlock()

	var b strings.Builder
	b.WriteString("## Directory access\n")
	if len(dirs) > 0 {
		b.WriteString("Besides the working directory, you can read and work in these user-granted directories:\n")
		for _, d := range dirs {
			fmt.Fprintf(&b, "- %s\n", d)
		}
	}
	b.WriteString("To work with a path OUTSIDE the working directory and any granted directories, ask the user to run /add-dir <path> — you cannot grant access yourself, and retrying a denied path will not help.")
	return b.String()
}

// pushTurnContext delivers the current session+working memory snapshot to the
// active client as ephemeral per-turn context. Safe to call with a nil client.
func (a *App) pushTurnContext() {
	if a == nil {
		return
	}
	// Read the client under clientMu (the LEAF lock), NOT a.mu: some callers of
	// pushTurnContext already hold a.mu and re-acquiring it self-deadlocks (the
	// v0.100.42 invariant), but the client is swapped by ApplyConfig/failover on
	// another goroutine, so a bare read races. Snapshot + release before building
	// turnContextContent (which takes grantedDirsMu) so the two leaf locks are
	// taken sequentially, never nested.
	cl := a.clientSnapshot()
	if cl == nil {
		return
	}
	cl.SetTurnContext(a.turnContextContent())
}
