package commands

import (
	"context"
	"strings"
	"testing"
)

type goalFakeApp struct {
	*fakeAppForMCP
	condition string
	clears    int
}

func (a *goalFakeApp) SetGoal(_ context.Context, condition string) error {
	a.condition = condition
	return nil
}
func (a *goalFakeApp) ClearGoal(context.Context) (string, error) { a.clears++; return "cleared", nil }
func (*goalFakeApp) GoalStatus() string                          { return "active condition" }

func TestGoalCommandStartsWorkOnlyForACondition(t *testing.T) {
	a := &goalFakeApp{fakeAppForMCP: &fakeAppForMCP{}}
	cmd := &GoalCommand{}
	if result, err := cmd.Execute(t.Context(), nil, a); err != nil || result != "active condition" || a.condition != "" {
		t.Fatal(result, err)
	}
	for _, alias := range []string{"clear", "stop", "off", "reset", "none", "cancel"} {
		if _, err := cmd.Execute(t.Context(), []string{alias}, a); err != nil {
			t.Fatal(err)
		}
	}
	if a.clears != 6 || a.condition != "" {
		t.Fatal("clear alias started model work")
	}
	result, err := cmd.Execute(t.Context(), []string{"all", "required", "tests", "pass"}, a)
	if err != nil || !strings.HasPrefix(result, PromptMarker) || a.condition != "all required tests pass" {
		t.Fatal(result, err)
	}
}
