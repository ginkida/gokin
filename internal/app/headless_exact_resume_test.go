package app

import (
	"bytes"
	"context"
	"io"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

func TestExactResumeHeadlessPassesPriorHistoryAndKeepsSessionID(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	workDir := t.TempDir()

	historyManager, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	saved := chat.NewSession()
	saved.SetID("saved-headless-session")
	saved.SetWorkDir(workDir)
	saved.SetProvider("mock")
	saved.SetHistory([]*genai.Content{
		genai.NewContentFromText("remember the prior constraint", genai.RoleUser),
		genai.NewContentFromText("I will preserve it", genai.RoleModel),
	})
	if err := historyManager.SaveFull(saved); err != nil {
		t.Fatalf("SaveFull: %v", err)
	}

	mock := testkit.NewMockClient().EnqueueText("continued with the prior constraint")
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{
		name:    "unused",
		results: []tools.ToolResult{tools.NewSuccessResult("unused")},
	})
	application.workDir = workDir
	application.session.SetWorkDir(workDir)
	manager, err := chat.NewSessionManager(application.session, chat.DefaultSessionManagerConfig())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	application.sessionManager = manager

	if err := application.ResumeSession("saved-headless-session"); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(context.Background(), "continue exactly", HeadlessOptions{
		OutputFormat: HeadlessOutputJSON,
		Stdout:       &stdout,
		Stderr:       io.Discard,
	})
	if err != nil {
		t.Fatalf("RunHeadlessWithOptions: %v", err)
	}
	_ = decodeSingleHeadlessResult(t, stdout.Bytes())
	if result.SessionID != "saved-headless-session" || application.session.GetID() != "saved-headless-session" {
		t.Fatalf("session identity drifted: result=%q active=%q", result.SessionID, application.session.GetID())
	}

	calls := mock.Calls()
	if len(calls) != 1 {
		t.Fatalf("model calls = %d, want one", len(calls))
	}
	if got := flattenAppTestHistory(calls[0].History); len(got) < 2 || got[0] != "remember the prior constraint" || got[1] != "I will preserve it" {
		t.Fatalf("resumed history not passed to model: %#v", got)
	}
	persisted, err := historyManager.LoadFull("saved-headless-session")
	if err != nil {
		t.Fatalf("LoadFull after headless turn: %v", err)
	}
	if persisted.ID != "saved-headless-session" || len(persisted.History) <= 2 {
		t.Fatalf("updated turn not persisted to exact session: id=%q history=%d", persisted.ID, len(persisted.History))
	}
}
