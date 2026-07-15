package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestOutputWindowSizeAppliesPendingContentAndSafeGeometry(t *testing.T) {
	output := NewOutputModel(DefaultStyles())
	original := strings.Repeat("x", 13)
	// Output can arrive before Bubble Tea announces the first terminal size.
	// The first WindowSizeMsg must make that pending transcript visible and
	// establish the same wrapping width as SetSize.
	output.AppendText(original)

	updated, _ := output.Update(tea.WindowSizeMsg{Width: 8, Height: 3})
	if !updated.Ready() {
		t.Fatal("first WindowSizeMsg did not ready the output viewport")
	}
	if updated.viewport.Width != 8 || updated.viewport.Height < 1 || updated.state.width != 4 {
		t.Fatalf("first resize geometry: viewport=%dx%d contentWidth=%d, want 8x>=1/4",
			updated.viewport.Width, updated.viewport.Height, updated.state.width)
	}
	assertWrappedOutputFitsAndPreserves(t, updated.state.cachedWrapped, original, 4)

	// Degenerate resize messages are possible while panes are being created or
	// torn down. Internal viewport dimensions must never become negative.
	updated, _ = updated.Update(tea.WindowSizeMsg{Width: -2, Height: -3})
	if updated.viewport.Width < 0 || updated.viewport.Height < 1 || updated.state.width < 1 {
		t.Fatalf("degenerate resize leaked invalid geometry: viewport=%dx%d contentWidth=%d",
			updated.viewport.Width, updated.viewport.Height, updated.state.width)
	}
	_ = updated.ViewWithHeight(1) // regression guard: rendering stays panic-free

	for width := 0; width <= 4; width++ {
		tiny := NewOutputModel(DefaultStyles())
		tiny.SetSize(width, 0)
		tiny.AppendText("abcdef")
		wrapWidth := max(width, 1)
		if tiny.viewport.Width < 0 || tiny.viewport.Height < 1 || tiny.state.width != wrapWidth {
			t.Fatalf("width=%d tiny geometry: viewport=%dx%d contentWidth=%d, want contentWidth=%d",
				width, tiny.viewport.Width, tiny.viewport.Height, tiny.state.width, wrapWidth)
		}
		assertWrappedOutputFitsAndPreserves(t, tiny.state.cachedWrapped, "abcdef", wrapWidth)
	}
}

func TestOutputClearResetsThinkingCodeBlocksAndStreamParser(t *testing.T) {
	output := NewOutputModel(DefaultStyles())
	output.SetSize(40, 6)

	output.AppendTextStream("```go\nold := true\n```\n")
	output.FlushStream()
	if output.GetCodeBlocks().Count() == 0 {
		t.Fatal("setup did not register the completed code block")
	}
	output.AppendThinkingStream("old reasoning")

	output.Clear()
	if output.IsThinkingActive() {
		t.Fatal("Clear left the previous thinking stream active")
	}
	if got := output.GetCodeBlocks().Count(); got != 0 {
		t.Fatalf("Clear retained %d navigable code blocks from erased output", got)
	}

	output.AppendThinkingStream("fresh reasoning")
	plain := stripAnsi(output.Content())
	if strings.Count(plain, "Thinking") != 1 || !strings.Contains(plain, "fresh reasoning") || strings.Contains(plain, "old reasoning") {
		t.Fatalf("thinking did not restart cleanly after Clear:\n%s", plain)
	}

	// An unfinished fence must not survive Clear and turn the next transcript
	// into a code block (or restore erased code through the registry on Flush).
	output.Clear()
	output.AppendTextStream("```go\nsecret")
	output.Clear()
	output.AppendTextStream("plain after clear\n")
	output.FlushStream()
	if got := output.GetCodeBlocks().Count(); got != 0 {
		t.Fatalf("stale stream parser registered %d erased code blocks after Clear", got)
	}
	if plain := stripAnsi(output.Content()); !strings.Contains(plain, "plain after clear") || strings.Contains(plain, "secret") {
		t.Fatalf("next stream inherited erased fenced content:\n%s", plain)
	}
}

func TestFilePeekPreservesUnicodeFilenameTail(t *testing.T) {
	panel := NewFilePeekPanel(DefaultStyles())
	panel.ShowPeek(FilePeekMsg{
		FilePath: "/项目/资料/配置🙂.go",
		Action:   "reading",
	})

	const width = 18 // "▸ Reading " leaves eight cells for the path.
	view := panel.View(width)
	plain := stripAnsi(view)
	if got := lipgloss.Width(view); got > width {
		t.Fatalf("file peek width=%d, want <=%d: %q", got, width, plain)
	}
	if !strings.Contains(plain, "置🙂.go") {
		t.Fatalf("cell-aware path elision discarded the identifying filename tail: %q", plain)
	}
}

func TestToastSentinelWidthKeepsPrioritizedNotificationVisible(t *testing.T) {
	manager := NewToastManager(DefaultStyles())
	manager.ShowError("Critical failure")
	manager.ShowInfo("Secondary update")

	view := manager.ViewLimit(0, 1)
	plain := stripAnsi(view)
	if !strings.Contains(plain, "Critical failure") || !strings.Contains(plain, "+1 more") {
		t.Fatalf("sentinel-width toast stack lost its selected row or hidden count: %q", plain)
	}
	if got := lipgloss.Width(view); got > 80 {
		t.Fatalf("sentinel-width toast used %d cells, want fallback <=80: %q", got, plain)
	}
}

func TestToolProgressTinyCancelHintRemainsRecognizable(t *testing.T) {
	bar := NewToolProgressBarModel(DefaultStyles())
	bar.Update(ToolProgressMsg{Name: "deploy", Progress: .5, Cancellable: true})

	for width := 1; width < lipgloss.Width("Esc cancel"); width++ {
		plain := stripAnsi(bar.View(width))
		if got := lipgloss.Width(plain); got > width {
			t.Fatalf("width=%d cancel hint rendered %d cells: %q", width, got, plain)
		}
		if width < lipgloss.Width("Esc") {
			if plain != "⎋" {
				t.Fatalf("width=%d tiny cancel hint=%q, want the Escape symbol", width, plain)
			}
		} else if plain != "Esc" {
			t.Fatalf("width=%d compact cancel hint=%q, want an unambiguous Esc", width, plain)
		}
	}
}

func TestToolProgressDoesNotSplitEmojiGraphemeWhenTruncated(t *testing.T) {
	bar := NewToolProgressBarModel(DefaultStyles())
	bar.SetReducedMotion(true)
	bar.Update(ToolProgressMsg{
		Name:        "x",
		Progress:    -1,
		CurrentStep: "👩‍💻👩‍💻",
	})

	plain := stripAnsi(bar.View(14))
	if strings.Contains(plain, "👩\u200d…") || strings.Contains(plain, "\u200d…") {
		t.Fatalf("progress truncation split a ZWJ emoji cluster: %q", plain)
	}
	if got := lipgloss.Width(plain); got > 14 {
		t.Fatalf("progress row width=%d, want <=14: %q", got, plain)
	}
}
