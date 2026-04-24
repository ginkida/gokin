package config

import (
	"testing"
)

// TestDefaultConfig_UpdateGitHubRepo pins the upstream repo for the
// self-update feature. Shipped v0.72.x+ with `"user/gokin"` as the
// default — every first user hit a 404 on `GET /repos/user/gokin/
// releases/latest` because that repo doesn't exist. This test makes
// sure nobody regresses it back to a placeholder.
//
// If the upstream moves, update both the default AND this assertion;
// there's a matching default in `internal/update/config.go` that must
// stay in sync.
func TestDefaultConfig_UpdateGitHubRepo(t *testing.T) {
	cfg := DefaultConfig()
	want := "ginkida/gokin"
	if cfg.Update.GitHubRepo != want {
		t.Errorf("DefaultConfig().Update.GitHubRepo = %q, want %q "+
			"(placeholder defaults break the self-updater: clients 404 "+
			"on the GitHub API and never discover new versions)",
			cfg.Update.GitHubRepo, want)
	}
}

// TestDefaultConfig_UpdateAutoCheckEnabled ensures fresh installs
// DO check for updates on startup — that's how users discover new
// releases without remembering to run `/update`. Flipping this off by
// default would mean even a correctly-configured repo goes unchecked.
func TestDefaultConfig_UpdateAutoCheckEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Update.Enabled {
		t.Error("Update.Enabled must default to true — disables the whole self-update feature when false")
	}
	if !cfg.Update.AutoCheck {
		t.Error("Update.AutoCheck must default to true — users never see update notifications otherwise")
	}
}
