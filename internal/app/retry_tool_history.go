package app

import "google.golang.org/genai"

// setRetainedToolHistory commits progress before acknowledging its exact tool
// outcomes. Older turns cannot acknowledge this request's checkpoints, even if
// a provider reused a call ID. If cleaning changed the old prefix, retain the
// conservative replay guard rather than guessing where the new history starts.
func (a *App) setRetainedToolHistory(before, retained []*genai.Content) {
	retained = a.session.SetHistoryWithSkillCarry(retained, 0)
	if a.executor == nil || len(retained) <= len(before) {
		return
	}
	for idx, message := range before {
		if retained[idx] != message {
			return
		}
	}
	a.executor.AcknowledgeToolResults(retained[len(before):])
}
