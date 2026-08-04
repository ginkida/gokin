package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderHealthLoadBoundsAndSanitizesDurableState(t *testing.T) {
	t.Run("values and names", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "health.json")
		stored := map[string]*providerHealth{
			"good":      {Score: 999, FailureStreak: -7},
			"bad\nname": {Score: 1},
			"nil":       nil,
		}
		data, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}

		withFreshProviderHealth(t, path, func() {
			if len(providerStats) != 1 || providerStats["good"] == nil {
				t.Fatalf("sanitized provider stats = %#v", providerStats)
			}
			if got := *providerStats["good"]; got.Score != 8 || got.FailureStreak != 0 {
				t.Fatalf("sanitized good health = %+v", got)
			}
		})
	})

	t.Run("provider count", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "health.json")
		stored := make(map[string]*providerHealth, maxPersistedProviders+1)
		for i := 0; i <= maxPersistedProviders; i++ {
			stored[fmt.Sprintf("provider-%d", i)] = &providerHealth{}
		}
		data, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		withFreshProviderHealth(t, path, func() {
			if len(providerStats) != 0 {
				t.Fatalf("oversized provider map was loaded: %d entries", len(providerStats))
			}
		})
	})

	t.Run("file bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "health.json")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxProviderHealthFileBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		withFreshProviderHealth(t, path, func() {
			if len(providerStats) != 0 {
				t.Fatalf("oversized health file was loaded: %#v", providerStats)
			}
		})
	})
}

func withFreshProviderHealth(t *testing.T, path string, check func()) {
	t.Helper()
	t.Setenv("GOKIN_PROVIDER_HEALTH_FILE", path)
	healthMu.Lock()
	savedStats, savedLoaded := providerStats, healthLoaded
	defer func() {
		providerStats, healthLoaded = savedStats, savedLoaded
		healthMu.Unlock()
	}()
	providerStats = make(map[string]*providerHealth)
	healthLoaded = false
	ensureHealthLoadedLocked()
	check()
}
