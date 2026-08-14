package repl

import (
	"encoding/binary"
	"testing"
)

func TestBuildDenySyscallFilterControlFlow(t *testing.T) {
	denied := []uint32{56, 57, 435}
	const auditArch = 0xc000003e
	program := buildDenySyscallFilter(binary.LittleEndian, auditArch, denied)
	wantInstructions := 5 + 2*len(denied)
	if len(program) != wantInstructions*8 {
		t.Fatalf("filter bytes=%d want=%d", len(program), wantInstructions*8)
	}
	read := func(index int) (uint16, uint8, uint8, uint32) {
		raw := program[index*8 : index*8+8]
		return binary.LittleEndian.Uint16(raw[:2]), raw[2], raw[3],
			binary.LittleEndian.Uint32(raw[4:])
	}
	if code, jt, jf, value := read(0); code != bpfLoadWordAbsolute || jt != 0 || jf != 0 || value != seccompDataArch {
		t.Fatalf("arch load instruction=%#x,%d,%d,%d", code, jt, jf, value)
	}
	if code, jt, jf, value := read(1); code != bpfJumpEqual || jt != 1 || jf != 0 || value != auditArch {
		t.Fatalf("arch check instruction=%#x,%d,%d,%#x", code, jt, jf, value)
	}
	if code, _, _, value := read(2); code != bpfReturn || value != seccompReturnErrno|1 {
		t.Fatalf("foreign ABI deny=%#x,%#x", code, value)
	}
	if code, jt, jf, value := read(3); code != bpfLoadWordAbsolute || jt != 0 || jf != 0 || value != 0 {
		t.Fatalf("syscall load instruction=%#x,%d,%d,%d", code, jt, jf, value)
	}
	for offset, syscallNumber := range denied {
		jump := 4 + offset*2
		if code, jt, jf, value := read(jump); code != bpfJumpEqual || jt != 0 || jf != 1 || value != syscallNumber {
			t.Fatalf("jump %d=%#x,%d,%d,%d", offset, code, jt, jf, value)
		}
		if code, _, _, value := read(jump + 1); code != bpfReturn || value != seccompReturnErrno|1 {
			t.Fatalf("deny %d=%#x,%#x", offset, code, value)
		}
	}
	if code, _, _, value := read(wantInstructions - 1); code != bpfReturn || value != seccompReturnAllow {
		t.Fatalf("allow instruction=%#x,%#x", code, value)
	}
}

func TestBuildDenySyscallFilterHonorsByteOrder(t *testing.T) {
	program := buildDenySyscallFilter(binary.BigEndian, 0x80000015, []uint32{0x01020304})
	if got := binary.BigEndian.Uint32(program[36:40]); got != 0x01020304 {
		t.Fatalf("big-endian syscall=%#x", got)
	}
}

func TestWorkerProcessSyscallTablesFailClosedAndCoverModernClone(t *testing.T) {
	for _, arch := range []string{
		"amd64", "386", "arm", "arm64", "riscv64", "loong64",
		"ppc64le", "ppc64", "s390x",
	} {
		t.Run(arch, func(t *testing.T) {
			denied, order, auditArch, err := workerProcessSyscalls(arch)
			if err != nil || order == nil || auditArch == 0 || len(denied) < 3 {
				t.Fatalf("process syscall table=%v order=%T audit_arch=%#x err=%v", denied, order, auditArch, err)
			}
			seenClone3 := false
			seenExecveat := false
			for _, number := range denied {
				seenClone3 = seenClone3 || number == 435
				seenExecveat = seenExecveat || number == 281 || number == 322 ||
					number == 354 || number == 358 || number == 362 || number == 387
			}
			if !seenClone3 || !seenExecveat {
				t.Fatalf("process syscall table lacks clone3/execveat: %v", denied)
			}
		})
	}
	denied, _, _, err := workerProcessSyscalls("amd64")
	if err != nil {
		t.Fatal(err)
	}
	x32Clone := uint32(0x40000000 | 56)
	foundX32 := false
	for _, number := range denied {
		foundX32 = foundX32 || number == x32Clone
	}
	if !foundX32 {
		t.Fatalf("amd64 table permits x32 clone alias: %v", denied)
	}
	if _, _, _, err := workerProcessSyscalls("mips64"); err == nil {
		t.Fatal("unknown Linux architecture did not fail closed")
	}
}
