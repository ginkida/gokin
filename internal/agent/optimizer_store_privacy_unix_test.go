//go:build !windows && !plan9

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOptimizerStoresRepairPrivateModes(t *testing.T) {
	configDir := t.TempDir()
	dir := filepath.Join(configDir, "memory")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		filepath.Join(dir, "strategy_metrics.json"):   `{}`,
		filepath.Join(dir, "prompt_variants.json"):    `{}`,
		filepath.Join(dir, "delegation_metrics.json"): `{"path_metrics":{},"rule_weights":{}}`,
	}
	for path, data := range paths {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	NewStrategyOptimizer(configDir)
	NewPromptOptimizer(configDir)
	NewDelegationMetrics(configDir)
	assertOptimizerMode(t, dir, 0o700)
	for path := range paths {
		assertOptimizerMode(t, path, 0o600)
	}
}

func TestOptimizerStoreRejectsSymlinkedDirectory(t *testing.T) {
	configDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(configDir, "memory")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	optimizer := NewStrategyOptimizer(configDir)
	if err := optimizer.writeSnapshot([]byte(`{}`)); err == nil {
		t.Fatal("optimizer write accepted a symlinked memory directory")
	}
	assertOptimizerMode(t, target, 0o755)
}

func assertOptimizerMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
