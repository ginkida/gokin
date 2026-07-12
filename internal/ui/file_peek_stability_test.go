package ui

import (
	"strings"
	"testing"
)

// The file-peek line must NOT render while the live activity card is on
// screen: its 1.5s TTL made it pop in/out below the card every couple of
// seconds, shifting the whole panel stack — the "Writing: … line jumps"
// field report. The card already carries the current tool + file, so
// nothing is lost. One activity surface per frame (the feed-vs-tree rule).
func TestFilePeek_SuppressedWhileLiveCardRenders(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.height = 30
	m.output.SetSize(100, 10)
	m.state = StateStreaming // live card renders in this state
	m.currentResponseBuf.WriteString("Пишу ответ прямо сейчас")
	m.filePeek.ShowPeek(FilePeekMsg{FilePath: "/w/internal/ratelimit/limiter.go", Action: "read", Content: "x\ny\n"})

	got := stripAnsi(m.View())
	if !strings.Contains(got, "Writing:") {
		t.Fatalf("live card must render during streaming:\n%s", got)
	}
	if strings.Contains(got, "limiter.go") {
		t.Fatalf("file peek must be suppressed while the card renders (it makes the card line jump):\n%s", got)
	}
}

// Outside the card's states the peek still renders — the suppression is
// per-frame, not a removal of the feature.
func TestFilePeek_StillRendersWithoutLiveCard(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.height = 30
	m.output.SetSize(100, 10)
	m.state = StateInput // no live card
	m.filePeek.ShowPeek(FilePeekMsg{FilePath: "/w/internal/ratelimit/limiter.go", Action: "read", Content: "x\ny\n"})

	got := stripAnsi(m.View())
	if !strings.Contains(got, "limiter.go") {
		t.Fatalf("file peek should render when no live card is on screen:\n%s", got)
	}
}
