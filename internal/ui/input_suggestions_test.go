package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// renderSuggestions must bound every line to the input width so long command
// descriptions / usage / file paths can't overflow a narrow terminal — the
// suggestion box has no fixed width and grows to its widest line.
func TestRenderSuggestions_CommandsTruncateToWidth(t *testing.T) {
	const term = 44
	m := NewInputModel(DefaultStyles(), "/tmp")
	m.SetWidth(term)
	m.suggestionType = SuggestionCommand
	m.suggestions = []CommandInfo{
		{
			Name:        "status",
			Description: strings.Repeat("a very long description that would overflow ", 6),
			Usage:       strings.Repeat("/status [a] [b] [c] [d] ", 6),
		},
		{Name: "x", Description: "short"},
	}
	m.suggestionIndex = 0

	assertNoLineExceeds(t, m.renderSuggestions(), term)
}

func TestRenderSuggestions_FilePathsTruncateToWidth(t *testing.T) {
	const term = 44
	m := NewInputModel(DefaultStyles(), "/tmp")
	m.SetWidth(term)
	m.suggestionType = SuggestionFile
	m.fileSuggestions = []string{
		"/tmp/" + strings.Repeat("deep/", 12) + "verylongfilename.go",
		"/tmp/short.go",
	}
	m.suggestionIndex = 0

	assertNoLineExceeds(t, m.renderSuggestions(), term)
}

func TestRenderSuggestionFootersMatchKeyboardBehavior(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*InputModel)
		want  []string
	}{
		{
			name: "command",
			setup: func(m *InputModel) {
				m.suggestionType = SuggestionCommand
				m.suggestions = []CommandInfo{{Name: "model", Description: "Switch AI model", Usage: "/model [name]"}}
			},
			want: []string{"Enter/Tab complete", "Esc close", "Usage: /model [name]"},
		},
		{
			name: "argument",
			setup: func(m *InputModel) {
				m.suggestionType = SuggestionArgument
				m.argSuggestions = []string{"--force"}
			},
			want: []string{"Tab complete", "Enter run as typed", "Esc close"},
		},
		{
			name: "file",
			setup: func(m *InputModel) {
				m.suggestionType = SuggestionFile
				m.fileSuggestions = []string{"/tmp/main.go"}
			},
			want: []string{"Enter/Tab insert path", "Esc close"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewInputModel(DefaultStyles(), "/tmp")
			m.SetWidth(100)
			tc.setup(&m)
			plain := stripAnsi(m.renderSuggestions())
			for _, want := range tc.want {
				if !strings.Contains(plain, want) {
					t.Fatalf("footer missing %q:\n%s", want, plain)
				}
			}
		})
	}
}

func TestRenderSuggestionFootersRetainEscapeOnNarrowWidth(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*InputModel)
	}{
		{
			name: "command",
			setup: func(m *InputModel) {
				m.suggestionType = SuggestionCommand
				m.suggestions = []CommandInfo{{Name: "save", Usage: "/save [name] [--force]"}}
			},
		},
		{
			name: "argument",
			setup: func(m *InputModel) {
				m.suggestionType = SuggestionArgument
				m.argSuggestions = []string{"--force"}
			},
		},
		{
			name: "file",
			setup: func(m *InputModel) {
				m.suggestionType = SuggestionFile
				m.fileSuggestions = []string{"/tmp/main.go"}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const width = 24
			m := NewInputModel(DefaultStyles(), "/tmp")
			m.SetWidth(width)
			tc.setup(&m)
			rendered := m.renderSuggestions()
			if !strings.Contains(stripAnsi(rendered), "Esc") {
				t.Fatalf("narrow footer hid dismissal key:\n%s", stripAnsi(rendered))
			}
			assertNoLineExceeds(t, rendered, width)
		})
	}
}

func TestShortComposerKeepsSelectedSuggestionAndActions(t *testing.T) {
	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 40, Height: 6})
	m.state = StateInput
	m.input.textarea.SetValue("/m")
	m.input.textarea.CursorEnd()
	m.input.suggestionType = SuggestionCommand
	m.input.suggestions = []CommandInfo{
		{Name: "memory", Description: "Show stored memories"},
		{Name: "model", Description: "Switch active model"},
		{Name: "mcp", Description: "Manage MCP servers"},
	}
	m.input.suggestionIndex = 1
	m.input.showSuggestions = true

	view := m.View()
	plain := stripAnsi(view)
	if got := lipgloss.Height(view); got != 6 {
		t.Fatalf("short suggestion frame height=%d, want 6:\n%s", got, plain)
	}
	for _, want := range []string{"/model", "Enter/Tab", "Esc"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("short suggestion view lost %q:\n%s", want, plain)
		}
	}
}

