package ui

import (
	"strings"
	"testing"
	"time"
)

func TestAppendThinkingStream_ShowsSingleThinkingHeader(t *testing.T) {
	output := NewOutputModel(DefaultStyles())

	output.AppendThinkingStream("Inspecting")
	output.AppendThinkingStream(" retry policy")
	output.EndThinking()

	rendered := stripAnsi(output.state.content.String())
	if strings.Count(rendered, "Thinking") != 1 {
		t.Fatalf("expected single thinking header, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Inspecting retry policy") {
		t.Fatalf("expected streaming thinking content, got:\n%s", rendered)
	}
}

func TestHandleToolResultWithInfo_ShowsPreviewAndExpandHint(t *testing.T) {
	m := NewModel()
	content := strings.Join([]string{
		"package ui",
		"",
		"func alpha() {}",
		"func beta() {}",
		"func gamma() {}",
		"func delta() {}",
		"func epsilon() {}",
		"func zeta() {}",
		"func eta() {}",
		"func theta() {}",
		"func iota() {}",
		"func kappa() {}",
	}, "\n")

	m.handleToolResultWithInfo(content, "read", "internal/ui/output.go", time.Now().Add(-1500*time.Millisecond))

	rendered := stripAnsi(m.output.state.content.String())
	for _, want := range []string{
		"Read",
		"12 lines from internal/ui/output.go",
		"Preview below - press e for full output",
		"package ui",
		"... +6 lines ...",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("tool result missing %q:\n%s", want, rendered)
		}
	}
}

func TestHandleToolResultWithInfo_UsesExplicitCompactMode(t *testing.T) {
	m := NewModel()
	content := strings.Join([]string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
		"line 8",
		"line 9",
		"line 10",
		"line 11",
		"line 12",
	}, "\n")

	m.toolOutput.ToggleAll()
	m.toolOutput.ToggleAll()
	m.handleToolResultWithInfo(content, "bash", "go test ./internal/ui", time.Now().Add(-2*time.Second))

	rendered := stripAnsi(m.output.state.content.String())
	if !strings.Contains(rendered, "Full output hidden - press e to expand, E to restore previews") {
		t.Fatalf("expected explicit compact-mode hint, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "Preview below - press e for full output") {
		t.Fatalf("compact mode should not show preview hint:\n%s", rendered)
	}
}
