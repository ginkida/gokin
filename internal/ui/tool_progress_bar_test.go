package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestLastNNonEmptyLines_EmptyInput(t *testing.T) {
	if got := lastNNonEmptyLines("", 3); got != nil {
		t.Errorf("empty input should produce nil, got %v", got)
	}
	if got := lastNNonEmptyLines("nonempty", 0); got != nil {
		t.Errorf("n=0 should produce nil, got %v", got)
	}
}

func TestLastNNonEmptyLines_SingleLineNoNewline(t *testing.T) {
	got := lastNNonEmptyLines("just one line", 3)
	if len(got) != 1 || got[0] != "just one line" {
		t.Errorf("got %v, want [just one line]", got)
	}
}

func TestLastNNonEmptyLines_SkipsEmptyAndWhitespaceOnlyLines(t *testing.T) {
	got := lastNNonEmptyLines("a\n\nb\n   \nc\n", 5)
	want := []string{"a", "b", "c"}
	if !equalStringSlices(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLastNNonEmptyLines_CapsAtN(t *testing.T) {
	got := lastNNonEmptyLines("a\nb\nc\nd\ne", 3)
	want := []string{"c", "d", "e"}
	if !equalStringSlices(got, want) {
		t.Errorf("got %v, want %v (n=3 keeps last 3)", got, want)
	}
}

func TestLastNNonEmptyLines_ReturnsOldestToNewest(t *testing.T) {
	// Reading order: first element is OLDEST of the last N, last element
	// is NEWEST. Important because the UI renders top-to-bottom.
	got := lastNNonEmptyLines("old\nmid\nnew", 3)
	if got[0] != "old" || got[2] != "new" {
		t.Errorf("ordering wrong: got %v, want oldest→newest", got)
	}
}

func TestLastNNonEmptyLines_HandlesCRLF(t *testing.T) {
	// Windows-style line endings must not produce empty-looking lines.
	got := lastNNonEmptyLines("a\r\nb\r\nc", 5)
	want := []string{"a", "b", "c"}
	if !equalStringSlices(got, want) {
		t.Errorf("CRLF handling: got %v, want %v", got, want)
	}
}

// ─── Tool progress bar view integration ───────────────────────────────────

func TestToolProgressBar_SingleLineStaysInline(t *testing.T) {
	// Classic short tool output: just one line. Must render as compact
	// single-line view (no history block) to preserve existing UX.
	bar := NewToolProgressBarModel(DefaultStyles())
	bar.Show("bash")
	bar.Update(ToolProgressMsg{
		Name:        "bash",
		Elapsed:     2 * time.Second,
		Progress:    -1,
		CurrentStep: "Running unit tests...",
	})
	out := bar.View(120)
	// Should be a single line (no embedded newlines aside from the
	// trailing one we don't add).
	if strings.Contains(out, "\n") {
		t.Errorf("single-line step should not add newlines: %q", out)
	}
	if !strings.Contains(out, "Running unit tests") {
		t.Errorf("expected step text in output: %q", out)
	}
}

func TestToolProgressBar_MultiLineShowsHistoryBlock(t *testing.T) {
	// Multi-line output (npm install / docker build style) renders the
	// last few lines as an indented history block so users can tell the
	// tool is actually progressing vs stuck.
	bar := NewToolProgressBarModel(DefaultStyles())
	bar.Show("bash")
	bar.Update(ToolProgressMsg{
		Name:     "bash",
		Elapsed:  5 * time.Second,
		Progress: -1,
		CurrentStep: "Installing @babel/parser@7.23.0\n" +
			"Installing typescript@5.3.3\n" +
			"Resolving packages: 200/250\n" +
			"Building lockfile entries...",
	})
	out := bar.View(120)
	if !strings.Contains(out, "\n") {
		t.Error("multi-line step should render across multiple lines")
	}
	if !strings.Contains(out, "Recent output") {
		t.Errorf("history block should be labeled, got:\n%s", out)
	}
	// Expect the 3 most recent non-empty lines in the history block.
	for _, needle := range []string{"typescript@5.3.3", "Resolving packages", "Building lockfile"} {
		if !strings.Contains(out, needle) {
			t.Errorf("history block missing %q; got:\n%s", needle, out)
		}
	}
	// The oldest line (babel/parser) should be dropped because we keep
	// only the last 3 non-empty lines.
	if strings.Contains(out, "babel/parser") {
		t.Errorf("history should have capped at maxProgressHistoryLines; got:\n%s", out)
	}
}

func TestToolProgressBar_NoStepProducesCompactLine(t *testing.T) {
	// Tool with no step info — spinner + name + elapsed, no history.
	bar := NewToolProgressBarModel(DefaultStyles())
	bar.Show("read")
	bar.Update(ToolProgressMsg{Name: "read", Elapsed: 100 * time.Millisecond, Progress: -1})
	out := bar.View(80)
	if strings.Contains(out, "\n") {
		t.Errorf("no step → no history block: %q", out)
	}
}

func TestToolProgressBar_CancellableShowsEscHint(t *testing.T) {
	bar := NewToolProgressBarModel(DefaultStyles())
	bar.Show("bash")
	bar.Update(ToolProgressMsg{
		Name:        "bash",
		Elapsed:     4 * time.Second,
		Progress:    0.5,
		CurrentStep: "Running migration",
		Cancellable: true,
	})

	out := bar.View(100)
	if !strings.Contains(out, "Esc cancel") {
		t.Fatalf("expected cancel hint, got:\n%s", out)
	}
}

func TestToolProgressHeartbeatStartsIndeterminate(t *testing.T) {
	msg, ok := ToolProgress("download", 2*time.Second)().(ToolProgressMsg)
	if !ok {
		t.Fatal("ToolProgress command returned the wrong message type")
	}
	if msg.Progress != -1 {
		t.Fatalf("heartbeat progress=%v, want indeterminate -1", msg.Progress)
	}

	bar := NewToolProgressBarModel(DefaultStyles())
	bar.Update(msg)
	if plain := stripAnsi(bar.View(60)); strings.Contains(plain, "0%") {
		t.Fatalf("indeterminate heartbeat rendered as a stuck 0%% operation: %q", plain)
	}
}

func TestToolProgressDetailedUpdatePreservesElapsedHeartbeat(t *testing.T) {
	bar := NewToolProgressBarModel(DefaultStyles())
	bar.Update(ToolProgressMsg{Name: "download", Elapsed: 7 * time.Second, Progress: -1})
	bar.Update(ToolProgressMsg{Name: "download", Progress: .5, CurrentStep: "halfway"})
	if bar.elapsed != 7*time.Second {
		t.Fatalf("detail update reset elapsed heartbeat to %v", bar.elapsed)
	}
	if plain := stripAnsi(bar.View(80)); !strings.Contains(plain, "7.0s") {
		t.Fatalf("elapsed timer jumped after detail update: %q", plain)
	}

	bar.Update(ToolProgressMsg{Name: "upload", Progress: .1})
	if bar.elapsed != 0 {
		t.Fatalf("new named operation inherited prior elapsed time: %v", bar.elapsed)
	}
}

func TestToolProgressFitsTinyHeightsAndKeepsCancel(t *testing.T) {
	bar := NewToolProgressBarModel(DefaultStyles())
	bar.Update(ToolProgressMsg{
		Name:        "deploy",
		Progress:    .4,
		Cancellable: true,
		CurrentStep: "preparing\nuploading\nverifying\nrestarting\nchecking health",
	})

	for height := 1; height <= 12; height++ {
		view := bar.View(52, height)
		if got, limit := lipgloss.Height(view), toolProgressHeightBudget(height); got > limit {
			t.Fatalf("height=%d rendered %d rows, want <=%d:\n%s", height, got, limit, stripAnsi(view))
		}
		if !strings.Contains(stripAnsi(view), "Esc cancel") {
			t.Fatalf("height=%d lost cancellation action:\n%s", height, stripAnsi(view))
		}
	}
}

func TestToolProgressSanitizesRuntimeLabelsAndHistory(t *testing.T) {
	bar := NewToolProgressBarModel(DefaultStyles())
	bar.Update(ToolProgressMsg{
		Name:        "bash\nforged\x1b]0;title\a",
		Progress:    -1,
		CurrentStep: "safe\x1b[2J\nnext\x1b]0;bad\a",
	})

	view := bar.View(80)
	plain := stripAnsi(view)
	if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b]0;") {
		t.Fatalf("runtime control sequence reached terminal output: %q", view)
	}
	for _, want := range []string{"Bash forged", "safe", "next"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("sanitized progress lost %q:\n%s", want, plain)
		}
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
