package client

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates provider-health persistence for this package's tests.
//
// ROOT CAUSE this prevents: getProviderHealth / recordProviderSuccess /
// recordProviderFailure persist to ~/Library/Application Support/gokin/
// provider_health.json (os.UserConfigDir). Without isolation the retry/fallback
// tests (a) READ the developer's REAL glm/kimi scores — which on a machine that
// actually runs gokin are degraded by real 1305/1308 overloads, so the GLM/Kimi
// retry-boost tests (TestAdaptiveStreamRetryPolicyGLMDefaults, etc.) fail
// LOCALLY while CI's fresh env stays green — and (b) WRITE test/glm/kimi scores
// back into that real file, degrading the installed gokin's actual retry
// behavior. Pointing GOKIN_PROVIDER_HEALTH_FILE at a throwaway file makes the
// suite deterministic and side-effect-free.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gokin-client-test-health-")
	if err != nil {
		panic(err)
	}
	os.Setenv("GOKIN_PROVIDER_HEALTH_FILE", filepath.Join(dir, "provider_health.json"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
