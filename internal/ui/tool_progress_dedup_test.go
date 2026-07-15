package ui

import (
	"strings"
	"testing"
	"time"
)

// A long-running tool used to render TWO spinner lines with the same
// tool+elapsed — the live card's current line AND the tool progress bar
// («⠋ Bash — $ go test … · 23.5s» over «⠏ Bash 23.5s go test…»). The bar is
// suppressed while the card renders unless it carries a signal the card
// lacks (determinate %, bytes, named step).

func longRunningBashModel() *Model {
	m := NewModel()
	m.width, m.height = 100, 30
	m.output.SetSize(100, 10)
	m.state = StateProcessing
	m.currentTool = "bash"
	m.currentToolInfo = "$ go test -race ./..."
	m.toolStartTime = time.Now().Add(-23 * time.Second)
	return m
}

func countBashLines(view string) int {
	n := 0
	for line := range strings.SplitSeq(view, "\n") {
		if strings.Contains(line, "Bash") || strings.Contains(line, "bash") {
			n++
		}
	}
	return n
}

func TestToolProgressBar_IndeterminateSuppressedUnderLiveCard(t *testing.T) {
	m := longRunningBashModel()
	m.toolProgressBar.Update(ToolProgressMsg{Name: "bash", Elapsed: 23 * time.Second, Progress: -1})

	view := stripAnsi(m.View())
	if got := countBashLines(view); got != 1 {
		t.Fatalf("want exactly ONE bash activity line (the card), got %d:\n%s", got, view)
	}
}

func TestToolProgressBar_DeterminateStillRenders(t *testing.T) {
	m := longRunningBashModel()
	m.toolProgressBar.Update(ToolProgressMsg{Name: "bash", Elapsed: 23 * time.Second, Progress: 0.5})

	view := stripAnsi(m.View())
	if got := countBashLines(view); got < 2 {
		t.Fatalf("a determinate bar carries distinct signal and must still render, got %d line(s):\n%s", got, view)
	}
}

func TestToolProgressBar_RendersWithoutLiveCard(t *testing.T) {
	m := NewModel()
	m.width, m.height = 100, 30
	m.output.SetSize(100, 10)
	m.state = StateInput // no live card
	m.toolProgressBar.Update(ToolProgressMsg{Name: "copy", Elapsed: 2 * time.Second, Progress: -1})

	view := stripAnsi(m.View())
	if !strings.Contains(view, "Copy") && !strings.Contains(view, "copy") {
		t.Fatalf("bar must render when no live card is on screen:\n%s", view)
	}
}
