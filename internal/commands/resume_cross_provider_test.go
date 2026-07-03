package commands

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/config"

	"google.golang.org/genai"
)

// resumeFakeApp wraps fakeAppForMCP to also serve a real *chat.Session /
// *chat.HistoryManager — ResumeCommand.Execute needs both (unlike the MCP
// tests, which stub them out entirely).
type resumeFakeApp struct {
	*fakeAppForMCP
	session *chat.Session
	hm      *chat.HistoryManager
}

func (f *resumeFakeApp) GetSession() *chat.Session                        { return f.session }
func (f *resumeFakeApp) GetHistoryManager() (*chat.HistoryManager, error) { return f.hm, nil }

// saveFixtureSession persists a session under the given ID/provider/workdir
// with one message, returning the HistoryManager it was saved through
// (backed by the XDG_CONFIG_HOME the package TestMain redirects to a temp
// dir — never touches the real user's session store).
func saveFixtureSession(t *testing.T, id, provider, workDir string) *chat.HistoryManager {
	t.Helper()
	hm, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	s := chat.NewSession()
	s.ID = id
	s.WorkDir = workDir
	s.SetProvider(provider)
	s.SetHistory([]*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)})
	if err := hm.SaveFull(s); err != nil {
		t.Fatalf("SaveFull: %v", err)
	}
	return hm
}

// TestResumeCommand_RefusesCrossProviderRestore (round 4) pins the fix: /resume
// must refuse to restore a session authored under a DIFFERENT provider than
// the current active one — the automatic startup auto-resume path already had
// this guard (app.go Run()), but /resume itself never checked it, silently
// restoring history whose thinking-signature/tool_use-ID/cache_control wire
// format doesn't round-trip across providers (see CLAUDE.md's Cross-provider
// history rules).
func TestResumeCommand_RefusesCrossProviderRestore(t *testing.T) {
	workDir := t.TempDir()
	hm := saveFixtureSession(t, "old-session", "kimi", workDir)

	currentSession := chat.NewSession()
	currentSession.WorkDir = workDir
	app := &resumeFakeApp{
		fakeAppForMCP: &fakeAppForMCP{
			cfg:     &config.Config{API: config.APIConfig{ActiveProvider: "deepseek"}, Model: config.ModelConfig{Provider: "deepseek"}},
			workDir: workDir,
		},
		session: currentSession,
		hm:      hm,
	}

	msg, err := (&ResumeCommand{}).Execute(context.Background(), []string{"old-session"}, app)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(msg, "kimi") || !strings.Contains(msg, "deepseek") {
		t.Fatalf("message = %q, want it to name both providers", msg)
	}
	if !strings.Contains(msg, "--force") {
		t.Fatalf("message = %q, want it to mention the --force escape hatch", msg)
	}
	if len(currentSession.GetHistory()) != 0 {
		t.Fatal("the current session's history must NOT be restored on a provider mismatch")
	}
}

// TestResumeCommand_ForceBypassesCrossProviderGuard mirrors the existing
// workdir-mismatch --force escape hatch — a conscious override must still work.
func TestResumeCommand_ForceBypassesCrossProviderGuard(t *testing.T) {
	workDir := t.TempDir()
	hm := saveFixtureSession(t, "old-session", "kimi", workDir)

	currentSession := chat.NewSession()
	currentSession.WorkDir = workDir
	app := &resumeFakeApp{
		fakeAppForMCP: &fakeAppForMCP{
			cfg:     &config.Config{API: config.APIConfig{ActiveProvider: "deepseek"}, Model: config.ModelConfig{Provider: "deepseek"}},
			workDir: workDir,
		},
		session: currentSession,
		hm:      hm,
	}

	msg, err := (&ResumeCommand{}).Execute(context.Background(), []string{"old-session", "--force"}, app)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(msg, "restored") {
		t.Fatalf("message = %q, want a restored confirmation", msg)
	}
	if len(currentSession.GetHistory()) != 1 {
		t.Fatalf("expected the session to actually be restored with --force, got %d messages", len(currentSession.GetHistory()))
	}
}

// TestResumeCommand_SameProviderRestoresNormally is the happy-path regression
// check — the new guard must not block a same-provider resume.
func TestResumeCommand_SameProviderRestoresNormally(t *testing.T) {
	workDir := t.TempDir()
	hm := saveFixtureSession(t, "old-session", "deepseek", workDir)

	currentSession := chat.NewSession()
	currentSession.WorkDir = workDir
	app := &resumeFakeApp{
		fakeAppForMCP: &fakeAppForMCP{
			cfg:     &config.Config{API: config.APIConfig{ActiveProvider: "deepseek"}, Model: config.ModelConfig{Provider: "deepseek"}},
			workDir: workDir,
		},
		session: currentSession,
		hm:      hm,
	}

	msg, err := (&ResumeCommand{}).Execute(context.Background(), []string{"old-session"}, app)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(msg, "restored") {
		t.Fatalf("message = %q, want a restored confirmation", msg)
	}
	if len(currentSession.GetHistory()) != 1 {
		t.Fatalf("expected the session to be restored, got %d messages", len(currentSession.GetHistory()))
	}
}

// TestResumeCommand_LegacySessionWithNoProviderTagRestoresNormally: a session
// saved before the provider tag existed (empty Provider) must be treated as
// compatible, not refused.
func TestResumeCommand_LegacySessionWithNoProviderTagRestoresNormally(t *testing.T) {
	workDir := t.TempDir()
	hm := saveFixtureSession(t, "old-session", "", workDir)

	currentSession := chat.NewSession()
	currentSession.WorkDir = workDir
	app := &resumeFakeApp{
		fakeAppForMCP: &fakeAppForMCP{
			cfg:     &config.Config{API: config.APIConfig{ActiveProvider: "deepseek"}, Model: config.ModelConfig{Provider: "deepseek"}},
			workDir: workDir,
		},
		session: currentSession,
		hm:      hm,
	}

	msg, err := (&ResumeCommand{}).Execute(context.Background(), []string{"old-session"}, app)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(msg, "restored") {
		t.Fatalf("message = %q, want a restored confirmation (legacy session with no provider tag must be treated as compatible)", msg)
	}
}
