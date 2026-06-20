package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtFileWord(t *testing.T) {
	cases := []struct {
		in   string
		word string
		ok   bool
	}{
		{"@main", "@main", true},
		{"look at @src/main.go", "@src/main.go", true},
		{"@main ", "", false},          // trailing space — word finished
		{"/clear", "", false},          // command, not @
		{"plain text", "", false},      // no @
		{"email user@host", "", false}, // @ not at token start
		{"@@x", "", false},             // double @
		{"@", "@", true},               // bare @ (shows top files)
		{"", "", false},
	}
	for _, c := range cases {
		w, ok := atFileWord(c.in)
		if ok != c.ok || (ok && w != c.word) {
			t.Errorf("atFileWord(%q) = (%q,%v), want (%q,%v)", c.in, w, ok, c.word, c.ok)
		}
	}
}

func TestUpdateAtFileSuggestions(t *testing.T) {
	work := t.TempDir()
	for _, f := range []string{"main.go", "main_test.go", "other.go"} {
		if err := os.WriteFile(filepath.Join(work, f), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	m := NewInputModel(nil, work)
	m.updateAtFileSuggestions("@main")
	if !m.showSuggestions {
		t.Fatal("@main should show file suggestions")
	}
	if len(m.fileSuggestions) < 2 {
		t.Errorf("@main should match main.go + main_test.go, got %v", m.fileSuggestions)
	}
	if m.suggestionType != SuggestionAtFile {
		// updateAtFileSuggestions doesn't set the type itself (the dispatch does);
		// but confirm it populated suggestions for the @ prefix.
	}
}

func TestAcceptFileSuggestion_AtMode(t *testing.T) {
	work := t.TempDir()
	m := NewInputModel(nil, work)

	// @-mode: the last word starts with @ -> the buffer keeps the @.
	m.textarea.SetValue("look at @mai")
	m.acceptFileSuggestion("main.go")
	if got := m.textarea.Value(); got != "look at @main.go " {
		t.Errorf("@-accept should produce '@main.go', got %q", got)
	}

	// plain mode: no @ -> bare path (e.g. for /open).
	m2 := NewInputModel(nil, work)
	m2.textarea.SetValue("open mai")
	m2.acceptFileSuggestion("main.go")
	if got := m2.textarea.Value(); got != "open main.go " {
		t.Errorf("plain accept should produce bare path, got %q", got)
	}
}

func TestAtFileDropdownRenders(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	m := NewInputModel(DefaultStyles(), work)
	m.textarea.SetValue("@main")
	m.suggestionType = SuggestionAtFile
	m.updateAtFileSuggestions("@main")
	if !m.showSuggestions || len(m.fileSuggestions) == 0 {
		t.Fatal("@main should populate file suggestions")
	}
	out := renderToPlain(m.View())
	if !strings.Contains(out, "main.go") {
		t.Errorf("@ file dropdown must render the matching file 'main.go':\n%s", out)
	}
}
