//go:build !windows && !plan9

package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProviderHealthOverridePreservesCallerDirectoryMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "provider-health.json")
	t.Setenv("GOKIN_PROVIDER_HEALTH_FILE", path)

	healthMu.Lock()
	savedStats := providerStats
	providerStats = map[string]*providerHealth{"test": {Score: 1}}
	persistHealthLocked()
	providerStats = savedStats
	healthMu.Unlock()

	for path, want := range map[string]os.FileMode{dir: 0o755, path: 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %04o, want %04o", path, got, want)
		}
	}
}
