package chat

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/skills"

	"google.golang.org/genai"
)

const sessionSkillCarryPrefix = "[gokin:active-skills:v1]\n"

func recordSessionSkill(t *testing.T, session *Session, name, rendered string) skills.Invocation {
	t.Helper()
	invocation, changed, err := session.InvocationLedger().Record(
		name,
		rendered,
		"project",
		"/workspace/.gokin/skills/"+name+"/SKILL.md",
	)
	if err != nil {
		t.Fatalf("record skill %q: %v", name, err)
	}
	if !changed {
		t.Fatalf("first record of skill %q reported changed=false", name)
	}
	return invocation
}

func sessionSkillCarryContents(history []*genai.Content) []*genai.Content {
	var result []*genai.Content
	for _, content := range history {
		if content == nil || content.Role != string(genai.RoleUser) || len(content.Parts) != 1 || content.Parts[0] == nil {
			continue
		}
		if strings.HasPrefix(content.Parts[0].Text, sessionSkillCarryPrefix) {
			result = append(result, content)
		}
	}
	return result
}

func sessionSkillCarryText(t *testing.T, history []*genai.Content) string {
	t.Helper()
	carries := sessionSkillCarryContents(history)
	if len(carries) != 1 {
		t.Fatalf("synthetic skill carry count = %d, want 1", len(carries))
	}
	part := carries[0].Parts[0]
	if part.FunctionCall != nil || part.FunctionResponse != nil || part.InlineData != nil || part.FileData != nil || part.Thought {
		t.Fatalf("skill carry is not a plain user text part: %#v", part)
	}
	return part.Text
}

func sessionToolPair(id, name string, response map[string]any) (*genai.Content, *genai.Content) {
	call := &genai.Content{
		Role: string(genai.RoleModel),
		Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID:   id,
			Name: name,
			Args: map[string]any{"name": "verify"},
		}}},
	}
	result := &genai.Content{
		Role: string(genai.RoleUser),
		Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID:       id,
			Name:     name,
			Response: response,
		}}},
	}
	return call, result
}

func assertBalancedSessionToolIDs(t *testing.T, history []*genai.Content) (map[string]int, map[string]int) {
	t.Helper()
	calls := make(map[string]int)
	responses := make(map[string]int)
	for _, content := range history {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			if part.FunctionCall != nil && part.FunctionCall.ID != "" {
				calls[part.FunctionCall.ID]++
			}
			if part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
				responses[part.FunctionResponse.ID]++
			}
		}
	}
	if !reflect.DeepEqual(calls, responses) {
		t.Fatalf("orphaned tool IDs after history gate: calls=%v responses=%v", calls, responses)
	}
	return calls, responses
}

func standaloneSessionInvocation(t *testing.T, name, rendered string) skills.Invocation {
	t.Helper()
	ledger := skills.NewInvocationLedger()
	invocation, changed, err := ledger.Record(name, rendered, "project", "/workspace/"+name+"/SKILL.md")
	if err != nil || !changed {
		t.Fatalf("construct persisted invocation: changed=%v err=%v", changed, err)
	}
	return invocation
}

func TestSessionSetHistoryReattachesExactlyOnePlainSkillCarry(t *testing.T) {
	session := NewSession()
	recordSessionSkill(t, session, "verify", "Run focused tests. <<ACTIVE-SKILL-SENTINEL>>")
	first := genai.NewContentFromText("raw first", genai.RoleUser)
	last := genai.NewContentFromText("raw last", genai.RoleModel)

	committed := session.SetHistoryWithSkillCarry([]*genai.Content{first, last}, 1)
	if len(committed) != 3 || committed[0] != first || committed[2] != last {
		t.Fatalf("unexpected committed history: %#v", committed)
	}
	if text := sessionSkillCarryText(t, committed); !strings.Contains(text, "<<ACTIVE-SKILL-SENTINEL>>") {
		t.Fatalf("carry omitted current rendered skill: %q", text)
	}

	// Feeding the committed history through the gate again must rebuild, not
	// duplicate, the synthetic message.
	session.SetHistory(committed)
	if carries := sessionSkillCarryContents(session.GetHistory()); len(carries) != 1 {
		t.Fatalf("second SetHistory produced %d carry messages, want 1", len(carries))
	}
}

