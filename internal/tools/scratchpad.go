package tools

import (
	"context"
	"fmt"

	"google.golang.org/genai"
)

// UpdateScratchpadTool allows agents to store persistent thoughts and facts.
type UpdateScratchpadTool struct {
	updater func(content string)
}

func NewUpdateScratchpadTool(updater func(string)) *UpdateScratchpadTool {
	return &UpdateScratchpadTool{updater: updater}
}

func (t *UpdateScratchpadTool) Name() string {
	return "update_scratchpad"
}

func (t *UpdateScratchpadTool) Description() string {
	return "Updates your internal scratchpad with important facts, thoughts, or plans you want to remember across turns. This content is visible to you in your system prompt and to the user in the UI."
}

func (t *UpdateScratchpadTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"content": {
					Type:        genai.TypeString,
					Description: "The new content for the scratchpad. This replaces the existing content.",
				},
			},
			Required: []string{"content"},
		},
	}
}

func (t *UpdateScratchpadTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	content, ok := args["content"].(string)
	if !ok {
		return ToolResult{}, fmt.Errorf("missing or invalid 'content' argument")
	}

	if t.updater != nil {
		t.updater(content)
	}

	return ToolResult{
		Content: "Scratchpad updated successfully.",
		Success: true,
	}, nil
}

func (t *UpdateScratchpadTool) Validate(args map[string]any) error {
	if _, ok := args["content"].(string); !ok {
		return fmt.Errorf("content is required and must be a string")
	}
	return nil
}

func (t *UpdateScratchpadTool) SetUpdater(updater func(string)) {
	t.updater = updater
}
