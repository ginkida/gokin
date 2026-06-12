package config

import "time"

// DefaultTimeout is used when no valid timeout is configured.
const DefaultTimeout = 30 * time.Second

// Config holds runtime settings loaded from the user's config file.
type Config struct {
	Timeout time.Duration
}

// EffectiveTimeout returns the timeout that should be applied to tool
// execution. A zero or negative configured value means "not set" and
// must fall back to DefaultTimeout.
func EffectiveTimeout(cfg Config) time.Duration {
	return cfg.Timeout
}
