package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHandleSubAgentActivity_JournalsToolEvents pins the unification gain:
// sub-agent tool calls land in the execution journal with agent_id — the
// journal used to be completely blind to them (eval scoring saw zero tool
// events for routed/delegated runs).
func TestHandleSubAgentActivity_JournalsToolEvents(t *testing.T) {
	workDir := t.TempDir()
	journal, err := NewExecutionJournal(workDir)
	if err != nil {
		t.Fatalf("NewExecutionJournal: %v", err)
	}

	a := &App{journal: journal}

	a.handleSubAgentActivity("agent-1", "explore", "map the auth flow", "", nil, "start")
	a.handleSubAgentActivity("agent-1", "explore", "", "read", map[string]any{"file_path": "auth.go"}, "tool_start")
	a.handleSubAgentActivity("agent-1", "explore", "", "read", nil, "tool_end")
	a.handleSubAgentActivity("agent-1", "explore", "", "", nil, "complete")

	data, err := os.ReadFile(filepath.Join(workDir, ".gokin", "execution_journal.jsonl"))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}

	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("parse journal line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	if len(events) != 4 {
		t.Fatalf("journal events = %d, want 4:\n%s", len(events), data)
	}

	kind := func(i int) string { s, _ := events[i]["event"].(string); return s }
	details := func(i int) map[string]any { d, _ := events[i]["details"].(map[string]any); return d }

	if kind(0) != "agent_start" || details(0)["agent_type"] != "explore" {
		t.Fatalf("event 0 = %v", events[0])
	}
	if kind(1) != "tool_start" || details(1)["tool"] != "read" || details(1)["agent_id"] != "agent-1" {
		t.Fatalf("tool_start must carry tool + agent_id: %v", events[1])
	}
	if kind(2) != "tool_end" || details(2)["agent_id"] != "agent-1" {
		t.Fatalf("tool_end must carry agent_id: %v", events[2])
	}
	if kind(3) != "agent_end" || details(3)["status"] != "complete" {
		t.Fatalf("event 3 = %v", events[3])
	}
}

// TestHandleSubAgentActivity_NilJournalAndProgramSafe: the unified sink must
// be inert when neither journal nor program exist (early lifecycle, tests).
func TestHandleSubAgentActivity_NilJournalAndProgramSafe(t *testing.T) {
	a := &App{}
	// Must not panic.
	a.handleSubAgentActivity("x", "general", "task", "read", nil, "tool_start")
	a.handleSubAgentActivity("x", "general", "", "", nil, "complete")
}

// capturePresenter records StreamText deliveries for assertions.
type capturePresenter struct {
	stdoutPresenter // embed for the no-op surface
	texts           []string
}

func (p *capturePresenter) StreamText(text string) { p.texts = append(p.texts, text) }

func TestDeliverUnstreamedResponse_RoutedTextReachesPresenter(t *testing.T) {
	cp := &capturePresenter{}
	a := &App{}
	a.setPresenter(cp)

	// Routed turn: nothing streamed, response came back as a string.
	a.deliverUnstreamedResponse("routed sub-agent answer")
	if len(cp.texts) != 1 || cp.texts[0] != "routed sub-agent answer" {
		t.Fatalf("texts = %v, want the routed answer delivered once", cp.texts)
	}

	// Idempotent: a second delivery attempt is a no-op.
	a.deliverUnstreamedResponse("routed sub-agent answer")
	if len(cp.texts) != 1 {
		t.Fatalf("second delivery must be a no-op, texts = %v", cp.texts)
	}
}

func TestDeliverUnstreamedResponse_StreamedTurnDoesNotDoublePrint(t *testing.T) {
	cp := &capturePresenter{}
	a := &App{}
	a.setPresenter(cp)

	// Direct turn: OnText already streamed (simulated by the counter).
	a.streamedChars = 42
	a.deliverUnstreamedResponse("already streamed answer")
	if len(cp.texts) != 0 {
		t.Fatalf("streamed turn must not re-deliver, texts = %v", cp.texts)
	}

	// Empty/whitespace responses never deliver.
	a.streamedChars = 0
	a.deliverUnstreamedResponse("   ")
	if len(cp.texts) != 0 {
		t.Fatalf("whitespace response must not deliver, texts = %v", cp.texts)
	}
}
