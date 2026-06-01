package context

import (
	"strings"
	"testing"

	"gokin/internal/chat"

	"google.golang.org/genai"
)

// TestSelectImportantWithinBudget pins the v0.86.6 importance-scored rescue:
// under budget pressure the highest-scored message (errors → Critical) wins,
// output stays chronological, and the no-budget / no-scorer cases are no-ops.
func TestSelectImportantWithinBudget(t *testing.T) {
	m := &ContextManager{messageScorer: NewMessageScorer()}

	low := genai.NewContentFromText("listing the files in the directory", genai.RoleModel)
	crit := genai.NewContentFromText("error: undefined symbol Foo, build failed", genai.RoleModel)
	norm := genai.NewContentFromText("we should split the parser into two files", genai.RoleUser)
	msgs := []*genai.Content{low, crit, norm} // chronological

	// No budget → nil. No scorer → nil.
	if got := m.selectImportantWithinBudget(msgs, 0); got != nil {
		t.Errorf("budget 0: want nil, got %d msgs", len(got))
	}
	if got := (&ContextManager{}).selectImportantWithinBudget(msgs, 10000); got != nil {
		t.Errorf("nil scorer: want nil, got %d msgs", len(got))
	}

	// Generous budget → all kept, in chronological (original) order.
	all := m.selectImportantWithinBudget(msgs, 100000)
	if len(all) != 3 {
		t.Fatalf("generous budget: want 3 kept, got %d", len(all))
	}
	if all[0] != low || all[1] != crit || all[2] != norm {
		t.Error("generous budget: messages not returned in chronological order")
	}

	// Budget that fits only one message → the highest-scored (the error) wins,
	// even though it's not the most recent. Budget computed from the real
	// estimator so the test survives estimator tweaks.
	critCost := EstimateContentsTokens([]*genai.Content{crit})
	one := m.selectImportantWithinBudget(msgs, critCost)
	if len(one) != 1 || one[0] != crit {
		t.Fatalf("tight budget: want exactly the critical error message, got %d msgs", len(one))
	}
}

// TestPruneOrphanedToolParts pins the orphan-safety net: a rescued
// FunctionResponse whose FunctionCall wasn't kept (and vice versa) is dropped,
// while complete pairs and plain text survive. Strict providers 400 on orphans.
func TestPruneOrphanedToolParts(t *testing.T) {
	callPart := &genai.Part{FunctionCall: &genai.FunctionCall{ID: "c1", Name: "read"}}
	respPart := &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "c1", Name: "read"}}
	orphanResp := &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "gone", Name: "edit"}}
	orphanCall := &genai.Part{FunctionCall: &genai.FunctionCall{ID: "lonely", Name: "bash"}}

	history := []*genai.Content{
		genai.NewContentFromText("hello", genai.RoleUser),
		{Role: genai.RoleModel, Parts: []*genai.Part{callPart}},
		{Role: genai.RoleUser, Parts: []*genai.Part{respPart}},     // pair with c1 — survives
		{Role: genai.RoleUser, Parts: []*genai.Part{orphanResp}},   // no matching call — dropped
		{Role: genai.RoleModel, Parts: []*genai.Part{orphanCall}},  // no matching response — dropped
	}

	out := pruneOrphanedToolParts(history)

	// Collect surviving tool IDs.
	ids := map[string]bool{}
	for _, msg := range out {
		for _, p := range msg.Parts {
			if p.FunctionCall != nil {
				ids["call:"+p.FunctionCall.ID] = true
			}
			if p.FunctionResponse != nil {
				ids["resp:"+p.FunctionResponse.ID] = true
			}
		}
	}
	if !ids["call:c1"] || !ids["resp:c1"] {
		t.Error("complete c1 pair was dropped")
	}
	if ids["resp:gone"] {
		t.Error("orphaned FunctionResponse 'gone' survived")
	}
	if ids["call:lonely"] {
		t.Error("orphaned FunctionCall 'lonely' survived")
	}

	// No-orphan history is returned unchanged (same backing slice).
	clean := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{callPart}},
		{Role: genai.RoleUser, Parts: []*genai.Part{respPart}},
	}
	if got := pruneOrphanedToolParts(clean); len(got) != 2 {
		t.Errorf("clean history altered: want 2 msgs, got %d", len(got))
	}
}

// TestEmergencyTruncateRescuesImportant pins the v0.86.6 fix end-to-end: a
// critical OLD message (error) is rescued past the recency cut while a large
// low-value OLD message is dropped, the budget guarantee holds, and the
// re-grounding note + preserved head survive.
func TestEmergencyTruncateRescuesImportant(t *testing.T) {
	const maxInput = 1500
	target := int(float64(maxInput) * 0.7)

	history := []*genai.Content{
		genai.NewContentFromText("you are gokin, a coding assistant", genai.RoleModel),       // [0] preserved
		genai.NewContentFromText("Implement the JSON parser and add tests", genai.RoleUser),  // [1] preserved (task)
		genai.NewContentFromText("error: the parser panicked on nested arrays", genai.RoleModel), // [2] OLD critical, small
		// [3] OLD, large, low-value — too big for the rescue budget AND low-scored → dropped.
		genai.NewContentFromText("DROPME "+strings.Repeat("i scrolled through the plain directory file names ", 60), genai.RoleUser),
	}
	// Recent filler to fill the recency budget and push [2]/[3] out of reach.
	for range 12 {
		history = append(history, genai.NewContentFromText(
			"continuing routine work "+strings.Repeat("processing the next step here ", 12), genai.RoleModel))
	}

	m := &ContextManager{
		session:       chat.NewSession(),
		tokenCounter:  &TokenCounter{limits: TokenLimits{MaxInputTokens: maxInput}},
		messageScorer: NewMessageScorer(),
		keyFiles:      map[string]bool{},
	}
	m.session.SetHistory(history)

	removed := m.EmergencyTruncate()
	if removed <= 0 {
		t.Fatalf("expected truncation to remove messages, removed=%d", removed)
	}

	newHistory := m.session.GetHistory()

	// Budget guarantee: result fits the target.
	if got := EstimateContentsTokens(newHistory); got > target {
		t.Errorf("result exceeds budget: %d tokens > target %d", got, target)
	}

	joined := ""
	for _, msg := range newHistory {
		for _, p := range msg.Parts {
			joined += p.Text + "\n"
		}
	}

	// Preserved head survives.
	if !strings.Contains(joined, "you are gokin") {
		t.Error("preserved system message dropped")
	}
	// Re-grounding note carries the original task forward.
	if !strings.Contains(joined, "Implement the JSON parser") {
		t.Error("re-grounding note (original task) missing after truncate")
	}
	// The critical OLD error message was rescued by importance, not recency.
	if !strings.Contains(joined, "panicked on nested arrays") {
		t.Error("critical old error message was NOT rescued")
	}
	// The large low-value OLD message was dropped (not rescued).
	if strings.Contains(joined, "DROPME") {
		t.Error("large low-value old message should have been dropped, but survived")
	}
}
