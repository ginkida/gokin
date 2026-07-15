package ui

import (
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

func TestWriteTerminalTitleKeepsUntrustedTextInsideOneOSC0Envelope(t *testing.T) {
	malicious := "trusted\a" +
		"\x1b]52;c;ZXZpbA==\a" +
		"after-osc\x1b\\after-st\u009c" +
		"\nnext\rline\tvalue\x00" +
		"\x1b[31mRED\x1b[0m"

	var output strings.Builder
	writeTerminalTitle(&output, malicious)
	got := output.String()
	payload := requireSingleOSC0Envelope(t, got)

	if count := strings.Count(got, "\x1b"); count != 1 {
		t.Fatalf("terminal title emitted %d ESC bytes, want only the OSC-0 introducer: %q", count, got)
	}
	if count := strings.Count(got, "\a"); count != 1 {
		t.Fatalf("terminal title emitted %d BEL bytes, want only the OSC-0 terminator: %q", count, got)
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("terminal title must stay on one line: %q", got)
	}

	for _, r := range payload {
		if unicode.IsControl(r) {
			t.Fatalf("terminal title payload contains control rune %U: %q", r, payload)
		}
	}
	for _, fragment := range []string{
		"]52;", "52;c;", "ZXZpbA==", "\x1b", "\a", "\u009c", "[31m", "[0m",
	} {
		if strings.Contains(payload, fragment) {
			t.Fatalf("terminal title retained unsafe fragment %q: %q", fragment, payload)
		}
	}
	if !strings.Contains(payload, "trusted") || !strings.Contains(payload, "RED") {
		t.Fatalf("sanitization removed ordinary visible text: %q", payload)
	}
}

func TestWriteTerminalTitlePreservesUnicodeAndCapsDisplayWidth(t *testing.T) {
	const unicodeTitle = "Gokin · модель Яндекс · ~/Проекты/кот"

	var normal strings.Builder
	writeTerminalTitle(&normal, unicodeTitle)
	if got := requireSingleOSC0Envelope(t, normal.String()); got != unicodeTitle {
		t.Fatalf("normal Unicode title changed:\n got: %q\nwant: %q", got, unicodeTitle)
	}

	var long strings.Builder
	writeTerminalTitle(&long, unicodeTitle+" · "+strings.Repeat("界", 300))
	payload := requireSingleOSC0Envelope(t, long.String())
	if !strings.HasPrefix(payload, unicodeTitle) {
		t.Fatalf("bounded title lost its Unicode prefix: %q", payload)
	}
	if width := lipgloss.Width(payload); width > 200 {
		t.Fatalf("terminal title display width = %d, want at most 200 cells", width)
	}
}

func requireSingleOSC0Envelope(t *testing.T, output string) string {
	t.Helper()

	const prefix = "\x1b]0;"
	if !strings.HasPrefix(output, prefix) {
		t.Fatalf("terminal title does not start with OSC-0: %q", output)
	}
	if !strings.HasSuffix(output, "\a") {
		t.Fatalf("terminal title does not end with BEL: %q", output)
	}
	return strings.TrimSuffix(strings.TrimPrefix(output, prefix), "\a")
}
