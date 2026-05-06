package app

import (
	"strings"
	"testing"
	"unicode/utf8"

	"gokin/internal/plan"

	"google.golang.org/genai"
)

// ─── lastModelText ─────────────────────────────────────────────────────────

func TestLastModelText_EmptyHistory(t *testing.T) {
	if got := lastModelText(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestLastModelText_FindsMostRecentModelMessage(t *testing.T) {
	history := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "u1"}}},
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "m1"}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "u2"}}},
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "m2"}}},
	}
	if got := lastModelText(history); got != "m2" {
		t.Errorf("got %q, want m2", got)
	}
}

func TestLastModelText_SkipsEmptyModelMessages(t *testing.T) {
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "real"}}},
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "   "}}},
	}
	if got := lastModelText(history); got != "real" {
		t.Errorf("got %q, want real (empty-text model msg should be skipped)", got)
	}
}

func TestLastModelText_ConcatenatesParts(t *testing.T) {
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{Text: "part1 "},
			{Text: "part2"},
		}},
	}
	if got := lastModelText(history); got != "part1 part2" {
		t.Errorf("got %q, want concatenation", got)
	}
}

func TestLastModelText_IgnoresNilContent(t *testing.T) {
	history := []*genai.Content{
		nil,
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "ok"}}},
	}
	if got := lastModelText(history); got != "ok" {
		t.Errorf("got %q, want ok", got)
	}
}

// ─── lastModelToolContext ──────────────────────────────────────────────────

func TestLastModelToolContext_ReturnsEmptyForNoTools(t *testing.T) {
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "plain"}}},
	}
	if got := lastModelToolContext(history); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestLastModelToolContext_IncludesAllToolsFromLastModelMsg(t *testing.T) {
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "read"}},
			{FunctionCall: &genai.FunctionCall{Name: "grep"}},
		}},
	}
	got := lastModelToolContext(history)
	if !strings.Contains(got, "read") || !strings.Contains(got, "grep") {
		t.Errorf("got %q, want both tool names", got)
	}
	if !strings.HasPrefix(got, "Tools already called") {
		t.Errorf("got %q, want prefix 'Tools already called'", got)
	}
}

func TestLastModelToolContext_WalksBackUntilModelWithTools(t *testing.T) {
	// Newer model message has no tools; older one does. The helper walks
	// backward until it finds a model message WITH tools, so it returns
	// the older one's tool list. Important for the retry-continuation path:
	// we want to tell the model which tools were called in its partial
	// response, even if the most recent model message was text-only.
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "oldtool"}},
		}},
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "plain"}}},
	}
	got := lastModelToolContext(history)
	if !strings.Contains(got, "oldtool") {
		t.Errorf("got %q, expected to find oldtool from earlier msg", got)
	}
}

// ─── lastCompleteSentence ─────────────────────────────────────────────────

func TestLastCompleteSentence(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"   ":                          "",
		"no terminator":                "",
		"one.":                         "one.",
		"two sentences. Second one":    "two sentences.",
		"trailing. ":                   "trailing.",
		"with newline\nno term":        "with newline",
		"question? yes":                "question?",
		"exclaim! yes":                 "exclaim!",
		"multiple. sentences! here?":   "multiple. sentences! here?",
	}
	for input, want := range cases {
		if got := lastCompleteSentence(input); got != want {
			t.Errorf("lastCompleteSentence(%q) = %q, want %q", input, got, want)
		}
	}
}

// ─── truncateTail ──────────────────────────────────────────────────────────

