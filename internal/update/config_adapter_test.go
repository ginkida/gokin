package update

import (
	"testing"
	"time"

	"gokin/internal/config"
)

func TestFromConfigPreservesTimeoutAndFields(t *testing.T) {
	source := &config.UpdateConfig{
		Enabled:           true,
		AutoCheck:         true,
		CheckInterval:     2 * time.Hour,
		AutoDownload:      true,
		IncludePrerelease: true,
		Channel:           "beta",
		GitHubRepo:        "owner/repo",
		MaxBackups:        7,
		VerifyChecksum:    true,
		NotifyOnly:        true,
		Timeout:           47 * time.Second,
	}

	got := FromConfig(source)
	if got.Timeout != source.Timeout {
		t.Fatalf("Timeout = %v, want %v", got.Timeout, source.Timeout)
	}
	if !got.Enabled || !got.AutoCheck || !got.AutoDownload || !got.IncludePrerelease ||
		got.CheckInterval != source.CheckInterval || got.Channel != ChannelBeta ||
		got.GitHubRepo != source.GitHubRepo || got.MaxBackups != source.MaxBackups ||
		!got.VerifyChecksum || !got.NotifyOnly {
		t.Fatalf("converted config did not preserve fields: %+v", got)
	}
}

func TestFromConfigDefaultsMissingTimeout(t *testing.T) {
	got := FromConfig(&config.UpdateConfig{})
	if got.Timeout != 30*time.Second {
		t.Fatalf("Timeout = %v, want 30s", got.Timeout)
	}
}

func TestFromConfigNilUsesRuntimeDefaults(t *testing.T) {
	got := FromConfig(nil)
	if !got.Enabled || got.Timeout != 30*time.Second || got.GitHubRepo == "" {
		t.Fatalf("nil source did not use runtime defaults: %+v", got)
	}
}
