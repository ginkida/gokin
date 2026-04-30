package chat

import (
	"testing"

	"google.golang.org/genai"
)

func TestNewSession(t *testing.T) {
	s := NewSession()
	if s.ID == "" {
		t.Error("ID should be generated")
	}
	if s.StartTime.IsZero() {
		t.Error("StartTime should be set")
	}
	if len(s.History) != 0 {
		t.Error("History should be empty")
	}
}

func TestSessionAddMessages(t *testing.T) {
	s := NewSession()

	s.AddUserMessage("hello")
	if s.MessageCount() != 1 {
		t.Errorf("MessageCount = %d, want 1", s.MessageCount())
	}

	s.AddModelMessage("hi there")
	if s.MessageCount() != 2 {
		t.Errorf("MessageCount = %d, want 2", s.MessageCount())
	}

	history := s.GetHistory()
	if len(history) != 2 {
		t.Fatalf("GetHistory len = %d", len(history))
	}
	if history[0].Role != string(genai.RoleUser) {
		t.Errorf("first message role = %q", history[0].Role)
	}
	if history[1].Role != string(genai.RoleModel) {
		t.Errorf("second message role = %q", history[1].Role)
	}
}

func TestSessionAddContent(t *testing.T) {
	s := NewSession()
	content := genai.NewContentFromText("test", genai.RoleUser)
	s.AddContent(content)

	if s.MessageCount() != 1 {
		t.Errorf("MessageCount = %d", s.MessageCount())
	}
}

func TestSessionClear(t *testing.T) {
	s := NewSession()
	s.AddUserMessage("msg1")
	s.AddModelMessage("msg2")
	s.Clear()

	if s.MessageCount() != 0 {
		t.Errorf("MessageCount after Clear = %d", s.MessageCount())
	}
}

func TestSessionGetHistoryCopy(t *testing.T) {
	s := NewSession()
	s.AddUserMessage("original")

	history := s.GetHistory()
	// Modifying the copy should not affect the session
	history = append(history, genai.NewContentFromText("extra", genai.RoleUser))

	if s.MessageCount() != 1 {
		t.Error("modifying GetHistory copy should not affect session")
	}
}

func TestSessionVersion(t *testing.T) {
	s := NewSession()
	v0 := s.GetVersion()

	s.AddUserMessage("msg")
	v1 := s.GetVersion()
	if v1 <= v0 {
		t.Error("version should increment on AddUserMessage")
	}

	s.AddModelMessage("reply")
	v2 := s.GetVersion()
	if v2 <= v1 {
		t.Error("version should increment on AddModelMessage")
	}

	s.Clear()
	v3 := s.GetVersion()
	if v3 <= v2 {
		t.Error("version should increment on Clear")
	}
}

func TestSessionGetHistoryWithVersion(t *testing.T) {
	s := NewSession()
	s.AddUserMessage("msg")

	history, version := s.GetHistoryWithVersion()
	if len(history) != 1 {
		t.Errorf("history len = %d", len(history))
	}
	if version != s.GetVersion() {
		t.Error("version should match")
	}
}

func TestSessionSetHistoryIfVersion(t *testing.T) {
	s := NewSession()
	s.AddUserMessage("msg1")
	v := s.GetVersion()

	// Correct version
	newHistory := []*genai.Content{
		genai.NewContentFromText("new", genai.RoleUser),
	}
	ok := s.SetHistoryIfVersion(newHistory, v)
	if !ok {
		t.Error("should succeed with correct version")
	}

	// Wrong version
	ok = s.SetHistoryIfVersion(newHistory, v) // v is now stale
	if ok {
		t.Error("should fail with stale version")
	}
}

func TestSessionSetHistory(t *testing.T) {
	s := NewSession()

	history := make([]*genai.Content, 0)
	for i := 0; i < 5; i++ {
		history = append(history, genai.NewContentFromText("msg", genai.RoleUser))
	}
	s.SetHistory(history)

	if s.MessageCount() != 5 {
		t.Errorf("MessageCount = %d, want 5", s.MessageCount())
	}
}

