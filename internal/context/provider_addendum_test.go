package context

import (
	"strings"
	"testing"
)

func TestProviderAddendum_KimiNonEmpty(t *testing.T) {
	got := providerAddendum("kimi")
	if got == "" {
		t.Fatal("kimi addendum should be non-empty")
	}
	// Spot-check that the key guardrails surface somewhere in the text.
	for _, needle := range []string{
		"Plan:",
		"read-before-edit",
		"delta-check",
		"Tool budget",
	} {
		if !strings.Contains(got, needle) {
			t.Errorf("kimi addendum missing %q marker", needle)
		}
	}
}

func TestProviderAddendum_NormalizesCase(t *testing.T) {
	// SetProvider lowercases before storing; providerAddendum should be
	// a pure map lookup on the exact family key.
	if providerAddendum("Kimi") != "" {
		t.Error("providerAddendum is case-sensitive by contract; caller must lowercase")
	}
	if providerAddendum("kimi") == "" {
		t.Error("lowercase kimi must hit the mapping")
	}
}

func TestProviderAddendum_UnknownProvider(t *testing.T) {
	for _, name := range []string{"glm", "minimax", "ollama", "anthropic", "", "random"} {
		if got := providerAddendum(name); got != "" {
			t.Errorf("provider %q should have no addendum yet, got %d chars", name, len(got))
		}
	}
}

func TestPromptBuilder_SetProviderInjectsAddendum(t *testing.T) {
	pb := NewPromptBuilder("/tmp/fake", &ProjectInfo{
		Type: ProjectTypeGo,
		Name: "fake",
	})
	pb.SetProvider("kimi")
	prompt := pb.Build()
	if !strings.Contains(prompt, "Kimi-specific") {
		t.Errorf("kimi provider didn't inject addendum; prompt tail: %q",
			prompt[maxInt(0, len(prompt)-400):])
	}
	if !strings.Contains(prompt, "read-before-edit") {
		t.Error("kimi addendum did not reach the built prompt")
	}
}

func TestPromptBuilder_SetProviderEmptyNoAddendum(t *testing.T) {
	pb := NewPromptBuilder("/tmp/fake", &ProjectInfo{
		Type: ProjectTypeGo,
		Name: "fake",
	})
	pb.SetProvider("")
	prompt := pb.Build()
	if strings.Contains(prompt, "Kimi-specific") {
		t.Error("empty provider must not inject Kimi addendum")
	}
}

func TestPromptBuilder_SetProviderInvalidatesCache(t *testing.T) {
	pb := NewPromptBuilder("/tmp/fake", &ProjectInfo{
		Type: ProjectTypeGo,
		Name: "fake",
	})
	first := pb.Build()
	pb.SetProvider("kimi")
	second := pb.Build()
	if first == second {
		t.Error("cached prompt survived provider change — cache invalidation broken")
	}
}

func TestPromptBuilder_SetProviderUnchangedSkipsDirty(t *testing.T) {
	// Setting the same provider twice must not force a rebuild on the
	// second call — avoids noisy cache churn.
	pb := NewPromptBuilder("/tmp/fake", &ProjectInfo{Type: ProjectTypeGo})
	pb.SetProvider("kimi")
	_ = pb.Build() // warm cache + clear dirty
	// Directly inspect dirty flag: the setter must leave it false when
	// the provider didn't change.
	pb.SetProvider("kimi")
	if pb.promptDirty {
		t.Error("re-setting same provider should not flip promptDirty")
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
