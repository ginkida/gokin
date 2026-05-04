package tools

import (
	"context"
	"os"
	"path/filepath"

	"gokin/internal/logging"

	"google.golang.org/genai"
)

// PinContextTool allows the agent to pin information to the system prompt.
// Pinned context is persisted to .gokin/pinned_context.md and restored on restart.
type PinContextTool struct {
	updater func(content string)
	workDir string
}

// NewPinContextTool creates a new PinContextTool.
func NewPinContextTool(updater func(content string)) *PinContextTool {
	return &PinContextTool{
		updater: updater,
	}
}

// SetWorkDir sets the working directory for pin persistence.
func (t *PinContextTool) SetWorkDir(dir string) {
	t.workDir = dir
}

// LoadPersistedPin reads pinned context from disk and applies it via updater.
// Called at app startup to restore the pin from a previous session.
func (t *PinContextTool) LoadPersistedPin() {
	if t.workDir == "" || t.updater == nil {
		return
	}
	path := filepath.Join(t.workDir, ".gokin", "pinned_context.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return // No pin file or not readable — that's fine
	}
	content := string(data)
	if content != "" {
		t.updater(content)
		logging.Debug("restored pinned context from disk", "size", len(content))
	}
}

// SetUpdater sets the function to update pinned context.
func (t *PinContextTool) SetUpdater(fn func(string)) {
	t.updater = fn
}

func (t *PinContextTool) Name() string {
	return "pin_context"
}

func (t *PinContextTool) Description() string {
	return `Pins a snippet of information to your system prompt for the rest of the session.
Use this for "hot memory" — to keep track of your current high-level goal, important file paths, or complex constraints that you don't want to lose focus on.

PARAMETERS:
- content (required): The information to pin. Providing an empty string or 'clear' will unpin all context.
- clear (optional): If true, clears the pinned context rather than setting it.`
}

func (t *PinContextTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"content": {
					Type:        genai.TypeString,
					Description: "Text to pin to system prompt",
				},
				"clear": {
					Type:        genai.TypeBoolean,
					Description: "If true, clear existing pinned context",
				},
			},
			Required: []string{"content"},
		},
	}
}

func (t *PinContextTool) Validate(args map[string]any) error {
	_, ok := GetString(args, "content")
	if !ok {
		return NewValidationError("content", "is required")
	}
	return nil
}

func (t *PinContextTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	content, _ := GetString(args, "content")
	clear, _ := args["clear"].(bool)

	if t.updater == nil {
		return NewErrorResult("pinned context not supported by this agent"), nil
	}

	if clear || content == "clear" {
		t.updater("")
		t.persistPin("")
		EmitMemoryNotify(ctx, "unpinned", "")
		return NewSuccessResult("Pinned context cleared."), nil
	}

	t.updater(content)
	t.persistPin(content)
	EmitMemoryNotify(ctx, "pinned", content)
	return NewSuccessResult("Information pinned to system prompt."), nil
}

// persistPin saves or removes the pin file on disk.
func (t *PinContextTool) persistPin(content string) {
	if t.workDir == "" {
		return
	}
	path := filepath.Join(t.workDir, ".gokin", "pinned_context.md")
	if content == "" {
		os.Remove(path)
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		logging.Warn("failed to create pinned context directory", "path", dir, "error", err)
		return
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		logging.Warn("failed to persist pinned context", "path", path, "error", err)
	}
}
