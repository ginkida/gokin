package setup

import (
	"strings"
	"testing"
)

// TestSetupChoices_NoAnthropicCase pins that the setup wizard no longer
// has a "anthropic" provider choice. Anthropic native was removed in
// v0.65; the case in buildSetupChoices was unreachable because `ordered`
// only iterates over real registry entries. Stale code that read as if
// it still worked.
//
// Inverse pin: no choice should have "Claude Sonnet" in its lines,
// since that was the Anthropic-case wording.
func TestSetupChoices_NoAnthropicCase(t *testing.T) {
	choices := buildSetupChoices()
	for _, c := range choices {
		for _, line := range c.Lines {
			if strings.Contains(line, "Claude Sonnet") {
				t.Errorf("wizard choice %q still contains stale Anthropic wording: %q",
					c.Title, line)
			}
		}
		// Title-level check too — no "Anthropic (Cloud)" surface.
		if strings.Contains(c.Title, "Anthropic (Cloud)") {
			t.Errorf("wizard still has Anthropic (Cloud) choice: %s", c.Title)
		}
	}
}

// TestSetupChoices_GLMVersionLabelIsCurrent pins that the GLM choice's
// description references the current major model line (5.x) rather than
// the pre-v0.71 "GLM-4/GLM-5" label. The current default is glm-5.2;
// "GLM-4/GLM-5" implied a 50/50 split that hasn't been true since v0.71.
func TestSetupChoices_GLMVersionLabelIsCurrent(t *testing.T) {
	choices := buildSetupChoices()
	var glmChoice *setupChoice
	for i, c := range choices {
		if strings.HasPrefix(c.Title, "GLM ") {
			glmChoice = &choices[i]
			break
		}
	}
	if glmChoice == nil {
		t.Fatal("GLM choice missing from wizard")
	}
	joined := strings.Join(glmChoice.Lines, "\n")

	// Stale wording must not return.
	if strings.Contains(joined, "GLM-4/GLM-5") {
		t.Errorf("GLM choice still uses stale GLM-4/GLM-5 label:\n%s", joined)
	}
	// Current label should mention the 5.x line.
	if !strings.Contains(joined, "GLM-5") {
		t.Errorf("GLM choice should mention GLM-5.x, got:\n%s", joined)
	}
}
