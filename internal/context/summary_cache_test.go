package context

import (
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestHashMessagesIgnoresNilMessagesAndParts(t *testing.T) {
	messages := []*genai.Content{
		nil,
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				nil,
				genai.NewPartFromText("hello"),
			},
		},
	}

	hashA := HashMessages(messages)
	hashB := HashMessages(messages)

	if hashA == "" {
		t.Fatal("hash should not be empty")
	}
	if hashA != hashB {
		t.Fatalf("hash should be stable, got %q and %q", hashA, hashB)
	}
}

// --- SummaryCache ---

func TestSummaryCache_PutAndGet(t *testing.T) {
	c := NewSummaryCache(10, time.Minute)
	if c.Size() != 0 {
		t.Fatalf("Size = %d, want 0", c.Size())
	}

	summary := genai.NewContentFromText("summary text", genai.RoleModel)
	c.Put("hash1", summary, 100, 0, 5)

	if c.Size() != 1 {
		t.Errorf("Size = %d, want 1", c.Size())
	}

	got, ok := c.Get("hash1")
	if !ok {
		t.Fatal("Get returned not ok")
	}
	if got.TokenCount != 100 {
		t.Errorf("TokenCount = %d, want 100", got.TokenCount)
	}
	if got.MessageRange.Start != 0 || got.MessageRange.End != 5 {
		t.Errorf("MessageRange = %+v, want {0,5}", got.MessageRange)
	}
}

func TestSummaryCache_GetMissing(t *testing.T) {
	c := NewSummaryCache(10, time.Minute)
	_, ok := c.Get("nonexistent")
	if ok {
		t.Error("Get on missing key should return false")
	}
}

func TestSummaryCache_PutDuplicateIgnored(t *testing.T) {
	c := NewSummaryCache(10, time.Minute)
	c.Put("hash1", genai.NewContentFromText("first", genai.RoleModel), 100, 0, 5)
	c.Put("hash1", genai.NewContentFromText("second", genai.RoleModel), 200, 6, 10)

	// Second Put on existing key should be ignored
	got, ok := c.Get("hash1")
	if !ok {
		t.Fatal("Get returned not ok")
	}
	if got.TokenCount != 100 {
		t.Errorf("TokenCount = %d, want 100 (original, duplicate ignored)", got.TokenCount)
	}
}

func TestSummaryCache_TTLExpiry(t *testing.T) {
	c := NewSummaryCache(10, 10*time.Millisecond)
	c.Put("hash1", genai.NewContentFromText("x", genai.RoleModel), 50, 0, 1)

	time.Sleep(20 * time.Millisecond)

	_, ok := c.Get("hash1")
	if ok {
		t.Error("Get should return false after TTL expiry")
	}
	if c.Size() != 0 {
		t.Errorf("Size = %d, want 0 after expired entry is observed", c.Size())
	}
}

func TestSummaryCache_TTLZeroNeverExpires(t *testing.T) {
	c := NewSummaryCache(10, 0) // no TTL
	c.Put("hash1", genai.NewContentFromText("x", genai.RoleModel), 50, 0, 1)

	time.Sleep(20 * time.Millisecond)

	_, ok := c.Get("hash1")
	if !ok {
		t.Error("Get should return true with TTL=0 (never expires)")
	}
}

func TestSummaryCache_ZeroCapacityDoesNotStore(t *testing.T) {
	c := NewSummaryCache(0, time.Minute)
	c.Put("hash1", genai.NewContentFromText("x", genai.RoleModel), 50, 0, 1)

	if c.Size() != 0 {
		t.Errorf("Size = %d, want 0 for zero-capacity cache", c.Size())
	}
	if _, ok := c.Get("hash1"); ok {
		t.Error("Get should return false for zero-capacity cache")
	}
}

func TestSummaryCache_LRUEviction(t *testing.T) {
	c := NewSummaryCache(2, time.Minute)

	c.Put("h1", genai.NewContentFromText("1", genai.RoleModel), 10, 0, 1)
	c.Put("h2", genai.NewContentFromText("2", genai.RoleModel), 10, 0, 1)

	// Access h1 to make it more recent than h2
	_, _ = c.Get("h1")

	// Put h3 → should evict h2 (the LRU)
	c.Put("h3", genai.NewContentFromText("3", genai.RoleModel), 10, 0, 1)

	if _, ok := c.Get("h2"); ok {
		t.Error("h2 should have been evicted (LRU)")
	}
	if _, ok := c.Get("h1"); !ok {
		t.Error("h1 should still be present (recently used)")
	}
	if _, ok := c.Get("h3"); !ok {
		t.Error("h3 should be present")
	}
}

