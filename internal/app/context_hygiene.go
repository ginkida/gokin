package app

import (
	"fmt"
	"strings"

	"google.golang.org/genai"
)

const (
	toolOutputHygieneKeepRecent = 10
	toolOutputHygieneMaxChars   = 4000
	toolOutputHygieneMaxLines   = 120
)

var noisyToolOutputs = map[string]bool{
	"grep":       true,
	"glob":       true,
	"bash":       true,
	"tree":       true,
	"git_diff":   true,
	"git_status": true,
}

// applyToolOutputHygiene compacts old oversized tool outputs in history.
// This keeps recent context intact while preventing historical tool dumps from
// occupying most of the prompt window.
func (a *App) applyToolOutputHygiene() {
	if a == nil || a.session == nil {
		return
	}

	history := a.session.GetHistory()
	if len(history) <= toolOutputHygieneKeepRecent {
		return
	}

	cutoff := len(history) - toolOutputHygieneKeepRecent
	changed := false
	compactedParts := 0

	for i := 0; i < cutoff; i++ {
		content := history[i]
		if content == nil || len(content.Parts) == 0 {
			continue
		}

		localChanged := false
		newParts := make([]*genai.Part, len(content.Parts))
		copy(newParts, content.Parts)

		for j, part := range content.Parts {
			if part == nil || part.FunctionResponse == nil || part.FunctionResponse.Response == nil {
				continue
			}
			toolName := part.FunctionResponse.Name
			if !noisyToolOutputs[toolName] {
				continue
			}

			raw, ok := part.FunctionResponse.Response["content"].(string)
			if !ok || raw == "" {
				continue
			}
			if compacted, _ := part.FunctionResponse.Response["content_compacted"].(bool); compacted {
				continue
			}

			lines := strings.Count(raw, "\n") + 1
			if len(raw) < toolOutputHygieneMaxChars && lines < toolOutputHygieneMaxLines {
				continue
			}

			placeholder := fmt.Sprintf("[Output of '%s' (%d lines) summarized by AI]", toolName, lines)
			newResp := make(map[string]any, len(part.FunctionResponse.Response)+4)
			for k, v := range part.FunctionResponse.Response {
				newResp[k] = v
			}
			newResp["content"] = placeholder
			newResp["content_compacted"] = true
			newResp["original_lines"] = lines
			newResp["original_chars"] = len(raw)

			newParts[j] = &genai.Part{
				FunctionResponse: &genai.FunctionResponse{
					ID:       part.FunctionResponse.ID,
					Name:     part.FunctionResponse.Name,
					Response: newResp,
				},
			}
			localChanged = true
			compactedParts++
		}

		if localChanged {
			history[i] = &genai.Content{
				Role:  content.Role,
				Parts: newParts,
			}
			changed = true
		}
	}

	if changed {
		a.session.SetHistory(history)
		a.journalEvent("context_hygiene_compacted", map[string]any{
			"parts": compactedParts,
		})
	}
}
