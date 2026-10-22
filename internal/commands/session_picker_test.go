package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/chat"
)

type pickerCommandApp struct {
	*resumeFakeApp
	menus   int
	show    bool
	choices []chat.SessionInfo
	note    string
}

func (a *pickerCommandApp) ShowSessionPicker(_ context.Context, choices []chat.SessionInfo, note string) bool {
	a.menus++
	a.choices = choices
	a.note = note
	return a.show
}

func TestResumePickerReportsPartialReadFailuresWithoutClaimingHistoryIsEmpty(t *testing.T) {
	dir := t.TempDir()
	hm := saveFixtureSession(t, "saved", "glm", dir)
	path := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gokin", "sessions", "broken.json")
	if err := os.WriteFile(path, []byte(`{"id":"broken",`), 0600); err != nil {
		t.Fatal(err)
	}
	a := &pickerCommandApp{resumeFakeApp: &resumeFakeApp{
		fakeAppForMCP: &fakeAppForMCP{workDir: dir}, hm: hm, session: chat.NewSession(),
	}, show: true}
	_, err := (&ResumeCommand{}).Execute(t.Context(), nil, a)
	if err != nil || !strings.Contains(a.note, "broken.json") || strings.Contains(a.note, "No readable sessions") {
		t.Fatalf("partial read note misstates saved history: %q %v", a.note, err)
	}
	a.show = false
	text, err := (&ResumeCommand{}).Execute(t.Context(), nil, a)
	if err != nil || !strings.Contains(text, "Example: /resume saved") || !strings.Contains(text, "broken.json") {
		t.Fatalf("plain list hides a damaged session: %q %v", text, err)
	}
}

func TestResumeCommandInteractivePickerAndPlainFallback(t *testing.T) {
	dir := t.TempDir()
	hm := saveFixtureSession(t, "saved", "glm", dir)
	a := &pickerCommandApp{resumeFakeApp: &resumeFakeApp{
		fakeAppForMCP: &fakeAppForMCP{workDir: dir}, hm: hm, session: chat.NewSession(),
	}, show: true}
	text, err := (&ResumeCommand{}).Execute(t.Context(), nil, a)
	if err != nil || text != "" || a.menus != 1 || len(a.choices) != 1 || a.switchCalls != 0 {
		t.Fatalf("picker invocation changed the session or printed a second list: %q %v %+v", text, err, a)
	}
	a.show = false
	text, err = (&ResumeCommand{}).Execute(t.Context(), nil, a)
	if err != nil || !strings.Contains(text, "Example: /resume saved") || a.switchCalls != 0 {
		t.Fatalf("plain runtime lost its copyable list: %q %v", text, err)
	}
}