func TestSummaryCache_Invalidate(t *testing.T) {
	c := NewSummaryCache(10, time.Minute)
	c.Put("h1", genai.NewContentFromText("1", genai.RoleModel), 10, 0, 1)
	c.Put("h2", genai.NewContentFromText("2", genai.RoleModel), 10, 0, 1)

	c.Invalidate("h1")

	if _, ok := c.Get("h1"); ok {
		t.Error("h1 should be invalidated")
	}
	if _, ok := c.Get("h2"); !ok {
		t.Error("h2 should still be present")
	}
	if c.Size() != 1 {
		t.Errorf("Size = %d, want 1 after invalidate", c.Size())
	}
}

func TestSummaryCache_InvalidateMissing(t *testing.T) {
	c := NewSummaryCache(10, time.Minute)
	// Should not panic and should leave cache unchanged
	c.Invalidate("nonexistent")
	if c.Size() != 0 {
		t.Errorf("Size = %d, want 0 after invalidating missing key", c.Size())
	}
}

func TestSummaryCache_Clear(t *testing.T) {
	c := NewSummaryCache(10, time.Minute)
	c.Put("h1", genai.NewContentFromText("1", genai.RoleModel), 10, 0, 1)
	c.Put("h2", genai.NewContentFromText("2", genai.RoleModel), 10, 0, 1)

	c.Clear()

	if c.Size() != 0 {
		t.Errorf("Size = %d, want 0 after Clear", c.Size())
	}
	if _, ok := c.Get("h1"); ok {
		t.Error("h1 should be gone after Clear")
	}
}

func TestSummaryCache_GetStats(t *testing.T) {
	c := NewSummaryCache(5, 30*time.Second)
	c.Put("h1", genai.NewContentFromText("1", genai.RoleModel), 10, 0, 1)
	_, _ = c.Get("h1")
	_, _ = c.Get("missing")

	stats := c.GetStats()
	if stats.Size != 1 {
		t.Errorf("Size = %d, want 1", stats.Size)
	}
	if stats.Hits != 1 {
		t.Errorf("Hits = %d, want 1", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("Misses = %d, want 1", stats.Misses)
	}
	if stats.HitRate != 0.5 {
		t.Errorf("HitRate = %v, want 0.5", stats.HitRate)
	}
	if stats.MaxSize != 5 {
		t.Errorf("MaxSize = %d, want 5", stats.MaxSize)
	}
	if stats.TTL != 30*time.Second {
		t.Errorf("TTL = %v, want 30s", stats.TTL)
	}
}

func TestSummaryCache_ClearResetsStats(t *testing.T) {
	c := NewSummaryCache(10, time.Minute)
	c.Put("h1", genai.NewContentFromText("1", genai.RoleModel), 10, 0, 1)
	_, _ = c.Get("h1")
	_, _ = c.Get("missing")

	c.Clear()

	stats := c.GetStats()
	if stats.Size != 0 {
		t.Errorf("Size = %d, want 0", stats.Size)
	}
	if stats.Hits != 0 || stats.Misses != 0 || stats.HitRate != 0 {
		t.Errorf("stats after Clear = hits:%d misses:%d rate:%v, want all zero", stats.Hits, stats.Misses, stats.HitRate)
	}
}

func TestSummaryCache_GetReturnsCopy(t *testing.T) {
	c := NewSummaryCache(1, time.Minute)
	c.Put("h1", genai.NewContentFromText("1", genai.RoleModel), 10, 0, 1)

	got1, _ := c.Get("h1")
	// Evict by adding a new entry
	c.Put("h2", genai.NewContentFromText("2", genai.RoleModel), 20, 0, 1)

	// got1 should still be valid (it's a copy)
	if got1.TokenCount != 10 {
		t.Errorf("copy TokenCount = %d, want 10", got1.TokenCount)
	}
}

func TestSummaryCache_PutClonesSummaryContent(t *testing.T) {
	c := NewSummaryCache(10, time.Minute)
	summary := genai.NewContentFromText("original", genai.RoleModel)
	c.Put("h1", summary, 10, 0, 1)

	summary.Parts[0].Text = "mutated after put"

	got, ok := c.Get("h1")
	if !ok {
		t.Fatal("Get returned not ok")
	}
	if got.Summary.Parts[0].Text != "original" {
		t.Fatalf("cached summary text = %q, want original", got.Summary.Parts[0].Text)
	}
}

func TestSummaryCache_GetClonesSummaryContent(t *testing.T) {
	c := NewSummaryCache(10, time.Minute)
	c.Put("h1", genai.NewContentFromText("original", genai.RoleModel), 10, 0, 1)

	got, ok := c.Get("h1")
	if !ok {
		t.Fatal("Get returned not ok")
	}
	got.Summary.Parts[0].Text = "mutated after get"

	again, ok := c.Get("h1")
	if !ok {
		t.Fatal("second Get returned not ok")
	}
	if again.Summary.Parts[0].Text != "original" {
		t.Fatalf("cached summary text after mutating Get result = %q, want original", again.Summary.Parts[0].Text)
	}
}

func TestSummaryCache_ClonesNestedSummaryData(t *testing.T) {
	c := NewSummaryCache(10, time.Minute)
	summary := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{
				FunctionResponse: &genai.FunctionResponse{
					Name: "read",
					Response: map[string]any{
						"meta": map[string]any{"path": "a.txt"},
					},
				},
			},
		},
	}
	c.Put("h1", summary, 10, 0, 1)

	summary.Parts[0].FunctionResponse.Response["meta"].(map[string]any)["path"] = "b.txt"

	got, ok := c.Get("h1")
	if !ok {
		t.Fatal("Get returned not ok")
	}
	meta := got.Summary.Parts[0].FunctionResponse.Response["meta"].(map[string]any)
	if meta["path"] != "a.txt" {
		t.Fatalf("cached nested response path = %v, want a.txt", meta["path"])
	}

	meta["path"] = "c.txt"
	again, ok := c.Get("h1")
	if !ok {
		t.Fatal("second Get returned not ok")
	}
	againMeta := again.Summary.Parts[0].FunctionResponse.Response["meta"].(map[string]any)
	if againMeta["path"] != "a.txt" {
		t.Fatalf("cached nested response path after mutating Get result = %v, want a.txt", againMeta["path"])
	}
}

