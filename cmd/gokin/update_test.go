package main

import (
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/update"
)

// --- convertConfig ---

func TestConvertConfig_PreservesAllFields(t *testing.T) {
	src := &config.UpdateConfig{
		Enabled:           true,
		AutoCheck:         true,
		CheckInterval:     2 * time.Hour,
		AutoDownload:      true,
		IncludePrerelease: true,
		Channel:           "beta",
		GitHubRepo:        "ginkida/gokin",
		MaxBackups:        7,
		VerifyChecksum:    true,
		NotifyOnly:        true,
		Timeout:           45 * time.Second,
	}

	got := convertConfig(src)
	if !got.Enabled || !got.AutoCheck || !got.AutoDownload || !got.IncludePrerelease {
		t.Errorf("bool flags not preserved: %+v", got)
	}
	if got.CheckInterval != 2*time.Hour {
		t.Errorf("CheckInterval = %v, want 2h", got.CheckInterval)
	}
	if got.Channel != update.Channel("beta") {
		t.Errorf("Channel = %v, want beta", got.Channel)
	}
	if got.GitHubRepo != "ginkida/gokin" {
		t.Errorf("GitHubRepo = %q", got.GitHubRepo)
	}
	if got.MaxBackups != 7 {
		t.Errorf("MaxBackups = %d", got.MaxBackups)
	}
	if !got.VerifyChecksum || !got.NotifyOnly {
		t.Errorf("VerifyChecksum/NotifyOnly not preserved: %+v", got)
	}
	if got.Timeout != 45*time.Second {
		t.Errorf("Timeout = %v, want 45s", got.Timeout)
	}
}

func TestConvertConfig_ZeroTimeoutDefaultsTo30s(t *testing.T) {
	src := &config.UpdateConfig{Timeout: 0}
	got := convertConfig(src)
	if got.Timeout != 30*time.Second {
		t.Fatalf("Timeout = %v, want 30s default", got.Timeout)
	}
}

func TestConvertConfig_DisabledByDefault(t *testing.T) {
	src := &config.UpdateConfig{}
	got := convertConfig(src)
	if got.Enabled {
		t.Error("Enabled = true, want false by default")
	}
	if got.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s default", got.Timeout)
	}
}

// --- Command constructors ---

func TestNewUpdateCmd_HasSubcommands(t *testing.T) {
	cmd := newUpdateCmd()
	subs := cmd.Commands()
	if len(subs) < 4 {
		t.Fatalf("expected at least 4 subcommands, got %d", len(subs))
	}
	seen := map[string]bool{}
	for _, c := range subs {
		seen[c.Use] = true
	}
	for _, want := range []string{"check", "install", "rollback", "list-backups"} {
		if !seen[want] {
			t.Errorf("missing subcommand %q", want)
		}
	}
}

func TestNewUpdateInstallCmd_HasForceFlag(t *testing.T) {
	cmd := newUpdateInstallCmd()
	if cmd.Flags().Lookup("force") == nil {
		t.Error("force flag not registered")
	}
	if cmd.Flags().Lookup("force").Shorthand != "f" {
		t.Error("force flag shorthand should be -f")
	}
}

func TestNewUpdateRollbackCmd_HasBackupFlag(t *testing.T) {
	cmd := newUpdateRollbackCmd()
	if cmd.Flags().Lookup("backup") == nil {
		t.Error("backup flag not registered")
	}
}

func TestNewUpdateListBackupsCmd_HasAlias(t *testing.T) {
	cmd := newUpdateListBackupsCmd()
	aliases := cmd.Aliases
	found := false
	for _, a := range aliases {
		if a == "backups" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing 'backups' alias, got %v", aliases)
	}
}

// --- CheckForUpdateOnStartup ---

func TestCheckForUpdateOnStartup_DisabledNoOp(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Update.Enabled = false
	cfg.Update.AutoCheck = true

	// Should return immediately without panicking.
	CheckForUpdateOnStartup(cfg, nil)
}

func TestCheckForUpdateOnStartup_AutoCheckOffNoOp(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Update.Enabled = true
	cfg.Update.AutoCheck = false

	CheckForUpdateOnStartup(cfg, nil)
}
