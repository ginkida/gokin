package repl

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDetectedSecureBackendReadsButCannotWriteOrConnect(t *testing.T) {
	if testing.Short() {
		t.Skip("secure backend integration test")
	}
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "proof.txt"), []byte("readable proof\n"), 0600); err != nil {
		t.Fatal(err)
	}
	initGit := exec.Command("git", "init", "--quiet")
	initGit.Dir = workDir
	if output, err := initGit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	manager, availability := OpenDetected(t.Context(), Options{
		WorkDir: workDir, CellTimeout: 5 * time.Second,
	})
	if !availability.Available {
		t.Skipf("secure backend unavailable on %s: %s", runtime.GOOS, availability.Reason)
	}
	if stats := manager.Stats(); !stats.Running || stats.Executions != 0 || stats.Generation != 1 {
		t.Fatalf("verified manager stats = %+v, want running generation 1 with zero user executions", stats)
	}
	t.Cleanup(func() { _ = manager.Close() })

	read, err := manager.Execute(t.Context(), `context.read_slice("proof.txt", 1, 1)`)
	if err != nil || !read.OK() || !strings.Contains(read.Value, "readable proof") {
		t.Fatalf("sandboxed read = %+v, err=%v", read, err)
	}
	if stats := manager.Stats(); stats.Generation != 1 || stats.Executions != 1 {
		t.Fatalf("first cell did not reuse verified worker: %+v", stats)
	}
	counted, err := manager.Execute(t.Context(), `context.count_code("proof", group_by="file")`)
	if err != nil || !counted.OK() || !strings.Contains(counted.Value, "'matching_lines': 1") ||
		!strings.Contains(counted.Value, "'proof.txt': 1") {
		t.Fatalf("sandboxed aggregate search = %+v, err=%v", counted, err)
	}
	inventory, err := manager.Execute(t.Context(), `context.file_stats(group_by="extension")`)
	if err != nil || !inventory.OK() || !strings.Contains(inventory.Value, "'matching_files': 1") ||
		!strings.Contains(inventory.Value, "'.txt': {'files': 1") {
		t.Fatalf("sandboxed streaming inventory = %+v, err=%v", inventory, err)
	}
	marker := filepath.Join(workDir, "must-not-write")
	write, err := manager.Execute(t.Context(), `open("must-not-write", "w").write("escape")`)
	if err != nil {
		t.Fatalf("Python write should be a cell error, not protocol failure: %v", err)
	}
	if write.Error == nil {
		t.Fatalf("sandboxed workspace write unexpectedly succeeded: %+v", write)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("sandboxed worker created %s: %v", marker, err)
	}
	runtimeWrite, err := manager.Execute(t.Context(), fmt.Sprintf(
		`open(%q, "w").write("escape")`, filepath.Join(manager.runtimeDir, "must-not-write")))
	if err != nil {
		t.Fatalf("runtime write denial should be a cell error, not protocol failure: %v", err)
	}
	if runtimeWrite.Error == nil {
		t.Fatalf("sandboxed worker wrote to parent-owned runtime directory: %+v", runtimeWrite)
	}
	if availability.Backend == BackendSandboxExec {
		// Bypass the worker and its Python audit hook entirely. The hard Seatbelt
		// profile itself must keep parent-owned runtime snapshots read-only.
		runtimeMarker := filepath.Join(manager.runtimeDir, "seatbelt-must-not-write")
		sandboxExec, lookupErr := exec.LookPath("sandbox-exec")
		if lookupErr != nil {
			t.Fatal(lookupErr)
		}
		probe := exec.Command(
			sandboxExec, "-f", filepath.Join(manager.runtimeDir, "sandbox.sb"),
			availability.PythonPath, "-I", "-c",
			fmt.Sprintf("open(%q, 'w').write('escape')", runtimeMarker),
		)
		probe.Dir = workDir
		probe.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + manager.runtimeDir,
			"TMPDIR=" + manager.runtimeDir, "PYTHONDONTWRITEBYTECODE=1"}
		if output, probeErr := probe.CombinedOutput(); probeErr == nil {
			t.Fatalf("Seatbelt profile allowed runtime write: %s", output)
		}
		if _, statErr := os.Stat(runtimeMarker); !os.IsNotExist(statErr) {
			t.Fatalf("Seatbelt probe created runtime marker: %v", statErr)
		}
		for name, code := range map[string]string{
			"fork": `import os
pid = os.fork()
os._exit(0) if pid == 0 else os.waitpid(pid, 0)`,
			"exec": `import os
os.execv("/bin/echo", ["echo", "escape"])`,
		} {
			t.Run("seatbelt blocks "+name+" without audit hook", func(t *testing.T) {
				probe := exec.Command(
					sandboxExec, "-f", filepath.Join(manager.runtimeDir, "sandbox.sb"),
					availability.PythonPath, "-I", "-c", code,
				)
				probe.Dir = workDir
				probe.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + manager.runtimeDir,
					"TMPDIR=" + manager.runtimeDir, "PYTHONDONTWRITEBYTECODE=1"}
				if output, probeErr := probe.CombinedOutput(); probeErr == nil {
					t.Fatalf("Seatbelt profile allowed %s: %s", name, output)
				}
			})
		}
	}

	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "ambient-secret")
	if err := os.WriteFile(outside, []byte("must remain unreadable"), 0600); err != nil {
		t.Fatal(err)
	}
	ambientRead, err := manager.Execute(t.Context(), fmt.Sprintf(`open(%q).read()`, outside))
	if err != nil {
		t.Fatalf("ambient read denial should be a cell error, not protocol failure: %v", err)
	}
	if ambientRead.Error == nil {
		t.Fatalf("sandboxed worker read outside workspace: %+v", ambientRead)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	connected := make(chan bool, 1)
	go func() {
		_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(1500 * time.Millisecond))
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
			connected <- true
			return
		}
		connected <- false
	}()
	network, err := manager.Execute(t.Context(), fmt.Sprintf(
		`import socket
s = socket.socket()
s.settimeout(1)
s.connect(("127.0.0.1", %d))`, port))
	if err != nil {
		t.Fatalf("network denial should be a cell error, not protocol failure: %v", err)
	}
	if network.Error == nil {
		t.Fatalf("sandboxed network connect unexpectedly succeeded: %+v", network)
	}
	if <-connected {
		t.Fatal("sandboxed worker reached host TCP listener")
	}

	if residentMemorySupported() {
		const memoryLimit = 48 * 1024 * 1024
		limitedManager, limitErr := NewManager(Options{
			WorkDir: workDir, PythonPath: availability.PythonPath, Backend: availability.Backend,
			CellTimeout: 3 * time.Second, MaxMemoryBytes: memoryLimit,
		})
		if limitErr != nil {
			t.Fatal(limitErr)
		}
		defer limitedManager.Close()
		tamper, executeErr := limitedManager.Execute(t.Context(), memoryWatchdogTamperCode(memoryLimit))
		if executeErr != nil || tamper.Error == nil || tamper.Error.Type != "PermissionError" || tamper.KernelReset {
			t.Fatalf("sandboxed watchdog reflection=%+v err=%v", tamper, executeErr)
		}
		limited, executeErr := limitedManager.Execute(t.Context(), memoryLimitProbeCode())
		if executeErr != nil || limited.Error == nil ||
			limited.Error.Type != "MemoryLimitExceeded" || !limited.KernelReset {
			t.Fatalf("sandboxed parent memory limit=%+v err=%v", limited, executeErr)
		}
	}
}

func TestPreflightMatchesDetectedBackendPrerequisites(t *testing.T) {
	workDir := t.TempDir()
	availability := Preflight(workDir)
	if availability.Available {
		if availability.PythonPath == "" || availability.Backend == BackendNone {
			t.Fatalf("available preflight omitted prerequisites: %+v", availability)
		}
		return
	}
	if strings.TrimSpace(availability.Reason) == "" {
		t.Fatalf("unavailable preflight omitted reason: %+v", availability)
	}
}
