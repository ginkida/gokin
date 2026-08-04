package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"gokin/internal/fileutil"
)

type providerHealth struct {
	Score         int
	LastFailure   time.Time
	LastSuccess   time.Time
	FailureStreak int
}

const (
	maxProviderHealthFileBytes int64 = 1 << 20
	maxPersistedProviders            = 128
	maxProviderNameBytes             = 128
	maxFailureStreak                 = 1_000_000
)

var (
	healthMu      sync.RWMutex
	providerStats = map[string]*providerHealth{}
	healthLoaded  bool
)

func ensureHealthLoadedLocked() {
	if healthLoaded {
		return
	}
	healthLoaded = true

	path, err := healthFilePath()
	if err != nil {
		return
	}
	data, err := fileutil.ReadPrivateFile(path, maxProviderHealthFileBytes)
	if err != nil {
		return
	}

	var stored map[string]*providerHealth
	if err := json.Unmarshal(data, &stored); err != nil {
		return
	}
	if len(stored) > maxPersistedProviders {
		return
	}
	clean := make(map[string]*providerHealth, len(stored))
	for name, stats := range stored {
		if !validPersistedProviderName(name) || stats == nil {
			continue
		}
		copy := *stats
		copy.Score = min(8, max(-20, copy.Score))
		copy.FailureStreak = min(maxFailureStreak, max(0, copy.FailureStreak))
		clean[name] = &copy
	}
	providerStats = clean
}

func validPersistedProviderName(name string) bool {
	if name == "" || len(name) > maxProviderNameBytes || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	return strings.IndexFunc(name, unicode.IsControl) < 0
}

func persistHealthLocked() {
	path, err := healthFilePath()
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if os.Getenv("GOKIN_PROVIDER_HEALTH_FILE") != "" {
		// The ops/test override may deliberately point into a shared parent
		// such as /tmp. Create missing parents privately, but never chmod an
		// existing caller-selected directory.
		if err := os.MkdirAll(dir, 0700); err != nil {
			return
		}
	} else if err := fileutil.EnsurePrivateDir(dir); err != nil {
		return
	}
	data, err := json.MarshalIndent(providerStats, "", "  ")
	if err != nil {
		return
	}
	_ = fileutil.AtomicWrite(path, data, 0600)
}

func healthFilePath() (string, error) {
	// Test/ops override: redirect provider-health persistence to an explicit
	// path. Tests (client + app TestMain) point this at a throwaway file so the
	// suite neither READS the developer's real (often degraded) glm/kimi scores
	// — which made the GLM/Kimi retry-boost tests fail locally while CI stayed
	// green — nor WRITES test scores back into the installed gokin's real
	// ~/Library/Application Support/gokin/provider_health.json. Empty by default
	// in normal runs.
	if override := os.Getenv("GOKIN_PROVIDER_HEALTH_FILE"); override != "" {
		return override, nil
	}
	configBase, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configBase, "gokin", "provider_health.json"), nil
}

// getProviderHealth returns a SNAPSHOT (value copy) of the provider's health,
// taken under healthMu. It previously returned the shared *providerHealth
// pointer, which let callers (AdaptiveRetryConfig / AdaptiveStreamRetryPolicy)
// read Score/FailureStreak lock-free while recordProviderSuccess/Failure mutated
// those same fields under healthMu — a data race. A value copy is race-free and
// every caller only reads.
func getProviderHealth(provider string) providerHealth {
	healthMu.Lock()
	defer healthMu.Unlock()
	ensureHealthLoadedLocked()
	stats, ok := providerStats[provider]
	if !ok {
		stats = &providerHealth{}
		providerStats[provider] = stats
	}
	return *stats
}

func recordProviderSuccess(provider string) {
	if provider == "" {
		return
	}
	healthMu.Lock()
	defer healthMu.Unlock()
	ensureHealthLoadedLocked()
	stats, ok := providerStats[provider]
	if !ok {
		stats = &providerHealth{}
		providerStats[provider] = stats
	}
	stats.LastSuccess = time.Now()
	stats.FailureStreak = 0
	if stats.Score < 8 {
		stats.Score++
	}
	persistHealthLocked()
}

func recordProviderFailure(provider string, retryable bool) {
	if provider == "" {
		return
	}
	healthMu.Lock()
	defer healthMu.Unlock()
	ensureHealthLoadedLocked()
	stats, ok := providerStats[provider]
	if !ok {
		stats = &providerHealth{}
		providerStats[provider] = stats
	}
	stats.LastFailure = time.Now()
	stats.FailureStreak++
	penalty := 2
	if retryable {
		penalty = 1
	}
	stats.Score -= penalty
	if stats.Score < -20 {
		stats.Score = -20
	}
	persistHealthLocked()
}

func providerScore(provider string) int {
	healthMu.RLock()
	defer healthMu.RUnlock()
	if !healthLoaded {
		return 0
	}
	stats, ok := providerStats[provider]
	if !ok {
		return 0
	}
	return stats.Score
}

func reorderProvidersByHealth(providers []string) []string {
	healthMu.Lock()
	ensureHealthLoadedLocked()
	healthMu.Unlock()

	out := append([]string(nil), providers...)
	sort.SliceStable(out, func(i, j int) bool {
		return providerScore(out[i]) > providerScore(out[j])
	})
	return out
}

// OrderProvidersByHealth returns a copy of providers ordered from healthiest
// to least healthy according to the persisted runtime health tracker.
func OrderProvidersByHealth(providers []string) []string {
	return reorderProvidersByHealth(providers)
}

// GetProviderHealthReport returns a human-readable report for provider health.
func GetProviderHealthReport() string {
	healthMu.Lock()
	defer healthMu.Unlock()
	ensureHealthLoadedLocked()

	if len(providerStats) == 0 {
		return "No provider health data."
	}

	type row struct {
		name  string
		stats *providerHealth
	}
	rows := make([]row, 0, len(providerStats))
	for name, stats := range providerStats {
		rows = append(rows, row{name: name, stats: stats})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].stats.Score > rows[j].stats.Score
	})

	out := "Provider health:\n"
	for _, r := range rows {
		last := "-"
		if !r.stats.LastSuccess.IsZero() {
			last = "success " + r.stats.LastSuccess.Format("2006-01-02 15:04:05")
		} else if !r.stats.LastFailure.IsZero() {
			last = "failure " + r.stats.LastFailure.Format("2006-01-02 15:04:05")
		}
		out +=
			"- " + r.name +
				": score=" + itoa(r.stats.Score) +
				", streak=" + itoa(r.stats.FailureStreak) +
				", last=" + last + "\n"
	}
	return out
}

func itoa(v int) string {
	return strconv.Itoa(v)
}
