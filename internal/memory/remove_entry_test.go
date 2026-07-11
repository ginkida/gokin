package memory

import (
	"fmt"
	"strings"
	"testing"
)

// RemoveEntry is the correction half of memory maintenance: a wrong or stale
// memorized entry otherwise pollutes every future session's prompt forever.
func TestProjectLearning_RemoveEntry(t *testing.T) {
	pl, err := NewProjectLearning(t.TempDir())
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}

	pl.SetPreference("style", "tabs")
	pl.SetPreference("fact:test_command", "go test ./...")
	pl.LearnPattern("retry-wrapper", "wrap API calls in retry", nil, nil)

	if !pl.RemoveEntry("style") {
		t.Fatal("bare preference should be removable")
	}
	if pl.GetPreference("style") != "" {
		t.Fatal("preference survived removal")
	}

	// memorize namespaces facts/conventions under a "fact:"/"convention:"
	// prefix — RemoveEntry must reach them by the BARE key the model used.
	if !pl.RemoveEntry("test_command") {
		t.Fatal("fact:-prefixed entry should be removable by bare key")
	}
	if pl.GetPreference("fact:test_command") != "" {
		t.Fatal("prefixed fact survived removal")
	}

	if !pl.RemoveEntry("retry-wrapper") {
		t.Fatal("pattern should be removable by name")
	}
	if strings.Contains(pl.FormatForPrompt(), "retry-wrapper") {
		t.Fatal("pattern survived removal in the prompt render")
	}

	if pl.RemoveEntry("never-existed") {
		t.Fatal("removing a missing entry must report false")
	}
}

// FormatForPrompt must stay bounded as the store grows — every entry sits in
// EVERY session's system prompt, and the agent is now coached to memorize
// proactively. Overflow is disclosed, never silently hidden.
func TestProjectLearning_FormatForPromptBoundsSections(t *testing.T) {
	pl, err := NewProjectLearning(t.TempDir())
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	const extra = 5
	for i := 0; i < maxPromptEntriesPerSection+extra; i++ {
		pl.SetPreference(fmt.Sprintf("fact:key-%02d", i), "value")
	}

	out := pl.FormatForPrompt()
	if got := strings.Count(out, "- **fact"); got != 0 {
		t.Fatalf("facts should render under the Facts section without the prefix, got %d prefixed keys:\n%s", got, out)
	}
	rendered := strings.Count(out, "- **key-")
	if rendered != maxPromptEntriesPerSection {
		t.Fatalf("rendered %d entries, want the %d cap:\n%s", rendered, maxPromptEntriesPerSection, out)
	}
	if !strings.Contains(out, fmt.Sprintf("and %d more", extra)) {
		t.Fatalf("overflow must be disclosed:\n%s", out)
	}

	// Under the cap: everything renders, no overflow line.
	pl2, err := NewProjectLearning(t.TempDir())
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}
	pl2.SetPreference("fact:only-one", "v")
	if out2 := pl2.FormatForPrompt(); strings.Contains(out2, "more (see") {
		t.Fatalf("no overflow line expected under the cap:\n%s", out2)
	}
}
