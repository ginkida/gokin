package security

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBashIsolationAbsoluteSecretRead(t *testing.T) {
	requireSandboxExec(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	secret := filepath.Join(home, ".ssh", "id_test")
	if err := os.MkdirAll(filepath.Dir(secret), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("synthetic secret"), 0600); err != nil {
		t.Fatal(err)
	}
	command, err := NewSandboxedCommand(t.Context(), t.TempDir(), "cat '"+secret+"'", DefaultSandboxConfig())
	if err != nil {
		t.Fatal(err)
	}
	result := command.Run(10 * time.Second)
	if result.Error != nil || result.ExitCode == 0 || len(result.Stdout) != 0 {
		t.Fatalf("absolute secret read escaped sandbox: exit=%d error=%v stdout=%q stderr=%s", result.ExitCode, result.Error, result.Stdout, result.Stderr)
	}
}

func TestBashIsolationBoundary(t *testing.T) {
	sandboxTestPathGo(t)
	requireSandboxExec(t)
	moduleCache := hostGoModuleCache()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("GOMODCACHE", moduleCache)
	t.Setenv("GOTOOLCHAIN", "auto")
	work := t.TempDir()
	outside := t.TempDir()
	config := DefaultSandboxConfig()
	config.RequireSandbox = true
	config.Policy = NewSandboxPolicy(work, config)
	run := func(command string, success bool) *SandboxResult {
		t.Helper()
		cmd, err := NewSandboxedCommand(t.Context(), work, command, config)
		if err != nil {
			t.Fatal(err)
		}
		result := cmd.Run(3 * time.Minute)
		if result.Error != nil || (result.ExitCode == 0) != success {
			t.Fatalf("%s: exit=%d error=%v stdout=%s stderr=%s", command, result.ExitCode, result.Error, result.Stdout, result.Stderr)
		}
		return result
	}
	run("printf allowed > inside", true)
	run("printf denied > '"+filepath.Join(outside, "escaped")+"'", false)
	if _, err := os.Stat(filepath.Join(outside, "escaped")); !os.IsNotExist(err) {
		t.Fatal("outside write escaped")
	}
	// Even granting a parent of credentials must not expose them.
	secret := filepath.Join(home, ".ssh", "synthetic")
	if err := os.MkdirAll(filepath.Dir(secret), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	config.Scope = CommandScope{WorkDir: work, AllowedDirs: []string{home}}
	if runtime.GOOS == "linux" {
		// Missing credential locations under HOME cannot be masked without
		// creating host mountpoints, so broad writable grants fail closed.
		cmd, err := NewSandboxedCommand(t.Context(), work, "true", config)
		if err == nil {
			cmd.Run(time.Second)
			t.Fatal("broad HOME grant with missing secrets was accepted")
		}
		if !strings.Contains(err.Error(), "narrower") {
			t.Fatal(err)
		}
	} else {
		run("cat '"+secret+"'", false)
	}
	config.Scope = CommandScope{}
	// A symlink inside the workspace cannot expand the mount/profile boundary.
	if err := os.Symlink(outside, filepath.Join(work, "escape")); err != nil {
		t.Fatal(err)
	}
	run("printf denied > escape/escaped", false)
	// Copy a dependency-free network helper into the workspace, so the test
	// verifies arbitrary child processes under the same policy as bash.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "probe.test"), data, 0700); err != nil {
		t.Fatal(err)
	}
	config.Environment = []string{"PATH=" + os.Getenv("PATH"), "GOKIN_BASH_NET_HELPER=loopback"}
	run("./probe.test -test.run '^TestBashIsolationNetworkHelper$'", true)
	var address string
	if runtime.GOOS == "darwin" {
		// Seatbelt's localhost filter permits every address of this host.
		// Verify host loopback access, then require EPERM for an external
		// TEST-NET address: a timeout would not prove policy enforcement.
		loopback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
		defer loopback.Close()
		address = strings.TrimPrefix(loopback.URL, "http://")
		config.Environment = []string{"PATH=" + os.Getenv("PATH"), "GOKIN_BASH_NET_HELPER=connect:" + address}
		run("./probe.test -test.run '^TestBashIsolationNetworkHelper$'", true)
		config.Environment = []string{"PATH=" + os.Getenv("PATH"), "GOKIN_BASH_NET_HELPER=deny-external"}
		run("./probe.test -test.run '^TestBashIsolationNetworkHelper$'", true)
	} else {
		listener, err := net.Listen("tcp4", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
		server.Listener = listener
		server.Start()
		defer server.Close()
		host := sandboxTestHostIP(t)
		address = net.JoinHostPort(host, strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
		config.Environment = []string{"PATH=" + os.Getenv("PATH"), "GOKIN_BASH_NET_HELPER=" + address}
		run("./probe.test -test.run '^TestBashIsolationNetworkHelper$'", true)
		if IsLinux() {
			config.Environment = []string{"PATH=" + os.Getenv("PATH"), "GOKIN_BASH_NET_HELPER=" + net.JoinHostPort("127.0.0.1", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))}
			run("./probe.test -test.run '^TestBashIsolationNetworkHelper$'", true)
		}
		if _, err := exec.LookPath("curl"); err == nil {
			run("curl --noproxy '*' --connect-timeout 1 --max-time 2 http://"+address, false)
		}
	}
	config.Environment = nil
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.test/isolation\n\ngo "+strings.TrimPrefix(runtime.Version(), "go")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "fixture_test.go"), []byte("package fixture\nimport \"testing\"\nfunc TestOffline(t *testing.T) {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("go test ./...", true)
	// Explicit opt-in retains the filesystem policy and can reach the same
	// local test server, never the internet.
	if _, err := exec.LookPath("curl"); err == nil {
		config.AllowNetwork = true
		config.Policy = NewSandboxPolicy(work, config)
		run("curl --noproxy '*' --connect-timeout 1 --max-time 2 http://"+address, true)
	}
}

func sandboxTestHostIP(t *testing.T) string {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		if ip, ok := address.(*net.IPNet); ok && !ip.IP.IsLoopback() && ip.IP.To4() != nil {
			return ip.IP.String()
		}
	}
	t.Fatal("network acceptance requires a non-loopback host interface")
	return ""
}

func TestBashIsolationNetworkHelper(t *testing.T) {
	mode := os.Getenv("GOKIN_BASH_NET_HELPER")
	if mode == "" {
		return
	}
	if mode == "deny-external" {
		connection, err := net.DialTimeout("tcp4", "192.0.2.1:9", time.Second)
		if connection != nil {
			connection.Close()
		}
		if !errors.Is(err, syscall.EPERM) {
			t.Fatalf("external TEST-NET connection must be denied with EPERM, got %v", err)
		}
		return
	}
	if mode != "loopback" {
		reachable := strings.HasPrefix(mode, "connect:")
		connection, err := net.DialTimeout("tcp", strings.TrimPrefix(mode, "connect:"), time.Second)
		if reachable {
			if err != nil {
				t.Fatalf("sandbox cannot reach permitted host loopback: %v", err)
			}
			connection.Close()
			return
		}
		if err == nil {
			connection.Close()
			t.Fatal("sandbox reached host listener")
		}
		fmt.Println("host listener denied")
		return
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
	fmt.Println("loopback works")
}
