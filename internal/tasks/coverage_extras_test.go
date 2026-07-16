package tasks

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// --- Manager.GetInfo (0% → 100%) ---

func TestManager_GetInfo_NotFound(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, ok := m.GetInfo("nonexistent"); ok {
		t.Fatal("GetInfo on non-existent task should return false")
	}
}

func TestManager_GetInfo_Found(t *testing.T) {
	m := NewManager(t.TempDir())
	id, err := m.Start(context.Background(), "echo hello")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	info, ok := m.GetInfo(id)
	if !ok {
		t.Fatal("GetInfo should find the task")
	}
	if info.ID != id {
		t.Fatalf("info.ID = %q, want %q", info.ID, id)
	}
}

// --- Manager.CancelAll (0% → 100%) ---

func TestManager_CancelAll_Empty(t *testing.T) {
	m := NewManager(t.TempDir())
	m.CancelAll()
	if m.RunningCount() != 0 {
		t.Fatalf("RunningCount after CancelAll on empty = %d, want 0", m.RunningCount())
	}
}

func TestManager_CancelAll_WithTasks(t *testing.T) {
	m := NewManager(t.TempDir())
	// Start a long-running task
	id, err := m.Start(context.Background(), "sleep 30")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for it to start
	time.Sleep(100 * time.Millisecond)

	if m.RunningCount() != 1 {
		t.Fatalf("RunningCount = %d, want 1", m.RunningCount())
	}

	m.CancelAll()

	// Give it time to cancel
	time.Sleep(100 * time.Millisecond)
	if m.RunningCount() != 0 {
		t.Fatalf("RunningCount after CancelAll = %d, want 0", m.RunningCount())
	}
	_ = id
}

// --- Manager.Cancel not found (71.4% → 100%) ---

func TestManager_Cancel_NotFound(t *testing.T) {
	m := NewManager(t.TempDir())
	if err := m.Cancel("nonexistent"); err == nil {
		t.Fatal("Cancel on non-existent should return error")
	}
}

// --- safeBuffer.TotalBytes (0% → 100%) ---

func TestSafeBuffer_TotalBytes(t *testing.T) {
	var b safeBuffer
	if b.TotalBytes() != 0 {
		t.Fatal("TotalBytes should be 0 initially")
	}

	b.Write([]byte("hello"))
	if b.TotalBytes() != 5 {
		t.Fatalf("TotalBytes = %d, want 5", b.TotalBytes())
	}

	b.Write([]byte(" world"))
	if b.TotalBytes() != 11 {
		t.Fatalf("TotalBytes = %d, want 11", b.TotalBytes())
	}
}

// --- killProcessGroup nil safety (0% → 100%) ---

func TestKillProcessGroup_NilCmd(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("killProcessGroup(nil) panicked: %v", r)
		}
	}()
	killProcessGroup(nil)
}

func TestKillProcessGroup_NilProcess(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("killProcessGroup(unstarted cmd) panicked: %v", r)
		}
	}()
	cmd := &exec.Cmd{}
	killProcessGroup(cmd)
}

// --- Manager.Count and RunningCount ---

func TestManager_Count(t *testing.T) {
	m := NewManager(t.TempDir())
	if m.Count() != 0 {
		t.Fatalf("Count = %d, want 0", m.Count())
	}

	m.Start(context.Background(), "echo test")
	time.Sleep(100 * time.Millisecond)

	if m.Count() != 1 {
		t.Fatalf("Count = %d, want 1", m.Count())
	}
}

// --- Manager.List ---

func TestManager_List_Empty(t *testing.T) {
	m := NewManager(t.TempDir())
	if list := m.List(); len(list) != 0 {
		t.Fatalf("List = %d, want 0", len(list))
	}
}

func TestManager_List_WithTasks(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Start(context.Background(), "echo a")
	m.Start(context.Background(), "echo b")
	time.Sleep(100 * time.Millisecond)

	if list := m.List(); len(list) != 2 {
		t.Fatalf("List = %d, want 2", len(list))
	}
}

// --- Manager.GetOutput ---

func TestManager_GetOutput_NotFound(t *testing.T) {
	m := NewManager(t.TempDir())
	if _, ok := m.GetOutput("nonexistent"); ok {
		t.Fatal("GetOutput on non-existent should return false")
	}
}
