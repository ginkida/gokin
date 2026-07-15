package skills

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/genai"
)

func carryTestInvocation(name, rendered string, sequence uint64) Invocation {
	return Invocation{
		Name:       name,
		Rendered:   rendered,
		RenderHash: renderHash(rendered),
		Source:     "project",
		Path:       "/project/" + name + "/SKILL.md",
		Sequence:   sequence,
	}
}

func carryTextAt(t *testing.T, content *genai.Content) string {
	t.Helper()
	if content == nil || content.Role != string(genai.RoleUser) || len(content.Parts) != 1 || content.Parts[0] == nil {
		t.Fatalf("not a single-part user content: %#v", content)
	}
	return content.Parts[0].Text
}

func carryMarkerNames(t *testing.T, text string) []string {
	t.Helper()
	markers, ok := parseCarryEnvelope(text)
	if !ok {
		t.Fatalf("invalid carry envelope:\n%s", text)
	}
	names := make([]string, len(markers))
	for i, marker := range markers {
		names[i] = marker.name
	}
	return names
}

func carryHeaderLine(invocation Invocation, bodyBytes int) string {
	return skillBlockStart + base64.RawURLEncoding.EncodeToString([]byte(invocation.Name)) +
		" hash=" + renderHash(invocation.Rendered) + " bytes=" + strconvItoaForCarryTest(bodyBytes) + "]"
}

// Keep test construction independent of fmt so malformed marker cases remain
// visibly line-oriented.
func strconvItoaForCarryTest(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [32]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}

func TestReattachInvocationsOrdersNewestFirstAndClampsInsertion(t *testing.T) {
	before := genai.NewContentFromText("before", genai.RoleUser)
	after := genai.NewContentFromText("after", genai.RoleModel)
	invocations := []Invocation{
		carryTestInvocation("older", "older instructions", 1),
		carryTestInvocation("newest", "newest instructions", 9),
		carryTestInvocation("middle", "middle instructions", 4),
	}

	got := ReattachInvocations([]*genai.Content{before, after}, invocations, 1)
	if len(got) != 3 || got[0] != before || got[2] != after {
		t.Fatalf("insertion result = %#v", got)
	}
	if names := carryMarkerNames(t, carryTextAt(t, got[1])); !reflect.DeepEqual(names, []string{"newest", "middle", "older"}) {
		t.Fatalf("marker order = %v", names)
	}

	front := ReattachInvocations([]*genai.Content{before}, invocations[:1], -100)
	if len(front) != 2 || front[1] != before || !isSyntheticCarryContent(front[0]) {
		t.Fatalf("negative insertAt was not clamped to front: %#v", front)
	}
	back := ReattachInvocations([]*genai.Content{after}, invocations[:1], 100)
	if len(back) != 2 || back[0] != after || !isSyntheticCarryContent(back[1]) {
		t.Fatalf("large insertAt was not clamped to back: %#v", back)
	}
}

func TestReattachInvocationsEnforcesPerSkillAndCombinedBounds(t *testing.T) {
	invocations := make([]Invocation, 0, 7)
	for sequence := uint64(1); sequence <= 7; sequence++ {
		name := "skill-" + strconvItoaForCarryTest(int(sequence))
		// A multibyte tail exercises the Unicode-safe boundary path while each
		// original remains within MaxRenderedSkillBytes.
		rendered := strings.Repeat("x", 29_001) + "界"
		invocations = append(invocations, carryTestInvocation(name, rendered, sequence))
	}

	got := ReattachInvocations(nil, invocations, 0)
	if len(got) != 1 {
		t.Fatalf("history len = %d, want one carry content", len(got))
	}
	text := carryTextAt(t, got[0])
	if tokens := estimatedCarryTokens(text); tokens > SkillCarryCombinedTokens {
		t.Fatalf("combined estimated tokens = %d, limit %d", tokens, SkillCarryCombinedTokens)
	}
	markers, ok := parseCarryEnvelope(text)
	if !ok || len(markers) != 5 {
		t.Fatalf("markers = %#v ok=%v, want five newest bounded blocks", markers, ok)
	}
	for i, marker := range markers {
		want := "skill-" + strconvItoaForCarryTest(7-i)
		if marker.name != want {
			t.Fatalf("marker[%d] = %q, want %q", i, marker.name, want)
		}
	}
	if strings.Contains(text, "skill-2") || strings.Contains(text, "skill-1") {
		t.Fatalf("oldest snapshots should be excluded by combined limit:\n%s", text)
	}
	if !strings.Contains(text, skillCarryTruncationNotice) || !utf8.ValidString(text) {
		t.Fatalf("truncated carry lacks notice or valid UTF-8")
	}

	for _, invocation := range latestCarryInvocations(invocations) {
		block, ok := buildCarryBlock(invocation, SkillCarryPerInvocationTokens)
		if !ok {
			t.Fatalf("could not build bounded block for %s", invocation.name)
		}
		if tokens := estimatedCarryTokens(block); tokens > SkillCarryPerInvocationTokens {
			t.Fatalf("block %s estimated tokens = %d", invocation.name, tokens)
		}
		if !utf8.ValidString(block) {
			t.Fatalf("block %s is invalid UTF-8", invocation.name)
		}
	}
}

