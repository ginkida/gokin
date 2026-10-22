package commands

import (
	"context"
	"strings"
	"testing"
)

func TestKeyboardHelpUsesActiveProfile(t *testing.T) {
	app := newModelApp("glm", "glm-5.3-flash")
	app.cfg.UI.Keybindings = "claude"
	for _, command := range []Command{&HelpCommand{handler: NewHandler()}, &ShortcutsCommand{}} {
		text, err := command.Execute(context.Background(), nil, app)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Alt+P", "F1", "F2", "Ctrl+S", "Stash/restore", "Ctrl+O", "transcript"} {
			if !strings.Contains(text, want) {
				t.Fatalf("/%s missing %q", command.Name(), want)
			}
		}
		if strings.Contains(text, "Ctrl+K — open the model selector") || strings.Contains(text, "Ctrl+P for the full command palette") {
			t.Fatal("help teaches original profile")
		}
	}
	app.cfg.UI.Keybindings = "gokin"
	text, err := (&ShortcutsCommand{}).Execute(context.Background(), nil, app)
	if err != nil || !strings.Contains(text, "Ctrl+K") || !strings.Contains(text, "Open settings") {
		t.Fatal("original profile no longer has correct help")
	}
}
