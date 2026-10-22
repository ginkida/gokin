package permission

import (
	"context"
	"testing"
)

func TestNoPromptContextPreservesPermissionFloorsAndIsRequestLocal(t *testing.T) {
	manager := NewManager(DefaultRules(), true)
	prompts := 0
	manager.SetPromptHandler(func(context.Context, *Request) (Decision, error) {
		prompts++
		return DecisionAllow, nil
	})
	ctx := ContextWithoutPermissionPrompt(t.Context())
	for _, tc := range []struct {
		command        string
		grants, denies []string
		allowed        bool
	}{
		{"git status --short", nil, nil, false},
		{"git status --short", []string{"bash(git status *)"}, nil, true},
		{"git status --short", []string{"bash"}, []string{"bash"}, false},
		{"git reset --hard HEAD~1", []string{"bash"}, nil, false},
	} {
		response, err := manager.CheckWithTemporaryToolRules(ctx, "bash", map[string]any{"command": tc.command}, tc.grants, tc.denies)
		if err != nil || response.Allowed != tc.allowed || prompts != 0 {
			t.Fatalf("%+v: response=%+v err=%v prompts=%d", tc, response, err, prompts)
		}
	}
	if response, err := manager.Check(t.Context(), "bash", map[string]any{"command": "git status --short"}); err != nil || !response.Allowed || prompts != 1 {
		t.Fatalf("interactive context changed: response=%+v err=%v prompts=%d", response, err, prompts)
	}
}
