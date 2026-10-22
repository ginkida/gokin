package commands

import (
	"context"
	"testing"

	"gokin/internal/config"
)

type logoutSessionEndApp struct {
	*fakeAppForAuth
	ended []string
}

func (a *logoutSessionEndApp) EndSessionHooks(_ context.Context, reason string) {
	a.ended = append(a.ended, reason)
}
func (a *logoutSessionEndApp) ClearConversationForSessionEnd(_ context.Context, reason string) error {
	if err := a.ClearConversationChecked(); err != nil {
		return err
	}
	a.ended = append(a.ended, reason)
	return nil
}

func TestLogoutSessionEndTargetsOnlyTheActiveProvider(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, mode := range []string{"only-active", "auto-switch", "other-provider"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{API: config.APIConfig{ActiveProvider: "glm", GLMKey: "fixture-active-key-0123456789"}, Model: config.ModelConfig{Provider: "glm", Name: "glm-5.3"}}
			args := []string{"glm"}
			if mode != "only-active" {
				cfg.API.DeepSeekKey = "fixture-other-key-0123456789"
			}
			if mode == "other-provider" {
				args = []string{"deepseek"}
			}
			a := &logoutSessionEndApp{fakeAppForAuth: newAuthApp(cfg)}
			if _, err := (&LogoutCommand{}).Execute(t.Context(), args, a); err != nil {
				t.Fatal(err)
			}
			if mode == "other-provider" {
				if len(a.ended) != 0 {
					t.Fatal("logout of another provider ended active session", a.ended)
				}
			} else if len(a.ended) != 1 || a.ended[0] != "logout" {
				t.Fatal("active logout lost or duplicated terminal event", a.ended)
			}
		})
	}
}
