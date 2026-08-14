package repl

import (
	"encoding/binary"
	"fmt"
)

const (
	bpfLoadWordAbsolute = 0x20
	bpfJumpEqual        = 0x15
	bpfReturn           = 0x06
	seccompReturnErrno  = 0x00050000
	seccompReturnAllow  = 0x7fff0000
	seccompDataArch     = 4
)

// buildDenySyscallFilter serializes Linux classic-BPF sock_filter records.
// seccomp_data.nr is the first 32-bit word, so each equality match falls
// through to EPERM while a mismatch skips that return and tests the next
// syscall. The final instruction permits everything else.
func buildDenySyscallFilter(order binary.ByteOrder, auditArch uint32, denied []uint32) []byte {
	program := make([]byte, 0, (5+2*len(denied))*8)
	appendFilter := func(code uint16, jumpTrue, jumpFalse uint8, value uint32) {
		var raw [8]byte
		order.PutUint16(raw[0:2], code)
		raw[2] = jumpTrue
		raw[3] = jumpFalse
		order.PutUint32(raw[4:8], value)
		program = append(program, raw[:]...)
	}
	// Reject every compat/foreign syscall ABI before inspecting its syscall
	// number. This closes x86 int80/x32 and ARM compat paths without accidentally
	// interpreting their numbering as the native architecture.
	appendFilter(bpfLoadWordAbsolute, 0, 0, seccompDataArch)
	appendFilter(bpfJumpEqual, 1, 0, auditArch)
	appendFilter(bpfReturn, 0, 0, seccompReturnErrno|1) // EPERM
	appendFilter(bpfLoadWordAbsolute, 0, 0, 0)
	for _, syscallNumber := range denied {
		appendFilter(bpfJumpEqual, 0, 1, syscallNumber)
		appendFilter(bpfReturn, 0, 0, seccompReturnErrno|1) // EPERM
	}
	appendFilter(bpfReturn, 0, 0, seccompReturnAllow)
	return program
}

func workerProcessSyscalls(arch string) ([]uint32, binary.ByteOrder, uint32, error) {
	// Order: clone, fork (when distinct), vfork (when distinct), execveat,
	// clone3. Modern architectures implement fork/vfork through clone only.
	switch arch {
	case "amd64":
		return []uint32{
			56, 57, 58, 322, 435,
			0x40000000 | 56, 0x40000000 | 57, 0x40000000 | 58,
			0x40000000 | 322, 0x40000000 | 435,
		}, binary.LittleEndian, 0xc000003e, nil
	case "386":
		return []uint32{120, 2, 190, 358, 435}, binary.LittleEndian, 0x40000003, nil
	case "arm":
		return []uint32{120, 2, 190, 387, 435}, binary.LittleEndian, 0x40000028, nil
	case "arm64":
		return []uint32{220, 281, 435}, binary.LittleEndian, 0xc00000b7, nil
	case "riscv64":
		return []uint32{220, 281, 435}, binary.LittleEndian, 0xc00000f3, nil
	case "loong64":
		return []uint32{220, 281, 435}, binary.LittleEndian, 0xc0000102, nil
	case "ppc64le":
		return []uint32{120, 2, 189, 362, 435}, binary.LittleEndian, 0xc0000015, nil
	case "ppc64":
		return []uint32{120, 2, 189, 362, 435}, binary.BigEndian, 0x80000015, nil
	case "s390x":
		return []uint32{120, 2, 190, 354, 435}, binary.BigEndian, 0x80000016, nil
	default:
		return nil, nil, 0, fmt.Errorf("unsupported Linux seccomp architecture %q", arch)
	}
}
