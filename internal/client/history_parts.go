package client

import "google.golang.org/genai"

// ResponsePartsForHistory returns a complete assistant turn suitable for
// appending to conversation history.
//
// Streaming providers expose ordinary text in Response.Text, while signed
// thinking blocks and native tool calls may be carried in Response.Parts.
// Treating Parts as an all-or-nothing replacement therefore loses one side of
// mixed responses (notably GLM thinking + final text). This helper merges both
// representations without mutating the response or duplicating tool calls.
func ResponsePartsForHistory(resp *Response) []*genai.Part {
	if resp == nil {
		return nil
	}

	parts := append([]*genai.Part(nil), resp.Parts...)
	callPointers := make(map[*genai.FunctionCall]struct{})
	callIDs := make(map[string]struct{})
	hasRegularText := false

	for _, part := range parts {
		if part == nil {
			continue
		}
		if part.Text != "" && !part.Thought {
			hasRegularText = true
		}
		if part.FunctionCall != nil {
			callPointers[part.FunctionCall] = struct{}{}
			if part.FunctionCall.ID != "" {
				callIDs[part.FunctionCall.ID] = struct{}{}
			}
		}
	}

	// Signed thinking parts are emitted separately from streamed ordinary
	// text. Keep text before the first tool call so the reconstructed turn
	// follows the provider's assistant-content ordering.
	if resp.Text != "" && !hasRegularText {
		insertAt := len(parts)
		for i, part := range parts {
			if part != nil && part.FunctionCall != nil {
				insertAt = i
				break
			}
		}
		parts = append(parts, nil)
		copy(parts[insertAt+1:], parts[insertAt:])
		parts[insertAt] = genai.NewPartFromText(resp.Text)
	}

	for _, call := range resp.FunctionCalls {
		if call == nil {
			continue
		}
		if _, exists := callPointers[call]; exists {
			continue
		}
		if call.ID != "" {
			if _, exists := callIDs[call.ID]; exists {
				continue
			}
			callIDs[call.ID] = struct{}{}
		}
		parts = append(parts, &genai.Part{FunctionCall: call})
		callPointers[call] = struct{}{}
	}

	return parts
}