func TestRenderSuggestions_OverflowIndicatorsMatchPosition(t *testing.T) {
	types := []struct {
		name  string
		setup func(*InputModel)
	}{
		{
			name: "command",
			setup: func(m *InputModel) {
				m.suggestionType = SuggestionCommand
				for i := range 8 {
					m.suggestions = append(m.suggestions, CommandInfo{Name: fmt.Sprintf("command-%d", i)})
				}
			},
		},
		{
			name: "argument",
			setup: func(m *InputModel) {
				m.suggestionType = SuggestionArgument
				for i := range 8 {
					m.argSuggestions = append(m.argSuggestions, fmt.Sprintf("--option-%d", i))
				}
			},
		},
		{
			name: "file",
			setup: func(m *InputModel) {
				m.suggestionType = SuggestionFile
				for i := range 8 {
					m.fileSuggestions = append(m.fileSuggestions, fmt.Sprintf("/tmp/file-%d", i))
				}
			},
		},
	}
	positions := []struct {
		name      string
		index     int
		want      []string
		forbidden []string
	}{
		{name: "top", index: 0, want: []string{"↓ 2 more"}, forbidden: []string{"↑ "}},
		{name: "middle", index: 6, want: []string{"↑ 1 more", "↓ 1 more"}},
		{name: "bottom", index: 7, want: []string{"↑ 2 more"}, forbidden: []string{"↓ "}},
	}

	for _, suggestionType := range types {
		for _, position := range positions {
			t.Run(suggestionType.name+"/"+position.name, func(t *testing.T) {
				const width = 60
				m := NewInputModel(DefaultStyles(), "/tmp")
				m.SetWidth(width)
				suggestionType.setup(&m)
				m.suggestionIndex = position.index

				rendered := m.renderSuggestions()
				plain := stripAnsi(rendered)
				for _, want := range position.want {
					if !strings.Contains(plain, want) {
						t.Errorf("missing overflow indicator %q:\n%s", want, plain)
					}
				}
				for _, forbidden := range position.forbidden {
					if strings.Contains(plain, forbidden) {
						t.Errorf("unexpected overflow indicator %q:\n%s", forbidden, plain)
					}
				}
				assertNoLineExceeds(t, rendered, width)
			})
		}
	}
}

func TestSuggestionNavigationStopsAtOverflowBoundaries(t *testing.T) {
	types := []struct {
		name  string
		setup func(*InputModel)
	}{
		{
			name: "command",
			setup: func(m *InputModel) {
				m.suggestionType = SuggestionCommand
				for i := range 8 {
					m.suggestions = append(m.suggestions, CommandInfo{Name: fmt.Sprintf("command-%d", i)})
				}
			},
		},
		{
			name: "argument",
			setup: func(m *InputModel) {
				m.suggestionType = SuggestionArgument
				for i := range 8 {
					m.argSuggestions = append(m.argSuggestions, fmt.Sprintf("--option-%d", i))
				}
			},
		},
		{
			name: "file",
			setup: func(m *InputModel) {
				m.suggestionType = SuggestionFile
				for i := range 8 {
					m.fileSuggestions = append(m.fileSuggestions, fmt.Sprintf("/tmp/file-%d", i))
				}
			},
		},
	}

	for _, suggestionType := range types {
		t.Run(suggestionType.name, func(t *testing.T) {
			m := NewInputModel(DefaultStyles(), "/tmp")
			suggestionType.setup(&m)
			m.showSuggestions = true

			m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
			if m.suggestionIndex != 0 {
				t.Fatalf("Up moved selection past the first item: index=%d", m.suggestionIndex)
			}

			for range 10 {
				m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
			}
			if m.suggestionIndex != 7 {
				t.Fatalf("Down boundary index=%d, want 7", m.suggestionIndex)
			}

			for range 10 {
				m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
			}
			if m.suggestionIndex != 0 {
				t.Fatalf("Up boundary index=%d, want 0", m.suggestionIndex)
			}
		})
	}
}

func TestUpdateFileSuggestions_QuotedPrefix(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "space file.go"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewInputModel(DefaultStyles(), work)
	m.updateFileSuggestions(`/open "space`)
	if !m.showSuggestions {
		t.Fatal("quoted file prefix should show suggestions")
	}
	if len(m.fileSuggestions) != 1 || filepath.Base(m.fileSuggestions[0]) != "space file.go" {
		t.Fatalf("fileSuggestions = %v, want space file.go", m.fileSuggestions)
	}
	if m.ghostText != " file.go" {
		t.Fatalf("ghostText = %q, want %q", m.ghostText, " file.go")
	}
}

func TestUpdateGhostText_FileQuotedPrefix(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "space file.go")

	m := NewInputModel(DefaultStyles(), work)
	m.suggestionType = SuggestionFile
	m.fileSuggestions = []string{path}

	m.updateGhostText(`/open "space`)
	if m.ghostText != " file.go" {
		t.Fatalf("ghostText = %q, want %q", m.ghostText, " file.go")
	}
}

func assertNoLineExceeds(t *testing.T, rendered string, max int) {
	t.Helper()
	for _, line := range strings.Split(stripAnsi(rendered), "\n") {
		if w := lipgloss.Width(line); w > max {
			t.Errorf("suggestion line overflows width %d (%d cols): %q", max, w, line)
		}
	}
}
