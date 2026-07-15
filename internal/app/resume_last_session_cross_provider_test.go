package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/config"

	"google.golang.org/genai"
)

// saveFixtureSessionForResume persists a session with the given provider tag
// under workDir via a real *chat.HistoryManager (backed by the test's
// XDG_DATA_HOME temp dir — never touches the real user's session store).
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
	t.Setenv("XDG_DATA_HOME", t.TempDir())
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
	t.Setenv("XDG_DATA_HOME", t.TempDir())
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

func TestResumeSession_ExactIDRestoresWithoutFallback(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir := t.TempDir()
	saveFixtureSessionForResume(t, "deepseek", workDir)

	session := chat.NewSession()
	session.SetWorkDir(workDir)
	sm, err := chat.NewSessionManager(session, chat.DefaultSessionManagerConfig())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	a := &App{
		config:         &config.Config{API: config.APIConfig{ActiveProvider: "deepseek"}, Model: config.ModelConfig{Provider: "deepseek"}},
		session:        session,
		sessionManager: sm,
	}

	if err := a.ResumeSession("fixture-session"); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if got := session.GetID(); got != "fixture-session" {
		t.Fatalf("active session ID = %q, want exact resumed ID", got)
	}
	if len(session.GetHistory()) != 1 || !a.sessionPreloaded {
		t.Fatalf("exact state was not restored: history=%d preloaded=%v", len(session.GetHistory()), a.sessionPreloaded)
	}
}

func TestResumeSession_ExactFailuresNeverMutateActiveSession(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, dataDir, activeWorkDir string)
		id      string
		want    string
	}{
		{
			name: "missing",
			id:   "missing-session",
			want: "failed to load",
		},
		{
			name: "corrupt",
			id:   "corrupt-session",
			want: "corrupt-session",
			prepare: func(t *testing.T, dataDir, _ string) {
				dir := filepath.Join(dataDir, "gokin", "sessions")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "corrupt-session.json"), []byte("{not-json"), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "foreign project",
			id:   "fixture-session",
			want: "different work directory",
			prepare: func(t *testing.T, _ string, _ string) {
				saveFixtureSessionForResume(t, "deepseek", t.TempDir())
			},
		},
		{
			name: "provider mismatch",
			id:   "fixture-session",
			want: "kimi",
			prepare: func(t *testing.T, _ string, activeWorkDir string) {
				saveFixtureSessionForResume(t, "kimi", activeWorkDir)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			t.Setenv("XDG_DATA_HOME", dataDir)
			activeWorkDir := t.TempDir()
			if tt.prepare != nil {
				tt.prepare(t, dataDir, activeWorkDir)
			}

			session := chat.NewSession()
			originalID := session.GetID()
			session.SetWorkDir(activeWorkDir)
			sm, err := chat.NewSessionManager(session, chat.DefaultSessionManagerConfig())
			if err != nil {
				t.Fatalf("NewSessionManager: %v", err)
			}
			a := &App{
				config:         &config.Config{API: config.APIConfig{ActiveProvider: "deepseek"}, Model: config.ModelConfig{Provider: "deepseek"}},
				session:        session,
				sessionManager: sm,
			}

			err = a.ResumeSession(tt.id)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ResumeSession error = %v, want %q", err, tt.want)
			}
			if session.GetID() != originalID || len(session.GetHistory()) != 0 || a.sessionPreloaded {
				t.Fatalf("failed exact resume mutated active state: id=%q history=%d preloaded=%v",
					session.GetID(), len(session.GetHistory()), a.sessionPreloaded)
			}
		})
	}
}
