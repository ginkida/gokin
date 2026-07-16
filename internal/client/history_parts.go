package client

import (
	"strings"

	"google.golang.org/genai"
)

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
	var regularText strings.Builder
	lastRegularTextAt := -1

	for i, part := range parts {
		if part == nil {
			continue
		}
		if part.Text != "" && !part.Thought {
			regularText.WriteString(part.Text)
			lastRegularTextAt = i
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
	textToInsert := ""
	textInsertAt := -1
	existingText := regularText.String()
	switch {
	case resp.Text == "" || existingText == resp.Text:
	case existingText == "":
		textToInsert = resp.Text
	case strings.HasPrefix(resp.Text, existingText):
		// Some adapters may emit an early text Part and continue accumulating
		// the rest only in Response.Text. Add exactly the missing suffix after
		// the last known text part; inserting it before the first tool call can
		// reorder interleaved text/tool content ("ab" + "c" into "acb").
		textToInsert = strings.TrimPrefix(resp.Text, existingText)
		textInsertAt = lastRegularTextAt + 1
	default:
		// The two representations disagree rather than one being a prefix.
		// Response.Text is the documented aggregate, so replace ordinary text
		// fields with that canonical value while retaining any non-text payload
		// that happened to share a Part.
		rebuilt := make([]*genai.Part, 0, len(parts))
		for _, part := range parts {
			if part == nil || part.Text == "" || part.Thought {
				rebuilt = append(rebuilt, part)
				continue
			}
			if textInsertAt < 0 {
				textInsertAt = len(rebuilt)
			}
			clone := *part
			clone.Text = ""
			if partHasNonTextPayload(&clone) {
				rebuilt = append(rebuilt, &clone)
			}
		}
		parts = rebuilt
		textToInsert = resp.Text
	}
	if textToInsert != "" {
		insertAt := textInsertAt
		if insertAt < 0 {
			insertAt = len(parts)
			for i, part := range parts {
				if part != nil && part.FunctionCall != nil {
					insertAt = i
					break
				}
			}
		}
		parts = append(parts, nil)
		copy(parts[insertAt+1:], parts[insertAt:])
		parts[insertAt] = genai.NewPartFromText(textToInsert)
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

func partHasNonTextPayload(part *genai.Part) bool {
	return part != nil && (part.MediaResolution != nil || part.CodeExecutionResult != nil ||
		part.ExecutableCode != nil || part.FileData != nil || part.FunctionCall != nil ||
		part.FunctionResponse != nil || part.InlineData != nil || part.Thought ||
		len(part.ThoughtSignature) > 0 || part.VideoMetadata != nil)
}
