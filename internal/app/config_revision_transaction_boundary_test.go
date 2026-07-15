package app

import (
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/permission"
	"gokin/internal/ui"
)

// A config revision is supposed to identify the snapshot committed by the
// operation that owns it. Commands currently stage changes through GetConfig,
// however, which exposes the live pointer before ApplyConfig/ApplyUIConfig can
// acquire a.mu. An unrelated emitter must not publish that staged value as if
// it were part of its own earlier commit.
func TestConfigRevisionDoesNotPublishUncommittedSettingFromLiveConfigPointer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Permission.Enabled = true
	cfg.UI.ReducedMotion = false

	program, captured := newCapturingProgram(t)
	app := &App{
		config:      cfg,
		permManager: permission.NewManager(nil, true),
		program:     program,
	}

	// This mirrors /set and /settings: the candidate is mutated first, then
	// handed to ApplyUIConfig. Pause before that apply call so the value is only
	// staged, not yet committed to the UI/runtime transaction.
	candidate := app.GetConfig()
	candidate.UI.ReducedMotion = true

	app.TogglePermissions()

	deadline := time.Now().Add(time.Second)
	var update ui.ConfigUpdateMsg
	found := false
	for time.Now().Before(deadline) {
		captured.mu.Lock()
		for _, msg := range captured.msgs {
			if configMsg, ok := msg.(ui.ConfigUpdateMsg); ok {
				update = configMsg
				found = true
				break
			}
		}
		captured.mu.Unlock()
		if found {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !found {
		t.Fatal("permission toggle did not emit ConfigUpdateMsg")
	}
	if update.Revision == 0 {
		t.Fatal("permission toggle emitted an unversioned config snapshot")
	}
	if update.ReducedMotion || update.Settings["reducedmotion"] {
		t.Fatalf("revision %d published staged reduced-motion change before its ApplyUIConfig commit: %+v",
			update.Revision, update)
	}
}

func TestCommitMCPConfigSnapshotDoesNotOverwriteNewerConfigCommit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.UI.ReducedMotion = false
	app := &App{config: cfg}

	// MCP add/remove can spend seconds connecting before it commits. Model that
	// in-flight operation with an old candidate, then commit an unrelated UI
	// setting while MCP is still running.
	staleMCP := app.GetConfig()
	staleMCP.MCP.Enabled = true
	staleMCP.MCP.Servers = append(staleMCP.MCP.Servers, config.MCPServerConfig{
		Name:      "docs",
		Transport: "http",
		URL:       "https://example.invalid/mcp",
	})

	newerUI := app.GetConfig()
	newerUI.UI.ReducedMotion = true
	if err := app.ApplyUIConfig(newerUI); err != nil {
		t.Fatalf("apply newer UI config: %v", err)
	}

	app.CommitMCPConfigSnapshot(staleMCP)
	got := app.GetConfig()
	if !got.UI.ReducedMotion {
		t.Fatal("late MCP snapshot overwrote a newer reduced-motion commit")
	}
	if !got.MCP.Enabled || len(got.MCP.Servers) != 1 || got.MCP.Servers[0].Name != "docs" {
		t.Fatalf("MCP section was not merged into the current config: %+v", got.MCP)
	}
}

func TestCommitMCPConfigSnapshotDoesNotDropConcurrentSiblingMCPCommit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	app := &App{config: config.DefaultConfig()}

	// mcp_admin is currently classified as read-only, so two add/remove calls
	// from one model response may execute in parallel and stage from the same
	// authoritative revision. Reproduce the commit order deterministically.
	first := app.GetConfig()
	second := app.GetConfig()
	first.MCP.Enabled = true
	first.MCP.Servers = append(first.MCP.Servers, config.MCPServerConfig{
		Name:      "docs",
		Transport: "http",
		URL:       "https://docs.example.invalid/mcp",
	})
	second.MCP.Enabled = true
	second.MCP.Servers = append(second.MCP.Servers, config.MCPServerConfig{
		Name:      "issues",
		Transport: "http",
		URL:       "https://issues.example.invalid/mcp",
	})

	app.CommitMCPConfigSnapshot(first)
	app.CommitMCPConfigSnapshot(second)

	got := app.GetConfig()
	names := make(map[string]bool, len(got.MCP.Servers))
	for _, server := range got.MCP.Servers {
		names[server.Name] = true
	}
	if !names["docs"] || !names["issues"] {
		t.Fatalf("sibling MCP commit was lost: servers=%+v", got.MCP.Servers)
	}
}

func TestApplyUIConfigMergesPresentationFieldsIntoLatestConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Permission.Enabled = true
	cfg.UI.ReducedMotion = false
	app := &App{
		config:      cfg,
		permManager: permission.NewManager(nil, true),
	}

	// The UI worker starts from an isolated candidate, then another config
	// surface commits while that worker is in flight.
	staleUI := app.GetConfig()
	if enabled := app.TogglePermissions(); enabled {
		t.Fatal("permission setup: toggle did not commit false")
	}
	staleUI.UI.ReducedMotion = true
	if err := app.ApplyUIConfig(staleUI); err != nil {
		t.Fatalf("apply UI config: %v", err)
	}

	got := app.GetConfig()
	if got.Permission.Enabled {
		t.Fatal("presentation-only apply overwrote the newer permission commit")
	}
	if !got.UI.ReducedMotion {
		t.Fatal("presentation-only apply lost its own reduced-motion change")
	}
	if app.permManager.IsEnabled() {
		t.Fatal("permission runtime setup unexpectedly changed")
	}
}

func TestApplyUIConfigDoesNotOverwriteNewerSiblingUICommit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.UI.ShowWelcome = true
	cfg.UI.ReducedMotion = false
	app := &App{config: cfg}

	staleUI := app.GetConfig()
	app.markOnboardingWelcomeSeen()
	if app.GetConfig().UI.ShowWelcome {
		t.Fatal("onboarding setup did not persist the newer welcome=false value")
	}

	staleUI.UI.ReducedMotion = true
	if err := app.ApplyUIConfig(staleUI); err != nil {
		t.Fatalf("apply UI config: %v", err)
	}
	got := app.GetConfig()
	if got.UI.ShowWelcome {
		t.Fatal("reduced-motion apply overwrote the newer onboarding UI commit")
	}
	if !got.UI.ReducedMotion {
		t.Fatal("reduced-motion apply lost its own value")
	}
}

func TestApplyConfigRejectsStaleCandidateInsteadOfOverwritingNewerCommit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.API.ActiveProvider = "glm"
	cfg.API.GLMKey = "test-key-that-is-long-enough-1234567890"
	cfg.Model.Provider = "glm"
	cfg.Model.Name = "glm-5.2"
	cfg.Permission.Enabled = true
	app := &App{
		config:      cfg,
		permManager: permission.NewManager(nil, true),
	}

	// Model/settings/login workers all hold an independently owned candidate
	// while the user can close their modal and commit another config action.
	stale := app.GetConfig()
	stale.DoneGate.Enabled = !stale.DoneGate.Enabled
	if enabled := app.TogglePermissions(); enabled {
		t.Fatal("permission setup: newer toggle did not commit false")
	}

	err := app.ApplyConfig(stale)
	if err == nil {
		t.Fatal("stale full-config candidate was accepted; expected a retryable conflict")
	}
	got := app.GetConfig()
	if got.Permission.Enabled || app.permManager.IsEnabled() {
		t.Fatal("stale full-config candidate overwrote newer permission state")
	}
}
