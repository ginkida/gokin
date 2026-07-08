package context

import (
	"testing"

	"google.golang.org/genai"
)

// --- FileActivityTracker ---

func TestFileActivityTracker_New(t *testing.T) {
	tr := NewFileActivityTracker()
	if tr == nil {
		t.Fatal("NewFileActivityTracker returned nil")
	}
	if files := tr.GetActiveFiles(10); len(files) != 0 {
		t.Errorf("new tracker GetActiveFiles = %v, want empty", files)
	}
}

func TestFileActivityTracker_RecordToolCall(t *testing.T) {
	tr := NewFileActivityTracker()

	tr.RecordToolCall("read", map[string]any{"file_path": "/a.go"}, 0)
	tr.RecordToolCall("edit", map[string]any{"file_path": "/b.go"}, 1)
	tr.RecordToolCall("read", map[string]any{"file_path": "/a.go"}, 2)

	files := tr.GetActiveFiles(10)
	if len(files) != 2 {
		t.Fatalf("GetActiveFiles = %v, want 2 files", files)
	}
}

func TestFileActivityTracker_RecordToolCall_NoFilePath(t *testing.T) {
	tr := NewFileActivityTracker()
	// No file_path / path key → should be ignored
	tr.RecordToolCall("bash", map[string]any{"command": "ls"}, 0)
	tr.RecordToolCall("read", nil, 1)

	if files := tr.GetActiveFiles(10); len(files) != 0 {
		t.Errorf("GetActiveFiles = %v, want empty (no file paths)", files)
	}
}

func TestFileActivityTracker_RecordToolCall_PathKey(t *testing.T) {
	tr := NewFileActivityTracker()
	// "path" key should also work
	tr.RecordToolCall("tree", map[string]any{"path": "/some/dir"}, 0)

	files := tr.GetActiveFiles(10)
	if len(files) != 1 || files[0] != "/some/dir" {
		t.Errorf("GetActiveFiles = %v, want [/some/dir]", files)
	}
}

func TestFileActivityTracker_GetActiveFiles_OrderedByRecency(t *testing.T) {
	tr := NewFileActivityTracker()

	tr.RecordToolCall("read", map[string]any{"file_path": "/old.go"}, 0)
	tr.RecordToolCall("read", map[string]any{"file_path": "/new.go"}, 5)
	tr.RecordToolCall("read", map[string]any{"file_path": "/middle.go"}, 3)

	files := tr.GetActiveFiles(3)
	// Most recent first: /new.go (5), /middle.go (3), /old.go (0)
	if len(files) != 3 {
		t.Fatalf("GetActiveFiles = %v, want 3", files)
	}
	if files[0] != "/new.go" {
		t.Errorf("first = %q, want /new.go (most recent)", files[0])
	}
	if files[1] != "/middle.go" {
		t.Errorf("second = %q, want /middle.go", files[1])
	}
	if files[2] != "/old.go" {
		t.Errorf("third = %q, want /old.go (oldest)", files[2])
	}
}

func TestFileActivityTracker_GetActiveFiles_Limit(t *testing.T) {
	tr := NewFileActivityTracker()

	for i := 0; i < 5; i++ {
		tr.RecordToolCall("read", map[string]any{"file_path": "/f" + string(rune('0'+i)) + ".go"}, i)
	}

	files := tr.GetActiveFiles(2)
	if len(files) != 2 {
		t.Errorf("GetActiveFiles(2) = %v, want 2 files", files)
	}
}

func TestFileActivityTracker_RecordExternalChange(t *testing.T) {
	tr := NewFileActivityTracker()
	tr.RecordToolCall("read", map[string]any{"file_path": "/a.go"}, 0)

	if tr.hasExternalChange("/a.go") {
		t.Error("should not have external change before recording")
	}

	tr.RecordExternalChange("/a.go")

	if !tr.hasExternalChange("/a.go") {
		t.Error("should have external change after recording")
	}
}

func TestFileActivityTracker_Reset(t *testing.T) {
	tr := NewFileActivityTracker()
	tr.RecordToolCall("read", map[string]any{"file_path": "/a.go"}, 0)
	tr.RecordExternalChange("/a.go")

	tr.Reset()

	if files := tr.GetActiveFiles(10); len(files) != 0 {
		t.Errorf("after Reset, GetActiveFiles = %v, want empty", files)
	}
	if tr.hasExternalChange("/a.go") {
		t.Error("after Reset, should not have external change")
	}
}

