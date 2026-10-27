package app

import (
	"encoding/json"
	"strings"

	"gokin/internal/donegate"
	"gokin/internal/skills"
	"gokin/internal/toolschema"
	"gokin/internal/ui"

	"google.golang.org/genai"
)

func sessionConversation(history []*genai.Content) ui.SessionConversationMsg {
	history = toolschema.EffectiveHistory(history)
	var result ui.SessionConversationMsg
	calls := make(map[string]*genai.FunctionCall)
	review := false
	for _, content := range history {
		if content == nil {
			continue
		}
		var visibleText strings.Builder
		hasCall := false
		for _, part := range content.Parts {
			if part == nil || part.Thought {
				continue
			}
			if call := part.FunctionCall; call != nil {
				calls[call.ID] = call
				hasCall = true
				continue
			}
			if response := part.FunctionResponse; response != nil {
				tool := ui.ToolResultMsg{Name: response.Name}
				if call := calls[response.ID]; call != nil {
					tool.Args = call.Args
				}
				tool.Content, _ = response.Response["content"].(string)
				tool.Error, _ = response.Response["error"].(string)
				success, known := response.Response["success"].(bool)
				tool.Failed = tool.Error != "" || known && !success
				if tool.Content == "" && tool.Error == "" {
					encoded, err := json.MarshalIndent(response.Response, "", "  ")
					if err == nil {
						tool.Content = string(encoded)
					}
				}
				result.Entries = append(result.Entries, ui.SessionConversationEntry{Tool: &tool})
				continue
			}
			visibleText.WriteString(part.Text)
		}
		text := visibleText.String()
		if strings.TrimSpace(text) == "" || skills.IsSyntheticCarryText(text) {
			continue
		}
		role := content.Role
		if role == genai.RoleUser {
			if strings.HasPrefix(text, "Final completion review before finishing this task.\n\nOriginal user request:\n") {
				review = true
				continue
			}
			review = false
		} else if role == genai.RoleModel && review {
			if hasCall || strings.TrimSpace(text) == donegate.CompletionReviewUnchanged {
				continue
			}
			role = "revision"
		}
		result.Entries = append(result.Entries, ui.SessionConversationEntry{Role: role, Text: text})
	}
	return result
}

func (a *App) publishSessionConversation() {
	a.mu.Lock()
	headless := a.headlessDirect
	a.mu.Unlock()
	if headless || a.session == nil || a.tui == nil && !a.hasProgram() {
		return
	}
	message := sessionConversation(a.session.GetHistory())
	if a.hasProgram() {
		a.safeSendToProgram(message)
	} else if a.tui != nil {
		// Startup has not published the event loop yet. Once it has, all
		// presentation changes above go through Bubble Tea's message stream.
		a.tui.RestoreSessionConversation(message)
	}
}