func TestSessionRawSkillResponseWitnessAndIdenticalShortNote(t *testing.T) {
	t.Run("successful current hash suppresses synthetic duplicate", func(t *testing.T) {
		session := NewSession()
		invocation := recordSessionSkill(t, session, "verify", "full immutable verify workflow")
		call, response := sessionToolPair("skill-full", "skill", map[string]any{
			"success": true,
			// The executor may prepend a notification, so content itself no
			// longer hashes to the immutable render. changed=true plus the
			// current render_hash is the authoritative full-delivery witness.
			"content": "[notification]\n" + invocation.Rendered,
			"data": map[string]any{
				"render_hash": invocation.RenderHash,
				"changed":     true,
			},
		})

		session.SetHistory([]*genai.Content{call, response})
		history := session.GetHistory()
		if len(history) != 2 || len(sessionSkillCarryContents(history)) != 0 {
			t.Fatalf("full raw skill delivery should suppress carry: %#v", history)
		}
		assertBalancedSessionToolIDs(t, history)
	})

	t.Run("identical short note does not suppress", func(t *testing.T) {
		session := NewSession()
		invocation := recordSessionSkill(t, session, "verify", "full immutable verify workflow")
		call, response := sessionToolPair("skill-dedup", "skill", map[string]any{
			"success": true,
			"content": "Skill verify is already loaded with the same rendered content.",
			"data": map[string]any{
				"render_hash": invocation.RenderHash,
				"changed":     false,
			},
		})

		session.SetHistory([]*genai.Content{call, response})
		history := session.GetHistory()
		if len(history) != 3 {
			t.Fatalf("history len = %d, want pair plus carry", len(history))
		}
		if text := sessionSkillCarryText(t, history); !strings.Contains(text, invocation.Rendered) {
			t.Fatalf("short dedup note suppressed immutable snapshot: %q", text)
		}
		assertBalancedSessionToolIDs(t, history)
	})
}

func TestSessionHardLimitKeepsActiveSkillAndLatestRawMessage(t *testing.T) {
	session := NewSession()
	recordSessionSkill(t, session, "reliability", "Preserve this workflow. <<HARD-LIMIT-SKILL-SENTINEL>>")
	history := make([]*genai.Content, 0, MaxMessages+7)
	for index := 0; index < MaxMessages+7; index++ {
		history = append(history, genai.NewContentFromText(fmt.Sprintf("raw-%03d", index), genai.RoleUser))
	}

	session.SetHistory(history)
	committed := session.GetHistory()
	if len(committed) != MaxMessages {
		t.Fatalf("history len = %d, want hard limit %d", len(committed), MaxMessages)
	}
	if text := sessionSkillCarryText(t, committed); !strings.Contains(text, "<<HARD-LIMIT-SKILL-SENTINEL>>") {
		t.Fatal("active skill sentinel was lost at hard history limit")
	}
	latest := history[len(history)-1]
	if committed[len(committed)-1] != latest {
		t.Fatal("newest raw message did not survive hard trimming")
	}
}

func TestSessionHardLimitNeverLeavesToolPairOrphansAcrossBoundary(t *testing.T) {
	session := NewSession()
	recordSessionSkill(t, session, "pair-safety", "Keep tool-call history structurally valid.")
	crossingCall, crossingResponse := sessionToolPair("crossing", "read", map[string]any{"success": true})
	retainedCall, retainedResponse := sessionToolPair("retained", "read", map[string]any{"success": true})

	// 101 raw contents put the trim boundary between crossingCall and
	// crossingResponse. A complete newer pair must remain intact.
	history := []*genai.Content{crossingCall, crossingResponse}
	for index := 0; index < 95; index++ {
		history = append(history, genai.NewContentFromText(fmt.Sprintf("middle-%02d", index), genai.RoleUser))
	}
	history = append(history,
		retainedCall,
		retainedResponse,
		genai.NewContentFromText("tail-a", genai.RoleUser),
		genai.NewContentFromText("tail-b", genai.RoleModel),
	)
	if len(history) != MaxMessages+1 {
		t.Fatalf("fixture len = %d, want %d", len(history), MaxMessages+1)
	}

	session.SetHistory(history)
	committed := session.GetHistory()
	if len(committed) > MaxMessages {
		t.Fatalf("history len = %d exceeds %d", len(committed), MaxMessages)
	}
	calls, responses := assertBalancedSessionToolIDs(t, committed)
	if calls["crossing"] != 0 || responses["crossing"] != 0 {
		t.Fatalf("split boundary pair survived partially: calls=%v responses=%v", calls, responses)
	}
	if calls["retained"] != 1 || responses["retained"] != 1 {
		t.Fatalf("newer complete pair was not retained: calls=%v responses=%v", calls, responses)
	}
	if len(sessionSkillCarryContents(committed)) != 1 {
		t.Fatal("active skill carry was not reserved alongside pair-safe trim")
	}
}