func TestSessionSetHistoryTrimming(t *testing.T) {
	s := NewSession()

	// Create history exceeding MaxMessages
	history := make([]*genai.Content, MaxMessages+20)
	for i := range history {
		history[i] = genai.NewContentFromText("msg", genai.RoleUser)
	}
	s.SetHistory(history)

	if s.MessageCount() > MaxMessages {
		t.Errorf("MessageCount = %d, should be <= %d", s.MessageCount(), MaxMessages)
	}
}

// TestSetHistoryTokenCountsInvariant guards the CLAUDE.md invariant:
// len(tokenCounts) == len(History) after every SetHistory/SetHistoryIfVersion call.
// Previously both methods used make([]int, 0) regardless of history length.
func TestSetHistoryTokenCountsInvariant(t *testing.T) {
	t.Run("SetHistory", func(t *testing.T) {
		s := NewSession()
		history := make([]*genai.Content, 5)
		for i := range history {
			history[i] = genai.NewContentFromText("msg", genai.RoleUser)
		}
		s.SetHistory(history)

		hist := s.GetHistory()
		counts := s.GetTokenCounts()
		if len(hist) != len(counts) {
			t.Errorf("SetHistory: len(history)=%d, len(tokenCounts)=%d — invariant violated", len(hist), len(counts))
		}
	})

	t.Run("SetHistoryIfVersion", func(t *testing.T) {
		s := NewSession()
		s.AddUserMessage("seed")
		v := s.GetVersion()

		history := make([]*genai.Content, 3)
		for i := range history {
			history[i] = genai.NewContentFromText("msg", genai.RoleUser)
		}
		s.SetHistoryIfVersion(history, v)

		hist := s.GetHistory()
		counts := s.GetTokenCounts()
		if len(hist) != len(counts) {
			t.Errorf("SetHistoryIfVersion: len(history)=%d, len(tokenCounts)=%d — invariant violated", len(hist), len(counts))
		}
	})
}

// TestRestoreFromState_LegacyEmptyTokenCounts guards the CLAUDE.md invariant
// for the restore path. Sessions saved before v0.78.32 had TokenCounts=nil
// (omitempty + empty slice), so RestoreFromState must pad with zeros rather
// than blindly copying the (shorter) saved slice.
func TestRestoreFromState_LegacyEmptyTokenCounts(t *testing.T) {
	s := NewSession()
	s.AddUserMessage("hello")
	s.AddUserMessage("world")

	state := s.GetState()
	// Simulate a pre-v0.78.32 save: zero out TokenCounts like the old code did.
	state.TokenCounts = nil

	s2 := NewSession()
	if err := s2.RestoreFromState(state); err != nil {
		t.Fatalf("RestoreFromState: %v", err)
	}

	hist := s2.GetHistory()
	counts := s2.GetTokenCounts()
	if len(hist) != len(counts) {
		t.Errorf("after legacy restore: len(history)=%d, len(tokenCounts)=%d — invariant violated", len(hist), len(counts))
	}
}

func TestSessionTokens(t *testing.T) {
	s := NewSession()

	if s.GetTokenCount() != 0 {
		t.Errorf("initial tokens = %d", s.GetTokenCount())
	}

	s.SetTotalTokens(1000)
	if s.GetTokenCount() != 1000 {
		t.Errorf("tokens = %d, want 1000", s.GetTokenCount())
	}
}

func TestSessionAddContentWithTokens(t *testing.T) {
	s := NewSession()

	content := genai.NewContentFromText("msg", genai.RoleUser)
	s.AddContentWithTokens(content, 50)

	if s.GetTokenCount() != 50 {
		t.Errorf("tokens = %d, want 50", s.GetTokenCount())
	}

	counts := s.GetTokenCounts()
	if len(counts) != 1 || counts[0] != 50 {
		t.Errorf("token counts = %v", counts)
	}
}

func TestSessionScratchpad(t *testing.T) {
	s := NewSession()

	if s.GetScratchpad() != "" {
		t.Error("initial scratchpad should be empty")
	}

	s.SetScratchpad("notes here")
	if s.GetScratchpad() != "notes here" {
		t.Errorf("scratchpad = %q", s.GetScratchpad())
	}
}

