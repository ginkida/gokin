package tools

import (
	"context"
	"fmt"

	"google.golang.org/genai"

	"gokin/internal/memory"
)

// MemorizeTool allows the agent to save project-specific knowledge.
type MemorizeTool struct {
	learning *memory.ProjectLearning
}

// NewMemorizeTool creates a new MemorizeTool instance.
func NewMemorizeTool(learning *memory.ProjectLearning) *MemorizeTool {
	return &MemorizeTool{
		learning: learning,
	}
}

// SetLearning sets the learning store for the tool.
func (t *MemorizeTool) SetLearning(learning *memory.ProjectLearning) {
	t.learning = learning
}

// GetLearning returns the configured project learning store.
func (t *MemorizeTool) GetLearning() *memory.ProjectLearning {
	return t.learning
}

func (t *MemorizeTool) Name() string {
	return "memorize"
}

func (t *MemorizeTool) Description() string {
	return "Saves a durable project fact, preference, convention, or pattern to the project-learning store (.gokin/project-memory.md + learning.yaml), loaded into context every future session. Use this for knowledge about THIS repository (e.g. the test command, a naming convention). The result reports whether a write actually happened. For ad-hoc keyed notes with tags/TTL/recall, use the `memory` tool instead; both stores are shown by `memory` action=list."
}

func (t *MemorizeTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"type": {
					Type:        genai.TypeString,
					Description: "Type of information: 'fact', 'preference', 'convention', 'pattern'",
					Enum:        []string{"fact", "preference", "convention", "pattern"},
				},
				"key": {
					Type:        genai.TypeString,
					Description: "A short, descriptive key or name for the knowledge (e.g., 'test_command', 'logging_library')",
				},
				"content": {
					Type:        genai.TypeString,
					Description: "The actual fact or preference to remember",
				},
			},
			Required: []string{"type", "key", "content"},
		},
	}
}

func (t *MemorizeTool) Validate(args map[string]any) error {
	infoType, _ := GetString(args, "type")
	key, _ := GetString(args, "key")
	content, _ := GetString(args, "content")

	if infoType == "" {
		return NewValidationError("type", "is required")
	}
	if key == "" {
		return NewValidationError("key", "is required")
	}
	if content == "" {
		return NewValidationError("content", "is required")
	}

	return nil
}

func (t *MemorizeTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if t.learning == nil {
		return NewErrorResult("project learning store not initialized"), nil
	}

	infoType, _ := GetString(args, "type")
	key, _ := GetString(args, "key")
	content, _ := GetString(args, "content")

	switch infoType {
	case "preference":
		t.learning.SetPreference(key, content)
	case "fact", "convention":
		// Store as a preference for now or extend ProjectLearning
		t.learning.SetPreference(fmt.Sprintf("%s:%s", infoType, key), content)
	case "pattern":
		t.learning.LearnPattern(key, content, nil, nil)
	default:
		return NewErrorResult(fmt.Sprintf("unknown information type: %s", infoType)), nil
	}

	// Flush immediately to ensure persistence. FlushChanged reports whether a
	// write ACTUALLY happened, so the success message never claims "(updated …)"
	// for a no-op flush — the honesty defect behind the field report.
	changed, err := t.learning.FlushChanged()
	if err != nil {
		return NewErrorResult(fmt.Sprintf("failed to save memory: %s", err)), nil
	}

	EmitMemoryNotify(ctx, "memorized", fmt.Sprintf("%s: %s", infoType, key))

	markdownPath := t.learning.MarkdownPath()
	msg := fmt.Sprintf("Memorized %s: %s", infoType, key)
	if changed {
		if markdownPath != "" {
			msg += fmt.Sprintf(" (updated %s — also shown in `memory list` and loaded next session)", markdownPath)
		}
	} else {
		msg += " (already current — nothing to write)"
	}

	// Declare the files written so the executor invalidates the read-dedup cache
	// (the agent can re-read to VERIFY) and records them for post-compaction
	// hints — memorize writes via ProjectLearning, bypassing the write-tool path
	// the executor normally watches. Only when a write actually happened.
	if changed {
		var written []string
		for _, p := range []string{markdownPath, t.learning.Path()} {
			if p != "" {
				written = append(written, p)
			}
		}
		if len(written) > 0 {
			return NewSuccessResultWithData(msg, map[string]any{"written_paths": written}), nil
		}
	}
	return NewSuccessResult(msg), nil
}
