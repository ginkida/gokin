package agent

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func fctMsgText(m *genai.Content) string {
	if m == nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range m.Parts {
		if p != nil {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

// forceCompactViaTruncation is the deterministic fallback when summarization
// fails. It MUST preserve the first 3 messages (system [0], greeting [1],
// original task [2]) so the agent never forgets its task mid-work, keep the last
// `keepEnd` messages, and keep the `keepMiddle` highest-scored middle messages.
// With relevanceScorer nil it uses primitiveScore (FunctionResponse +3, error/
// failed text +2, plain text 0), giving deterministic selection.
func TestForceCompactViaTruncation_PreservesTaskAndStructure(t *testing.T) {
	a := &Agent{ID: "t", originalPrompt: "fix the bug"}

	hist := []*genai.Content{
		genai.NewContentFromText("SYSTEM PROMPT", genai.RoleUser),    // [0]
		genai.NewContentFromText("GREETING", genai.RoleModel),        // [1]
		genai.NewContentFromText("TASK: fix the bug", genai.RoleUser), // [2] — must survive at index 2
	}
	// 10 middle messages: 4 high-score (contain "error"), 6 plain (score 0).
	middleIsErr := []bool{false, true, false, true, false, true, false, true, false, false}
	for i, isErr := range middleIsErr {
		if isErr {
			hist = append(hist, genai.NewContentFromText(fmt.Sprintf("MID-ERR-%d error occurred", i), genai.RoleModel))
		} else {
			hist = append(hist, genai.NewContentFromText(fmt.Sprintf("MID-PLAIN-%d nothing", i), genai.RoleModel))
		}
	}
	// 6 trailing messages — all must survive.
	for i := range 6 {
		hist = append(hist, genai.NewContentFromText(fmt.Sprintf("END-%d", i), genai.RoleModel))
	}
	a.history = hist
	origLen := len(a.history) // 19

	if err := a.forceCompactViaTruncation(); err != nil {
		t.Fatalf("forceCompactViaTruncation: %v", err)
	}
	h := a.history

	if len(h) >= origLen {
		t.Fatalf("history not compacted: len %d >= orig %d", len(h), origLen)
	}
	// THE critical invariant: the original task is preserved verbatim at [2].
	if fctMsgText(h[0]) != "SYSTEM PROMPT" || fctMsgText(h[1]) != "GREETING" ||
		!strings.Contains(fctMsgText(h[2]), "TASK: fix the bug") {
		t.Fatalf("first-3 not preserved: %q / %q / %q", fctMsgText(h[0]), fctMsgText(h[1]), fctMsgText(h[2]))
	}
	// [3] is the truncate notice.
	if !strings.Contains(fctMsgText(h[3]), "compacted") {
		t.Errorf("expected truncate notice at [3], got %q", fctMsgText(h[3]))
	}
	// All 6 trailing messages survive.
	for i := range 6 {
		want := fmt.Sprintf("END-%d", i)
		found := false
		for _, m := range h {
			if fctMsgText(m) == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("trailing message %q dropped", want)
		}
	}
	// The 4 high-score middle messages survive; the 6 low-score ones are dropped.
	errKept, plainKept := 0, 0
	for _, m := range h {
		txt := fctMsgText(m)
		if strings.Contains(txt, "MID-ERR-") {
			errKept++
		}
		if strings.Contains(txt, "MID-PLAIN-") {
			plainKept++
		}
	}
	if errKept != 4 {
		t.Errorf("expected 4 high-score middle messages kept, got %d", errKept)
	}
	if plainKept != 0 {
		t.Errorf("expected 0 low-score middle messages kept, got %d", plainKept)
	}
}

// Below the size floor (len <= 10, or < keepStart+keepEnd+keepMiddle) the
// fallback is a no-op — it must not touch a short history.
func TestForceCompactViaTruncation_NoOpOnShortHistory(t *testing.T) {
	a := &Agent{ID: "t"}
	for i := range 8 {
		a.history = append(a.history, genai.NewContentFromText(fmt.Sprintf("m%d", i), genai.RoleUser))
	}
	before := len(a.history)
	if err := a.forceCompactViaTruncation(); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(a.history) != before {
		t.Errorf("short history mutated: len %d, want %d", len(a.history), before)
	}
}
