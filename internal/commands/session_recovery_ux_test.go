package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/chat"
)

func TestFormatRecentSessionsExampleUsesFirstVisibleProjectSession(t *testing.T) {
	now := time.Now()
	sessions := []chat.SessionInfo{
		{ID: "other-project", WorkDir: "/work/other", Summary: "newest elsewhere", LastActive: now},
		{ID: "current-project", WorkDir: "/work/current", Summary: "visible choice", LastActive: now.Add(-time.Minute)},
	}

	got, shown := formatRecentSessions(sessions, "/work/current")
	if shown != 1 {
		t.Fatalf("shown = %d, want 1; output = %q", shown, got)
	}
	if strings.Contains(got, "other-project") {
		t.Fatalf("output includes a session from another project: %q", got)
	}
	if !strings.Contains(got, "Example: /resume current-project") {
		t.Fatalf("example does not use the first visible session: %q", got)
	}
}

func TestFormatRecentSessionsKeepsRowsTerminalSafe(t *testing.T) {
	sessions := []chat.SessionInfo{
		{ID: "bad\nforged", Summary: "must be skipped"},
		{ID: "safe-id", Summary: "topic\nFORGED\x1b[31m red"},
	}

	got, shown := formatRecentSessions(sessions, "")
	if shown != 1 {
		t.Fatalf("shown = %d, want 1; output = %q", shown, got)
	}
	plain := strings.NewReplacer(colorGreen, "", colorReset, "").Replace(got)
	if strings.ContainsAny(plain, "\r\x1b") || strings.Contains(plain, "topic\nFORGED") {
		t.Fatalf("persisted text injected terminal control or a forged row: %q", got)
	}
	if !strings.Contains(plain, "topic FORGED [31m red") {
		t.Fatalf("summary was not folded into one readable line: %q", got)
	}
}

func TestSessionIDValidationRejectsUncopyableAndEscapingNames(t *testing.T) {
	for _, id := range []string{"", ".", "..", "-option-like", "../outside", `folder\outside`, "ads:stream", "CON", "trailing.", "two words", "line\nbreak", strings.Repeat("x", maxSessionIDRunes+1)} {
		if validSessionID(id) {
			t.Errorf("validSessionID(%q) = true, want false", id)
		}
	}
	for _, id := range []string{"20260714-120000-a1b2c3", "release.v2", "checkpoint_два"} {
		if !validSessionID(id) {
			t.Errorf("validSessionID(%q) = false, want true", id)
		}
	}
}

func TestSaveAndResumeRejectUnsafeSessionIDsBeforeDiskAccess(t *testing.T) {
	hm, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	session := chat.NewSession()
	originalID := session.GetID()
	app := &resumeFakeApp{
		fakeAppForMCP: &fakeAppForMCP{workDir: t.TempDir()},
		session:       session,
		hm:            hm,
	}

	msg, err := (&SaveCommand{}).Execute(context.Background(), []string{"../outside"}, app)
	if err != nil {
		t.Fatalf("save Execute returned error: %v", err)
	}
	if !strings.Contains(msg, "Invalid session name") {
		t.Fatalf("save message = %q, want actionable validation error", msg)
	}
	if session.GetID() != originalID {
		t.Fatalf("invalid save changed session ID from %q to %q", originalID, session.GetID())
	}

	msg, err = (&ResumeCommand{}).Execute(context.Background(), []string{"../outside"}, app)
	if err != nil {
		t.Fatalf("resume Execute returned error: %v", err)
	}
	if !strings.Contains(msg, "Invalid session ID") || !strings.Contains(msg, "/resume (no args)") {
		t.Fatalf("resume message = %q, want recovery guidance", msg)
	}
}
