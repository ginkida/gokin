//go:build darwin && cgo

package repl

/*
#cgo LDFLAGS: -lproc
#include <libproc.h>
#include <stdint.h>
#include <sys/resource.h>

static int gokin_proc_resident_bytes(int pid, uint64_t *resident) {
	struct rusage_info_v2 usage = {0};
	if (proc_pid_rusage(pid, RUSAGE_INFO_V2, (rusage_info_t *)&usage) != 0) {
		return -1;
	}
	*resident = usage.ri_resident_size;
	return 0;
}
*/
import "C"

import "fmt"

func residentMemorySupported() bool { return true }

func residentMemoryBytes(pid int) (uint64, error) {
	var resident C.uint64_t
	if result := C.gokin_proc_resident_bytes(C.int(pid), &resident); result != 0 {
		return 0, fmt.Errorf("query resident memory for pid %d", pid)
	}
	return uint64(resident), nil
}
