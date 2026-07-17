package ui

import (
	"strings"
	"testing"
	"time"
)

// The end-of-turn recap (v0.100.91, the Claude-Code "※ recap" idiom): after a
// tool-using turn with a hidden todos panel, one dim line anchors progress +
// the next step. All calm-UI gates verified here.
func TestTurnRecap_EmitsProgressAndNextStep(t *testing.T) {
	m := NewModel()
	m.responseToolCount = 3
	m.todoItems = []string{"● wire the adapter", "◐ add regression tests", "○ update docs"}

	recap := stripAnsi(m.buildTurnRecap())
	if !strings.Contains(recap, "1/3 tasks") || !strings.Contains(recap, "next: add regression tests") {
		t.Fatalf("recap = %q, want progress + in-progress item as next", recap)
	}
	if !strings.Contains(recap, "ctrl+t") {
		t.Fatalf("recap = %q, want the hidden-panel pointer", recap)
	}

	// Dedup latch: identical state emits nothing on the next turn…
	if again := m.buildTurnRecap(); again != "" {
		t.Fatalf("identical recap re-emitted: %q", again)
	}
	// …but a state change re-arms it.
	m.todoItems = []string{"● wire the adapter", "● add regression tests", "◐ update docs"}
	if changed := stripAnsi(m.buildTurnRecap()); !strings.Contains(changed, "2/3 tasks") ||
		!strings.Contains(changed, "next: update docs") {
		t.Fatalf("changed recap = %q, want refreshed progress", changed)
	}
}

func TestTurnRecap_AllDoneAndCalmGates(t *testing.T) {
	m := NewModel()
	m.responseToolCount = 2
	m.todoItems = []string{"● a", "● b"}
	if recap := stripAnsi(m.buildTurnRecap()); !strings.Contains(recap, "all 2 tasks done") {
		t.Fatalf("completion recap = %q", recap)
	}

	// Visible panel → silent (the same info is already on screen).
	m2 := NewModel()
	m2.responseToolCount = 2
	m2.todosVisible = true
	m2.todoItems = []string{"◐ a", "○ b"}
	if recap := m2.buildTurnRecap(); recap != "" {
		t.Fatalf("recap emitted with the todos panel visible: %q", recap)
	}

	// Conversational turn (no tools) → silent.
	m3 := NewModel()
	m3.todoItems = []string{"◐ a", "○ b"}
	if recap := m3.buildTurnRecap(); recap != "" {
		t.Fatalf("recap emitted for a tool-less turn: %q", recap)
	}

	// Single-item list → silent (the answer itself covers it).
	m4 := NewModel()
	m4.responseToolCount = 1
	m4.todoItems = []string{"◐ only task"}
	if recap := m4.buildTurnRecap(); recap != "" {
		t.Fatalf("recap emitted for a single-item list: %q", recap)
	}
}

// The extended burst line (v0.100.91): quiet searches and listings join
// read/bash in the aggregate header — the Claude-Code "searched for 2
// patterns" shape. Only body-less results reach the buffer (the caller's
// `body == ""` gate), so match-carrying greps still render individually.
func TestAggregateHeader_IncludesSearchesAndListings(t *testing.T) {
	entries := []aggToolEntry{
		{name: "read", target: "checker.go", duration: time.Millisecond},
		{name: "grep", target: "TODO", duration: time.Millisecond},
		{name: "glob", target: "**/*.go", duration: time.Millisecond},
		{name: "list_dir", target: "internal", duration: time.Millisecond},
		{name: "bash", target: "go vet ./...", duration: time.Millisecond},
	}
	header, _ := aggregateToolHeader(entries)
	want := "Read checker.go, searched 2 patterns, listed 1 directory, ran 1 shell command"
	if header != want {
		t.Fatalf("header = %q, want %q", header, want)
	}

	// Search-only burst capitalizes its first verb.
	searchOnly := []aggToolEntry{
		{name: "grep", target: "a"},
		{name: "grep", target: "b"},
	}
	header, _ = aggregateToolHeader(searchOnly)
	if header != "Searched 2 patterns" {
		t.Fatalf("search-only header = %q", header)
	}
}

func TestAggregatableToolLine_CoversQuietInspectionTools(t *testing.T) {
	for _, tool := range []string{"read", "bash", "grep", "glob", "list_dir"} {
		if !aggregatableToolLine(tool) {
			t.Errorf("%q must be aggregatable (empty-body results only)", tool)
		}
	}
	for _, tool := range []string{"edit", "write", "todo", "web_fetch"} {
		if aggregatableToolLine(tool) {
			t.Errorf("%q must never aggregate — its body is the signal", tool)
		}
	}
}
