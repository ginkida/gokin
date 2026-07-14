package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFileBrowserEnterAddsReferenceWithoutLosingDraft(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewModel()
	m.workDir = dir
	m.state = StateProcessing
	m.input.textarea.SetValue("review")
	var selected string
	m.SetFileSelectCallback(func(path string) { selected = path })

	updated, _ := m.Update(FileBrowserRequestMsg{StartPath: dir})
	opened := updated.(Model)
	if opened.state != StateFileBrowser || opened.workspaceOverlayReturnState != StateProcessing {
		t.Fatalf("browser did not preserve processing state: state=%v return=%v", opened.state, opened.workspaceOverlayReturnState)
	}
	for i, entry := range opened.fileBrowser.entries {
		if entry.Path == file {
			opened.fileBrowser.selectedIndex = i
			break
		}
	}
	browser, cmd := opened.fileBrowser.Update(tea.KeyMsg{Type: tea.KeyEnter})
	opened.fileBrowser = browser
	if cmd == nil {
		t.Fatal("Enter on a file did not emit an open action")
	}
	updated, _ = opened.Update(cmd())
	closed := updated.(Model)
	if closed.state != StateProcessing || closed.fileBrowserActive {
		t.Fatalf("selection did not restore active work: state=%v active=%v", closed.state, closed.fileBrowserActive)
	}
	if got := closed.input.Value(); got != "review @main.go" {
		t.Fatalf("composer=%q, want preserved draft plus relative reference", got)
	}
	if selected != file {
		t.Fatalf("selection observer got %q, want %q", selected, file)
	}
	if !activeToastContains(&closed, "Added 1 file reference to draft") {
		t.Fatal("selection lacks visible success feedback")
	}
	if !strings.Contains(renderToPlain(closed.input.View()), "@main.go") {
		t.Fatal("inserted reference is not visible in composer")
	}
}

func TestFileBrowserOpenFailureRestoresLifecycleAndShowsSafeFeedback(t *testing.T) {
	m := NewModel()
	m.state = StateInput
	m.workspaceOverlayReturnState = StateStreaming
	unsafePath := filepath.Join(t.TempDir(), "\x1b]0;hijacked-title\amissing\nfolder")

	updated, _ := m.Update(FileBrowserRequestMsg{StartPath: unsafePath})
	got := updated.(Model)
	if got.state != StateInput || got.fileBrowserActive {
		t.Fatalf("failed open changed workspace state: state=%v active=%v", got.state, got.fileBrowserActive)
	}
	if got.workspaceOverlayReturnState != StateStreaming {
		t.Fatalf("failed open overwrote prior overlay lifecycle: return=%v", got.workspaceOverlayReturnState)
	}
	if !activeToastContains(&got, "Could not open file browser") {
		t.Fatal("failed open lacks immediate toast feedback")
	}
	output := got.output.state.content.String()
	if strings.Contains(output, "\x1b]") || strings.Contains(output, "hijacked-title") {
		t.Fatalf("failed open retained terminal control payload: %q", output)
	}
	if !strings.Contains(stripAnsi(output), "Could not open file browser") {
		t.Fatalf("failed open lacks durable feedback: %q", output)
	}
}

func TestFileBrowserMultiSelectQuotesPathsAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	spacePath := filepath.Join(dir, "space name.go")
	otherPath := filepath.Join(dir, "other.go")
	m := NewModel()
	m.workDir = dir
	m.state = StateFileBrowser
	m.fileBrowserActive = true
	m.workspaceOverlayReturnState = StateInput
	m.input.textarea.SetValue("compare")
	var observed []string
	m.SetFileSelectCallback(func(path string) { observed = append(observed, path) })

	updated, _ := m.Update(FileBrowserActionMsg{
		Action: FileBrowserActionSelect,
		Files:  []string{spacePath, otherPath, spacePath, "bad\npath"},
	})
	got := updated.(Model)
	if got.state != StateInput {
		t.Fatalf("multi-select state=%v, want input", got.state)
	}
	value := got.input.Value()
	for _, want := range []string{`compare`, `@"space name.go"`, `@other.go`} {
		if !strings.Contains(value, want) {
			t.Fatalf("composer missing %q: %q", want, value)
		}
	}
	if strings.Count(value, "space name.go") != 1 || strings.Contains(value, "bad") {
		t.Fatalf("composer did not deduplicate/sanitize selection: %q", value)
	}
	if len(observed) != 2 {
		t.Fatalf("selection observer received duplicate or unsafe paths: %q", observed)
	}
	if !activeToastContains(&got, "Added 2 file references to draft") {
		t.Fatal("multi-select count feedback is missing")
	}
}

func TestFileBrowserReturnStateTracksStreamAndCompletion(t *testing.T) {
	dir := t.TempDir()

	t.Run("stream continues", func(t *testing.T) {
		m := NewModel()
		m.state = StateProcessing
		updated, _ := m.Update(FileBrowserRequestMsg{StartPath: dir})
		browser := updated.(Model)
		updated, _ = browser.Update(StreamTextMsg("working"))
		browser = updated.(Model)
		if browser.state != StateFileBrowser || browser.workspaceOverlayReturnState != StateStreaming {
			t.Fatalf("stream clobbered browser or stale return state: state=%v return=%v", browser.state, browser.workspaceOverlayReturnState)
		}
		updated, _ = browser.Update(FileBrowserActionMsg{Action: FileBrowserActionClose})
		if got := updated.(Model).state; got != StateStreaming {
			t.Fatalf("close state=%v, want streaming", got)
		}
	})

	t.Run("request completed", func(t *testing.T) {
		m := NewModel()
		m.state = StateProcessing
		updated, _ := m.Update(FileBrowserRequestMsg{StartPath: dir})
		browser := updated.(Model)
		updated, _ = browser.Update(ResponseDoneMsg{})
		browser = updated.(Model)
		if browser.state != StateFileBrowser || browser.workspaceOverlayReturnState != StateInput {
			t.Fatalf("completion should keep browser but downgrade return: state=%v return=%v", browser.state, browser.workspaceOverlayReturnState)
		}
		updated, _ = browser.Update(FileBrowserActionMsg{Action: FileBrowserActionClose})
		if got := updated.(Model).state; got != StateInput {
			t.Fatalf("close after completion state=%v, want input", got)
		}
	})
}

func TestFileBrowserFooterDescribesComposerOutcome(t *testing.T) {
	m := NewFileBrowserModel(DefaultStyles())
	m.SetSize(90, 20)
	m.currentDir = t.TempDir()
	m.entries = []FileEntry{{Name: "main.go", Path: "/tmp/main.go"}}
	m.selectedFiles = map[string]bool{"/tmp/main.go": true}
	m.updateViewport()

	view := renderToPlain(m.View())
	for _, want := range []string{"Enter Add to draft", "y: Add selection to draft"} {
		if !strings.Contains(view, want) {
			t.Fatalf("file browser footer missing %q:\n%s", want, view)
		}
	}
}
