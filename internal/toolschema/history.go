package toolschema

import (
	"strings"

	"google.golang.org/genai"
)

// EffectiveCall returns the target identity for a well-formed dispatcher call.
// It is a presentation resolver, not validation or authorization. Args remain
// shared; execution callers must clone them before hooks or other mutations.
func EffectiveCall(call *genai.FunctionCall) *genai.FunctionCall {
	if call == nil || call.Name != "use_tool" || len(call.Args) != 2 {
		return call
	}
	name, ok := call.Args["name"].(string)
	if !ok || name == "" || strings.TrimSpace(name) != name || name == "use_tool" || name == "tool_search" {
		return call
	}
	args, ok := call.Args["arguments"].(map[string]any)
	if !ok || args == nil {
		return call
	}
	effective := *call
	effective.Name, effective.Args = name, args
	return &effective
}

// ResponseCalls indexes original wire pairs without rewriting history. IDs are
// authoritative when present; ID-less providers pair same-name calls in order.
// An unpaired/mismatched response cannot claim another tool's identity.
type ResponseCalls map[*genai.FunctionResponse]*genai.FunctionCall

func IndexResponseCalls(history []*genai.Content) ResponseCalls {
	type key struct{ id, name string }
	pending := make(map[key][]*genai.FunctionCall)
	resolved := make(ResponseCalls)
	for _, content := range history {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			if call := part.FunctionCall; call != nil {
				k := key{call.ID, call.Name}
				pending[k] = append(pending[k], call)
			}
			if response := part.FunctionResponse; response != nil {
				k := key{response.ID, response.Name}
				if queue := pending[k]; len(queue) > 0 {
					resolved[response] = EffectiveCall(queue[0])
					pending[k] = queue[1:]
				}
			}
		}
	}
	return resolved
}

func (calls ResponseCalls) Name(response *genai.FunctionResponse) string {
	if call := calls[response]; call != nil {
		return call.Name
	}
	if response != nil {
		return response.Name
	}
	return ""
}

// EffectiveHistory is a read-only view for semantic scanners, summaries and
// display. Provider serialization, persistence and pair validation must use the
// original history. Maps and unchanged parts are shared, so do not mutate it.
func EffectiveHistory(history []*genai.Content) []*genai.Content {
	calls := IndexResponseCalls(history)
	view := append([]*genai.Content(nil), history...)
	for i, content := range history {
		if content == nil {
			continue
		}
		var parts []*genai.Part
		for j, part := range content.Parts {
			if part == nil {
				continue
			}
			copy := *part
			if call := part.FunctionCall; call != nil {
				copy.FunctionCall = EffectiveCall(call)
			} else if response := part.FunctionResponse; response != nil && calls.Name(response) != response.Name {
				effective := *response
				effective.Name = calls.Name(response)
				copy.FunctionResponse = &effective
			}
			if copy.FunctionCall == part.FunctionCall && copy.FunctionResponse == part.FunctionResponse {
				continue
			}
			if parts == nil {
				parts = append([]*genai.Part(nil), content.Parts...)
			}
			parts[j] = &copy
		}
		if parts != nil {
			copy := *content
			copy.Parts = parts
			view[i] = &copy
		}
	}
	return view
}
