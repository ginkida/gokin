package app

import (
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/config"
)

func TestRemovedProviderSessionRefusedAsCrossProvider(t *testing.T) {
	for _, provider := range []string{"kimi", "minimax"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			workDir := t.TempDir()
			saveFixtureSessionForResume(t, provider, workDir)
			session := chat.NewSession()
			session.WorkDir = workDir
			manager, err := chat.NewSessionManager(session, chat.DefaultSessionManagerConfig())
			if err != nil {
				t.Fatal(err)
			}
			app := &App{config: config.DefaultConfig(), session: session, sessionManager: manager}
			err = app.ResumeLastSession()
			if err == nil || !strings.Contains(err.Error(), provider) || !strings.Contains(err.Error(), "glm") {
				t.Fatalf("restore error=%v", err)
			}
			if app.sessionPreloaded || len(session.GetHistory()) != 0 {
				t.Fatal("foreign session was adopted")
			}
		})
	}
}
