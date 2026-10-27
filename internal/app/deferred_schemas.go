package app

import "google.golang.org/genai"

// Policy selection remains independent of publication. Raw registry refreshes
// use the client's installed profile and cannot widen the last selected policy.
func (a *App) configureToolSchemaPublication(eligible []*genai.Tool) []*genai.Tool {
	if a.session != nil {
		a.session.ToolSchemaProfile().Configure(eligible)
	}
	return eligible
}
