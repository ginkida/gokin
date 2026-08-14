//go:build linux

package repl

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

const seccompRuntimeHelperEnv = "GOKIN_REPL_SECCOMP_RUNTIME_HELPER"

// TestWorkerSeccompFilterRuntime runs the actual cBPF program in a disposable
// test process. The outer process remains unrestricted so the Go test harness
// can continue; the helper proves the serialized filter is accepted by the
// running Linux kernel and denies real process creation, not merely that its
// instruction bytes look plausible.
func TestWorkerSeccompFilterRuntime(t *testing.T) {
	if mode := os.Getenv(seccompRuntimeHelperEnv); mode != "" {
		runWorkerSeccompRuntimeHelper(mode)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerSeccompFilterRuntime$")
	cmd.Env = append(os.Environ(), seccompRuntimeHelperEnv+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("seccomp runtime helper: %v\n%s", err, output)
	}
	if string(output) != "seccomp-runtime-ok\n" {
		t.Fatalf("seccomp runtime helper output=%q", output)
	}
}

func TestWorkerSeccompFilterAllowsPythonStartupButNotFork(t *testing.T) {
	if mode := os.Getenv(seccompRuntimeHelperEnv); mode != "" {
		runWorkerSeccompRuntimeHelper(mode)
		return
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerSeccompFilterAllowsPythonStartupButNotFork$")
	cmd.Env = append(os.Environ(), seccompRuntimeHelperEnv+"=python")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("seccomp Python helper: %v\n%s", err, output)
	}
	if string(output) != "python-seccomp-ok\n" {
		t.Fatalf("seccomp Python helper output=%q", output)
	}
}

func runWorkerSeccompRuntimeHelper(mode string) {
	runtime.LockOSThread()
	python := ""
	if mode == "python" {
		var err error
		python, err = exec.LookPath("python3")
		if err != nil {
			seccompHelperFail("find python3", err)
		}
	}
	denied, order, auditArch, err := workerProcessSyscalls(runtime.GOARCH)
	if err != nil {
		seccompHelperFail("syscall table", err)
	}
	raw := buildDenySyscallFilter(order, auditArch, denied)
	filters := make([]unix.SockFilter, len(raw)/8)
	for index := range filters {
		item := raw[index*8 : index*8+8]
		filters[index] = unix.SockFilter{
			Code: order.Uint16(item[0:2]), Jt: item[2], Jf: item[3],
			K: order.Uint32(item[4:8]),
		}
	}
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		seccompHelperFail("PR_SET_NO_NEW_PRIVS", err)
	}
	if err := unix.Prctl(
		unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER,
		uintptr(unsafe.Pointer(&program)), 0, 0,
	); err != nil {
		seccompHelperFail("PR_SET_SECCOMP", err)
	}
	if mode == "python" {
		code := `import errno, os
try:
    pid = os.fork()
except OSError as exc:
    if exc.errno != errno.EPERM:
        raise
else:
    if pid == 0:
        os._exit(0)
    os.waitpid(pid, 0)
    raise RuntimeError("fork unexpectedly succeeded")
print("python-seccomp-ok")`
		if err := unix.Exec(python, []string{python, "-I", "-c", code}, os.Environ()); err != nil {
			seccompHelperFail("exec Python", err)
		}
		seccompHelperFail("exec Python returned", nil)
	}

	// os/exec chooses clone/vfork/clone3 according to the Go version and host.
	// Whichever route it selects must fail before /bin/true exists as a child.
	if err := exec.Command("/bin/true").Run(); !errors.Is(err, syscall.EPERM) {
		seccompHelperFail("process creation was not denied", err)
	}
	// execveat is not used by the Go launcher above; exercise it explicitly.
	_, _, errno := unix.RawSyscall6(
		unix.SYS_EXECVEAT,
		^uintptr(0), uintptr(unsafe.Pointer(nil)), uintptr(unsafe.Pointer(nil)),
		uintptr(unsafe.Pointer(nil)), 0, 0,
	)
	if errno != syscall.EPERM {
		seccompHelperFail("execveat was not denied", errno)
	}
	fmt.Fprintln(os.Stdout, "seccomp-runtime-ok")
	os.Exit(0)
}

func seccompHelperFail(stage string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", stage, err)
	os.Exit(2)
}