func TestSessionSetHistoryIfVersionWithSkillCarryStaleIsNoOp(t *testing.T) {
	session := NewSession()
	recordSessionSkill(t, session, "stable", "Do not mutate stale optimistic snapshots.")
	session.AddContentWithTokens(genai.NewContentFromText("current", genai.RoleUser), 17)
	before, currentVersion := session.GetHistoryWithVersion()
	beforeCounts := session.GetTokenCounts()
	beforeTotal := session.GetTokenCount()

	committed, ok := session.SetHistoryIfVersionWithSkillCarry(
		[]*genai.Content{genai.NewContentFromText("must-not-commit", genai.RoleUser)},
		currentVersion-1,
		0,
	)
	if ok || committed != nil {
		t.Fatalf("stale write result = (%#v, %v), want (nil, false)", committed, ok)
	}
	after, afterVersion := session.GetHistoryWithVersion()
	if afterVersion != currentVersion || !reflect.DeepEqual(after, before) {
		t.Fatalf("stale write mutated history/version: before=%#v/%d after=%#v/%d", before, currentVersion, after, afterVersion)
	}
	if !reflect.DeepEqual(session.GetTokenCounts(), beforeCounts) || session.GetTokenCount() != beforeTotal {
		t.Fatal("stale write mutated token accounting")
	}
	if len(session.InvocationLedger().SnapshotNewestFirst()) != 1 {
		t.Fatal("stale write mutated active skill ledger")
	}
}

func TestSessionAddContentWithTokensMaintainsCarryLimitAndAccounting(t *testing.T) {
	session := NewSession()
	recordSessionSkill(t, session, "accounting", "Keep active instructions while appending messages.")
	for index := 1; index <= MaxMessages+17; index++ {
		session.AddContentWithTokens(
			genai.NewContentFromText(fmt.Sprintf("message-%03d", index), genai.RoleUser),
			index,
		)
	}

	history := session.GetHistory()
	counts := session.GetTokenCounts()
	if len(history) != MaxMessages || len(counts) != len(history) {
		t.Fatalf("history/count lengths = %d/%d, want %d/%d", len(history), len(counts), MaxMessages, MaxMessages)
	}
	if len(sessionSkillCarryContents(history)) != 1 {
		t.Fatal("append/trim cycle did not retain exactly one skill carry")
	}
	sum := 0
	for index, count := range counts {
		sum += count
		if strings.HasPrefix(history[index].Parts[0].Text, sessionSkillCarryPrefix) && count != 0 {
			t.Fatalf("synthetic carry token count = %d, want 0 until recounted", count)
		}
	}
	if total := session.GetTokenCount(); total != sum {
		t.Fatalf("totalTokens = %d, sum(tokenCounts) = %d", total, sum)
	}
}

func TestSessionStateJSONRoundTripRestoresSkillLedgerAndCarry(t *testing.T) {
	source := NewSession()
	invocation := recordSessionSkill(t, source, "resume", "Resume with this exact workflow. <<ROUNDTRIP-SENTINEL>>")
	source.SetHistory([]*genai.Content{genai.NewContentFromText("raw task", genai.RoleUser)})

	payload, err := json.Marshal(source.GetState())
	if err != nil {
		t.Fatalf("marshal SessionState: %v", err)
	}
	if !strings.Contains(string(payload), `"invoked_skills"`) {
		t.Fatalf("serialized state omitted invoked_skills: %s", payload)
	}
	var persisted SessionState
	if err := json.Unmarshal(payload, &persisted); err != nil {
		t.Fatalf("unmarshal SessionState: %v", err)
	}

	restored := NewSession()
	if err := restored.RestoreFromState(&persisted); err != nil {
		t.Fatalf("RestoreFromState: %v", err)
	}
	snapshot := restored.InvocationLedger().SnapshotNewestFirst()
	if len(snapshot) != 1 || snapshot[0] != invocation {
		t.Fatalf("restored ledger = %#v, want %#v", snapshot, invocation)
	}
	if text := sessionSkillCarryText(t, restored.GetHistory()); !strings.Contains(text, "<<ROUNDTRIP-SENTINEL>>") {
		t.Fatalf("restored carry omitted sentinel: %q", text)
	}
}

