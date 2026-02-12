package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

type providerHealth struct {
	Score         int
	LastFailure   time.Time
	LastSuccess   time.Time
	FailureStreak int
}

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
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var stored map[string]*providerHealth
	if err := json.Unmarshal(data, &stored); err != nil {
		return
	}
	if stored != nil {
		providerStats = stored
	}
}

func persistHealthLocked() {
	path, err := healthFilePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	data, err := json.MarshalIndent(providerStats, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func healthFilePath() (string, error) {
	configBase, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configBase, "gokin", "provider_health.json"), nil
}

func getProviderHealth(provider string) *providerHealth {
	healthMu.Lock()
	defer healthMu.Unlock()
	ensureHealthLoadedLocked()
	stats, ok := providerStats[provider]
	if !ok {
		stats = &providerHealth{}
		providerStats[provider] = stats
	}
	return stats
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