// --- RelevanceScorer ---

func TestNewRelevanceScorer(t *testing.T) {
	s := NewRelevanceScorer()
	if s == nil {
		t.Fatal("NewRelevanceScorer returned nil")
	}
}

func TestRelevanceScorer_ScoreMessages_Empty(t *testing.T) {
	s := NewRelevanceScorer()
	scores := s.ScoreMessages(nil, nil)
	if len(scores) != 0 {
		t.Errorf("ScoreMessages(nil) = %v, want empty", scores)
	}
}

func TestRelevanceScorer_ScoreMessages_NilMessage(t *testing.T) {
	s := NewRelevanceScorer()
	history := []*genai.Content{nil}
	scores := s.ScoreMessages(history, nil)
	if len(scores) != 1 || scores[0] != 0 {
		t.Errorf("ScoreMessages with nil msg = %v, want [0]", scores)
	}
}

func TestRelevanceScorer_ScoreMessages_TextOnly(t *testing.T) {
	s := NewRelevanceScorer()
	history := []*genai.Content{
		genai.NewContentFromText("hello", genai.RoleUser),
	}
	scores := s.ScoreMessages(history, nil)
	if len(scores) != 1 {
		t.Fatalf("len = %d, want 1", len(scores))
	}
	// Text-only message: no tool, so baseScore=0; single message so recency=0 (total=1, no ramp)
	// Could still get pairBonus=0, errorBonus=0
	if scores[0] < 0 || scores[0] > 10 {
		t.Errorf("score = %f, want in [0,10]", scores[0])
	}
}

func TestRelevanceScorer_ScoreMessages_FunctionCall(t *testing.T) {
	s := NewRelevanceScorer()
	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						Name: "write",
						Args: map[string]any{"file_path": "/test.go"},
					},
				},
			},
		},
	}
	scores := s.ScoreMessages(history, nil)
	if len(scores) != 1 {
		t.Fatalf("len = %d, want 1", len(scores))
	}
	// write has baseScore 3.0
	if scores[0] < 3.0 {
		t.Errorf("write score = %f, want >= 3.0 (baseToolScore)", scores[0])
	}
}

func TestRelevanceScorer_ScoreMessages_RecencyBonus(t *testing.T) {
	s := NewRelevanceScorer()
	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": "/old.go"}}},
			},
		},
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": "/new.go"}}},
			},
		},
	}
	scores := s.ScoreMessages(history, nil)
	// Both have baseScore 0.5 (read). Newer one (index 1) should score higher due to recency.
	if scores[1] <= scores[0] {
		t.Errorf("newer score %f should be > older score %f (recency bonus)", scores[1], scores[0])
	}
}

func TestRelevanceScorer_ScoreMessages_ErrorBonus(t *testing.T) {
	s := NewRelevanceScorer()
	history := []*genai.Content{
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{
					FunctionResponse: &genai.FunctionResponse{
						Name: "bash",
						Response: map[string]any{
							"error": "command failed",
						},
					},
				},
			},
		},
	}
	scores := s.ScoreMessages(history, nil)
	// Error in FunctionResponse → +1.5
	if scores[0] < 1.5 {
		t.Errorf("error score = %f, want >= 1.5 (errorBonus)", scores[0])
	}
}

func TestRelevanceScorer_ScoreMessages_ErrorBonusFromText(t *testing.T) {
	s := NewRelevanceScorer()
	history := []*genai.Content{
		genai.NewContentFromText("something failed unexpectedly", genai.RoleUser),
	}
	scores := s.ScoreMessages(history, nil)
	// "failed" keyword → +1.0
	if scores[0] < 1.0 {
		t.Errorf("text error score = %f, want >= 1.0", scores[0])
	}
}

func TestRelevanceScorer_ScoreMessages_PairBonus(t *testing.T) {
	s := NewRelevanceScorer()
	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "call1", Name: "read", Args: map[string]any{"file_path": "/x.go"}}},
			},
		},
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{FunctionResponse: &genai.FunctionResponse{ID: "call1", Name: "read"}},
			},
		},
	}
	scores := s.ScoreMessages(history, nil)
	// Both should get pair bonus since call1 has both a call and a response
	if scores[0] < 1.0 {
		t.Errorf("call with matching response score = %f, want >= 1.0 (pair bonus)", scores[0])
	}
	if scores[1] < 1.0 {
		t.Errorf("response with matching call score = %f, want >= 1.0 (pair bonus)", scores[1])
	}
}

