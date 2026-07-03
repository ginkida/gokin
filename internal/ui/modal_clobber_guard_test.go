package ui

import "testing"

// Round 4 self-improvement audit: PermissionRequestMsg/QuestionRequestMsg/
// PlanApprovalRequestMsg already guarded against clobbering an active modal
// (isModalState() check), but DiffPreviewRequestMsg, MultiDiffPreviewRequestMsg,
// SearchResultsRequestMsg, GitStatusRequestMsg, FileBrowserRequestMsg,
// OpenSettingsMsg, and OpenKeyEntryMsg did not — each unconditionally
// overwrote m.state. Since these all arrive asynchronously via
// safeSendToProgram from independent goroutines (a background /loop
// iteration's permission prompt vs. a foreground Ctrl+S, for instance),
// Bubble Tea's FIFO message processing can deliver them in either order:
// if an active PermissionPrompt/QuestionPrompt/PlanApproval is clobbered by
// one of these, the abandoned request's blocked decision channel hangs until
// ctx cancellation or its configured timeout (which per v0.100.56 can be -1,
// i.e. indefinite), with the user given no way to ever see or answer it.
//
// Each test below puts the model in an active modal state (StatePermissionPrompt)
// then delivers the message under test through the FULL Update path, asserting
// the modal survives (state unchanged) and, where a blocking decision channel
// exists, that it was resolved (auto-rejected) rather than left dangling.

func newModalGuardTestModel() Model {
	m := NewModel()
	m.width = 80
	m.height = 24
	m.permRequest = &PermissionRequestMsg{ID: "req-1", ToolName: "bash", RiskLevel: "high"}
	m.state = StatePermissionPrompt
	return *m
}

func TestDiffPreviewRequestMsg_DoesNotClobberActiveModal(t *testing.T) {
	m := newModalGuardTestModel()
	var gotDecision DiffDecision
	rejectCalled := false
	m.onDiffDecision = func(d DiffDecision) {
		rejectCalled = true
		gotDecision = d
	}

	updated, _ := m.Update(DiffPreviewRequestMsg{FilePath: "x.go", NewContent: "package x"})
	m2 := updated.(Model)

	if m2.state != StatePermissionPrompt {
		t.Fatalf("state = %v, want StatePermissionPrompt (active modal clobbered)", m2.state)
	}
	if m2.permRequest == nil || m2.permRequest.ID != "req-1" {
		t.Fatal("the active permission request was discarded")
	}
	if !rejectCalled || gotDecision != DiffReject {
		t.Fatalf("onDiffDecision called=%v decision=%v, want called=true decision=DiffReject (blocked caller left hanging)", rejectCalled, gotDecision)
	}
}

func TestMultiDiffPreviewRequestMsg_DoesNotClobberActiveModal(t *testing.T) {
	m := newModalGuardTestModel()
	var gotDecisions map[string]DiffDecision
	m.onMultiDiffDecision = func(d map[string]DiffDecision) { gotDecisions = d }

	updated, _ := m.Update(MultiDiffPreviewRequestMsg{Files: []DiffFile{
		{FilePath: "a.go"}, {FilePath: "b.go"},
	}})
	m2 := updated.(Model)

	if m2.state != StatePermissionPrompt {
		t.Fatalf("state = %v, want StatePermissionPrompt (active modal clobbered)", m2.state)
	}
	if len(gotDecisions) != 2 || gotDecisions["a.go"] != DiffReject || gotDecisions["b.go"] != DiffReject {
		t.Fatalf("onMultiDiffDecision = %v, want all files rejected (blocked caller left hanging)", gotDecisions)
	}
}

func TestSearchResultsRequestMsg_DoesNotClobberActiveModal(t *testing.T) {
	m := newModalGuardTestModel()
	updated, _ := m.Update(SearchResultsRequestMsg{Query: "foo", Tool: "grep"})
	m2 := updated.(Model)
	if m2.state != StatePermissionPrompt {
		t.Fatalf("state = %v, want StatePermissionPrompt (active modal clobbered)", m2.state)
	}
	if m2.searchRequest != nil {
		t.Fatal("searchRequest should not be set while another modal is active")
	}
}

func TestGitStatusRequestMsg_DoesNotClobberActiveModal(t *testing.T) {
	m := newModalGuardTestModel()
	updated, _ := m.Update(GitStatusRequestMsg{Branch: "main"})
	m2 := updated.(Model)
	if m2.state != StatePermissionPrompt {
		t.Fatalf("state = %v, want StatePermissionPrompt (active modal clobbered)", m2.state)
	}
	if m2.gitStatusRequest != nil {
		t.Fatal("gitStatusRequest should not be set while another modal is active")
	}
}

func TestFileBrowserRequestMsg_DoesNotClobberActiveModal(t *testing.T) {
	m := newModalGuardTestModel()
	updated, _ := m.Update(FileBrowserRequestMsg{StartPath: "."})
	m2 := updated.(Model)
	if m2.state != StatePermissionPrompt {
		t.Fatalf("state = %v, want StatePermissionPrompt (active modal clobbered)", m2.state)
	}
	if m2.fileBrowserActive {
		t.Fatal("fileBrowserActive should not be set while another modal is active")
	}
}

func TestOpenSettingsMsg_DoesNotClobberActiveModal(t *testing.T) {
	m := newModalGuardTestModel()
	updated, _ := m.Update(OpenSettingsMsg{Model: "glm-5.2", Provider: "glm"})
	m2 := updated.(Model)
	if m2.state != StatePermissionPrompt {
		t.Fatalf("state = %v, want StatePermissionPrompt — Ctrl+S/the /settings command must not clobber an active blocking prompt", m2.state)
	}
}

func TestOpenKeyEntryMsg_DoesNotClobberActiveModal(t *testing.T) {
	m := newModalGuardTestModel()
	updated, _ := m.Update(OpenKeyEntryMsg{Provider: "glm"})
	m2 := updated.(Model)
	if m2.state != StatePermissionPrompt {
		t.Fatalf("state = %v, want StatePermissionPrompt — /login <provider> must not clobber an active blocking prompt", m2.state)
	}
}

// Sanity check: with NO active modal, each message still opens normally
// (the guard must not regress the happy path).
func TestDiffPreviewRequestMsg_OpensNormallyWithNoActiveModal(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.height = 24
	m.state = StateInput

	updated, _ := m.Update(DiffPreviewRequestMsg{FilePath: "x.go", NewContent: "package x"})
	m2 := updated.(Model)
	if m2.state != StateDiffPreview {
		t.Fatalf("state = %v, want StateDiffPreview (guard should not block the happy path)", m2.state)
	}
}

func TestOpenSettingsMsg_OpensNormallyWithNoActiveModal(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.height = 24
	m.state = StateInput

	updated, _ := m.Update(OpenSettingsMsg{Model: "glm-5.2", Provider: "glm"})
	m2 := updated.(Model)
	if m2.state != StateSettings {
		t.Fatalf("state = %v, want StateSettings (guard should not block the happy path)", m2.state)
	}
}
