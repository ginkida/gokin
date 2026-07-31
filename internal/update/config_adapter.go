package update

import (
	"time"

	"gokin/internal/config"
)

// FromConfig converts the application's persisted update settings into the
// runtime updater configuration. Keeping this mapping in one place prevents
// the CLI and the interactive /update command from silently drifting apart.
func FromConfig(source *config.UpdateConfig) *Config {
	if source == nil {
		return DefaultConfig()
	}

	timeout := source.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &Config{
		Enabled:           source.Enabled,
		AutoCheck:         source.AutoCheck,
		CheckInterval:     source.CheckInterval,
		AutoDownload:      source.AutoDownload,
		IncludePrerelease: source.IncludePrerelease,
		Channel:           Channel(source.Channel),
		GitHubRepo:        source.GitHubRepo,
		MaxBackups:        source.MaxBackups,
		VerifyChecksum:    source.VerifyChecksum,
		NotifyOnly:        source.NotifyOnly,
		Timeout:           timeout,
	}
}
