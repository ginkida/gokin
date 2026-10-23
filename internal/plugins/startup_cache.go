package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"gokin/internal/fileutil"
)

type cachedStartupKey struct{}
type explicitSyncKey struct{}

// SyncCached loads declarations using only local directories and cached installs.
// Downloads are left to SyncStartup on an app-owned background context.
func (m *Manager) SyncCached(ctx context.Context) (StartupReport, error) {
	return m.syncLifecycle(context.WithValue(ctx, cachedStartupKey{}, true), true, false)
}

func cachedStartup(ctx context.Context) bool {
	cached, _ := ctx.Value(cachedStartupKey{}).(bool)
	return cached
}
func cachedSource(source Source) bool {
	return source.Kind == "directory" || source.Kind == "file" || source.Kind == "settings" || source.Kind == "relative"
}

func requireCachedSource(ctx context.Context, source Source) error {
	if cachedStartup(ctx) && !cachedSource(source) {
		return fmt.Errorf("source is not cached; deferred to background plugin refresh")
	}
	return nil
}

type marketplaceFailure struct {
	Source     string    `json:"source"`
	LastFailed time.Time `json:"last_failed"`
	Failures   int       `json:"failures"`
}

func marketplaceSourceKey(source Source) string {
	data, _ := json.Marshal(source)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
func marketplaceBackoff(failures int) time.Duration {
	switch {
	case failures <= 1:
		return 6 * time.Hour
	case failures == 2:
		return 12 * time.Hour
	default:
		return 24 * time.Hour
	}
}
func (m *Manager) marketplaceFailures() (map[string]marketplaceFailure, error) {
	raw, err := readObject(filepath.Join(m.pluginDir, "gokin", "marketplace_failures.json"))
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	failures := map[string]marketplaceFailure{}
	if err := json.Unmarshal(data, &failures); err != nil {
		return nil, err
	}
	return failures, nil
}
func (m *Manager) marketplaceRetryAllowed(ctx context.Context, name string, source Source) bool {
	if explicit, _ := ctx.Value(explicitSyncKey{}).(bool); explicit {
		return true
	}
	failures, err := m.marketplaceFailures()
	if err != nil {
		return false
	} // An unreadable retry ledger must not cause a network storm.
	entry, exists := failures[name]
	return !exists || entry.Source != marketplaceSourceKey(source) || time.Since(entry.LastFailed) >= marketplaceBackoff(entry.Failures)
}

// Called after the marketplace operation releases its registry lock. Explicit
// operations don't consult backoff, but success clears any remembered failure.
func (m *Manager) recordMarketplaceAttempt(name string, source Source, failed bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return m.recordMarketplaceAttemptLocked(name, source, failed)
}

// Caller owns the registry lock (dependency installs already hold it).
func (m *Manager) recordMarketplaceAttemptLocked(name string, source Source, failed bool) error {
	failures, err := m.marketplaceFailures()
	if err != nil {
		return err
	}
	if !failed {
		delete(failures, name)
	} else {
		entry := failures[name]
		key := marketplaceSourceKey(source)
		if entry.Source != key {
			entry = marketplaceFailure{Source: key}
		}
		entry.Failures = min(entry.Failures+1, 3)
		entry.LastFailed = time.Now().UTC()
		failures[name] = entry
	}
	data, err := json.Marshal(failures)
	if err != nil {
		return err
	}
	return fileutil.AtomicWrite(filepath.Join(m.pluginDir, "gokin", "marketplace_failures.json"), data, 0600)
}