func TestEstimatedCarryTokensIsRuneAwareAndCodeConservative(t *testing.T) {
	if got := estimatedCarryTokens(strings.Repeat("x", 15_000)); got != 5_000 {
		t.Fatalf("ASCII letters estimate = %d, want 5000", got)
	}
	if got := estimatedCarryTokens(strings.Repeat("я", 15_000)); got != 5_000 {
		t.Fatalf("Cyrillic letters estimate = %d, want 5000", got)
	}
	if got := estimatedCarryTokens(strings.Repeat("界", 5_000)); got != 5_000 {
		t.Fatalf("CJK estimate = %d, want 5000", got)
	}
	if got := estimatedCarryTokens(strings.Repeat("{", 10_000)); got != 5_000 {
		t.Fatalf("code punctuation estimate = %d, want 5000", got)
	}
}

func TestReattachInvocationsKeepsBeginningWhenTruncated(t *testing.T) {
	rendered := strings.Repeat("начало-", 2_000) + "THE-END-MUST-BE-GONE"
	invocation := carryTestInvocation("unicode", rendered, 1)
	got := ReattachInvocations(nil, []Invocation{invocation}, 0)
	text := carryTextAt(t, got[0])
	if !strings.Contains(text, strings.Repeat("начало-", 20)) {
		t.Fatal("carry did not retain the beginning of rendered instructions")
	}
	if strings.Contains(text, "THE-END-MUST-BE-GONE") {
		t.Fatal("carry retained the tail despite truncation")
	}
	if !strings.Contains(text, "Skill snapshot truncated") || !utf8.ValidString(text) {
		t.Fatal("carry lacks explicit truncation notice or split UTF-8")
	}
}

func TestReattachInvocationsChangedDataHashWitnessesNotificationModifiedResponse(t *testing.T) {
	invocation := carryTestInvocation("verify", "full immutable skill render", 1)
	responseFor := func(changed bool) *genai.Content {
		return genai.NewContentFromParts([]*genai.Part{
			genai.NewPartFromFunctionResponse("skill", map[string]any{
				"success": true,
				"content": "[pending notification appended]\n" + invocation.Rendered,
				"data": map[string]any{
					"render_hash": invocation.RenderHash,
					"changed":     changed,
				},
			}),
		}, genai.RoleUser)
	}

	fullDelivery := responseFor(true)
	got := ReattachInvocations([]*genai.Content{fullDelivery}, []Invocation{invocation}, 1)
	if len(got) != 1 || got[0] != fullDelivery {
		t.Fatalf("changed=true render hash should witness full delivery: %#v", got)
	}

	shortDedup := responseFor(false)
	got = ReattachInvocations([]*genai.Content{shortDedup}, []Invocation{invocation}, 1)
	if len(got) != 2 || got[0] != shortDedup || !isSyntheticCarryContent(got[1]) {
		t.Fatalf("changed=false hash must not witness full delivery: %#v", got)
	}

	failed := responseFor(true)
	failed.Parts[0].FunctionResponse.Response["success"] = false
	got = ReattachInvocations([]*genai.Content{failed}, []Invocation{invocation}, 1)
	if len(got) != 2 || !isSyntheticCarryContent(got[1]) {
		t.Fatalf("failed response data must not witness full delivery: %#v", got)
	}
}

