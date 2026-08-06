package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"gokin/internal/harness"

	"google.golang.org/genai"
)

type HarnessTool struct {
	mu              sync.RWMutex
	store           *harness.Store
	onPromptChanged func()
}

func NewHarnessTool(store *harness.Store) *HarnessTool { return &HarnessTool{store: store} }

func (t *HarnessTool) SetStore(store *harness.Store) {
	t.mu.Lock()
	t.store = store
	t.mu.Unlock()
}

func (t *HarnessTool) SetPromptChangedCallback(callback func()) {
	t.mu.Lock()
	t.onPromptChanged = callback
	t.mu.Unlock()
}

func (t *HarnessTool) Name() string { return "harness" }

func (t *HarnessTool) Description() string {
	return "Manage the bounded hybrid continual harness. Prompt patches are session-only; episodic memory is project-scoped; skill code is staged for human review and never auto-activated. This tool cannot modify permissions, sandbox policy, built-in tools, or immutable system instructions."
}

func (t *HarnessTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"action": {
					Type:        genai.TypeString,
					Description: "One of: prompt_create, prompt_list, prompt_update, prompt_delete, memory_put, memory_get, memory_list, memory_delete, skill_propose, skill_list, skill_delete.",
					Enum:        []string{"prompt_create", "prompt_list", "prompt_update", "prompt_delete", "memory_put", "memory_get", "memory_list", "memory_delete", "skill_propose", "skill_list", "skill_delete"},
				},
				"id":          {Type: genai.TypeString, Description: "Prompt patch ID for update/delete."},
				"text":        {Type: genai.TypeString, Description: "Prompt patch text."},
				"key":         {Type: genai.TypeString, Description: "Episodic memory key."},
				"value":       {Type: genai.TypeString, Description: "Episodic memory value."},
				"name":        {Type: genai.TypeString, Description: "Lowercase staged skill name."},
				"description": {Type: genai.TypeString, Description: "Staged skill description."},
				"code":        {Type: genai.TypeString, Description: "Python helper source to stage without executing."},
			},
			Required: []string{"action"},
		},
	}
}

func (t *HarnessTool) Validate(args map[string]any) error {
	action := strings.TrimSpace(GetStringDefault(args, "action", ""))
	require := func(field string) error {
		value, ok := GetString(args, field)
		if !ok || strings.TrimSpace(value) == "" {
			return NewValidationError(field, "must be a non-empty string")
		}
		return nil
	}
	switch action {
	case "prompt_create":
		return require("text")
	case "prompt_list", "memory_list", "skill_list":
		return nil
	case "prompt_update":
		if err := require("id"); err != nil {
			return err
		}
		return require("text")
	case "prompt_delete":
		return require("id")
	case "memory_put":
		if err := require("key"); err != nil {
			return err
		}
		if _, ok := GetString(args, "value"); !ok {
			return NewValidationError("value", "must be a string")
		}
		return nil
	case "memory_get", "memory_delete":
		return require("key")
	case "skill_propose":
		for _, field := range []string{"name", "description", "code"} {
			if err := require(field); err != nil {
				return err
			}
		}
		return nil
	case "skill_delete":
		return require("name")
	default:
		return NewValidationError("action", "is not supported")
	}
}

func (t *HarnessTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := t.Validate(args); err != nil {
		return NewErrorResult("validation error: " + err.Error()), nil
	}
	t.mu.RLock()
	store := t.store
	callback := t.onPromptChanged
	t.mu.RUnlock()
	if store == nil {
		return NewErrorResult("continual harness is unavailable in this session"), nil
	}
	action := GetStringDefault(args, "action", "")
	var data any
	var err error
	promptChanged := false
	switch action {
	case "prompt_create":
		data, err = store.CreatePrompt(GetStringDefault(args, "text", ""))
		promptChanged = err == nil
	case "prompt_list":
		data = store.ListPrompts()
	case "prompt_update":
		data, err = store.UpdatePrompt(GetStringDefault(args, "id", ""), GetStringDefault(args, "text", ""))
		promptChanged = err == nil
	case "prompt_delete":
		err = store.DeletePrompt(GetStringDefault(args, "id", ""))
		data = map[string]any{"deleted": err == nil}
		promptChanged = err == nil
	case "memory_put":
		data, err = store.PutMemoryContext(ctx, GetStringDefault(args, "key", ""), GetStringDefault(args, "value", ""))
	case "memory_get":
		var entry harness.MemoryEntry
		var ok bool
		entry, ok, err = store.GetMemoryFresh(GetStringDefault(args, "key", ""))
		if err != nil {
			break
		}
		if !ok {
			err = fmt.Errorf("episodic memory key not found")
		} else {
			data = entry
		}
	case "memory_list":
		data, err = store.ListMemoryFresh()
	case "memory_delete":
		err = store.DeleteMemoryContext(ctx, GetStringDefault(args, "key", ""))
		data = map[string]any{"deleted": err == nil}
	case "skill_propose":
		data, err = store.ProposeSkill(
			GetStringDefault(args, "name", ""),
			GetStringDefault(args, "description", ""),
			GetStringDefault(args, "code", ""),
		)
	case "skill_list":
		data, err = store.ListSkills()
	case "skill_delete":
		err = store.DeleteSkill(GetStringDefault(args, "name", ""))
		data = map[string]any{"deleted": err == nil}
	}
	if err != nil {
		return NewErrorResult("harness " + action + " failed: " + err.Error()), nil
	}
	if promptChanged && callback != nil {
		callback()
	}
	return NewSuccessResultWithData("harness "+action+" completed", data), nil
}