func TestRelevanceScorer_ScoreMessages_StalenessDiscount(t *testing.T) {
	s := NewRelevanceScorer()
	tr := NewFileActivityTracker()
	tr.RecordToolCall("read", map[string]any{"file_path": "/a.go"}, 0)
	tr.RecordExternalChange("/a.go")

	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": "/a.go"}}},
			},
		},
	}

	scoresWithTracker := s.ScoreMessages(history, tr)
	scoresNoTracker := s.ScoreMessages(history, nil)

	// With external change recorded, staleness discount applies
	if scoresWithTracker[0] >= scoresNoTracker[0] {
		t.Errorf("with staleness discount score %f should be < without %f", scoresWithTracker[0], scoresNoTracker[0])
	}
}

func TestRelevanceScorer_ScoreMessages_EditProximityBonus(t *testing.T) {
	s := NewRelevanceScorer()
	tr := NewFileActivityTracker()

	// read at index 0, edit at index 1 on same file
	tr.RecordToolCall("read", map[string]any{"file_path": "/a.go"}, 0)
	tr.RecordToolCall("edit", map[string]any{"file_path": "/a.go"}, 1)

	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": "/a.go"}}},
			},
		},
	}

	scores := s.ScoreMessages(history, tr)
	// read followed by nearby edit → proximity bonus (up to 2.0) + base 0.5
	if scores[0] < 0.5 {
		t.Errorf("edit proximity score = %f, want >= 0.5", scores[0])
	}
}

func TestRelevanceScorer_ScoreMessages_ClampedTo10(t *testing.T) {
	s := NewRelevanceScorer()
	// Construct a message that would score very high: write (3.0) + error (1.5) + pair (1.0) + recency max
	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{
					FunctionCall: &genai.FunctionCall{
						ID:   "c1",
						Name: "write",
						Args: map[string]any{"file_path": "/x.go"},
					},
				},
			},
		},
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{
					FunctionResponse: &genai.FunctionResponse{
						ID:   "c1",
						Name: "write",
						Response: map[string]any{
							"error": "disk full",
						},
					},
				},
			},
		},
	}
	scores := s.ScoreMessages(history, nil)
	for i, sc := range scores {
		if sc > 10 {
			t.Errorf("score[%d] = %f, want <= 10 (clamped)", i, sc)
		}
		if sc < 0 {
			t.Errorf("score[%d] = %f, want >= 0 (clamped)", i, sc)
		}
	}
}

func TestRelevanceScorer_ScoreMessage_Single(t *testing.T) {
	s := NewRelevanceScorer()
	msg := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "edit", Args: map[string]any{"file_path": "/a.go"}}},
		},
	}
	score := s.ScoreMessage(msg, 0, 1, nil)
	if score < 3.0 {
		t.Errorf("ScoreMessage edit = %f, want >= 3.0", score)
	}
}

func TestRelevanceScorer_ScoreMessage_NilMsg(t *testing.T) {
	s := NewRelevanceScorer()
	if score := s.ScoreMessage(nil, 0, 1, nil); score != 0 {
		t.Errorf("ScoreMessage(nil) = %f, want 0", score)
	}
}

func TestRelevanceScorer_ScoreMessage_WithIndexOffset(t *testing.T) {
	s := NewRelevanceScorer()
	tr := NewFileActivityTracker()
	tr.RecordToolCall("read", map[string]any{"file_path": "/a.go"}, 5)
	tr.RecordToolCall("edit", map[string]any{"file_path": "/a.go"}, 6)

	// Sub-slice starting at global index 5
	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": "/a.go"}}},
			},
		},
	}
	scores := s.ScoreMessages(history, tr, 5)
	if len(scores) != 1 {
		t.Fatalf("len = %d, want 1", len(scores))
	}
	// With offset, edit proximity should kick in (global index 5 → edit at 6)
	if scores[0] < 0.5 {
		t.Errorf("with offset score = %f, want >= 0.5 (edit proximity)", scores[0])
	}
}

// --- helpers ---