func TestSessionWorkDir(t *testing.T) {
	s := NewSession()
	s.SetWorkDir("/workspace")
	if s.WorkDir != "/workspace" {
		t.Errorf("WorkDir = %q", s.WorkDir)
	}
}

func TestSessionFork(t *testing.T) {
	s := NewSession()
	s.AddUserMessage("original")
	s.SetScratchpad("notes")

	branch := s.Fork("experiment")
	if branch == nil {
		t.Fatal("Fork should return non-nil")
	}
	if branch.MessageCount() != 1 {
		t.Errorf("branch should have same history, got %d", branch.MessageCount())
	}

	// Changes to branch should not affect parent
	branch.AddUserMessage("branch msg")
	if s.MessageCount() != 1 {
		t.Error("branch changes should not affect parent")
	}
}

func TestSessionGetBranch(t *testing.T) {
	s := NewSession()
	s.Fork("exp1")

	branch, ok := s.GetBranch("exp1")
	if !ok || branch == nil {
		t.Error("should find branch")
	}

	_, ok = s.GetBranch("nonexistent")
	if ok {
		t.Error("should not find nonexistent branch")
	}
}

func TestSessionListBranches(t *testing.T) {
	s := NewSession()
	branches := s.ListBranches()
	if branches != nil {
		t.Error("no branches should return nil")
	}

	s.Fork("b")
	s.Fork("a")
	branches = s.ListBranches()
	if len(branches) != 2 {
		t.Fatalf("branches = %d", len(branches))
	}
	// Should be sorted
	if branches[0] != "a" || branches[1] != "b" {
		t.Errorf("branches = %v, want [a, b]", branches)
	}
}

func TestSessionCheckpoints(t *testing.T) {
	s := NewSession()
	s.AddUserMessage("msg1")
	s.AddModelMessage("reply1")

	s.SaveCheckpoint("after-init")

	s.AddUserMessage("msg2")
	s.AddModelMessage("reply2")

	if s.MessageCount() != 4 {
		t.Fatalf("MessageCount = %d, want 4", s.MessageCount())
	}

	// Restore checkpoint
	ok := s.RestoreCheckpoint("after-init")
	if !ok {
		t.Fatal("RestoreCheckpoint should succeed")
	}
	if s.MessageCount() != 2 {
		t.Errorf("MessageCount after restore = %d, want 2", s.MessageCount())
	}

	// Non-existent checkpoint
	ok = s.RestoreCheckpoint("nonexistent")
	if ok {
		t.Error("should fail for nonexistent checkpoint")
	}
}

func TestSessionListCheckpoints(t *testing.T) {
	s := NewSession()
	cps := s.ListCheckpoints()
	if cps != nil {
		t.Error("no checkpoints should return nil")
	}

	s.AddUserMessage("msg1")
	s.SaveCheckpoint("first")
	s.AddUserMessage("msg2")
	s.SaveCheckpoint("second")

	cps = s.ListCheckpoints()
	if len(cps) != 2 {
		t.Fatalf("checkpoints = %d", len(cps))
	}
	// Should be sorted by index
	if cps[0] != "first" || cps[1] != "second" {
		t.Errorf("checkpoints = %v", cps)
	}
}

func TestSessionRestoreCheckpointCleansLater(t *testing.T) {
	s := NewSession()
	s.AddUserMessage("msg1")
	s.SaveCheckpoint("cp1")
	s.AddUserMessage("msg2")
	s.SaveCheckpoint("cp2")
	s.AddUserMessage("msg3")
	s.SaveCheckpoint("cp3")

	// Restore to cp1 should remove cp2 and cp3
	s.RestoreCheckpoint("cp1")
	cps := s.ListCheckpoints()
	if len(cps) != 1 {
		t.Errorf("should only have cp1 after restore, got %v", cps)
	}
}

func TestSessionChangeHandler(t *testing.T) {
	s := NewSession()

	var events []ChangeEvent
	s.SetChangeHandler(func(e ChangeEvent) {
		events = append(events, e)
	})

	s.AddUserMessage("msg1")
	s.AddModelMessage("reply")
	s.Clear()

	if len(events) != 3 {
		t.Errorf("events = %d, want 3", len(events))
	}
	// First event: 0 -> 1
	if events[0].OldCount != 0 || events[0].NewCount != 1 {
		t.Errorf("event[0] = %+v", events[0])
	}
	// Last event: Clear -> 0
	if events[2].NewCount != 0 {
		t.Errorf("clear event NewCount = %d", events[2].NewCount)
	}
}

