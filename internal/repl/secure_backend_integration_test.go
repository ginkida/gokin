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
	availability := Detect(t.Context(), workDir)
	if !availability.Available {
		t.Skipf("secure backend unavailable on %s: %s", runtime.GOOS, availability.Reason)
	}
	manager, err := NewManager(Options{
		WorkDir: workDir, PythonPath: availability.PythonPath, Backend: availability.Backend,
		CellTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	read, err := manager.Execute(t.Context(), `context.read_slice("proof.txt", 1, 1)`)
	if err != nil || !read.OK() || !strings.Contains(read.Value, "readable proof") {
		t.Fatalf("sandboxed read = %+v, err=%v", read, err)
	}
	status, err := manager.Execute(t.Context(), `context.git_status()`)
	if err != nil || !status.OK() || !strings.Contains(status.Value, "proof.txt") {
		t.Fatalf("sandboxed git status = %+v, err=%v", status, err)
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
}
