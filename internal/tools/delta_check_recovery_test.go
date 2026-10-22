package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gokin/internal/testkit"
)

func TestFinalDeltaVerificationDoesNotClearUnavailableChecks(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	for name, content := range map[string]string{"go.mod": "module example.com/fixture\n\ngo 1.22\n", "value.go": "package fixture\nvar Value = 1\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", t.TempDir()) // A skipped compiler is not a passing compiler.
	executor := NewExecutor(NewRegistry(), nil, time.Second)
	executor.SetWorkDir(root)
	executor.SetDeltaCheckConfig(true, time.Second, false, 2)
	executor.deltaCheckBlocked, executor.deltaCheckBarrierRefused = true, true
	executor.deltaBaselineCaptured = true
	executor.deltaPendingPaths = map[string]struct{}{"value.go": {}}
	for range 2 {
		if err := executor.verifyRecoveredDeltaCheck(context.Background()); err == nil {
			t.Fatal("an unavailable build tool cleared a refused compiler check")
		}
		if !executor.deltaCheckBlocked || !executor.deltaCheckBarrierRefused || len(executor.deltaPendingPaths) == 0 {
			t.Fatal("unverified repair lost its pending verification state")
		}
	}
}