func TestExtractFilePath(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"file_path", map[string]any{"file_path": "/x.go"}, "/x.go"},
		{"path", map[string]any{"path": "/dir"}, "/dir"},
		{"neither", map[string]any{"command": "ls"}, ""},
		{"nil", nil, ""},
		{"empty file_path", map[string]any{"file_path": ""}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractFilePath(c.args); got != c.want {
				t.Errorf("extractFilePath(%+v) = %q, want %q", c.args, got, c.want)
			}
		})
	}
}

func TestIsWriteTool(t *testing.T) {
	writeTools := []string{"write", "edit", "delete", "copy", "move", "mkdir", "bash", "git_add", "git_commit"}
	for _, name := range writeTools {
		if !isWriteTool(name) {
			t.Errorf("isWriteTool(%q) = false, want true", name)
		}
	}
	nonWrite := []string{"read", "grep", "glob", "list_dir", "tree", "ask_user", "", "unknown"}
	for _, name := range nonWrite {
		if isWriteTool(name) {
			t.Errorf("isWriteTool(%q) = true, want false", name)
		}
	}
}

func TestExtractFileRefs(t *testing.T) {
	msg := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": "/a.go"}}},
			{FunctionCall: &genai.FunctionCall{Name: "edit", Args: map[string]any{"file_path": "/b.go"}}},
			{FunctionCall: &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": "/a.go"}}}, // dup
			{Text: "just text"},
			nil,
		},
	}
	refs := extractFileRefs(msg)
	if len(refs) != 2 {
		t.Fatalf("extractFileRefs = %v, want 2 unique paths", refs)
	}
	// Should contain /a.go and /b.go
	hasA, hasB := false, false
	for _, r := range refs {
		if r == "/a.go" {
			hasA = true
		}
		if r == "/b.go" {
			hasB = true
		}
	}
	if !hasA || !hasB {
		t.Errorf("expected /a.go and /b.go in %v", refs)
	}
}

func TestExtractFileRefs_NilMsg(t *testing.T) {
	// extractFileRefs dereferences msg.Parts — nil msg would panic.
	// The caller (ScoreMessages) guards nil messages before calling.
	msg := &genai.Content{Role: genai.RoleUser}
	refs := extractFileRefs(msg)
	if len(refs) != 0 {
		t.Errorf("extractFileRefs on empty msg = %v, want empty", refs)
	}
}

// --- computeBaseToolScore ---

func TestComputeBaseToolScore(t *testing.T) {
	s := NewRelevanceScorer()
	msg := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "read"}},
			{FunctionCall: &genai.FunctionCall{Name: "write"}}, // higher
			{FunctionCall: &genai.FunctionCall{Name: "grep"}},
		},
	}
	if score := s.computeBaseToolScore(msg); score != 3.0 {
		t.Errorf("computeBaseToolScore = %f, want 3.0 (write)", score)
	}
}

func TestComputeBaseToolScore_UnknownTool(t *testing.T) {
	s := NewRelevanceScorer()
	msg := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "unknown_tool"}},
		},
	}
	if score := s.computeBaseToolScore(msg); score != 0 {
		t.Errorf("computeBaseToolScore unknown = %f, want 0", score)
	}
}

func TestComputeBaseToolScore_FunctionResponse(t *testing.T) {
	s := NewRelevanceScorer()
	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{Name: "bash"}},
		},
	}
	if score := s.computeBaseToolScore(msg); score != 2.5 {
		t.Errorf("computeBaseToolScore bash response = %f, want 2.5", score)
	}
}

// --- computeErrorBonus ---

func TestComputeErrorBonus_FunctionResponseError(t *testing.T) {
	s := NewRelevanceScorer()
	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{
				Name:     "bash",
				Response: map[string]any{"error": "exit code 1"},
			}},
		},
	}
	if bonus := s.computeErrorBonus(msg); bonus != 1.5 {
		t.Errorf("computeErrorBonus = %f, want 1.5", bonus)
	}
}

func TestComputeErrorBonus_FunctionResponseEmptyError(t *testing.T) {
	s := NewRelevanceScorer()
	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{
				Name:     "bash",
				Response: map[string]any{"error": ""},
			}},
		},
	}
	if bonus := s.computeErrorBonus(msg); bonus != 0 {
		t.Errorf("computeErrorBonus empty error = %f, want 0", bonus)
	}
}

