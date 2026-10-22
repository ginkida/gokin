package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
)

func TestApplicationStartupAppliesKeyboardProfile(t *testing.T) {
	for _, profile := range []string{"claude", "gokin"} {
		cfg := config.DefaultConfig()
		cfg.UI.Keybindings = profile
		b := &Builder{cfg: cfg, projectInfo: &appcontext.ProjectInfo{}, options: BuildOptions{Bare: true}}
		if err := b.initUI(); err != nil {
			t.Fatal(err)
		}
		var model tea.Model = *b.tuiModel
		for _, msg := range []tea.Msg{tea.WindowSizeMsg{Width: 80, Height: 24}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")}, tea.KeyMsg{Type: tea.KeyCtrlA}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")}} {
			model, _ = model.Update(msg)
		}
		text := ansi.Strip(model.View())
		if profile == "claude" && !strings.Contains(text, "Xhello") {
			t.Fatal("application default did not preserve readline editing")
		}
		if profile == "gokin" && !strings.Contains(text, "helloX") {
			t.Fatal("original profile changed input ownership")
		}
	}
}
