package app

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/commands"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/permission"
	"gokin/internal/testkit"
)

func TestLogoutOnlyProviderRemovesCredentialFromAuthoritativeConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.API.ActiveProvider = "glm"
	cfg.API.Backend = "glm"
	cfg.API.GLMKey = "test-key-that-must-not-return-1234567890"
	cfg.Model.Provider = "glm"
	cfg.Model.Name = "glm-5.2"
	mockClient := testkit.NewMockClient().WithModel("glm-5.2")
	contextManager := appcontext.NewContextManager(
		context.Background(), chat.NewSession(), mockClient, &cfg.Context,
	)
	app := &App{
		config:         cfg,
		ctx:            context.Background(),
		client:         mockClient,
		contextManager: contextManager,
		permManager:    permission.NewManager(nil, true),
	}

	out, err := (&commands.LogoutCommand{}).Execute(context.Background(), []string{"glm"}, app)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(out, "API key removed") {
		t.Fatalf("logout did not report removal: %q", out)
	}
	if key := app.GetConfig().API.GLMKey; key != "" {
		t.Fatalf("logout reported success but authoritative config retained key %q", key)
	}

	// Any later config save must not resurrect the credential from App.config.
	app.TogglePermissions()
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload config after later toggle: %v", err)
	}
	if key := reloaded.API.GLMKey; key != "" {
		t.Fatalf("later config commit resurrected logged-out credential %q", key)
	}
}