func TestComputeErrorBonus_TextKeywords(t *testing.T) {
	s := NewRelevanceScorer()
	keywords := []string{"error", "failed", "panic", "exception", "Error", "FAILED"}
	for _, kw := range keywords {
		msg := genai.NewContentFromText("something "+kw+" happened", genai.RoleUser)
		if bonus := s.computeErrorBonus(msg); bonus != 1.0 {
			t.Errorf("computeErrorBonus(%q) = %f, want 1.0", kw, bonus)
		}
	}
}

func TestComputeErrorBonus_NoError(t *testing.T) {
	s := NewRelevanceScorer()
	msg := genai.NewContentFromText("all good here", genai.RoleUser)
	if bonus := s.computeErrorBonus(msg); bonus != 0 {
		t.Errorf("computeErrorBonus (no error) = %f, want 0", bonus)
	}
}

// --- computePairBonus ---

func TestComputePairBonus_CallWithResponse(t *testing.T) {
	s := NewRelevanceScorer()
	msg := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "c1", Name: "read"}},
		},
	}
	responseIDs := map[string]bool{"c1": true}
	if bonus := s.computePairBonus(msg, responseIDs, nil); bonus != 1.0 {
		t.Errorf("computePairBonus = %f, want 1.0", bonus)
	}
}

func TestComputePairBonus_ResponseWithCall(t *testing.T) {
	s := NewRelevanceScorer()
	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{ID: "c1", Name: "read"}},
		},
	}
	callIDs := map[string]bool{"c1": true}
	if bonus := s.computePairBonus(msg, nil, callIDs); bonus != 1.0 {
		t.Errorf("computePairBonus = %f, want 1.0", bonus)
	}
}

func TestComputePairBonus_NoMatch(t *testing.T) {
	s := NewRelevanceScorer()
	msg := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "c2", Name: "read"}},
		},
	}
	responseIDs := map[string]bool{"c1": true}
	if bonus := s.computePairBonus(msg, responseIDs, nil); bonus != 0 {
		t.Errorf("computePairBonus no match = %f, want 0", bonus)
	}
}

func TestComputePairBonus_NilMaps(t *testing.T) {
	s := NewRelevanceScorer()
	msg := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "c1", Name: "read"}},
		},
	}
	if bonus := s.computePairBonus(msg, nil, nil); bonus != 0 {
		t.Errorf("computePairBonus nil maps = %f, want 0", bonus)
	}
}

// --- computeEditProximityBonus ---

func TestComputeEditProximityBonus_ReadThenEdit(t *testing.T) {
	s := NewRelevanceScorer()
	tr := NewFileActivityTracker()
	tr.RecordToolCall("read", map[string]any{"file_path": "/a.go"}, 0)
	tr.RecordToolCall("edit", map[string]any{"file_path": "/a.go"}, 1)

	msg := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": "/a.go"}}},
		},
	}

	tr.mu.RLock()
	bonus := s.computeEditProximityBonus(msg, 0, tr)
	tr.mu.RUnlock()

	if bonus <= 0 {
		t.Errorf("computeEditProximityBonus = %f, want > 0 (read followed by edit)", bonus)
	}
}

func TestComputeEditProximityBonus_NoFiles(t *testing.T) {
	s := NewRelevanceScorer()
	tr := NewFileActivityTracker()
	msg := genai.NewContentFromText("no tools here", genai.RoleUser)

	tr.mu.RLock()
	bonus := s.computeEditProximityBonus(msg, 0, tr)
	tr.mu.RUnlock()

	if bonus != 0 {
		t.Errorf("computeEditProximityBonus (no files) = %f, want 0", bonus)
	}
}

func TestComputeEditProximityBonus_NoEditAfter(t *testing.T) {
	s := NewRelevanceScorer()
	tr := NewFileActivityTracker()
	tr.RecordToolCall("read", map[string]any{"file_path": "/a.go"}, 0)
	// No edit after

	msg := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "read", Args: map[string]any{"file_path": "/a.go"}}},
		},
	}

	tr.mu.RLock()
	bonus := s.computeEditProximityBonus(msg, 0, tr)
	tr.mu.RUnlock()

	if bonus != 0 {
		t.Errorf("computeEditProximityBonus (no edit after) = %f, want 0", bonus)
	}
}
