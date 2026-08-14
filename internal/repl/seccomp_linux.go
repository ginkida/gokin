//go:build linux

package repl

import (
	"os"
	"runtime"
)

// openWorkerSeccompFilter returns the inherited classic-BPF program consumed
// by bubblewrap's --seccomp FD. Syscall numbers are stable Linux UAPI values;
// unknown architectures fail closed instead of launching without the filter.
func openWorkerSeccompFilter(runtimeDir string) (*os.File, error) {
	denied, order, auditArch, err := workerProcessSyscalls(runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	filter, err := os.CreateTemp(runtimeDir, "worker-seccomp-*.bpf")
	if err != nil {
		return nil, err
	}
	path := filter.Name()
	keep := false
	defer func() {
		if !keep {
			_ = filter.Close()
		}
		_ = os.Remove(path)
	}()
	if err := filter.Chmod(0o600); err != nil {
		return nil, err
	}
	if _, err := filter.Write(buildDenySyscallFilter(order, auditArch, denied)); err != nil {
		return nil, err
	}
	if _, err := filter.Seek(0, 0); err != nil {
		return nil, err
	}
	keep = true
	return filter, nil
}
