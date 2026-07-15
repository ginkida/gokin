package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// RequestID is intentionally part of the public request/response messages in
// these tests. File path and content are not sufficient ownership checks: an
// identical review can be opened again after request A has settled, producing
// the classic A -> B -> late A ABA sequence.
func TestStaleSingleDiffResponseCannotSettleNewerIdenticalRequest(t *testing.T) {
	m := *NewModel()
	var decisions []DiffDecision
	m.SetDiffDecisionCallback(func(decision DiffDecision) {
		decisions = append(decisions, decision)
	})

	requestA := DiffPreviewRequestMsg{
		RequestID:  "single-diff-A",
		FilePath:   "same.go",
		OldContent: "old\n",
		NewContent: "new\n",
		ToolName:   "edit",
	}
	openedA, _ := updateWorkspaceOwnershipModel(t, m, requestA)
	decidedA, responseCmdA := updateWorkspaceOwnershipModel(t, openedA, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if responseCmdA == nil {
		t.Fatal("setup: single diff A did not emit a response")
	}
	responseA, ok := responseCmdA().(DiffPreviewResponseMsg)
	if !ok {
		t.Fatalf("single diff A emitted %T, want DiffPreviewResponseMsg", responseCmdA())
	}
	if responseA.RequestID != requestA.RequestID {
		t.Fatalf("single diff response lost owner: got %q want %q", responseA.RequestID, requestA.RequestID)
	}
	settledA, _ := updateWorkspaceOwnershipModel(t, decidedA, responseA)
	if settledA.diffRequest != nil || len(decisions) != 1 || decisions[0] != DiffApply {
		t.Fatalf("setup: single diff A did not settle once: request=%+v decisions=%v", settledA.diffRequest, decisions)
	}

	requestB := requestA
	requestB.RequestID = "single-diff-B"
	openedB, _ := updateWorkspaceOwnershipModel(t, settledA, requestB)
	if openedB.state != StateDiffPreview || openedB.diffRequest == nil || openedB.diffRequest.RequestID != requestB.RequestID {
		t.Fatalf("setup: single diff B did not open: state=%v request=%+v", openedB.state, openedB.diffRequest)
	}

	afterLateA, _ := updateWorkspaceOwnershipModel(t, openedB, responseA)
	if afterLateA.state != StateDiffPreview || afterLateA.diffRequest == nil || afterLateA.diffRequest.RequestID != requestB.RequestID {
		t.Errorf("late single response A settled B: state=%v request=%+v", afterLateA.state, afterLateA.diffRequest)
	}
	if len(decisions) != 1 {
		t.Errorf("late single response A reached callback: decisions=%v", decisions)
	}
	if afterLateA.state != StateDiffPreview || afterLateA.diffRequest == nil {
		return
	}

	decidedB, responseCmdB := updateWorkspaceOwnershipModel(t, afterLateA, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if responseCmdB == nil {
		t.Fatal("single diff B did not emit its own response after stale A was ignored")
	}
	responseB, ok := responseCmdB().(DiffPreviewResponseMsg)
	if !ok {
		t.Fatalf("single diff B emitted %T, want DiffPreviewResponseMsg", responseCmdB())
	}
	if responseB.RequestID != requestB.RequestID {
		t.Fatalf("single diff B response owner=%q, want %q", responseB.RequestID, requestB.RequestID)
	}
	settledB, _ := updateWorkspaceOwnershipModel(t, decidedB, responseB)
	if settledB.diffRequest != nil || len(decisions) != 2 || decisions[1] != DiffReject {
		t.Errorf("matching single response B did not settle exactly B: request=%+v decisions=%v", settledB.diffRequest, decisions)
	}
}

func TestStaleMultiDiffResponseCannotSettleNewerIdenticalRequest(t *testing.T) {
	m := *NewModel()
	var decisions []map[string]DiffDecision
	m.SetMultiDiffDecisionCallback(func(got map[string]DiffDecision) {
		decisions = append(decisions, cloneDiffDecisions(got))
	})

	requestA := MultiDiffPreviewRequestMsg{
		RequestID: "multi-diff-A",
		Files: []DiffFile{{
			FilePath: "same.go", OldContent: "old\n", NewContent: "new\n",
		}},
	}
	openedA, _ := updateWorkspaceOwnershipModel(t, m, requestA)
	decidedA, responseCmdA := updateWorkspaceOwnershipModel(t, openedA, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if responseCmdA == nil {
		t.Fatal("setup: multi diff A did not emit a response")
	}
	responseA, ok := responseCmdA().(MultiDiffPreviewResponseMsg)
	if !ok {
		t.Fatalf("multi diff A emitted %T, want MultiDiffPreviewResponseMsg", responseCmdA())
	}
	if responseA.RequestID != requestA.RequestID {
		t.Fatalf("multi diff response lost owner: got %q want %q", responseA.RequestID, requestA.RequestID)
	}
	settledA, _ := updateWorkspaceOwnershipModel(t, decidedA, responseA)
	if settledA.multiDiffRequest != nil || len(decisions) != 1 || decisions[0]["same.go"] != DiffApply {
		t.Fatalf("setup: multi diff A did not settle once: request=%+v decisions=%v", settledA.multiDiffRequest, decisions)
	}

	requestB := requestA
	requestB.RequestID = "multi-diff-B"
	openedB, _ := updateWorkspaceOwnershipModel(t, settledA, requestB)
	if openedB.state != StateMultiDiffPreview || openedB.multiDiffRequest == nil || openedB.multiDiffRequest.RequestID != requestB.RequestID {
		t.Fatalf("setup: multi diff B did not open: state=%v request=%+v", openedB.state, openedB.multiDiffRequest)
	}

	afterLateA, _ := updateWorkspaceOwnershipModel(t, openedB, responseA)
	if afterLateA.state != StateMultiDiffPreview || afterLateA.multiDiffRequest == nil || afterLateA.multiDiffRequest.RequestID != requestB.RequestID {
		t.Errorf("late multi response A settled B: state=%v request=%+v", afterLateA.state, afterLateA.multiDiffRequest)
	}
	if len(decisions) != 1 {
		t.Errorf("late multi response A reached callback: decisions=%v", decisions)
	}
	if afterLateA.state != StateMultiDiffPreview || afterLateA.multiDiffRequest == nil {
		return
	}

	decidedB, responseCmdB := updateWorkspaceOwnershipModel(t, afterLateA, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if responseCmdB == nil {
		t.Fatal("multi diff B did not emit its own response after stale A was ignored")
	}
	responseB, ok := responseCmdB().(MultiDiffPreviewResponseMsg)
	if !ok {
		t.Fatalf("multi diff B emitted %T, want MultiDiffPreviewResponseMsg", responseCmdB())
	}
	if responseB.RequestID != requestB.RequestID {
		t.Fatalf("multi diff B response owner=%q, want %q", responseB.RequestID, requestB.RequestID)
	}
	settledB, _ := updateWorkspaceOwnershipModel(t, decidedB, responseB)
	if settledB.multiDiffRequest != nil || len(decisions) != 2 || decisions[1]["same.go"] != DiffReject {
		t.Errorf("matching multi response B did not settle exactly B: request=%+v decisions=%v", settledB.multiDiffRequest, decisions)
	}
}

// Inline Git diff loads need an operation RequestID in addition to FilePath.
// The path can be identical after closing and reopening Git Status, so the
// action emitted by the panel and the eventual result must carry the same ID.
func TestGitStatusDiffIgnoresLateSameFileResponseFromPreviousOpening(t *testing.T) {
	m := *NewModel()
	m.width, m.height = 90, 24
	diffRequests := 0
	m.SetGitStatusActionCallback(func(action GitAction, _ []string, _ string) {
		if action == GitActionDiff {
			diffRequests++
		}
	})
	request := GitStatusRequestMsg{
		Branch: "main",
		Entries: []GitFileEntry{{
			FilePath: "same.go", Status: GitFileModified,
		}},
	}

	openedA, _ := updateWorkspaceOwnershipModel(t, m, request)
	afterDiffKeyA, diffCmdA := updateWorkspaceOwnershipModel(t, openedA, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if diffCmdA == nil {
		t.Fatal("setup: Git Status A did not emit a diff action")
	}
	diffActionA, ok := diffCmdA().(GitStatusActionMsg)
	if !ok {
		t.Fatalf("Git Status A emitted %T, want GitStatusActionMsg", diffCmdA())
	}
	if diffActionA.RequestID == "" {
		t.Fatal("Git diff action A has no RequestID")
	}
	loadingA, _ := updateWorkspaceOwnershipModel(t, afterDiffKeyA, diffActionA)
	afterCloseKey, closeA := updateWorkspaceOwnershipModel(t, loadingA, tea.KeyMsg{Type: tea.KeyEsc})
	if closeA == nil {
		t.Fatal("setup: Git Status A did not emit close")
	}
	closedA, _ := updateWorkspaceOwnershipModel(t, afterCloseKey, closeA())

	openedB, _ := updateWorkspaceOwnershipModel(t, closedA, request)
	afterDiffKeyB, diffCmdB := updateWorkspaceOwnershipModel(t, openedB, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if diffCmdB == nil {
		t.Fatal("setup: Git Status B did not emit a diff action")
	}
	diffActionB, ok := diffCmdB().(GitStatusActionMsg)
	if !ok {
		t.Fatalf("Git Status B emitted %T, want GitStatusActionMsg", diffCmdB())
	}
	if diffActionB.RequestID == "" || diffActionB.RequestID == diffActionA.RequestID {
		t.Fatalf("same-file Git diff requests are not uniquely owned: A=%q B=%q", diffActionA.RequestID, diffActionB.RequestID)
	}
	loadingB, _ := updateWorkspaceOwnershipModel(t, afterDiffKeyB, diffActionB)
	if diffRequests != 2 {
		t.Fatalf("setup: dispatched Git diff requests=%d, want 2", diffRequests)
	}

	afterLateA, _ := updateWorkspaceOwnershipModel(t, loadingB, GitStatusDiffMsg{
		RequestID: diffActionA.RequestID,
		FilePath:  "same.go",
		Content:   "stale content from A",
	})
	previewAfterA := stripAnsi(afterLateA.gitStatusModel.diffViewport.View())
	if afterLateA.state != StateGitStatus || afterLateA.gitStatusModel.pendingDiffPath != "same.go" ||
		strings.Contains(previewAfterA, "stale content from A") || !strings.Contains(previewAfterA, "Loading diff") {
		t.Errorf("late same-file response A consumed B: state=%v pending=%q preview=%q", afterLateA.state, afterLateA.gitStatusModel.pendingDiffPath, previewAfterA)
	}

	loadedB, _ := updateWorkspaceOwnershipModel(t, afterLateA, GitStatusDiffMsg{
		RequestID: diffActionB.RequestID,
		FilePath:  "same.go",
		Content:   "fresh content from B",
	})
	previewAfterB := stripAnsi(loadedB.gitStatusModel.diffViewport.View())
	if loadedB.gitStatusModel.pendingDiffPath != "" || !strings.Contains(previewAfterB, "fresh content from B") {
		t.Errorf("matching same-file response B did not resolve B: pending=%q preview=%q", loadedB.gitStatusModel.pendingDiffPath, previewAfterB)
	}
}
