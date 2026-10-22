package ui

import (
	"strings"
	"testing"
)

func TestRestoredConversationReplacesHistoryAndPreservesReaderDraft(t *testing.T) {
	m := claudeComposer(t)
	m.output.SetMarkdownRendering(false)
	m.output.AppendLine("old session secret")
	m.lastResponseText = "old answer"
	m.recordTranscriptTool(ToolResultMsg{Name: "old-tool", Content: "old tool output"}, "old context")
	m.input.textarea.SetValue("next draft")
	m.openTranscript()
	updated, _ := m.Update(SessionConversationMsg{Entries: []SessionConversationEntry{
		{Role: "user", Text: "Saved question"},
		{Tool: &ToolResultMsg{Name: "read", Args: map[string]any{"file_path": "sum.go"}, Content: "saved tool contents"}},
		{Role: "model", Text: "Saved answer"},
		{Role: "revision", Text: "Corrected saved answer"},
	}})
	m = updated.(Model)
	visible := stripAnsi(m.output.Content())
	if strings.Contains(visible, "old session") || !strings.Contains(visible, "Saved question") || !strings.Contains(visible, "Updated result") {
		t.Fatalf("wrong conversation after restore: %s", visible)
	}
	if m.state != StateTranscript || m.input.Value() != "next draft" || m.lastResponseText != "Corrected saved answer" {
		t.Fatalf("reader/draft/copy changed: %v %q %q", m.state, m.input.Value(), m.lastResponseText)
	}
	if !strings.Contains(m.transcript.conversation, "Saved answer") || strings.Contains(m.transcript.tools, "old tool output") || !strings.Contains(m.transcript.tools, "saved tool contents") {
		t.Fatalf("reader retained stale history: %+v", m.transcript)
	}
}

func TestRestoredConversationCannotInjectTerminalControlsOrJoinFences(t *testing.T) {
	m := claudeComposer(t)
	m.RestoreSessionConversation(SessionConversationMsg{Entries: []SessionConversationEntry{
		{Role: "model", Text: "```go\nunfinished"},
		{Role: "user", Text: "NEXT_USER\x1b]0;forged-title\x07"},
		{Role: "model", Text: "FINAL_ANSWER"},
	}})
	if strings.Contains(m.output.Content(), "\x1b]0;") || !strings.Contains(stripAnsi(m.output.Content()), "NEXT_USER") || m.lastResponseText != "FINAL_ANSWER" {
		t.Fatal("restored content injected a control or lost a later message")
	}
}
