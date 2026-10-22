package commands

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/goals"
)

type GoalCommand struct{}

func GoalStartsWork(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if len(args) == 1 {
		switch strings.ToLower(args[0]) {
		case "clear", "stop", "off", "reset", "none", "cancel":
			return false
		}
	}
	return true
}

func (*GoalCommand) Name() string { return "goal" }
func (*GoalCommand) Description() string {
	return "Work across turns until a completion condition holds"
}
func (*GoalCommand) Usage() string { return "/goal [condition|clear]" }
func (*GoalCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{Category: CategorySession, HasArgs: true, ArgHint: "condition|clear"}
}

type goalRuntime interface {
	SetGoal(context.Context, string) error
	ClearGoal(context.Context) (string, error)
	GoalStatus() string
}

func (*GoalCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	runtime, ok := app.(goalRuntime)
	if !ok {
		return "", fmt.Errorf("goals are unavailable in this runtime")
	}
	if len(args) == 0 {
		return runtime.GoalStatus(), nil
	}
	if len(args) == 1 {
		switch strings.ToLower(args[0]) {
		case "clear", "stop", "off", "reset", "none", "cancel":
			return runtime.ClearGoal(ctx)
		}
	}
	condition := strings.Join(args, " ")
	if err := runtime.SetGoal(ctx, condition); err != nil {
		return "", err
	}
	return PromptMarker + goals.InitialPrompt(condition), nil
}
