package permission

import "testing"

func TestDontAskTrustedBashWrapperIsExactAndPreservesDenials(t *testing.T) {
	wrapped := "( cd '/project' || exit 1; go vet . )"
	inspection := "( cd '/project' || exit 1; git diff --stat )"
	for _, tc := range []struct {
		name, command, inner string
		deny, allowed        bool
	}{
		{"vet needs a grant", wrapped, "go vet .", false, false},
		{"trusted inspection", inspection, "git diff --stat", false, true},
		{"hook rewrite", "printf changed > source.go", "git diff --stat", false, false},
		{"unknown inner", wrapped, "arbitrary-check", false, false},
		{"explicit deny", inspection, "git diff --stat", true, false},
		{"elevated wrapper", "git reset --hard HEAD", "git diff --stat", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := DefaultRules()
			if tc.deny {
				rules.SetPolicy("bash", LevelDeny)
			}
			manager := NewManager(rules, true)
			manager.SetDontAsk(true)
			bound := wrapped
			if tc.name == "trusted inspection" || tc.name == "hook rewrite" || tc.name == "explicit deny" {
				bound = inspection
			}
			if tc.name == "elevated wrapper" {
				bound = tc.command
			}
			ctx := ContextWithReadOnlyBashWrapper(t.Context(), bound, tc.inner)
			response, err := manager.Check(ctx, "bash", map[string]any{"command": tc.command})
			if err != nil || response.Allowed != tc.allowed {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}
}
