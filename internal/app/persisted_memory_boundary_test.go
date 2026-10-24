package app

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/client"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/memory"
	"gokin/internal/testkit"

	"google.golang.org/genai"
)

func newMemoryBoundaryApp(t *testing.T, workDir string) *App {
	t.Helper()
	b := NewBuilderWithOptions(config.DefaultConfig(), workDir, BuildOptions{NonInteractive: true})
	b.mainClient = testkit.NewMockClient()
	b.configDir = t.TempDir()
	t.Cleanup(b.cancel)
	if err := b.initSession(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		b.contextManager.Close()
		if b.projectMemory != nil {
			b.projectMemory.StopWatching()
		}
	})
	a := &App{
		config: b.cfg, workDir: workDir, session: b.session,
		sessionManager: b.sessionManager, client: b.mainClient,
		workingMemory: b.workingMemory, sessionMemory: b.sessionMemory,
	}
	a.sessionMemory.SetOnUpdate(a.persistSessionMemory)
	t.Cleanup(func() {
		if err := a.ReleaseSessionWriterLease(); err != nil {
			t.Error(err)
		}
	})
	return a
}

func TestConversationMemoryFollowsSwitchAndClear(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := testkit.ResolvedTempDir(t)
	a := newMemoryBoundaryApp(t, root)
	a.session.AddUserMessage("Update m.go")
	a.updateWorkingMemoryFromTurn("Update m.go", "SESSION-A: Updated m.go.")
	aID := a.session.GetID()
	if err := a.sessionManager.Save(); err != nil {
		t.Fatal(err)
	}
	b := newMemoryBoundaryApp(t, root)
	b.session.AddUserMessage("Investigate the other task")
	b.updateWorkingMemoryFromTurn("Investigate the other task", "SESSION-B: Investigated the task.")
	if err := b.sessionManager.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SwitchSession(t.Context(), b.session.GetState(), false); err != nil {
		t.Fatal(err)
	}
	if got := a.turnContextContent(); !strings.Contains(got, "SESSION-B") || strings.Contains(got, "SESSION-A") {
		t.Fatalf("interactive switch retained the wrong memory: %s", got)
	}
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := a.turnContextContent(); strings.Contains(got, "SESSION-") {
		t.Fatalf("clear retained a prior session's facts: %s", got)
	}
	if _, err := a.ResumePreviousClearSession(t.Context(), b.session.GetID(), false); err != nil {
		t.Fatal(err)
	}
	if got := a.turnContextContent(); !strings.Contains(got, "SESSION-B") || strings.Contains(got, "SESSION-A") {
		t.Fatalf("previous-clear resume lost its memory: %s", got)
	}
	resumed := newMemoryBoundaryApp(t, root)
	if err := resumed.ResumeSession(aID); err != nil {
		t.Fatal(err)
	}
	if got := resumed.turnContextContent(); !strings.Contains(got, "SESSION-A") || strings.Contains(got, "SESSION-B") {
		t.Fatalf("exact resume lost original memory: %s", got)
	}
}

func TestFreshSessionIgnoresUnownedWorkspaceMemory(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := testkit.ResolvedTempDir(t)
	wm := appcontext.NewWorkingMemoryManager(root)
	wm.UpdateFromTurn(appcontext.WorkingMemoryTurn{Response: "LEGACY-RESULT", TouchedPaths: []string{"m.go"}, Commands: []string{"go build ./..."}})
	if err := os.WriteFile(filepath.Join(root, ".gokin", ".session-memory.md"), []byte("# Session Memory\nLEGACY-SESSION-TASK"), 0600); err != nil {
		t.Fatal(err)
	}
	a := newMemoryBoundaryApp(t, root)
	if got := a.turnContextContent(); strings.Contains(got, "LEGACY-") || strings.Contains(got, "# Working Memory") || strings.Contains(got, "# Session Memory") {
		t.Fatalf("new session inherited unowned workspace snapshots:\n%s", got)
	}
}

func TestWorkingMemoryResumesOnlyWithItsSession(t *testing.T) {
	for _, provider := range []string{"glm", "deepseek"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			root := testkit.ResolvedTempDir(t)
			a := newMemoryBoundaryApp(t, root)
			a.session.AddUserMessage("Add a comment in m.go")
			a.responseTouchedPaths = []string{"m.go"}
			a.responseCommands = []string{"go build ./..."}
			a.updateWorkingMemoryFromTurn("Add a comment in m.go", "SESSION-A: Added // Add adds two ints. above Add in m.go.")
			a.sessionMemory.Extract([]*genai.Content{
				genai.NewContentFromText("SESSION-A-TASK: Add a comment in m.go", genai.RoleUser),
				genai.NewContentFromText("Inspecting m.go", genai.RoleModel),
				genai.NewContentFromText("Continue the comment edit", genai.RoleUser),
				genai.NewContentFromText("Done", genai.RoleModel),
			}, 20_000)
			if err := a.sessionManager.Save(); err != nil {
				t.Fatal(err)
			}
			aID := a.session.GetID()
			b := newMemoryBoundaryApp(t, root)
			requests := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requests <- string(body)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			t.Cleanup(server.Close)
			c, err := client.NewAnthropicClient(client.AnthropicConfig{APIKey: "test", BaseURL: server.URL, Provider: provider, Model: provider + "-test"})
			if err != nil {
				t.Fatal(err)
			}
			b.client = c
			send := func() string {
				t.Helper()
				b.pushTurnContext()
				stream, err := c.SendMessageWithHistory(t.Context(), b.session.GetHistory(), "Continue the task")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := stream.CollectContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				return <-requests
			}
			if body := send(); strings.Contains(body, "SESSION-A") || strings.Contains(body, "Files changed:") || strings.Contains(body, "Verification already run:") {
				t.Errorf("fresh B's first request inherited A's completed-turn facts: %s", body)
			}
			b.session.AddUserMessage("Investigate another task")
			b.updateWorkingMemoryFromTurn("Investigate another task", "SESSION-B: Investigated another task.")
			store, err := memory.NewStore(t.TempDir(), root, 100)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Add(memory.NewEntry("SESSION-B-KEYED: Temporary investigation note", memory.MemorySession).WithKey("temporary")); err != nil {
				t.Fatal(err)
			}
			b.memoryStore = store
			if err := b.ResumeSession(aID); err != nil {
				t.Fatal(err)
			}
			if body := send(); !strings.Contains(body, "Latest result: SESSION-A") || !strings.Contains(body, "Files changed: m.go") || !strings.Contains(body, "Verification already run: go build ./...") || strings.Contains(body, "SESSION-B") {
				t.Errorf("resumed A did not restore its own working memory: %s", body)
			}
			if got := b.sessionMemory.GetContent(); got != a.sessionMemory.GetContent() || got == "" {
				t.Errorf("resume did not restore A's automatic session summary: %q", got)
			}
			if _, ok := store.Get("temporary"); ok {
				t.Error("switch retained B's keyed session memory")
			}
		})
	}
}
