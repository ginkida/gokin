package context

import (
	"strings"
	"testing"
)

func TestPromptBuilderBareModeIsMinimalAndDeterministic(t *testing.T) {
	workDir := t.TempDir()
	builder := NewPromptBuilder(workDir, &ProjectInfo{
		Type: ProjectTypeGo,
		Name: "SHOULD_NOT_APPEAR",
	})
	builder.SetDetectedContext("DETECTED_MARKER")
	builder.SetToolHints("TOOL_HINT_MARKER")
	builder.SetProvider("glm")
	builder.SetPlanAutoDetect(true)
	builder.SetPinnedContent("PINNED_MARKER")
	builder.SetBareMode(true)

	got := builder.Build()
	for _, forbidden := range []string{
		"SHOULD_NOT_APPEAR",
		"DETECTED_MARKER",
		"TOOL_HINT_MARKER",
		"PINNED_MARKER",
		"GLM-specific",
		"ACTIVE CONTRACT",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("bare prompt contains auto-discovered content %q:\n%s", forbidden, got)
		}
	}
	for _, required := range []string{"Read", "Edit", "Bash", workDir} {
		if !strings.Contains(got, required) {
			t.Fatalf("bare prompt missing %q:\n%s", required, got)
		}
	}
}

func TestPromptBuilderBareModePreservesExplicitPlanBanner(t *testing.T) {
	builder := NewPromptBuilder(t.TempDir(), &ProjectInfo{})
	builder.SetBareMode(true)
	builder.SetPlanMode(true)
	if got := builder.Build(); !strings.HasPrefix(got, planModeBanner) {
		t.Fatalf("bare plan prompt does not start with plan banner:\n%s", got)
	}
}
