package config

import (
	"fmt"
	"strings"
	"time"
)

const (
	minHTTPTimeout        = 1 * time.Second
	maxHTTPTimeout        = 30 * time.Minute
	minStreamIdleTimeout  = 5 * time.Second
	maxStreamIdleTimeout  = 30 * time.Minute
	maxRetryDelay         = 5 * time.Minute
	minProviderRetryDelay = 100 * time.Millisecond
)

// ValidateRetryConfig validates retry-related config values, including provider overrides.
func ValidateRetryConfig(cfg *Config) error {
	if cfg == nil {
		return nil
	}

	retry := cfg.API.Retry
	if retry.MaxRetries < 0 {
		return fmt.Errorf("invalid api.retry.max_retries: must be >= 0, got %d", retry.MaxRetries)
	}
	if retry.RetryDelay < 0 {
		return fmt.Errorf("invalid api.retry.retry_delay: must be >= 0, got %s", retry.RetryDelay)
	}
	if retry.RetryDelay > 0 && retry.RetryDelay < minProviderRetryDelay {
		return fmt.Errorf("invalid api.retry.retry_delay: too small (%s), minimum is %s", retry.RetryDelay, minProviderRetryDelay)
	}
	if retry.RetryDelay > maxRetryDelay {
		return fmt.Errorf("invalid api.retry.retry_delay: too large (%s), maximum is %s", retry.RetryDelay, maxRetryDelay)
	}
	if err := validateTimeoutRange("api.retry.http_timeout", retry.HTTPTimeout, minHTTPTimeout, maxHTTPTimeout); err != nil {
		return err
	}
	if err := validateTimeoutRange("api.retry.stream_idle_timeout", retry.StreamIdleTimeout, minStreamIdleTimeout, maxStreamIdleTimeout); err != nil {
		return err
	}

	for provider, override := range retry.Providers {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key == "" {
			return fmt.Errorf("invalid api.retry.providers.%q: provider key is empty", provider)
		}
		if GetProvider(key) == nil {
			return fmt.Errorf("invalid api.retry.providers.%q: unknown provider (supported: %s)", provider, strings.Join(ProviderNames(), ", "))
		}
		if err := validateTimeoutRange(
			fmt.Sprintf("api.retry.providers.%s.http_timeout", key),
			override.HTTPTimeout,
			minHTTPTimeout,
			maxHTTPTimeout,
		); err != nil {
			return err
		}
		if err := validateTimeoutRange(
			fmt.Sprintf("api.retry.providers.%s.stream_idle_timeout", key),
			override.StreamIdleTimeout,
			minStreamIdleTimeout,
			maxStreamIdleTimeout,
		); err != nil {
			return err
		}
	}

	return nil
}

func validateTimeoutRange(field string, value, min, max time.Duration) error {
	if value == 0 {
		return nil
	}
	if value < 0 {
		return fmt.Errorf("invalid %s: must be >= 0, got %s", field, value)
	}
	if value < min {
		return fmt.Errorf("invalid %s: too small (%s), minimum is %s", field, value, min)
	}
	if value > max {
		return fmt.Errorf("invalid %s: too large (%s), maximum is %s", field, value, max)
	}
	return nil
}
