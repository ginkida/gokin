package permission

import (
	"context"
	"testing"
)

func TestPermissionPreflightUsesDispatcherPolicyWithoutPrompting(t *testing.T) {
	for _, test := range []struct {
		name      string
		policy    Level
		command   string
		allows    []string
		denies    []string
		grants    []string
		skillDeny []string
		session   Decision
		dontAsk   bool
		canPrompt bool
		want      bool
	}{
		{name: "allow", policy: LevelAllow, want: true},
		{name: "hard deny", policy: LevelDeny, canPrompt: true},
		{name: "interactive ask", policy: LevelAsk, canPrompt: true, want: true},
		{name: "headless ask", policy: LevelAsk},
		{name: "scoped allow", policy: LevelAsk, command: "go test ./...", allows: []string{"bash(go test:*)"}, want: true},
		{name: "scoped allow mismatch", policy: LevelAsk, command: "go build ./...", allows: []string{"bash(go test:*)"}},
		{name: "scoped deny", policy: LevelAllow, command: "go test ./...", denies: []string{"bash(go test:*)"}},
		{name: "skill grant", policy: LevelAsk, grants: []string{"bash"}, want: true},
		{name: "skill deny", policy: LevelAllow, skillDeny: []string{"bash"}},
		{name: "deny beats grant", policy: LevelDeny, grants: []string{"bash"}},
		{name: "session allow", policy: LevelAsk, session: DecisionAllowSession, want: true},
		{name: "session deny", policy: LevelAsk, session: DecisionDenySession},
		{name: "dontAsk read", policy: LevelAsk, command: "git status", dontAsk: true, want: true},
		{name: "dontAsk build", policy: LevelAsk, command: "go build ./...", dontAsk: true},
		{name: "dontAsk explicitly granted build", policy: LevelAsk, command: "go build ./...", dontAsk: true, allows: []string{"bash(go build:*)"}, want: true},
		{name: "dontAsk mutation", policy: LevelAsk, command: "touch output", dontAsk: true},
		{name: "elevated allow", policy: LevelAllow, command: "sudo true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rules := DefaultRules()
			rules.SetPolicy("bash", test.policy)
			manager := NewManager(rules, true)
			manager.SetDontAsk(test.dontAsk)
			manager.SetRunToolRules(test.allows, test.denies)
			args := map[string]any{"command": test.command}
			if test.session != DecisionPending {
				manager.RememberWithArgs("bash", args, test.session)
			}
			prompts := 0
			manager.SetPromptHandler(func(context.Context, *Request) (Decision, error) {
				prompts++
				if test.canPrompt {
					return DecisionAllow, nil
				}
				return DecisionDeny, nil
			})
			before := manager.sessionCache.Len()
			preview, err := manager.Preflight(t.Context(), "bash", args, test.grants, test.skillDeny, test.canPrompt)
			if err != nil || preview.Allowed != test.want || prompts != 0 || manager.sessionCache.Len() != before {
				t.Fatalf("preview=%+v err=%v prompts=%d", preview, err, prompts)
			}
			actual, err := manager.CheckWithTemporaryToolRules(t.Context(), "bash", args, test.grants, test.skillDeny)
			if err != nil || actual.Allowed != preview.Allowed {
				t.Fatalf("preflight diverged from dispatch: preview=%+v actual=%+v err=%v", preview, actual, err)
			}
		})
	}
}

func TestUnconditionallyDeniedDoesNotSamplePatternRules(t *testing.T) {
	for _, test := range []struct {
		name     string
		policy   Level
		allows   []string
		denies   []string
		headless bool
		dontAsk  bool
		disabled bool
		want     bool
	}{
		{name: "hard deny", policy: LevelDeny, want: true},
		{name: "hard deny beats allow", policy: LevelDeny, allows: []string{"bash(go test:*)"}, want: true},
		{name: "allow", policy: LevelAllow, headless: true},
		{name: "interactive ask", policy: LevelAsk},
		{name: "headless ask", policy: LevelAsk, headless: true, want: true},
		{name: "pattern allow retains ask", policy: LevelAsk, headless: true, allows: []string{"bash(go test:*)"}},
		{name: "bare allow retains ask", policy: LevelAsk, headless: true, allows: []string{"bash"}},
		{name: "other tool allow", policy: LevelAsk, headless: true, allows: []string{"read"}, want: true},
		{name: "pattern deny", policy: LevelAllow, headless: true, denies: []string{"bash(go test:*)"}},
		{name: "bare deny", policy: LevelAllow, denies: []string{"bash"}, want: true},
		{name: "wildcard deny", policy: LevelAllow, denies: []string{"b*"}, want: true},
		{name: "bypass config deny", policy: LevelDeny, disabled: true},
		{name: "bypass run deny", policy: LevelAllow, disabled: true, denies: []string{"bash"}, want: true},
		{name: "dontAsk permits some inputs", policy: LevelAsk, headless: true, dontAsk: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rules := DefaultRules()
			rules.SetPolicy("bash", test.policy)
			manager := NewManager(rules, !test.disabled)
			manager.SetDontAsk(test.dontAsk)
			manager.SetRunToolRules(test.allows, test.denies)
			if got := manager.UnconditionallyDenied(t.Context(), "bash", test.headless); got != test.want {
				t.Fatalf("UnconditionallyDenied=%v, want %v", got, test.want)
			}
		})
	}
}
