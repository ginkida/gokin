//go:build linux

package security

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
)

func bashSocketFilter(arch string) ([]byte, error) {
	var audit, socket, pair uint32
	switch arch {
	case "amd64":
		audit, socket, pair = 0xc000003e, 41, 53
	case "arm64":
		audit, socket, pair = 0xc00000b7, 198, 199
	case "riscv64":
		audit, socket, pair = 0xc00000f3, 198, 199
	case "loong64":
		audit, socket, pair = 0xc0000102, 198, 199
	default:
		return nil, fmt.Errorf("AF_UNIX seccomp unavailable on linux/%s", arch)
	}
	var program []byte
	add := func(code uint16, yes, no byte, k uint32) {
		var raw [8]byte
		binary.LittleEndian.PutUint16(raw[:2], code)
		raw[2], raw[3] = yes, no
		binary.LittleEndian.PutUint32(raw[4:], k)
		program = append(program, raw[:]...)
	}
	const deny = 0x00050001 // SECCOMP_RET_ERRNO | EPERM
	add(0x20, 0, 0, 4)      // seccomp_data.arch
	add(0x15, 1, 0, audit)
	add(0x06, 0, 0, deny)
	add(0x20, 0, 0, 0) // seccomp_data.nr
	if arch == "amd64" {
		add(0x45, 0, 1, 0x40000000) // reject the alternate x32 ABI too
		add(0x06, 0, 0, deny)
	}
	add(0x15, 0, 3, socket)
	add(0x20, 0, 0, 16) // socket's domain argument
	add(0x15, 0, 1, 1)  // AF_UNIX
	add(0x06, 0, 0, deny)
	// Datagram pairs can reconnect/sendto filesystem sockets. Keep stream
	// pairs for subprocess IPC, which cannot be retargeted to a host socket.
	add(0x20, 0, 0, 0)
	add(0x15, 0, 6, pair)
	add(0x20, 0, 0, 16)
	add(0x15, 0, 4, 1)
	add(0x20, 0, 0, 24)  // socketpair's type argument
	add(0x54, 0, 0, 0xf) // strip SOCK_CLOEXEC / SOCK_NONBLOCK
	add(0x15, 1, 0, 1)   // SOCK_STREAM
	add(0x06, 0, 0, deny)
	add(0x06, 0, 0, 0x7fff0000)
	return program, nil
}

func openBashSocketFilter(runtimeDir string) (*os.File, error) {
	program, err := bashSocketFilter(runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(runtimeDir, "socket-*.bpf")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(file.Name())
	if _, err = file.Write(program); err == nil {
		_, err = file.Seek(0, 0)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
