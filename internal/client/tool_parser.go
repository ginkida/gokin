package client

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"

	"gokin/internal/logging"

	"google.golang.org/genai"
)

var textToolCallSeq atomic.Uint64

// ToolCallFromText represents a tool call parsed from text output.
type ToolCallFromText struct {
	Type       string         `json:"type"`
	Tool       string         `json:"tool"`
	Name       string         `json:"name"`       // alias for "tool"; Anthropic tool_use uses this
	Args       map[string]any `json:"args"`       // native fallback shape
	Input      map[string]any `json:"input"`      // Anthropic tool_use shape
	Parameters map[string]any `json:"parameters"` // common LLM alias
}

type openAIToolCallFromText struct {
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments any    `json:"arguments"`
	} `json:"function"`
}

type toolCallsEnvelopeFromText struct {
	ToolCalls []openAIToolCallFromText `json:"tool_calls"`
}

// ParseToolCallsFromText attempts to extract tool calls from model text output.
// This is used as a fallback when models don't support native function calling.
// Supports multiple formats:
//   - {"tool": "name", "args": {...}}
//   - {"name": "tool_name", "args": {...}}
//   - ```json\n{"tool": "name", "args": {...}}\n```
//   - Multiple tool calls in sequence
func ParseToolCallsFromText(text string) []*genai.FunctionCall {
	if text == "" {
		return nil
	}

	var calls []*genai.FunctionCall

	// Try extracting from JSON code blocks first
	calls = extractFromCodeBlocks(text)
	if len(calls) > 0 {
		return calls
	}

	// Try extracting bare JSON objects
	calls = extractFromBareJSON(text)
	if len(calls) > 0 {
		return calls
	}

	return nil
}

// toolCallFallbackProvider is implemented by clients that emit/parse tool calls
// as TEXT — i.e. models without native function-calling (OllamaClient for a
// model whose profile has SupportsTools == false). Such models are prompted
// (ToolCallFallbackPrompt) to write tool calls as {"tool":…,"args":…} JSON.
type toolCallFallbackProvider interface {
	NeedsToolCallFallback() bool
}

// ApplyTextToolCallFallback converts a model's TEXT-emitted tool call into a
// real FunctionCall, in place, for clients that need the fallback. It is the
// single shared implementation used by BOTH agentic loops — the foreground
// executor and the sub-agent loop — so a weak/Ollama model that writes
// {"tool":…,"args":…} JSON instead of making a native call behaves identically
// in each. (Before this was extracted, only the foreground executor parsed the
// text, so the SAME model run as a sub-agent left its tool calls as inert text
// and never executed — "sub-agent loops, shows a tool, no meaningful output".)
//
// No-op (returns 0) unless: cl reports NeedsToolCallFallback(), the response has
// NO native function calls, its text is non-empty, and that text contains at
// least one parseable tool call. On success it sets resp.FunctionCalls and
// CLEARS resp.Text (the JSON was a tool call, not prose). cl is `any` to match
// the existing untyped assertion idiom — most clients don't implement the
// capability and must pass through untouched.
func ApplyTextToolCallFallback(cl any, resp *Response) int {
	if resp == nil || len(resp.FunctionCalls) > 0 || resp.Text == "" {
		return 0
	}
	fb, ok := cl.(toolCallFallbackProvider)
	if !ok || !fb.NeedsToolCallFallback() {
		return 0
	}
	parsed := ParseToolCallsFromText(resp.Text)
	if len(parsed) == 0 {
		return 0
	}
	resp.FunctionCalls = parsed
	resp.Text = "" // strip the JSON — it's a tool call, not text to display
	return len(parsed)
}

// extractFromCodeBlocks extracts tool calls from fenced JSON-ish blocks.
var codeBlockPattern = regexp.MustCompile("(?s)```(?:json|tool|tool_code)?\\s*\\n?(.*?)\\s*\\n?```")

func extractFromCodeBlocks(text string) []*genai.FunctionCall {
	matches := codeBlockPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	var calls []*genai.FunctionCall
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		calls = append(calls, parseToolCallsJSON(match[1])...)
	}
	return calls
}

// extractFromBareJSON extracts tool calls from bare JSON objects in text.
func extractFromBareJSON(text string) []*genai.FunctionCall {
	var calls []*genai.FunctionCall

	// Find all JSON-like objects in the text
	objects := findJSONObjects(text)
	for _, obj := range objects {
		calls = append(calls, parseToolCallsJSON(obj)...)
	}
	return calls
}

// findJSONObjects extracts JSON objects from text by matching braces.
func findJSONObjects(text string) []string {
	var objects []string
	i := 0
	for i < len(text) {
		if text[i] == '{' {
			// Find matching closing brace
			depth := 0
			inString := false
			escaped := false
			j := i
			for j < len(text) {
				ch := text[j]
				if escaped {
					escaped = false
					j++
					continue
				}
				if ch == '\\' && inString {
					escaped = true
					j++
					continue
				}
				if ch == '"' {
					inString = !inString
				}
				if !inString {
					if ch == '{' {
						depth++
					} else if ch == '}' {
						depth--
						if depth == 0 {
							candidate := text[i : j+1]
							// Quick sanity check: must contain "tool" or "name" key
							if strings.Contains(candidate, `"tool"`) || strings.Contains(candidate, `"name"`) {
								objects = append(objects, candidate)
							}
							break
						}
					}
				}
				j++
			}
			if depth != 0 {
				// Unmatched brace, skip
				i++
				continue
			}
			i = j + 1
		} else {
			i++
		}
	}
	return objects
}

