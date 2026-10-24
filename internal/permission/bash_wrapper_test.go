package permission

import "testing"

func TestDontAskTrustedBashWrapperIsExactAndPreservesDenials(t *testing.T) {
	wrapped := "( cd '/project' || exit 1; go vet . )"
	for _, tc := range []struct {
		name, command, inner string
		deny, allowed        bool
	}{
		{"trusted vet", wrapped, "go vet .", false, true},
		{"hook rewrite", "printf changed > source.go", "go vet .", false, false},
		{"unknown inner", wrapped, "arbitrary-check", false, false},
		{"explicit deny", wrapped, "go vet .", true, false},
		{"elevated wrapper", "git reset --hard HEAD", "go vet .", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := DefaultRules()
			if tc.deny {
				rules.SetPolicy("bash", LevelDeny)
			}
			manager := NewManager(rules, true)
			manager.SetDontAsk(true)
			bound := wrapped
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