func TestReattachInvocationsRawSuccessfulSkillResponseSuppressesCarry(t *testing.T) {
	invocation := carryTestInvocation("verify", "run focused tests", 1)
	response := genai.NewContentFromParts([]*genai.Part{
		genai.NewPartFromFunctionResponse("skill", map[string]any{
			"success": true,
			"content": invocation.Rendered,
		}),
	}, genai.RoleUser)
	history := []*genai.Content{response}

	got := ReattachInvocations(history, []Invocation{invocation}, 0)
	if len(got) != 1 || got[0] != response || &got[0] != &history[0] {
		t.Fatalf("matching raw response should suppress carry: %#v", got)
	}

	cases := []map[string]any{
		{"success": false, "content": invocation.Rendered},
		{"success": "true", "content": invocation.Rendered},
		{"success": true, "content": "old render"},
		{"success": true, "content": 42},
	}
	for index, payload := range cases {
		part := genai.NewPartFromFunctionResponse("skill", payload)
		if index == len(cases)-1 {
			part.FunctionResponse.Name = "not-skill"
		}
		got = ReattachInvocations([]*genai.Content{genai.NewContentFromParts([]*genai.Part{part}, genai.RoleUser)}, []Invocation{invocation}, 10)
		if len(got) != 2 || !isSyntheticCarryContent(got[1]) {
			t.Fatalf("case %d incorrectly suppressed carry: %#v", index, got)
		}
	}
}

func TestReattachInvocationsExactUserSkillPromptSuppressesCarry(t *testing.T) {
	invocation := carryTestInvocation("explicit", "Loaded explicit /skill workflow in full", 1)
	rawPrompt := genai.NewContentFromText(invocation.Rendered, genai.RoleUser)
	got := ReattachInvocations([]*genai.Content{rawPrompt}, []Invocation{invocation}, 0)
	if len(got) != 1 || got[0] != rawPrompt {
		t.Fatalf("exact explicit skill prompt should witness delivery: %#v", got)
	}

	mentioned := genai.NewContentFromText("summary: "+invocation.Rendered, genai.RoleUser)
	got = ReattachInvocations([]*genai.Content{mentioned}, []Invocation{invocation}, 1)
	if len(got) != 2 || got[0] != mentioned || !isSyntheticCarryContent(got[1]) {
		t.Fatalf("non-exact mention incorrectly suppressed carry: %#v", got)
	}
}

func TestReattachInvocationsMarkerLookingRawTextNeverSuppressesBody(t *testing.T) {
	current := carryTestInvocation("review", "new review instructions", 2)
	currentMarker := carryHeaderLine(current, len(current.Rendered))
	raw := genai.NewContentFromText("summary copied this marker:\n"+currentMarker+"\nwithout being a carry envelope", genai.RoleUser)
	history := []*genai.Content{raw}

	got := ReattachInvocations(history, []Invocation{current}, 1)
	if len(got) != 2 || got[0] != raw || !isSyntheticCarryContent(got[1]) {
		t.Fatalf("copied current marker incorrectly suppressed carry: %#v", got)
	}

	old := carryTestInvocation("review", "old review instructions", 1)
	oldMarker := carryHeaderLine(old, len(old.Rendered))
	rawOld := genai.NewContentFromText(oldMarker, genai.RoleUser)
	got = ReattachInvocations([]*genai.Content{rawOld}, []Invocation{current}, 0)
	if len(got) != 2 || !isSyntheticCarryContent(got[0]) || got[1] != rawOld {
		t.Fatalf("old marker incorrectly suppressed current snapshot: %#v", got)
	}
}

func TestReattachInvocationsReplacesOldHashAndRemovesStaleCarry(t *testing.T) {
	rawBefore := genai.NewContentFromText("raw before", genai.RoleUser)
	rawAfter := genai.NewContentFromText("raw after", genai.RoleModel)
	old := carryTestInvocation("review", "old instructions", 1)
	first := ReattachInvocations([]*genai.Content{rawBefore, rawAfter}, []Invocation{old}, 1)
	if len(first) != 3 || !isSyntheticCarryContent(first[1]) {
		t.Fatalf("initial carry = %#v", first)
	}

	current := carryTestInvocation("review", "new instructions", 2)
	replaced := ReattachInvocations(first, []Invocation{current}, 1)
	if len(replaced) != 3 || replaced[0] != rawBefore || replaced[2] != rawAfter || !isSyntheticCarryContent(replaced[1]) {
		t.Fatalf("replacement = %#v", replaced)
	}
	text := carryTextAt(t, replaced[1])
	if strings.Contains(text, old.Rendered) || !strings.Contains(text, current.Rendered) {
		t.Fatalf("old carry was not replaced by current hash:\n%s", text)
	}

	withoutActive := ReattachInvocations(replaced, nil, 1)
	if len(withoutActive) != 2 || withoutActive[0] != rawBefore || withoutActive[1] != rawAfter {
		t.Fatalf("stale carry not removed while preserving raw history: %#v", withoutActive)
	}
}

