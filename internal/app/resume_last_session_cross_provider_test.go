package app

import (
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/config"

	"google.golang.org/genai"
)

// saveFixtureSessionForResume persists a session with the given provider tag
// under workDir via a real *chat.HistoryManager (backed by the XDG_CONFIG_HOME
// this package's TestMain redirects to a temp dir — never touches the real
// user's session store).
func saveFixtureSessionForResume(t *testing.T, provider, workDir string) {
	t.Helper()
	hm, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	s := chat.NewSession()
	s.ID = "fixture-session"
	s.WorkDir = workDir
	s.SetProvider(provider)
	s.SetHistory([]*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)})
	if err := hm.SaveFull(s); err != nil {
		t.Fatalf("SaveFull: %v", err)
	}
}

// TestResumeLastSession_RefusesCrossProviderRestore (round 4) pins the fix:
// the --resume CLI flag (both interactive and headless route through
// ResumeLastSession) must refuse to silently restore a session authored
// under a DIFFERENT provider than the current one. Before the fix,
// ResumeLastSession had NO provider check at all — unlike Run()'s automatic
// startup auto-resume path — and worse, it sets a.sessionPreloaded=true,
// which makes Run()'s OWN guard a structural no-op
// (`if a.sessionPreloaded { sessionRestored = true }`). So this path was the
// ONLY one that could ever catch a cross-provider `--resume`.
func TestResumeLastSession_RefusesCrossProviderRestore(t *testing.T) {
	workDir := t.TempDir()
	saveFixtureSessionForResume(t, "kimi", workDir)

	session := chat.NewSession()
	session.WorkDir = workDir
	sm, err := chat.NewSessionManager(session, chat.DefaultSessionManagerConfig())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	a := &App{
		config:         &config.Config{API: config.APIConfig{ActiveProvider: "deepseek"}, Model: config.ModelConfig{Provider: "deepseek"}},
		session:        session,
		sessionManager: sm,
	}

	err = a.ResumeLastSession()
	if err == nil {
		t.Fatal("expected ResumeLastSession to refuse a cross-provider restore, got nil error")
	}
	if !strings.Contains(err.Error(), "kimi") || !strings.Contains(err.Error(), "deepseek") {
		t.Fatalf("error = %q, want it to name both providers", err.Error())
	}
	if len(session.GetHistory()) != 0 {
		t.Fatal("the session's history must NOT be restored on a provider mismatch")
	}
	if a.sessionPreloaded {
		t.Fatal("sessionPreloaded must NOT be set on a refused restore — it would make Run()'s own auto-resume guard a no-op for a session that was never actually restored")
	}
}

// TestResumeLastSession_SameProviderRestoresNormally is the happy-path
// regression check — the new guard must not block a same-provider --resume.
func TestResumeLastSession_SameProviderRestoresNormally(t *testing.T) {
	workDir := t.TempDir()
	saveFixtureSessionForResume(t, "deepseek", workDir)

	session := chat.NewSession()
	session.WorkDir = workDir
	sm, err := chat.NewSessionManager(session, chat.DefaultSessionManagerConfig())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	a := &App{
		config:         &config.Config{API: config.APIConfig{ActiveProvider: "deepseek"}, Model: config.ModelConfig{Provider: "deepseek"}},
		session:        session,
		sessionManager: sm,
	}

	if err := a.ResumeLastSession(); err != nil {
		t.Fatalf("ResumeLastSession: %v", err)
	}
	if len(session.GetHistory()) != 1 {
		t.Fatalf("expected the session to be restored, got %d messages", len(session.GetHistory()))
	}
	if !a.sessionPreloaded {
		t.Fatal("expected sessionPreloaded to be set after a successful restore")
	}
}
