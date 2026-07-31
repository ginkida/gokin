//go:build unix

package codeintel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestManagedGoplsForcedCloseKillsDescendants(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	provider := newTestProvider(t, t.TempDir(), Options{
		Command: os.Args[0],
		Args: []string{
			"-test.run=TestManagedGoplsHelperProcess", "--", "--codeintel-helper",
			"--hang-after-eof", "--child-pid-file=" + pidFile,
		},
		ShutdownTimeout: 30 * time.Millisecond,
	})
	if _, err := provider.CallReadOnly(context.Background(), "go_workspace", nil); err != nil {
		t.Fatalf("managed helper call: %v", err)
	}

	var childPID int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			childPID, err = strconv.Atoi(string(data))
			if err != nil {
				t.Fatalf("parse child PID: %v", err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID <= 0 {
		t.Fatal("helper did not publish child PID")
	}

	if err := provider.Close(); err == nil {
		t.Fatal("forced close unexpectedly reported a graceful exit")
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("gopls descendant %d survived forced provider close", childPID)
}
