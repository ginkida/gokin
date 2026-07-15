package commands

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"gokin/internal/tools"
)

// SkillCommand lists or explicitly loads a reusable SKILL.md workflow. Loaded
// content is submitted to the model as the next user prompt, matching the
// existing file-command handoff without executing any side effect itself.
type SkillCommand struct{}

func (c *SkillCommand) Name() string        { return "skill" }
func (c *SkillCommand) Description() string { return "List or run reusable project/user workflows" }
func (c *SkillCommand) Usage() string       { return "/skill [name] [arguments...]" }

func (c *SkillCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "skill",
		Priority: 84,
		HasArgs:  true,
		ArgHint:  "[name] [arguments...]",
	}
}

func (c *SkillCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	if app == nil || app.GetToolRegistry() == nil {
		return "", fmt.Errorf("skills are unavailable: tool registry is not initialized")
	}
	registered, ok := app.GetToolRegistry().Get("skill")
	if !ok {
		return "", fmt.Errorf("skills are unavailable in this runtime")
	}
	skillTool, ok := registered.(*tools.SkillTool)
	if !ok {
		return "", fmt.Errorf("registered skill tool has incompatible type")
	}

	var result tools.ToolResult
	var err error
	if len(args) == 0 {
		result, err = skillTool.ExecuteForUser(nil)
	} else {
		if rawArguments, ok := RawInvocationArguments(ctx); ok {
			_, workflowArguments := splitSkillInvocationArguments(rawArguments)
			result, err = skillTool.ExecuteForUserRawArguments(args[0], workflowArguments)
		} else {
			result, err = skillTool.ExecuteForUserArguments(args[0], args[1:])
		}
	}
	if err != nil {
		return "", err
	}
	if !result.Success {
		return "", fmt.Errorf("%s", result.Error)
	}
	if len(args) == 0 {
		return result.Content, nil
	}
	return PromptMarker + result.Content, nil
}

func splitSkillInvocationArguments(raw string) (name, arguments string) {
	raw = strings.TrimLeftFunc(raw, unicode.IsSpace)
	if raw == "" {
		return "", ""
	}
	separator := strings.IndexFunc(raw, unicode.IsSpace)
	if separator < 0 {
		return raw, ""
	}
	return raw[:separator], strings.TrimLeftFunc(raw[separator:], unicode.IsSpace)
}
