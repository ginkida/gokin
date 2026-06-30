package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates config-file I/O for this package's tests into a temporary
// XDG_CONFIG_HOME.
//
// ROOT CAUSE this prevents: several tests construct a throwaway *App with an
// empty &config.Config{} and exercise ToggleSandbox / TogglePermissions /
// ApplyConfig — all of which call (*config.Config).Save(). Save writes to the
// process-wide getConfigPath() (XDG_CONFIG_HOME or ~/.config/gokin/config.yaml).
// Without isolation, `go test ./...` OVERWRITES the developer's REAL config
// with the empty test config: it wipes provider API keys (so /login "doesn't
// stick"), blanks the model/provider, and zeroes tools.timeout (so the
// installed gokin then fails EVERY command with "context deadline exceeded").
// That was the actual cause of the repeated "config keeps resetting" reports.
//
// Individual tests may still t.Setenv a different XDG_CONFIG_HOME to assert
// specific persistence behavior; this only sets a safe default.
//
// It ALSO isolates client provider-health persistence (GOKIN_PROVIDER_HEALTH_FILE):
// the emergency-failover tests pick a candidate chain ordered partly by provider
// health, so reading the developer's REAL degraded glm score (e.g. -20 from real
// 1305/1308 overloads) made the failover pick kimi over glm and the assertion
// "backend == glm" fail locally while CI stayed green. A throwaway file keeps it
// deterministic and stops the suite writing into the installed gokin's real file.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gokin-app-test-cfg-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Setenv("GOKIN_PROVIDER_HEALTH_FILE", filepath.Join(dir, "provider_health.json"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