func TestTruncateTail_Boundaries(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"empty", "", 10, ""},
		{"within limit", "hello", 10, "hello"},
		{"exactly limit", "hello", 5, "hello"},
		{"over limit keeps last N", "12345678901234567890", 5, "..." + "67890"},
		{"negative max is no-op", "anything", -1, "anything"},
		{"zero max is no-op", "anything", 0, "anything"},
		{"leading whitespace trimmed", "   hello   ", 100, "hello"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := truncateTail(c.in, c.max); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestTruncateTail_MultibyteSafe is a regression for v0.79.3: truncateTail
// used to byte-slice the tail, producing invalid UTF-8 when the recovery hint
// covered a Cyrillic / CJK / emoji response. The hint then went into the
// next prompt verbatim, where some providers rejected it as invalid input.
func TestTruncateTail_MultibyteSafe(t *testing.T) {
	cases := []string{
		strings.Repeat("я", 300),                  // Cyrillic
		strings.Repeat("世界", 200),                 // CJK
		strings.Repeat("🚀", 100),                  // emoji (4 bytes each)
		"english " + strings.Repeat("привет ", 50), // mixed
	}
	for i, in := range cases {
		got := truncateTail(in, 100)
		if !utf8.ValidString(got) {
			t.Errorf("case %d: produced invalid UTF-8 (bytes: % x)", i, []byte(got))
		}
		if strings.Contains(got, string(utf8.RuneError)) {
			t.Errorf("case %d: contains replacement char: %q", i, got)
		}
	}
}

// ─── trimToLastModelMessage ───────────────────────────────────────────────

func TestTrimToLastModelMessage_NoModelMessageReturnsMinLen(t *testing.T) {
	history := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "u"}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "u2"}}},
	}
	got := trimToLastModelMessage(history, 1)
	if len(got) != 1 {
		t.Errorf("len = %d, want 1 (fallback to minLen)", len(got))
	}
}

func TestTrimToLastModelMessage_TrimsToLastModel(t *testing.T) {
	history := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "u1"}}},
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "m1"}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "u2"}}},
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "m2"}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "u3 orphan"}}},
	}
	got := trimToLastModelMessage(history, 0)
	if len(got) != 4 {
		t.Errorf("len = %d, want 4 (cut orphaned u3)", len(got))
	}
	if got[len(got)-1].Role != genai.RoleModel {
		t.Errorf("last role = %v, want Model", got[len(got)-1].Role)
	}
}

func TestTrimToLastModelMessage_RespectsMinLen(t *testing.T) {
	// Model message lives BEFORE minLen — must not cut below minLen.
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "m1 — before fence"}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "u2 — after fence"}}},
	}
	got := trimToLastModelMessage(history, 2)
	if len(got) != 2 {
		t.Errorf("len = %d, want 2 (minLen protected)", len(got))
	}
}

// ─── maxPlanExecutionRounds ───────────────────────────────────────────────

func TestMaxPlanExecutionRounds(t *testing.T) {
	cases := map[int]int{
		0:   40,  // non-positive → floor
		-5:  40,  // same
		1:   40,  // 1*12 = 12 → floor 40
		3:   40,  // 3*12 = 36 → still floor
		4:   48,  // 4*12 = 48
		10:  120, // 10*12 = 120
		50:  600, // 50*12 = 600 — at cap
		100: 600, // over cap
	}
	for steps, want := range cases {
		if got := maxPlanExecutionRounds(steps); got != want {
			t.Errorf("maxPlanExecutionRounds(%d) = %d, want %d", steps, got, want)
		}
	}
}

// ─── requiresHumanCheckpoint ──────────────────────────────────────────────

func TestRequiresHumanCheckpoint_NilStep(t *testing.T) {
	if requiresHumanCheckpoint(nil) {
		t.Error("nil step should not require checkpoint")
	}
}