func TestReattachInvocationsAdjustsInsertionAfterRemovingEarlierCarry(t *testing.T) {
	summary := genai.NewContentFromText("summary", genai.RoleUser)
	tail := genai.NewContentFromText("tail", genai.RoleModel)
	old := carryTestInvocation("review", "old instructions", 1)
	withOldCarry := ReattachInvocations([]*genai.Content{summary, tail}, []Invocation{old}, 0)
	if len(withOldCarry) != 3 || !isSyntheticCarryContent(withOldCarry[0]) {
		t.Fatalf("fixture carry = %#v", withOldCarry)
	}

	current := carryTestInvocation("review", "current instructions", 2)
	// Index 2 in the supplied history means immediately after summary. Removing
	// the old carry at index 0 must translate it to raw index 1.
	refreshed := ReattachInvocations(withOldCarry, []Invocation{current}, 2)
	if len(refreshed) != 3 || refreshed[0] != summary || !isSyntheticCarryContent(refreshed[1]) || refreshed[2] != tail {
		t.Fatalf("adjusted insertion = %#v", refreshed)
	}
}

func TestReattachInvocationsCreatesOnePlainUserContentForManySkills(t *testing.T) {
	invocations := []Invocation{
		carryTestInvocation("one", "one body", 3),
		carryTestInvocation("two", "two body", 2),
		carryTestInvocation("three", "three body", 1),
	}
	got := ReattachInvocations(nil, invocations, 0)
	if len(got) != 1 || !isSyntheticCarryContent(got[0]) {
		t.Fatalf("expected one plain synthetic user content: %#v", got)
	}
	if names := carryMarkerNames(t, carryTextAt(t, got[0])); !reflect.DeepEqual(names, []string{"one", "two", "three"}) {
		t.Fatalf("names = %v", names)
	}
}

func TestReattachInvocationsRobustToNilInvalidContentAndPreservesRawMessages(t *testing.T) {
	invocation := carryTestInvocation("safe", "safe body", 1)
	invalidEnvelope := genai.NewContentFromText(skillCarryStart+"\nraw user text\n"+skillCarryEnd, genai.RoleUser)
	mixedPart := &genai.Part{Text: "raw", FunctionResponse: &genai.FunctionResponse{Name: "skill"}}
	history := []*genai.Content{
		nil,
		{Role: string(genai.RoleUser), Parts: nil},
		{Role: string(genai.RoleUser), Parts: []*genai.Part{nil}},
		invalidEnvelope,
		{Role: string(genai.RoleUser), Parts: []*genai.Part{mixedPart}},
		{Role: string(genai.RoleUser), Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "skill", Response: nil}}}},
	}
	got := ReattachInvocations(history, []Invocation{invocation}, len(history))
	if len(got) != len(history)+1 || !isSyntheticCarryContent(got[len(got)-1]) {
		t.Fatalf("invalid history result = %#v", got)
	}
	for i := range history {
		if got[i] != history[i] {
			t.Fatalf("raw history[%d] was altered or removed", i)
		}
	}
}

func TestReattachInvocationsDoesNotMutateInputsOrBackingArray(t *testing.T) {
	raw := genai.NewContentFromText("raw", genai.RoleUser)
	sentinel := genai.NewContentFromText("sentinel", genai.RoleModel)
	backing := make([]*genai.Content, 2, 4)
	backing[0], backing[1] = raw, sentinel
	history := backing[:1]
	invocation := carryTestInvocation("immutable", "rendered value", 7)
	invocations := []Invocation{invocation}

	got := ReattachInvocations(history, invocations, 1)
	if len(got) != 2 || backing[1] != sentinel {
		t.Fatalf("caller backing array overwritten: backing=%#v got=%#v", backing, got)
	}
	if history[0] != raw || invocations[0] != invocation {
		t.Fatalf("inputs mutated: history=%#v invocations=%#v", history, invocations)
	}
	if got[0] != raw || !strings.Contains(carryTextAt(t, got[1]), invocation.Rendered) {
		t.Fatalf("unexpected output: %#v", got)
	}
}

func TestReattachInvocationsSkipsInvalidInvocationWithoutMutatingHistory(t *testing.T) {
	raw := genai.NewContentFromText("raw", genai.RoleUser)
	history := []*genai.Content{raw}
	invalid := []Invocation{
		{Name: "bad/name", Rendered: "body", Sequence: 3},
		{Name: "empty", Rendered: " \n", Sequence: 2},
		{Name: "utf8", Rendered: string([]byte{0xff}), Sequence: 1},
	}
	got := ReattachInvocations(history, invalid, 0)
	if len(got) != 1 || got[0] != raw || &got[0] != &history[0] {
		t.Fatalf("invalid invocations changed history: %#v", got)
	}
}
