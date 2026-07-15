package ui

import (
	"strings"
	"testing"
)

// A failed command submission leaves the palette open. In the smallest
// supported terminal, its recovery key must therefore remain visible even
// when the inline error consumes an additional row.
func TestCompactPaletteSubmitErrorKeepsRecoveryKeyVisible(t *testing.T) {
	p := NewCommandPalette(DefaultStyles())
	p.commands = []EnhancedPaletteCommand{{
		Name:     "help",
		Shortcut: "/help",
		Enabled:  true,
		Type:     CommandTypeSlash,
	}}
	p.visible = true
	p.filterCommands("")
	p.SetSubmitError("Unavailable: command submission is not connected")

	view := p.View(10, 6)
	assertPaletteGeometry(t, view, 10, 6)
	plain := stripAnsi(view)
	for _, want := range []string{"Esc", "Unavai"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("compact palette error hid recovery context %q:\n%s", want, plain)
		}
	}
}