func TestSummaryCache_ClonesAllPartVariants(t *testing.T) {
	c := NewSummaryCache(10, time.Minute)
	willContinue := true
	numberValue := 1.5
	boolValue := true
	fps := 12.0
	numTokens := int32(32)
	summary := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{
				Text:             "thought text",
				Thought:          true,
				ThoughtSignature: []byte{1, 2, 3},
				InlineData:       &genai.Blob{MIMEType: "image/png", Data: []byte{4, 5, 6}, DisplayName: "img"},
				FileData:         &genai.FileData{FileURI: "file://x", MIMEType: "text/plain", DisplayName: "file"},
				ExecutableCode:   &genai.ExecutableCode{Code: "print(1)", Language: genai.LanguagePython},
				CodeExecutionResult: &genai.CodeExecutionResult{
					Outcome: genai.OutcomeOK,
					Output:  "ok",
				},
				VideoMetadata:   &genai.VideoMetadata{FPS: &fps},
				MediaResolution: &genai.PartMediaResolution{NumTokens: &numTokens},
				FunctionCall: &genai.FunctionCall{
					ID:           "call-id",
					Name:         "tool",
					Args:         map[string]any{"nested": map[string]any{"k": "v"}},
					WillContinue: &willContinue,
					PartialArgs: []*genai.PartialArg{{
						JsonPath:     "$.x",
						NumberValue:  &numberValue,
						BoolValue:    &boolValue,
						WillContinue: &willContinue,
						StringValue:  "value",
						NULLValue:    "NULL_VALUE",
					}},
				},
			},
			{
				FunctionResponse: &genai.FunctionResponse{
					ID:           "call-id",
					Name:         "tool",
					WillContinue: &willContinue,
					Response:     map[string]any{"ok": true},
					Parts: []*genai.FunctionResponsePart{{
						InlineData: &genai.FunctionResponseBlob{MIMEType: "image/png", Data: []byte{7, 8}, DisplayName: "blob"},
						FileData:   &genai.FunctionResponseFileData{FileURI: "file://resp", MIMEType: "text/plain", DisplayName: "resp"},
					}},
				},
			},
		},
	}
	c.Put("h1", summary, 10, 0, 1)

	summary.Parts[0].ThoughtSignature[0] = 9
	summary.Parts[0].InlineData.Data[0] = 9
	*summary.Parts[0].VideoMetadata.FPS = 24
	*summary.Parts[0].MediaResolution.NumTokens = 64
	summary.Parts[0].FunctionCall.Args["nested"].(map[string]any)["k"] = "mutated"
	*summary.Parts[0].FunctionCall.PartialArgs[0].NumberValue = 2.5
	summary.Parts[1].FunctionResponse.Parts[0].InlineData.Data[0] = 9

	got, ok := c.Get("h1")
	if !ok {
		t.Fatal("Get returned not ok")
	}
	part := got.Summary.Parts[0]
	if !part.Thought || part.ThoughtSignature[0] != 1 {
		t.Fatalf("thought fields not cloned/preserved: thought=%v signature=%v", part.Thought, part.ThoughtSignature)
	}
	if part.InlineData.Data[0] != 4 || part.InlineData.DisplayName != "img" {
		t.Fatalf("inline data not cloned/preserved: %+v", part.InlineData)
	}
	if *part.VideoMetadata.FPS != 12 || *part.MediaResolution.NumTokens != 32 {
		t.Fatalf("media metadata not cloned/preserved: fps=%v tokens=%v", *part.VideoMetadata.FPS, *part.MediaResolution.NumTokens)
	}
	if part.ExecutableCode.Code != "print(1)" || part.CodeExecutionResult.Output != "ok" {
		t.Fatalf("code fields not preserved: exec=%+v result=%+v", part.ExecutableCode, part.CodeExecutionResult)
	}
	if part.FunctionCall.Args["nested"].(map[string]any)["k"] != "v" {
		t.Fatalf("function call args not deep-cloned: %+v", part.FunctionCall.Args)
	}
	if *part.FunctionCall.PartialArgs[0].NumberValue != 1.5 || !*part.FunctionCall.PartialArgs[0].BoolValue || !*part.FunctionCall.PartialArgs[0].WillContinue {
		t.Fatalf("partial args not cloned/preserved: %+v", part.FunctionCall.PartialArgs[0])
	}
	responsePart := got.Summary.Parts[1].FunctionResponse.Parts[0]
	if got.Summary.Parts[1].FunctionResponse.WillContinue == nil || !*got.Summary.Parts[1].FunctionResponse.WillContinue {
		t.Fatal("function response WillContinue not preserved")
	}
	if responsePart.InlineData.Data[0] != 7 || responsePart.FileData.FileURI != "file://resp" {
		t.Fatalf("function response parts not cloned/preserved: %+v", responsePart)
	}

	part.ThoughtSignature[0] = 10
	part.InlineData.Data[0] = 10
	responsePart.InlineData.Data[0] = 10

	again, ok := c.Get("h1")
	if !ok {
		t.Fatal("second Get returned not ok")
	}
	if again.Summary.Parts[0].ThoughtSignature[0] != 1 || again.Summary.Parts[0].InlineData.Data[0] != 4 {
		t.Fatal("mutating returned part data changed cached part data")
	}
	if again.Summary.Parts[1].FunctionResponse.Parts[0].InlineData.Data[0] != 7 {
		t.Fatal("mutating returned function response part changed cached data")
	}
}

