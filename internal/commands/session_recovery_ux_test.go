package commands

import (
	"context"
	"errors"
	"os"
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

func TestNamedSaveSnapshotsWithoutChangingActiveIdentity(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	hm, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	session := chat.NewSession()
	session.SetID("active-session")
	session.AddUserMessage("unsaved active history")
	session.AddPendingRecovery(chat.SerializedPendingRecovery{
		ID:        "active-recovery",
		SessionID: "active-session",
		Message:   "resume source mutation",
		State:     chat.PendingRecoveryScheduled,
	}, "")
	app := &resumeFakeApp{
		fakeAppForMCP: &fakeAppForMCP{workDir: t.TempDir()},
		session:       session,
		hm:            hm,
	}

	message, err := (&SaveCommand{}).Execute(context.Background(), []string{"named-copy"}, app)
	if err != nil {
		t.Fatalf("SaveCommand.Execute: %v", err)
	}
	if !strings.Contains(message, "named-copy") {
		t.Fatalf("save message = %q", message)
	}
	if got := session.GetID(); got != "active-session" {
		t.Fatalf("named save changed active identity to %q", got)
	}
	state, err := hm.LoadFull("named-copy")
	if err != nil {
		t.Fatalf("LoadFull named snapshot: %v", err)
	}
	if state.ID != "named-copy" || len(state.History) != 1 {
		t.Fatalf("named snapshot = %+v", state)
	}
	if len(state.PendingRecoveries) != 0 {
		t.Fatalf("named clone inherited source recovery: %+v", state.PendingRecoveries)
	}
	if got := session.GetPendingRecoveries(); len(got) != 1 || got[0].ID != "active-recovery" {
		t.Fatalf("named save changed source recovery: %+v", got)
	}
	lease, err := chat.AcquireSessionWriterLease("named-copy")
	if err != nil {
		t.Fatalf("named save leaked target lease: %v", err)
	}
	_ = lease.Release()
}

func TestNamedSaveRefusesBusyTargetEvenWithForce(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	hm, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	held, err := chat.AcquireSessionWriterLease("busy-target")
	if err != nil {
		t.Fatalf("AcquireSessionWriterLease: %v", err)
	}
	defer held.Release()

	session := chat.NewSession()
	session.SetID("active-session")
	session.AddUserMessage("must not overwrite another writer")
	app := &resumeFakeApp{
		fakeAppForMCP: &fakeAppForMCP{workDir: t.TempDir()},
		session:       session,
		hm:            hm,
	}
	message, err := (&SaveCommand{}).Execute(context.Background(), []string{"busy-target", "--force"}, app)
	if err != nil {
		t.Fatalf("SaveCommand.Execute: %v", err)
	}
	if !strings.Contains(message, "open in another") || !strings.Contains(message, "Nothing was overwritten") {
		t.Fatalf("busy save message = %q", message)
	}
	if _, loadErr := hm.LoadFull("busy-target"); !os.IsNotExist(loadErr) {
		t.Fatalf("busy target was written: %v", loadErr)
	}
	if duplicate, leaseErr := chat.AcquireSessionWriterLease("busy-target"); !errors.Is(leaseErr, chat.ErrSessionWriterLeaseBusy) {
		if leaseErr == nil {
			_ = duplicate.Release()
		}
		t.Fatalf("busy lease changed: %v", leaseErr)
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