func TestSessionExportMarkdown(t *testing.T) {
	s := NewSession()
	s.SetWorkDir("/workspace")
	s.AddUserMessage("hello world")
	s.AddModelMessage("hi there")

	md := s.ExportMarkdown()
	if md == "" {
		t.Fatal("export should not be empty")
	}

	for _, expected := range []string{"# Session", "User", "Assistant", "hello world", "hi there"} {
		if !containsStr(md, expected) {
			t.Errorf("markdown missing %q", expected)
		}
	}
}

func TestSessionExportJSON(t *testing.T) {
	s := NewSession()
	s.AddUserMessage("test message")

	data, err := s.ExportJSON()
	if err != nil {
		t.Fatalf("ExportJSON: %v", err)
	}
	if len(data) == 0 {
		t.Error("JSON export should not be empty")
	}

	json := string(data)
	if !containsStr(json, "test message") {
		t.Error("JSON should contain message")
	}
}

func TestGenerateSessionID(t *testing.T) {
	id1 := generateSessionID()
	id2 := generateSessionID()
	if id1 == id2 {
		t.Error("IDs should be unique")
	}
	if len(id1) < 10 {
		t.Errorf("ID too short: %q", id1)
	}
}

func TestSessionToolCheckpointPersistence(t *testing.T) {
	s := NewSession()
	s.AddUserMessage("test message")

	// Set tool checkpoints
	checkpoints := []SerializedToolCheckpoint{
		{CallID: "c1", ToolName: "write", Args: map[string]any{"file": "a.go"}, Signature: "sig1"},
		{CallID: "c2", ToolName: "edit", Args: map[string]any{"file": "b.go"}, Signature: "sig2"},
	}
	s.SetToolCheckpoints(checkpoints)

	// Get state
	state := s.GetState()
	if len(state.ToolCheckpoints) != 2 {
		t.Fatalf("state ToolCheckpoints = %d, want 2", len(state.ToolCheckpoints))
	}
	if state.ToolCheckpoints[0].ToolName != "write" {
		t.Errorf("checkpoint[0].ToolName = %q", state.ToolCheckpoints[0].ToolName)
	}

	// Restore to new session
	s2 := NewSession()
	if err := s2.RestoreFromState(state); err != nil {
		t.Fatalf("RestoreFromState: %v", err)
	}

	restored := s2.GetToolCheckpoints()
	if len(restored) != 2 {
		t.Fatalf("restored checkpoints = %d, want 2", len(restored))
	}
	if restored[1].CallID != "c2" || restored[1].ToolName != "edit" {
		t.Errorf("restored[1] = %+v", restored[1])
	}
}

func TestSessionToolCheckpointEmpty(t *testing.T) {
	s := NewSession()
	checkpoints := s.GetToolCheckpoints()
	if len(checkpoints) != 0 {
		t.Errorf("empty session should have 0 checkpoints, got %d", len(checkpoints))
	}

	// GetState with no checkpoints
	state := s.GetState()
	if state.ToolCheckpoints != nil {
		t.Error("empty session state should have nil ToolCheckpoints")
	}
}

func TestSessionToolCheckpointCopy(t *testing.T) {
	s := NewSession()
	checkpoints := []SerializedToolCheckpoint{
		{CallID: "c1", ToolName: "write"},
	}
	s.SetToolCheckpoints(checkpoints)

	// Modify original — should not affect session
	checkpoints[0].ToolName = "modified"
	got := s.GetToolCheckpoints()
	if got[0].ToolName != "write" {
		t.Error("SetToolCheckpoints should copy, not store reference")
	}

	// Modify returned — should not affect session
	got[0].ToolName = "modified2"
	got2 := s.GetToolCheckpoints()
	if got2[0].ToolName != "write" {
		t.Error("GetToolCheckpoints should return a copy")
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