func TestHashMessages_DifferentContent(t *testing.T) {
	a := []*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)}
	b := []*genai.Content{genai.NewContentFromText("world", genai.RoleUser)}
	if HashMessages(a) == HashMessages(b) {
		t.Error("different content should produce different hashes")
	}
}

func TestHashMessages_Empty(t *testing.T) {
	h := HashMessages(nil)
	if h == "" {
		t.Error("HashMessages(nil) should not be empty")
	}
}

func TestHashMessages_FunctionCall(t *testing.T) {
	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{
				FunctionCall: &genai.FunctionCall{
					Name: "read",
					Args: map[string]any{"file_path": "/x.go"},
				},
			},
		},
	}
	h1 := HashMessages([]*genai.Content{msg})
	h2 := HashMessages([]*genai.Content{msg})
	if h1 != h2 {
		t.Errorf("FunctionCall hash not stable: %q vs %q", h1, h2)
	}
	if h1 == "" {
		t.Error("hash should not be empty")
	}
}

func TestHashMessages_FunctionCallArgsSorted(t *testing.T) {
	msgA := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{
				FunctionCall: &genai.FunctionCall{
					Name: "read",
					Args: map[string]any{
						"limit":     50,
						"file_path": "/x.go",
						"offset":    10,
					},
				},
			},
		},
	}
	msgB := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{
				FunctionCall: &genai.FunctionCall{
					Name: "read",
					Args: map[string]any{
						"offset":    10,
						"file_path": "/x.go",
						"limit":     50,
					},
				},
			},
		},
	}

	if got, want := HashMessages([]*genai.Content{msgA}), HashMessages([]*genai.Content{msgB}); got != want {
		t.Fatalf("hash should be independent of FunctionCall Args map iteration order: got %q, want %q", got, want)
	}
}

