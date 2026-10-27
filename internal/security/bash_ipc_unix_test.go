//go:build unix

package security

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestBashIsolationIPCHelper(t *testing.T) {
	if os.Getenv("GOKIN_BASH_IPC_HELPER") == "" {
		return
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	if _, err := unix.Write(fds[0], []byte("ok")); err != nil {
		t.Fatal(err)
	}
	var data [2]byte
	if n, err := unix.Read(fds[1], data[:]); err != nil || n != 2 || string(data[:]) != "ok" {
		t.Fatalf("socketpair: %q %v", data, err)
	}
}

func TestBashIsolationBasicWorkloads(t *testing.T) {
	requireSandboxExec(t)
	t.Setenv("HOME", t.TempDir())
	for _, fixture := range []struct{ name, command string }{
		{"python3", `python3 -c "import socket; a,b=socket.socketpair(); a.send(b'ok'); assert b.recv(2)==b'ok'"`},
		{"node", `node -e "if (require('child_process').execFileSync('sh',['-c','printf ok']).toString()!=='ok') process.exit(1)"`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if _, err := exec.LookPath(fixture.name); err != nil {
				if os.Getenv("GOKIN_TEST_REQUIRE_WORKLOADS") == "1" {
					t.Fatal(err)
				}
				t.Skipf("workload executable unavailable: %v", err)
			}
			config := DefaultSandboxConfig()
			config.RequireSandbox = true
			cmd, err := NewSandboxedCommand(t.Context(), t.TempDir(), fixture.command, config)
			if err != nil {
				t.Fatal(err)
			}
			if result := cmd.Run(20 * time.Second); result.Error != nil || result.ExitCode != 0 {
				t.Fatalf("workload: %+v %s", result, result.Stderr)
			}
		})
	}
}
