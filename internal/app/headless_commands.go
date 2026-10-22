package app

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/commands"
)

// Resolve against the live catalog after SessionStart and SDK reloads. A
// workflow is expanded before the model sees it; local commands return their
// text without inventing a model turn or invoking interactive presentation.
func (a *App) headlessSlashCommand(ctx context.Context, raw string) (context.Context, string, string, bool, error) {
	if a.commandHandler == nil {
		return ctx, "", "", false, nil
	}
	name, args, recognized := a.commandHandler.Parse(raw)
	if !recognized || name == "goal" {
		return ctx, "", "", false, nil
	}
	if a.commandHandler.IsPromptCommand(name, args) {
		var err error
		ctx, err = a.withPromptSubmission(ctx, raw)
		if err != nil {
			return ctx, "", "", true, err
		}
	}
	ctx = commands.WithRawInvocation(ctx, raw)
	output, err := a.commandHandler.Execute(ctx, name, args, a)
	if err != nil {
		return ctx, "", "", true, err
	}
	if prompt, ok := strings.CutPrefix(output, commands.PromptMarker); ok {
		return ctx, prompt, "", true, nil
	}
	if strings.HasPrefix(output, "__") {
		return ctx, "", "", true, fmt.Errorf("/%s requires an interactive interface; pass explicit command arguments where available", name)
	}
	return ctx, "", output, true, nil
}
