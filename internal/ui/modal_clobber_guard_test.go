package ui

import (
	"strings"
	"testing"
)

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

func TestBlockingPromptCollisionsExplainAutomaticOutcome(t *testing.T) {
	tests := []struct {
		name      string
		message   any
		configure func(*Model)
		want      string
	}{
		{
			name:      "permission",
			message:   PermissionRequestMsg{ID: "incoming", ToolName: "write"},
			configure: func(m *Model) { m.SetPermissionCallback(func(string, PermissionDecision) {}) },
			want:      "Incoming permission request denied",
		},
		{
			name:      "question",
			message:   QuestionRequestMsg{Question: "Incoming?"},
			configure: func(m *Model) { m.SetQuestionCallback(func(string) {}) },
			want:      "Incoming question cancelled",
		},
		{
			name:      "plan",
			message:   PlanApprovalRequestMsg{Title: "Incoming plan"},
			configure: func(m *Model) { m.SetPlanApprovalCallback(func(PlanApprovalDecision) {}) },
			want:      "Incoming plan rejected",
		},
		{
			name:      "diff",
			message:   DiffPreviewRequestMsg{FilePath: "incoming.go"},
			configure: func(m *Model) { m.SetDiffDecisionCallback(func(DiffDecision) {}) },
			want:      "Incoming diff rejected",
		},
		{
			name:      "multi diff",
			message:   MultiDiffPreviewRequestMsg{Files: []DiffFile{{FilePath: "incoming.go"}}},
			configure: func(m *Model) { m.SetMultiDiffDecisionCallback(func(map[string]DiffDecision) {}) },
			want:      "Incoming multi-file diff rejected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModalGuardTestModel()
			tt.configure(&m)
			updated, _ := m.Update(tt.message)
			got := updated.(Model)
			if got.state != StatePermissionPrompt || got.permRequest == nil || got.permRequest.ID != "req-1" {
				t.Fatalf("incoming request clobbered active prompt: state=%v request=%v", got.state, got.permRequest)
			}
			if got.toastManager.Count() != 1 || got.toastManager.toasts[0].Type != ToastWarning || !strings.Contains(got.toastManager.toasts[0].Message, tt.want) {
				t.Fatalf("collision feedback=%+v, want warning containing %q", got.toastManager.toasts, tt.want)
			}
			if rows := got.notificationRows(); len(rows) != 1 || !rows[0].active || !strings.Contains(rows[0].toast.Message, tt.want) {
				t.Fatalf("collision outcome missing from Notification Center: %+v", rows)
			}
		})
	}
}

func TestBlockingPromptCollisionDoesNotClaimResolutionWithoutHandler(t *testing.T) {
	m := newModalGuardTestModel()
	updated, _ := m.Update(QuestionRequestMsg{Question: "Incoming?"})
	got := updated.(Model)
	if got.toastManager.Count() != 1 {
		t.Fatalf("missing collision feedback: %+v", got.toastManager.toasts)
	}
	message := got.toastManager.toasts[0].Message
	if !strings.Contains(message, "could not be resolved") || !strings.Contains(message, "handler unavailable") || strings.Contains(message, "cancelled") {
		t.Fatalf("unwired collision feedback is dishonest: %q", message)
	}
}

func TestAsyncSurfaceCollisionsNamePreservedContextAndRecovery(t *testing.T) {
	tests := []struct {
		name       string
		state      State
		message    any
		want       []string
		configure  func(*Model)
		assertKept func(*testing.T, Model)
	}{
		{
			name:    "search while command palette open",
			state:   StateCommandPalette,
			message: SearchResultsRequestMsg{Query: "needle", Tool: "grep"},
			want:    []string{`Search results for "needle" were not opened`, "Command Palette kept open", "run the search again"},
			configure: func(m *Model) {
				m.commandPalette.visible = true
				m.commandPalette.SetQuery("settings")
			},
			assertKept: func(t *testing.T, m Model) {
				if m.commandPalette.GetQuery() != "settings" || m.searchRequest != nil {
					t.Fatalf("palette/search state was not preserved: query=%q request=%v", m.commandPalette.GetQuery(), m.searchRequest)
				}
			},
		},
		{
			name:    "git while notifications open",
			state:   StateNotificationCenter,
			message: GitStatusRequestMsg{Branch: "main"},
			want:    []string{"Git status update was not opened", "Notifications kept open", "reopen Git status"},
		},
		{
			name:    "browser while settings open",
			state:   StateSettings,
			message: FileBrowserRequestMsg{StartPath: "."},
			want:    []string{"File browser did not open", "Settings kept open", "close it and retry"},
		},
		{
			name:    "settings while shortcuts open",
			state:   StateShortcutsOverlay,
			message: OpenSettingsMsg{},
			want:    []string{"Settings did not open", "Keyboard shortcuts kept open", "close it and retry"},
		},
		{
			name:    "key entry while decision open",
			state:   StatePermissionPrompt,
			message: OpenKeyEntryMsg{Provider: "openai"},
			want:    []string{"API key entry did not open", "Permission prompt kept open", "resolve it and retry"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			m.width, m.height = 80, 24
			m.state = tc.state
			if tc.configure != nil {
				tc.configure(m)
			}
			updated, _ := m.Update(tc.message)
			got := updated.(Model)
			if got.state != tc.state {
				t.Fatalf("active surface was clobbered: state=%v want=%v", got.state, tc.state)
			}
			if got.toastManager.Count() != 1 {
				t.Fatalf("collision should emit one warning: %+v", got.toastManager.toasts)
			}
			message := got.toastManager.toasts[0].Message
			for _, want := range tc.want {
				if !strings.Contains(message, want) {
					t.Fatalf("collision message missing %q: %q", want, message)
				}
			}
			if tc.assertKept != nil {
				tc.assertKept(t, got)
			}
		})
	}
}

func TestBlockingPromptCollisionNamesNonBlockingSurfaceItPreserved(t *testing.T) {
	m := NewModel()
	m.state = StateCommandPalette
	m.commandPalette.visible = true
	m.commandPalette.SetQuery("model")
	m.SetPermissionCallback(func(string, PermissionDecision) {})

	updated, _ := m.Update(PermissionRequestMsg{ID: "incoming", ToolName: "write"})
	got := updated.(Model)
	if got.state != StateCommandPalette || got.commandPalette.GetQuery() != "model" {
		t.Fatalf("incoming prompt discarded palette context: state=%v query=%q", got.state, got.commandPalette.GetQuery())
	}
	message := got.toastManager.toasts[0].Message
	for _, want := range []string{"Incoming permission request denied", "Command Palette kept open"} {
		if !strings.Contains(message, want) {
			t.Fatalf("prompt collision missing %q: %q", want, message)
		}
	}
	if strings.Contains(message, "finish the active prompt") {
		t.Fatalf("non-blocking palette was mislabeled as a decision prompt: %q", message)
	}
}
