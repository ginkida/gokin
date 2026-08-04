package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"gokin/internal/logging"
	"gokin/internal/pinned"

	"google.golang.org/genai"
)

// PinContextTool allows the agent to pin information to the system prompt.
// Pinned context is persisted to .gokin/pinned_context.md and restored on restart.
type PinContextTool struct {
	mu      sync.Mutex
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
	t.mu.Lock()
	defer t.mu.Unlock()
	t.workDir = dir
}

// LoadPersistedPin reads pinned context from disk and applies it via updater.
// Called at app startup to restore the pin from a previous session.
func (t *PinContextTool) LoadPersistedPin() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.workDir == "" || t.updater == nil {
		return
	}
	content, err := pinned.Load(t.workDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logging.Warn("failed to restore pinned context", "error", err)
		}
		return
	}
	// An empty persisted value is a durable clear marker and must overwrite a
	// previously-active value when this method is called more than once.
	t.updater(content)
	logging.Debug("restored pinned context from disk", "size", len(content))
}

// SetUpdater sets the function to update pinned context.
func (t *PinContextTool) SetUpdater(fn func(string)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.updater = fn
}

func (t *PinContextTool) persistenceWorkDir() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.workDir
}

func (t *PinContextTool) Name() string {
	return "pin_context"
}

func (t *PinContextTool) Description() string {
	return `Pins a snippet of information to your system prompt for the rest of the session.
Use this for "hot memory" — to keep track of your current high-level goal, important file paths, or complex constraints that you don't want to lose focus on.

PARAMETERS:
- content (required): The information to pin. Providing an empty string or 'clear' will unpin all context.
- clear (optional): If true, clears the pinned context rather than setting it.
Pinned content is limited to 64 KiB.`
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
	content, ok := GetString(args, "content")
	if !ok {
		return NewValidationError("content", "is required")
	}
	clear, _ := args["clear"].(bool)
	if !clear && content != "clear" && len(content) > pinned.MaxContentBytes {
		return NewValidationError("content", fmt.Sprintf("exceeds the %d-byte limit", pinned.MaxContentBytes))
	}
	return nil
}

func (t *PinContextTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	content, _ := GetString(args, "content")
	clear, _ := args["clear"].(bool)

	if t.updater == nil {
		return NewErrorResult("pinned context not supported by this agent"), nil
	}

	if clear || content == "clear" {
		if err := t.persistPin(""); err != nil {
			logging.Warn("failed to clear persisted pinned context", "error", err)
			return NewErrorResult(fmt.Sprintf("failed to clear pinned context: %v", err)), nil
		}
		t.updater("")
		EmitMemoryNotify(ctx, "unpinned", "")
		return NewSuccessResult("Pinned context cleared."), nil
	}

	if len(content) > pinned.MaxContentBytes {
		return NewErrorResult(fmt.Sprintf("pinned context exceeds the %d-byte limit", pinned.MaxContentBytes)), nil
	}
	if err := t.persistPin(content); err != nil {
		logging.Warn("failed to persist pinned context", "error", err)
		return NewErrorResult(fmt.Sprintf("failed to persist pinned context: %v", err)), nil
	}
	t.updater(content)
	EmitMemoryNotify(ctx, "pinned", content)
	return NewSuccessResult("Information pinned to system prompt."), nil
}

// persistPin saves the pin when this tool is bound to a workspace. Tools used
// without a workspace retain their historical session-only behavior.
func (t *PinContextTool) persistPin(content string) error {
	if t.workDir == "" {
		return nil
	}
	return pinned.Save(t.workDir, content)
}
