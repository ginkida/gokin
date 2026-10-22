package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// SessionConversationMsg replaces the visible conversation after a successful
// session switch. It is presentation only: it never replays tools or submits
// the restored messages to the model.
type SessionConversationMsg struct {
	Entries []SessionConversationEntry
}

type SessionConversationEntry struct {
	Role string
	Text string
	Tool *ToolResultMsg
}

func (m *Model) RestoreSessionConversation(msg SessionConversationMsg) {
	state, returnState := m.state, m.transientOverlayReturnState
	m.ClearOutput()
	m.discardPartialResponse()
	m.lastResponseText = ""
	m.lastResponseWasPartial = false
	var conversation strings.Builder
	conversation.WriteString(lipgloss.NewStyle().Foreground(ColorInfo).Render("Restored conversation") + "\n\n")
	appendMarkdown := func(text string) {
		if m.output.markdownRendering && m.output.renderer != nil {
			if rendered, err := m.output.renderer.Render(text); err == nil {
				text = rendered
			}
		}
		conversation.WriteString(text + "\n\n")
	}
	for _, entry := range msg.Entries {
		if entry.Tool != nil {
			tool := *entry.Tool
			context := formatArgsSummary(tool.Args)
			m.recordTranscriptTool(tool, context)
			outcome := "restored"
			if tool.Failed {
				outcome = "failed · restored"
			} else {
				m.rememberToolOutput(tool.Name, tool.Content)
			}
			fmt.Fprintf(&conversation, "%s%s · %s\n\n", safeTerminalDisplayText(tool.Name), safeTerminalDisplayText(context), outcome)
			continue
		}
		text := safeTerminalDisplayText(entry.Text)
		if strings.TrimSpace(text) == "" {
			continue
		}
		switch entry.Role {
		case "user":
			conversation.WriteString(m.styles.FormatUserMessage(text) + "\n\n")
		case "model", "revision":
			if entry.Role == "revision" {
				conversation.WriteString("Updated result\n\n")
			}
			// Render each message separately: an unfinished Markdown fence in
			// one old reply must not consume the next user's message.
			appendMarkdown(text)
			m.lastResponseText = text
		}
	}
	// Commit once, avoiding a full viewport rebuild for every historical turn.
	m.output.AppendText(conversation.String())
	// A session switch can finish while its user is reading the transcript.
	// Keep the reader open, but discard the previous session's search snapshot.
	if state == StateTranscript {
		m.state, m.transientOverlayReturnState = state, returnState
		m.openTranscript()
		m.transientOverlayReturnState = returnState
	}
}