func TestSessionRestoreSkillLedgerIsReplaceNotMerge(t *testing.T) {
	t.Run("legacy state clears preexisting ledger", func(t *testing.T) {
		legacy := NewSession()
		legacy.SetHistory([]*genai.Content{genai.NewContentFromText("legacy raw", genai.RoleUser)})
		state := legacy.GetState()
		state.InvokedSkills = nil

		target := NewSession()
		recordSessionSkill(t, target, "stale", "must not survive legacy resume")
		target.SetHistory(nil)
		if err := target.RestoreFromState(state); err != nil {
			t.Fatalf("RestoreFromState: %v", err)
		}
		if got := target.InvocationLedger().SnapshotNewestFirst(); len(got) != 0 {
			t.Fatalf("legacy restore retained stale invocations: %#v", got)
		}
		if carries := sessionSkillCarryContents(target.GetHistory()); len(carries) != 0 {
			t.Fatalf("legacy restore retained stale carry: %#v", carries)
		}
	})

	t.Run("corrupt entry is skipped while valid subset replaces stale", func(t *testing.T) {
		valid := standaloneSessionInvocation(t, "valid", "valid persisted workflow")
		corrupt := standaloneSessionInvocation(t, "corrupt", "corrupt persisted workflow")
		corrupt.RenderHash = strings.Repeat("0", 64)
		state := NewSession().GetState()
		state.InvokedSkills = []skills.Invocation{corrupt, valid}

		target := NewSession()
		recordSessionSkill(t, target, "stale", "must be replaced")
		if err := target.RestoreFromState(state); err != nil {
			t.Fatalf("RestoreFromState: %v", err)
		}
		got := target.InvocationLedger().SnapshotNewestFirst()
		if len(got) != 1 || got[0] != valid {
			t.Fatalf("valid restored subset = %#v, want only %#v", got, valid)
		}
		if text := sessionSkillCarryText(t, target.GetHistory()); !strings.Contains(text, valid.Rendered) || strings.Contains(text, "must be replaced") {
			t.Fatalf("carry does not reflect validated replacement set: %q", text)
		}
	})

	t.Run("over-cap payload clears stale ledger fail-closed", func(t *testing.T) {
		valid := standaloneSessionInvocation(t, "valid", "bounded workflow")
		state := NewSession().GetState()
		state.InvokedSkills = make([]skills.Invocation, skills.MaxInvocationRestoreEntries+1)
		for index := range state.InvokedSkills {
			state.InvokedSkills[index] = valid
		}

		target := NewSession()
		recordSessionSkill(t, target, "stale", "must be cleared on over-cap restore")
		target.SetHistory(nil)
		if err := target.RestoreFromState(state); err != nil {
			t.Fatalf("RestoreFromState should keep the healthy session resumable: %v", err)
		}
		if got := target.InvocationLedger().SnapshotNewestFirst(); len(got) != 0 {
			t.Fatalf("over-cap restore retained stale ledger: %#v", got)
		}
		if carries := sessionSkillCarryContents(target.GetHistory()); len(carries) != 0 {
			t.Fatalf("over-cap restore retained stale carry: %#v", carries)
		}
	})
}

func TestSessionClearRemovesSkillLedgerAndNamedCheckpoints(t *testing.T) {
	session := NewSession()
	recordSessionSkill(t, session, "clearable", "session-scoped instructions")
	session.SetHistory([]*genai.Content{genai.NewContentFromText("raw", genai.RoleUser)})
	session.SaveCheckpoint("before-clear")

	session.Clear()
	if session.MessageCount() != 0 || len(session.InvocationLedger().SnapshotNewestFirst()) != 0 {
		t.Fatal("Clear retained history or active Skill snapshots")
	}
	if checkpoints := session.ListCheckpoints(); len(checkpoints) != 0 {
		t.Fatalf("Clear retained named checkpoints: %v", checkpoints)
	}
	if session.RestoreCheckpoint("before-clear") {
		t.Fatal("checkpoint remained restorable after Clear")
	}
	state := session.GetState()
	if len(state.InvokedSkills) != 0 || len(state.CheckpointInvokedSkills) != 0 {
		t.Fatalf("cleared state retained Skill checkpoint data: %#v", state)
	}
}

