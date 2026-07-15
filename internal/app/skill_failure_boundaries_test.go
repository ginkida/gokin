package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

const (
	skillBoundaryCarryPrefix = "[gokin:active-skills:v1]\n"
	skillBoundarySentinel    = "<<APP-SKILL-FAILURE-BOUNDARY-SENTINEL>>"
)

func loadSessionBoundSkill(t *testing.T, session *chat.Session, body string) tools.ToolResult {
	t.Helper()

	root := t.TempDir()
	directory := filepath.Join(root, "reliable")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	document := "---\nname: Reliable workflow\ndescription: Preserve the active workflow\n---\n" + body
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := tools.NewSkillToolWithCatalog(skills.NewCatalog([]skills.Root{{
		Path:   root,
		Source: "project",
	}}))
	tool.SetInvocationLedger(session.InvocationLedger())
	result, err := tool.Execute(context.Background(), map[string]any{"name": "reliable"})
	if err != nil || !result.Success {
		t.Fatalf("load bound Skill = %#v, err=%v", result, err)
	}
	if !strings.Contains(result.Content, skillBoundarySentinel) {
		t.Fatalf("rendered Skill omitted sentinel: %q", result.Content)
	}
	snapshot := session.InvocationLedger().SnapshotNewestFirst()
	if len(snapshot) != 1 || snapshot[0].Rendered != result.Content {
		t.Fatalf("session ledger was not updated before history commit: %#v", snapshot)
	}
	return result
}

func skillBoundaryToolPair(id string, result tools.ToolResult) (*genai.Content, *genai.Content) {
	call := &genai.FunctionCall{ID: id, Name: "skill", Args: map[string]any{"name": "reliable"}}
	callContent := genai.NewContentFromParts([]*genai.Part{{FunctionCall: call}}, genai.RoleModel)
	responsePart := genai.NewPartFromFunctionResponse("skill", result.ToMap())
	responsePart.FunctionResponse.ID = id
	responseContent := genai.NewContentFromParts([]*genai.Part{responsePart}, genai.RoleUser)
	return callContent, responseContent
}

func skillBoundaryCarryTexts(history []*genai.Content) []string {
	var carries []string
	for _, content := range history {
		if content == nil || content.Role != string(genai.RoleUser) || len(content.Parts) != 1 || content.Parts[0] == nil {
			continue
		}
		part := content.Parts[0]
		if part.FunctionCall == nil && part.FunctionResponse == nil && strings.HasPrefix(part.Text, skillBoundaryCarryPrefix) {
			carries = append(carries, part.Text)
		}
	}
	return carries
}

func assertOneSkillBoundaryCarry(t *testing.T, history []*genai.Content) {
	t.Helper()
	carries := skillBoundaryCarryTexts(history)
	if len(carries) != 1 {
		t.Fatalf("synthetic active-Skill carry count = %d, want 1; history=%#v", len(carries), history)
	}
	if !strings.Contains(carries[0], skillBoundarySentinel) {
		t.Fatalf("synthetic carry omitted immutable Skill sentinel: %q", carries[0])
	}
}

func countSkillBoundaryRawResponses(history []*genai.Content) int {
	count := 0
	for _, content := range history {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil || part.FunctionResponse == nil || part.FunctionResponse.Name != "skill" {
				continue
			}
			if rendered, _ := part.FunctionResponse.Response["content"].(string); strings.Contains(rendered, skillBoundarySentinel) {
				count++
			}
		}
	}
	return count
}

func countSkillBoundaryFunctionParts(history []*genai.Content) int {
	count := 0
	for _, content := range history {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part != nil && (part.FunctionCall != nil || part.FunctionResponse != nil) {
				count++
			}
		}
	}
	return count
}

func skillBoundaryLargeHistory(messages int) []*genai.Content {
	history := make([]*genai.Content, 0, messages)
	for index := 0; index < messages; index++ {
		var role genai.Role = genai.RoleUser
		if index%2 == 1 {
			role = genai.RoleModel
		}
		text := strings.Repeat(string(rune('a'+index%20)), 1_200)
		history = append(history, genai.NewContentFromText(text, role))
	}
	return history
}

func newSkillBoundaryContextManager(t *testing.T, session *chat.Session) *appcontext.ContextManager {
	t.Helper()
	manager := appcontext.NewContextManager(
		context.Background(),
		session,
		testkit.NewMockClient(),
		&config.ContextConfig{MaxInputTokens: 200},
	)
	t.Cleanup(manager.Close)
	return manager
}

// A terminal model error can leave a successful tool result as the final user
// message. The production terminal path trims that result back to the last
// model message and strips its now-orphaned FunctionCall. The session-owned
// ledger must still reconstruct exactly one active-Skill context block.
func TestTerminalFailureTrimReattachesSessionSkillSnapshot(t *testing.T) {
	session := chat.NewSession()
	session.SetHistory([]*genai.Content{
		genai.NewContentFromText("original task", genai.RoleUser),
		genai.NewContentFromText("working", genai.RoleModel),
	})
	baseline := session.GetHistory()

	result := loadSessionBoundSkill(t, session, "Run focused checks. "+skillBoundarySentinel)
	call, response := skillBoundaryToolPair("terminal-skill", result)
	localExecutorHistory := append(append([]*genai.Content(nil), baseline...), call, response)

	trimmed := trimToLastModelMessage(localExecutorHistory, len(baseline))
	session.SetHistory(trimmed)
	committed := session.GetHistory()

	assertOneSkillBoundaryCarry(t, committed)
	if got := countSkillBoundaryFunctionParts(committed); got != 0 {
		t.Fatalf("terminal repair retained %d orphan tool parts, want 0", got)
	}
}