func TestHashMessages_FunctionCallArgValuesAffectHash(t *testing.T) {
	msgA := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{
				FunctionCall: &genai.FunctionCall{
					Name: "read",
					Args: map[string]any{
						"file_path": "/a.go",
						"limit":     50,
					},
				},
			},
		},
	}
	msgB := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{
				FunctionCall: &genai.FunctionCall{
					Name: "read",
					Args: map[string]any{
						"file_path": "/b.go",
						"limit":     50,
					},
				},
			},
		},
	}

	if got, other := HashMessages([]*genai.Content{msgA}), HashMessages([]*genai.Content{msgB}); got == other {
		t.Fatal("different FunctionCall argument values should produce different hashes")
	}
}

func TestHashMessages_FunctionCallArgNumericTypesAffectHash(t *testing.T) {
	msgA := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{
				FunctionCall: &genai.FunctionCall{
					Name: "read",
					Args: map[string]any{
						"file_path": "/x.go",
						"offset":    1,
					},
				},
			},
		},
	}
	msgB := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{
				FunctionCall: &genai.FunctionCall{
					Name: "read",
					Args: map[string]any{
						"file_path": "/x.go",
						"offset":    float64(1),
					},
				},
			},
		},
	}

	if got, other := HashMessages([]*genai.Content{msgA}), HashMessages([]*genai.Content{msgB}); got == other {
		t.Fatal("different FunctionCall numeric argument types should produce different hashes")
	}
}

func TestHashMessages_FunctionCallNestedArgsSorted(t *testing.T) {
	msgA := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{
				FunctionCall: &genai.FunctionCall{
					Name: "tree",
					Args: map[string]any{
						"filter": map[string]any{
							"kind": "test",
							"max":  5,
						},
						"path": "internal",
					},
				},
			},
		},
	}
	msgB := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{
				FunctionCall: &genai.FunctionCall{
					Name: "tree",
					Args: map[string]any{
						"path": "internal",
						"filter": map[string]any{
							"max":  5,
							"kind": "test",
						},
					},
				},
			},
		},
	}

	if got, want := HashMessages([]*genai.Content{msgA}), HashMessages([]*genai.Content{msgB}); got != want {
		t.Fatalf("hash should be independent of nested FunctionCall Args map iteration order: got %q, want %q", got, want)
	}
}

func TestHashMessages_FunctionResponse(t *testing.T) {
	msg := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{
				FunctionResponse: &genai.FunctionResponse{
					Name: "read",
					Response: map[string]any{
						"content": "file contents here",
					},
				},
			},
		},
	}
	h := HashMessages([]*genai.Content{msg})
	if h == "" {
		t.Error("hash should not be empty")
	}
}

func TestHashMessages_FunctionResponseValuesAffectHash(t *testing.T) {
	msgA := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{
				FunctionResponse: &genai.FunctionResponse{
					Name: "read",
					Response: map[string]any{
						"output": "file A contents",
					},
				},
			},
		},
	}
	msgB := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{
				FunctionResponse: &genai.FunctionResponse{
					Name: "read",
					Response: map[string]any{
						"output": "file B contents",
					},
				},
			},
		},
	}

	if got, other := HashMessages([]*genai.Content{msgA}), HashMessages([]*genai.Content{msgB}); got == other {
		t.Fatal("different FunctionResponse payload values should produce different hashes")
	}
}

func TestHashMessages_FunctionResponseNestedPayloadSorted(t *testing.T) {
	msgA := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{
				FunctionResponse: &genai.FunctionResponse{
					Name: "grep",
					Response: map[string]any{
						"data": map[string]any{
							"count": 2,
							"files": []any{"a.go", "b.go"},
						},
					},
				},
			},
		},
	}
	msgB := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{
				FunctionResponse: &genai.FunctionResponse{
					Name: "grep",
					Response: map[string]any{
						"data": map[string]any{
							"files": []any{"a.go", "b.go"},
							"count": 2,
						},
					},
				},
			},
		},
	}

	if got, want := HashMessages([]*genai.Content{msgA}), HashMessages([]*genai.Content{msgB}); got != want {
		t.Fatalf("hash should be independent of nested FunctionResponse map order: got %q, want %q", got, want)
	}
}

func TestHashMessages_LongTextTruncated(t *testing.T) {
	long := make([]rune, 600)
	for i := range long {
		long[i] = 'x'
	}
	msgs := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: string(long)}}},
	}
	// Should not panic and should produce a stable hash
	h1 := HashMessages(msgs)
	h2 := HashMessages(msgs)
	if h1 != h2 {
		t.Errorf("long text hash not stable")
	}
}