// parseToolCallsJSON parses a JSON object or array as one or more tool calls.
func parseToolCallsJSON(jsonStr string) []*genai.FunctionCall {
	jsonStr = strings.TrimSpace(jsonStr)
	if jsonStr == "" {
		return nil
	}

	if strings.HasPrefix(jsonStr, "[") {
		var rawItems []json.RawMessage
		if err := json.Unmarshal([]byte(jsonStr), &rawItems); err != nil {
			return nil
		}
		var calls []*genai.FunctionCall
		for _, item := range rawItems {
			calls = append(calls, parseToolCallsJSON(string(item))...)
		}
		return calls
	}

	if calls := parseOpenAIToolCallsJSON(jsonStr); len(calls) > 0 {
		return calls
	}

	fc := parseToolCallJSON(jsonStr)
	if fc == nil {
		return nil
	}
	return []*genai.FunctionCall{fc}
}

func parseOpenAIToolCallsJSON(jsonStr string) []*genai.FunctionCall {
	var env toolCallsEnvelopeFromText
	if err := json.Unmarshal([]byte(jsonStr), &env); err != nil || len(env.ToolCalls) == 0 {
		return nil
	}

	calls := make([]*genai.FunctionCall, 0, len(env.ToolCalls))
	for _, tc := range env.ToolCalls {
		toolName := strings.TrimSpace(tc.Function.Name)
		if toolName == "" {
			continue
		}
		args := parseOpenAIArguments(tc.Function.Arguments)
		calls = append(calls, &genai.FunctionCall{
			ID:   nextTextToolCallID(toolName),
			Name: toolName,
			Args: args,
		})
	}
	return calls
}

func parseOpenAIArguments(raw any) map[string]any {
	switch v := raw.(type) {
	case string:
		var args map[string]any
		if err := json.Unmarshal([]byte(v), &args); err == nil && args != nil {
			return args
		}
	case map[string]any:
		return v
	}
	return map[string]any{}
}

// parseToolCallJSON parses a single JSON object as a tool call.
func parseToolCallJSON(jsonStr string) *genai.FunctionCall {
	jsonStr = strings.TrimSpace(jsonStr)

	var tc ToolCallFromText
	if err := json.Unmarshal([]byte(jsonStr), &tc); err != nil {
		return nil
	}

	// Determine tool name from "tool" or "name" field
	toolName := tc.Tool
	if toolName == "" {
		toolName = tc.Name
	}
	if toolName == "" {
		return nil
	}

	args := tc.Args
	if args == nil {
		args = tc.Input
	}
	if args == nil {
		args = tc.Parameters
	}
	if args == nil {
		args = make(map[string]any)
	}

	logging.Debug("parsed tool call from text", "tool", toolName, "args_count", len(args))

	return &genai.FunctionCall{
		ID:   nextTextToolCallID(toolName),
		Name: toolName,
		Args: args,
	}
}

func nextTextToolCallID(toolName string) string {
	seq := textToolCallSeq.Add(1)
	safeName := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, toolName)
	safeName = strings.Trim(safeName, "_")
	if safeName == "" {
		safeName = "tool"
	}
	return fmt.Sprintf("text_call_%s_%d", safeName, seq)
}

// ToolCallFallbackPrompt returns the system prompt addition that instructs models
// to output tool calls in a parseable JSON format.
func ToolCallFallbackPrompt(toolDeclarations []*genai.FunctionDeclaration) string {
	if len(toolDeclarations) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## Tool Calling Instructions\n\n")
	sb.WriteString("You have access to tools. To call a tool, output a JSON object in a code block:\n\n")
	sb.WriteString("```json\n{\"tool\": \"tool_name\", \"args\": {\"param1\": \"value1\"}}\n```\n\n")
	sb.WriteString("The parser also accepts Anthropic-style `{\"type\":\"tool_use\",\"name\":\"tool_name\",\"input\":{...}}` if your model naturally emits that shape.\n\n")
	sb.WriteString("IMPORTANT RULES:\n")
	sb.WriteString("- Output ONLY the JSON block when calling a tool, no other text before or after\n")
	sb.WriteString("- Wait for the tool result before continuing\n")
	sb.WriteString("- Use exact parameter names as defined below\n")
	sb.WriteString("- You can call only ONE tool at a time\n\n")
	sb.WriteString("Available tools:\n\n")

	for _, decl := range toolDeclarations {
		fmt.Fprintf(&sb, "### %s\n", decl.Name)
		fmt.Fprintf(&sb, "%s\n", decl.Description)

		if decl.Parameters != nil && len(decl.Parameters.Properties) > 0 {
			sb.WriteString("Parameters:\n")
			required := make(map[string]bool)
			for _, r := range decl.Parameters.Required {
				required[r] = true
			}
			for name, prop := range decl.Parameters.Properties {
				reqMark := ""
				if required[name] {
					reqMark = " (required)"
				}
				fmt.Fprintf(&sb, "- `%s`%s: %s\n", name, reqMark, prop.Description)
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
