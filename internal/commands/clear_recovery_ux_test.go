package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/plan"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

type clearFakeApp struct {
	*fakeAppForMCP
	session     *chat.Session
	planManager *plan.Manager
	todo        *tools.TodoTool
	clearCalls  int
	refreshes   int
}

func (f *clearFakeApp) GetSession() *chat.Session     { return f.session }
func (f *clearFakeApp) GetPlanManager() *plan.Manager { return f.planManager }
func (f *clearFakeApp) GetTodoTool() *tools.TodoTool  { return f.todo }
func (f *clearFakeApp) RefreshTokenCount()            { f.refreshes++ }
func (f *clearFakeApp) ClearConversation() {
	f.clearCalls++
	if f.session != nil {
		f.session.Clear()
	}
}

func newClearRecoveryFixture(t *testing.T, breakPlanStore bool) (*clearFakeApp, *plan.Plan) {
	t.Helper()
	base := t.TempDir()
	store, err := plan.NewPlanStore(base)
	if err != nil {
		t.Fatalf("NewPlanStore: %v", err)
	}
	if breakPlanStore {
		plansDir := filepath.Join(base, "plans")
		if err := os.RemoveAll(plansDir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(plansDir, []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	pm := plan.NewManager(true, true)
	pm.SetPlanStore(store)
	activePlan := plan.NewPlan("Active work", "Must remain recoverable")
	pm.SetPlan(activePlan)

	session := chat.NewSession()
	session.SetHistory([]*genai.Content{genai.NewContentFromText("keep me", genai.RoleUser)})
	todo := tools.NewTodoTool()
	todo.RestoreItems([]tools.TodoItem{{Content: "keep task", Status: "pending"}})

	return &clearFakeApp{
		fakeAppForMCP: &fakeAppForMCP{},
		session:       session,
		planManager:   pm,
		todo:          todo,
	}, activePlan
}

func TestClearCancelsWhenActivePlanRecoveryCannotBeSaved(t *testing.T) {
	app, _ := newClearRecoveryFixture(t, true)

	got, err := (&ClearCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(got, "Clear cancelled") || !strings.Contains(got, "No conversation data was cleared") {
		t.Fatalf("message does not explain the safe cancellation: %q", got)
	}
	if !strings.Contains(got, "/clear --force") {
		t.Fatalf("message lacks explicit override: %q", got)
	}
	if app.clearCalls != 0 || app.refreshes != 0 {
		t.Fatalf("cancelled clear mutated app lifecycle: clear=%d refresh=%d", app.clearCalls, app.refreshes)
	}
	if len(app.session.GetHistory()) != 1 || len(app.todo.GetItems()) != 1 {
		t.Fatal("cancelled clear discarded conversation state")
	}
}

func TestClearForceIsExplicitAboutMissingPlanRecovery(t *testing.T) {
	app, _ := newClearRecoveryFixture(t, true)

	got, err := (&ClearCommand{}).Execute(context.Background(), []string{"--force"}, app)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(got, "removed 1 messages and 1 todos") || !strings.Contains(got, "Active plan was not saved") {
		t.Fatalf("forced outcome is not honest about removals and recovery: %q", got)
	}
	if app.clearCalls != 1 || app.refreshes != 1 {
		t.Fatalf("forced clear lifecycle: clear=%d refresh=%d, want 1/1", app.clearCalls, app.refreshes)
	}
	if len(app.session.GetHistory()) != 0 || len(app.todo.GetItems()) != 0 {
		t.Fatal("forced clear did not start a clean conversation")
	}
}

func TestClearPersistsActivePlanBeforeMutation(t *testing.T) {
	app, activePlan := newClearRecoveryFixture(t, false)

	got, err := (&ClearCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(got, "Active plan saved for /resume-plan") {
		t.Fatalf("successful recovery snapshot is not surfaced: %q", got)
	}
	loaded, err := app.planManager.GetPlanStore().Load(activePlan.ID)
	if err != nil {
		t.Fatalf("active plan was not persisted: %v", err)
	}
	if loaded.ID != activePlan.ID {
		t.Fatalf("loaded plan ID = %q, want %q", loaded.ID, activePlan.ID)
	}
}

func TestClearRejectsUnknownArgumentsWithoutMutation(t *testing.T) {
	app := &clearFakeApp{fakeAppForMCP: &fakeAppForMCP{}, session: chat.NewSession()}
	got, err := (&ClearCommand{}).Execute(context.Background(), []string{"now"}, app)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(got, "/clear --force") || app.clearCalls != 0 {
		t.Fatalf("unknown argument outcome = %q, clearCalls=%d", got, app.clearCalls)
	}
}
