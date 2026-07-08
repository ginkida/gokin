package ui

import "testing"

// TestDebugState_BackgroundTasksIsADeepCopy (round 8) pins the fix: DebugState
// used to return the LIVE m.backgroundTasks map directly. handleBackgroundTask
// both inserts/deletes map entries AND mutates existing *BackgroundTaskState
// fields (incl. the ToolsUsed slice) in place for progress updates, so a
// caller on a different goroutine holding the "snapshot" could observe
// torn/changing data. This proves the snapshot is now fully independent: a
// map-level mutation (insert/delete) and a struct/slice-field mutation on
// the LIVE state, both performed AFTER taking the snapshot, must not be
// visible in it.
func TestDebugState_BackgroundTasksIsADeepCopy(t *testing.T) {
	m := NewModel()
	m.backgroundTasks["task1"] = &BackgroundTaskState{
		ID:        "task1",
		Status:    "running",
		ToolsUsed: []string{"read"},
	}

	snapshot := m.DebugState()

	// Mutate the LIVE state after taking the snapshot: map insert, map
	// delete, in-place struct field write, in-place slice append.
	m.backgroundTasks["task1"].Status = "completed"
	m.backgroundTasks["task1"].ToolsUsed = append(m.backgroundTasks["task1"].ToolsUsed, "write")
	m.backgroundTasks["task2"] = &BackgroundTaskState{ID: "task2"}
	delete(m.backgroundTasks, "task1")

	if len(snapshot.BackgroundTasks) != 1 {
		t.Fatalf("snapshot map size changed after live mutation: got %d, want 1 (map: %v)", len(snapshot.BackgroundTasks), snapshot.BackgroundTasks)
	}
	task, ok := snapshot.BackgroundTasks["task1"]
	if !ok {
		t.Fatal("snapshot lost task1 after the live map deleted it")
	}
	if task.Status != "running" {
		t.Fatalf("snapshot task1.Status = %q, want %q — live field mutation leaked into the snapshot", task.Status, "running")
	}
	if len(task.ToolsUsed) != 1 || task.ToolsUsed[0] != "read" {
		t.Fatalf("snapshot task1.ToolsUsed = %v, want [read] — live slice mutation leaked into the snapshot", task.ToolsUsed)
	}
}

// TestUpdate_DebugStateRequestMsgDeliversSnapshot pins the message-handling
// wiring: sending DebugStateRequestMsg through Update must deliver a
// UIDebugState reflecting current Model state via the response channel.
func TestUpdate_DebugStateRequestMsgDeliversSnapshot(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.processingLabel = "test-label"

	respCh := make(chan UIDebugState, 1)
	m.Update(DebugStateRequestMsg{Resp: respCh})

	select {
	case state := <-respCh:
		if state.ProcessingLabel != "test-label" {
			t.Fatalf("ProcessingLabel = %q, want %q", state.ProcessingLabel, "test-label")
		}
	default:
		t.Fatal("DebugStateRequestMsg did not deliver a response synchronously")
	}
}

// TestUpdate_DebugStateRequestMsgNilRespDoesNotPanic guards the defensive
// nil-check — a request with no response channel must be a safe no-op, not
// a nil-channel-send panic.
func TestUpdate_DebugStateRequestMsgNilRespDoesNotPanic(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.Update(DebugStateRequestMsg{Resp: nil})
}