func TestSessionForkSkillLedgerIsIndependent(t *testing.T) {
	parent := NewSession()
	parentInvocation := recordSessionSkill(t, parent, "review", "parent review workflow")
	parent.SetHistory([]*genai.Content{genai.NewContentFromText("shared raw", genai.RoleUser)})

	branch := parent.Fork("experiment")
	branchInvocation, changed, err := branch.InvocationLedger().Record(
		"review",
		"branch review workflow",
		"project",
		"/workspace/branch/review/SKILL.md",
	)
	if err != nil || !changed {
		t.Fatalf("change branch invocation: changed=%v err=%v", changed, err)
	}
	branch.SetHistory(branch.GetHistory())

	parentSnapshot := parent.InvocationLedger().SnapshotNewestFirst()
	branchSnapshot := branch.InvocationLedger().SnapshotNewestFirst()
	if len(parentSnapshot) != 1 || parentSnapshot[0] != parentInvocation {
		t.Fatalf("branch mutation changed parent ledger: %#v", parentSnapshot)
	}
	if len(branchSnapshot) != 1 || branchSnapshot[0] != branchInvocation {
		t.Fatalf("branch ledger = %#v, want %#v", branchSnapshot, branchInvocation)
	}
	if text := sessionSkillCarryText(t, parent.GetHistory()); !strings.Contains(text, parentInvocation.Rendered) || strings.Contains(text, branchInvocation.Rendered) {
		t.Fatalf("parent carry was contaminated by branch: %q", text)
	}
	if text := sessionSkillCarryText(t, branch.GetHistory()); !strings.Contains(text, branchInvocation.Rendered) || strings.Contains(text, parentInvocation.Rendered) {
		t.Fatalf("branch carry did not switch independently: %q", text)
	}
}

func TestSessionCheckpointRollbackRestoresPriorSkillSnapshot(t *testing.T) {
	session := NewSession()
	prior := recordSessionSkill(t, session, "deploy", "deploy workflow v1")
	session.SetHistory([]*genai.Content{genai.NewContentFromText("before", genai.RoleUser)})
	session.SaveCheckpoint("v1")

	current, changed, err := session.InvocationLedger().Record(
		"deploy",
		"deploy workflow v2",
		"project",
		"/workspace/deploy/SKILL.md",
	)
	if err != nil || !changed {
		t.Fatalf("record changed skill: changed=%v err=%v", changed, err)
	}
	session.AddUserMessage("after checkpoint")
	if text := sessionSkillCarryText(t, session.GetHistory()); !strings.Contains(text, current.Rendered) {
		t.Fatalf("current carry was not refreshed before rollback: %q", text)
	}

	if !session.RestoreCheckpoint("v1") {
		t.Fatal("RestoreCheckpoint(v1) failed")
	}
	snapshot := session.InvocationLedger().SnapshotNewestFirst()
	if len(snapshot) != 1 || snapshot[0] != prior {
		t.Fatalf("checkpoint restored ledger = %#v, want %#v", snapshot, prior)
	}
	text := sessionSkillCarryText(t, session.GetHistory())
	if !strings.Contains(text, prior.Rendered) || strings.Contains(text, current.Rendered) {
		t.Fatalf("checkpoint carry does not contain only prior snapshot: %q", text)
	}
}

func TestSessionRejectsCorruptCheckpointIndexBeforeChangingSkillLedger(t *testing.T) {
	session := NewSession()
	active := recordSessionSkill(t, session, "active", "keep current active workflow")
	session.SetHistory([]*genai.Content{genai.NewContentFromText("current", genai.RoleUser)})
	session.Checkpoints = map[string]int{"negative": -1, "past-end": session.MessageCount() + 10}
	session.checkpointInvokedSkills = map[string][]skills.Invocation{
		"negative": nil,
		"past-end": nil,
	}

	for _, name := range []string{"negative", "past-end"} {
		if session.RestoreCheckpoint(name) {
			t.Fatalf("RestoreCheckpoint(%q) accepted corrupt index", name)
		}
		got := session.InvocationLedger().SnapshotNewestFirst()
		if len(got) != 1 || got[0] != active {
			t.Fatalf("RestoreCheckpoint(%q) changed ledger before rejecting index: %#v", name, got)
		}
	}
}
