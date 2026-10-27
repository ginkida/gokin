//go:build linux

package security

import (
	"encoding/binary"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestBashSocketFilterABIs(t *testing.T) {
	for _, abi := range []struct {
		arch                string
		audit, socket, pair uint32
	}{
		{"amd64", 0xc000003e, 41, 53}, {"arm64", 0xc00000b7, 198, 199},
		{"riscv64", 0xc00000f3, 198, 199}, {"loong64", 0xc0000102, 198, 199},
	} {
		t.Run(abi.arch, func(t *testing.T) {
			program, err := bashSocketFilter(abi.arch)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range []struct {
				nr, arch, domain, kind uint32
				deny                   bool
			}{
				{abi.socket, abi.audit, 1, 0, true}, {abi.socket, abi.audit, 2, 0, false},
				{abi.pair, abi.audit, 1, 1, false}, {abi.socket, 0, 1, 0, true},
				{abi.pair, abi.audit, 1, 2, true}, {abi.pair, abi.audit, 1, 0x80001, false},
			} {
				var input [64]byte
				binary.LittleEndian.PutUint32(input[:], c.nr)
				binary.LittleEndian.PutUint32(input[4:], c.arch)
				binary.LittleEndian.PutUint32(input[16:], c.domain)
				binary.LittleEndian.PutUint32(input[24:], c.kind)
				var value uint32
				for pc := 0; pc < len(program)/8; pc++ {
					ins := program[pc*8:]
					code := binary.LittleEndian.Uint16(ins)
					k := binary.LittleEndian.Uint32(ins[4:])
					switch code {
					case 0x20:
						value = binary.LittleEndian.Uint32(input[k:])
					case 0x54:
						value &= k
					case 0x15, 0x45:
						match := value == k
						if code == 0x45 {
							match = value&k != 0
						}
						if match {
							pc += int(ins[2])
						} else {
							pc += int(ins[3])
						}
					case 0x06:
						if (k == 0x00050001) != c.deny {
							t.Fatalf("ABI %s syscall=%d domain=%d action=%x", abi.arch, c.nr, c.domain, k)
						}
						pc = len(program)
					default:
						t.Fatalf("unexpected BPF opcode %x", code)
					}
				}
			}
		})
	}
	if _, err := bashSocketFilter("386"); err == nil {
		t.Fatal("unsupported ABI must fail closed")
	}
}

func TestBashIsolationDatagramSocketpair(t *testing.T) {
	requireSandboxExec(t)
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	address := filepath.Join(root, "host.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: address, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "probe.test"), data, 0700); err != nil {
		t.Fatal(err)
	}
	config := DefaultSandboxConfig()
	config.RequireSandbox = true
	config.Environment = []string{"GOKIN_DGRAM_SOCKET_HELPER=" + address}
	cmd, err := NewSandboxedCommand(t.Context(), root, "./probe.test -test.run '^TestBashIsolationDatagramSocketpairHelper$'", config)
	if err != nil {
		t.Fatal(err)
	}
	if result := cmd.Run(10 * time.Second); result.Error != nil || result.ExitCode != 0 {
		t.Fatalf("datagram pair escape: %+v stdout=%s stderr=%s", result, result.Stdout, result.Stderr)
	}
}

func TestBashIsolationDatagramSocketpairHelper(t *testing.T) {
	address := os.Getenv("GOKIN_DGRAM_SOCKET_HELPER")
	if address == "" {
		return
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err == unix.EPERM {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	if err := unix.Connect(fds[0], &unix.SockaddrUnix{Name: address}); err == nil {
		t.Fatal("datagram socketpair reconnected to host socket")
	}
}

func TestBashSecretSymlinksCannotBeRebound(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(map[bool]string{false: "secret", true: "parent"}[parent], func(t *testing.T) {
			home, outside := t.TempDir(), t.TempDir()
			link := filepath.Join(home, ".ssh")
			secret := link
			if parent {
				link = filepath.Join(home, ".config")
				secret = filepath.Join(link, "gokin")
				if err := os.Mkdir(filepath.Join(outside, "gokin"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", "true")
			cmd.Dir = home
			t.Cleanup(func() {
				for _, file := range cmd.ExtraFiles {
					file.Close()
				}
			})
			err := wrapBashSandbox(cmd, "unused", []sandboxMount{{home, true}}, []string{secret}, t.TempDir(), false)
			if err == nil || !strings.Contains(err.Error(), "narrower") {
				t.Fatalf("rebindable secret alias was accepted: %v", err)
			}
		})
	}
}