func TestRequiresHumanCheckpoint_KeywordMatching(t *testing.T) {
	cases := []struct {
		name    string
		step    *plan.Step
		require bool
	}{
		{"empty", &plan.Step{}, false},
		{"innocuous", &plan.Step{Title: "Read config", Description: "Parse yaml"}, false},
		{"migration keyword", &plan.Step{Title: "Run DB migration"}, true},
		{"case insensitive MIGRATE", &plan.Step{Description: "MIGRATE TO V2"}, true},
		{"drop table", &plan.Step{Title: "Drop Table users"}, true},
		{"deploy", &plan.Step{Title: "Deploy to staging"}, true},
		{"production", &plan.Step{Description: "push to production"}, true},
		{"billing", &plan.Step{Title: "Update billing records"}, true},
		{"keyword in description wins", &plan.Step{Title: "cleanup", Description: "bulk delete rows"}, true},
		{"mass delete — not a keyword", &plan.Step{Description: "mass delete"}, false},
		{"mass update — keyword", &plan.Step{Description: "mass update records"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := requiresHumanCheckpoint(c.step)
			if got != c.require {
				t.Errorf("got %v, want %v (step=%+v)", got, c.require, c.step)
			}
		})
	}
}

// ─── buildContinuationRetryMessage ────────────────────────────────────────

func TestBuildContinuationRetryMessage_NoHistoryIncludesGenericPrefix(t *testing.T) {
	got := buildContinuationRetryMessage("please continue", nil)
	if !strings.Contains(got, "previous response was interrupted") {
		t.Errorf("got %q, missing generic interrupt prefix", got)
	}
	if !strings.Contains(got, "please continue") {
		t.Error("user message must be preserved")
	}
}

func TestBuildContinuationRetryMessage_IncludesAnchorFromLastModelText(t *testing.T) {
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "First sentence. Second sentence. Third incomplete"}}},
	}
	got := buildContinuationRetryMessage("more", history)
	if !strings.Contains(got, "Last complete sentence") {
		t.Errorf("got %q, missing anchor marker", got)
	}
	if !strings.Contains(got, "Second sentence.") {
		t.Errorf("got %q, expected anchor to be 'Second sentence.'", got)
	}
}

func TestBuildContinuationRetryMessage_IncludesToolContext(t *testing.T) {
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "read"}},
		}},
	}
	got := buildContinuationRetryMessage("continue", history)
	if !strings.Contains(got, "Tools already called") {
		t.Errorf("got %q, missing tool context line", got)
	}
	if !strings.Contains(got, "read") {
		t.Errorf("got %q, tool name missing", got)
	}
}

func TestBuildContinuationRetryMessage_FallsBackToTruncatedTail(t *testing.T) {
	// Long text with no sentence terminator — should use truncateTail.
	long := strings.Repeat("x ", 200)
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: long}}},
	}
	got := buildContinuationRetryMessage("continue", history)
	if !strings.Contains(got, "previous response was interrupted") {
		t.Errorf("missing prefix in %q", got)
	}
	if !strings.Contains(got, "...") {
		t.Error("expected truncateTail marker '...' in anchor")
	}
}

// ─── isOverloadError — additional cases beyond existing test ─────────────

func TestIsOverloadError_CaseInsensitive(t *testing.T) {
	cases := []string{
		"Server OVERLOADED",
		"RATE LIMIT hit",
		"Too Many Requests",
	}
	for _, msg := range cases {
		if !isOverloadError(errWrap(msg)) {
			t.Errorf("should match: %q", msg)
		}
	}
}

// ─── isAlnum helper ──────────────────────────────────────────────────────

func TestIsAlnum(t *testing.T) {
	for b := byte('0'); b <= '9'; b++ {
		if !isAlnum(b) {
			t.Errorf("%c should be alnum", b)
		}
	}
	for b := byte('a'); b <= 'z'; b++ {
		if !isAlnum(b) {
			t.Errorf("%c should be alnum", b)
		}
	}
	for b := byte('A'); b <= 'Z'; b++ {
		if !isAlnum(b) {
			t.Errorf("%c should be alnum", b)
		}
	}
	for _, b := range []byte{' ', ':', '.', '-', '_', '\n'} {
		if isAlnum(b) {
			t.Errorf("%c should not be alnum", b)
		}
	}
}

// errWrap is a lightweight error constructor to keep tests dense.
type testErr string

func (e testErr) Error() string { return string(e) }
func errWrap(s string) error    { return testErr(s) }
