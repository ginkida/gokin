package app

import (
	"strings"
	"testing"

	"gokin/internal/config"
)

// TestAppBuildEvidenceFooterIfEnabled_RespectsConfigKnob — the main
// integration concern: a user that flips
// `completion.evidence_footer: false` must see zero footer even when
// touched-paths / verification would otherwise produce one.
func TestAppBuildEvidenceFooterIfEnabled_RespectsConfigKnob(t *testing.T) {
	app := &App{
		config: &config.Config{
			Completion: config.CompletionConfig{EvidenceFooter: false},
		},
		responseTouchedPaths: []string{"internal/app/foo.go"},
		responseCommands:     []string{"go test ./..."},
	}

	if got := app.buildEvidenceFooterIfEnabled("OK."); got != "" {
		t.Errorf("expected empty footer when knob is off, got %q", got)
	}
}

func TestAppBuildEvidenceFooterIfEnabled_DefaultOnProducesFooter(t *testing.T) {
	app := &App{
		config: &config.Config{
			Completion: config.CompletionConfig{EvidenceFooter: true},
		},
		responseTouchedPaths: []string{"internal/app/foo.go"},
		responseCommands:     []string{"go test ./..."},
	}

	got := app.buildEvidenceFooterIfEnabled("Wrote the change.")
	if !strings.Contains(got, "📁 Changed: foo.go") {
		t.Errorf("expected Changed clause: %q", got)
	}
	if !strings.Contains(got, "✓ Verified: go test") {
		t.Errorf("expected Verified clause: %q", got)
	}
}

// Regression: with no config present (nil pointer), the method must not
// crash and must behave as if the knob is on — otherwise tests that
// construct a minimal App for other features would silently lose the
// footer and this feature would look accidentally broken.
func TestAppBuildEvidenceFooterIfEnabled_NilConfigTreatedAsEnabled(t *testing.T) {
	app := &App{
		config:               nil,
		responseTouchedPaths: []string{"foo.go"},
	}
	got := app.buildEvidenceFooterIfEnabled("OK.")
	if got == "" {
		t.Errorf("expected footer when config is nil (default-on), got empty")
	}
}