// The context-too-long branch truncates Session history before the executor's
// local history is committed. SkillTool records synchronously into the bound
// Session ledger, so the just-loaded workflow survives that ordering.
func TestEmergencyTruncateBeforeLocalCommitCarriesBoundSkill(t *testing.T) {
	session := chat.NewSession()
	session.SetHistory(skillBoundaryLargeHistory(18))
	baseline := session.GetHistory()
	result := loadSessionBoundSkill(t, session, "Keep retry state stable. "+skillBoundarySentinel)
	call, response := skillBoundaryToolPair("uncommitted-skill", result)
	localExecutorHistory := append(append([]*genai.Content(nil), baseline...), call, response)
	if countSkillBoundaryRawResponses(localExecutorHistory) != 1 {
		t.Fatal("test setup lacks the executor-local successful Skill response")
	}
	if carries := skillBoundaryCarryTexts(session.GetHistory()); len(carries) != 0 {
		t.Fatalf("Skill unexpectedly reached Session history before truncation: %#v", carries)
	}

	manager := newSkillBoundaryContextManager(t, session)
	if removed := manager.EmergencyTruncate(); removed <= 0 {
		t.Fatalf("EmergencyTruncate removed %d messages, want a destructive boundary", removed)
	}
	committed := session.GetHistory()
	assertOneSkillBoundaryCarry(t, committed)
	if got := countSkillBoundaryRawResponses(committed); got != 0 {
		t.Fatalf("uncommitted executor response leaked into Session history: %d", got)
	}
}

// Ordinary retry persistence keeps complete FunctionCall/FunctionResponse
// pairs. A current full Skill response is already the delivery witness and
// must suppress a redundant synthetic copy.
func TestOrdinaryRetryWithCurrentSkillResponseHasNoSyntheticDuplicate(t *testing.T) {
	session := chat.NewSession()
	session.SetHistory([]*genai.Content{
		genai.NewContentFromText("original task", genai.RoleUser),
		genai.NewContentFromText("working", genai.RoleModel),
	})
	baseline := session.GetHistory()
	result := loadSessionBoundSkill(t, session, "Retry without duplication. "+skillBoundarySentinel)
	call, response := skillBoundaryToolPair("retry-skill", result)
	localExecutorHistory := append(append([]*genai.Content(nil), baseline...), call, response)

	cleaned := stripOrphanFunctionCalls(localExecutorHistory)
	session.SetHistory(cleaned)
	committed := session.GetHistory()

	if carries := skillBoundaryCarryTexts(committed); len(carries) != 0 {
		t.Fatalf("current full Skill response produced %d synthetic duplicates: %#v", len(carries), carries)
	}
	if got := countSkillBoundaryRawResponses(committed); got != 1 {
		t.Fatalf("current full Skill response count = %d, want exactly 1", got)
	}
	if got := countSkillBoundaryFunctionParts(committed); got != 2 {
		t.Fatalf("ordinary retry did not retain the balanced tool pair: parts=%d", got)
	}
}

// Auto-resume uses EmergencyTruncate after a terminal retryable failure. This
// regression starts with the full Skill response in raw history (and therefore
// no synthetic copy), then forces that old pair out during compaction. The
// latest immutable ledger snapshot must be reattached for the resumed turn.
func TestAutoResumeCompactionReattachesDroppedSkillResponse(t *testing.T) {
	session := chat.NewSession()
	result := loadSessionBoundSkill(t, session, strings.Repeat("workflow-step ", 240)+skillBoundarySentinel)
	call, response := skillBoundaryToolPair("old-skill", result)

	history := []*genai.Content{
		genai.NewContentFromText("system grounding", genai.RoleUser),
		genai.NewContentFromText("acknowledged", genai.RoleModel),
		call,
		response,
	}
	history = append(history, skillBoundaryLargeHistory(18)...)
	session.SetHistory(history)
	if carries := skillBoundaryCarryTexts(session.GetHistory()); len(carries) != 0 {
		t.Fatalf("raw full response should initially suppress synthetic carry: %#v", carries)
	}
	if got := countSkillBoundaryRawResponses(session.GetHistory()); got != 1 {
		t.Fatalf("initial full Skill response count = %d, want 1", got)
	}

	manager := newSkillBoundaryContextManager(t, session)
	a := &App{contextManager: manager}
	if removed := a.performAutoResumeCompaction(); removed <= 0 {
		t.Fatalf("auto-resume compaction removed %d messages, want destructive compaction", removed)
	}
	committed := session.GetHistory()
	assertOneSkillBoundaryCarry(t, committed)
	if got := countSkillBoundaryRawResponses(committed); got != 0 {
		t.Fatalf("old full Skill response unexpectedly survived forced auto-resume compaction: %d", got)
	}
}
