package app

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/testkit"

	"google.golang.org/genai"
)

type snapshotBoundaryClient struct {
	*testkit.MockClient
	resets int
}

func (c *snapshotBoundaryClient) ResetConversationCache() { c.resets++ }

func TestSnapshotCacheResetsAtPublishedConversationBoundaries(t *testing.T) {
	for _, boundary := range []string{"clear", "resume-exact", "resume-last", "fork", "select-id", "activation", "previous-clear"} {
		t.Run(boundary, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			root := testkit.ResolvedTempDir(t)
			message := "shared opening"
			if boundary == "select-id" {
				message = ""
			}
			a := newSessionLeaseTestApp(t, "cache-current", "glm", root, message)
			c := &snapshotBoundaryClient{MockClient: testkit.NewMockClient()}
			a.client = c
			var err error
			switch boundary {
			case "clear":
				err = a.ClearConversationChecked()
			case "resume-exact", "resume-last":
				saveSessionLeaseFixture(t, "cache-target", "glm", root, "shared opening")
				if boundary == "resume-exact" {
					err = a.ResumeSession("cache-target")
				} else {
					err = a.ResumeLastSession()
				}
			case "fork":
				err = a.ForkLoadedSession("cache-fork")
			case "select-id":
				err = a.SelectNewSessionID("cache-selected")
			case "activation":
				a.mu.Lock()
				a.setClientLocked(c)
				a.mu.Unlock()
			case "previous-clear":
				// Exercise the process-local resume path as well as disk resume.
				a.sessionManager, err = chat.NewSessionManager(a.session, chat.SessionManagerConfig{Enabled: false})
				if err == nil {
					err = a.ClearConversationForRewind(t.Context())
				}
				c.resets = 0
				if err == nil {
					_, err = a.ResumePreviousClearSession(t.Context(), "cache-current", false)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.resets == 0 {
				t.Fatal("published conversation boundary retained private snapshot cache")
			}
		})
	}
}

func TestRejectedAndNoopSessionSwitchPreserveSnapshotCache(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := testkit.ResolvedTempDir(t)
	a := newSessionLeaseTestApp(t, "cache-kept", "glm", root, "shared opening")
	c := &snapshotBoundaryClient{MockClient: testkit.NewMockClient()}
	a.client = c
	foreign := saveSessionLeaseFixture(t, "cache-foreign", "deepseek", root, "shared opening")
	if _, err := a.SwitchSession(t.Context(), foreign, false); err == nil || c.resets != 0 {
		t.Fatalf("rejected switch reset the current conversation: %v resets=%d", err, c.resets)
	}
	if _, err := a.SwitchSession(t.Context(), a.session.GetState(), false); err != nil || c.resets != 0 {
		t.Fatalf("same-session no-op reset its cache: %v resets=%d", err, c.resets)
	}
}

func TestSwitchSessionCannotReplayPrivateSnapshotWithSharedOpening(t *testing.T) {
	for _, provider := range []string{"deepseek", "glm"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
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
			c.SetSystemInstruction("Stable system instruction")
			c.SetTurnContext("PRIVATE-SESSION-A-SNAPSHOT")
			stream, err := c.SendMessageWithHistory(t.Context(), nil, "shared opening")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stream.CollectContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(<-requests, "PRIVATE-SESSION-A-SNAPSHOT") {
				t.Fatal("private snapshot was not seeded")
			}
			root := testkit.ResolvedTempDir(t)
			a := newSessionLeaseTestApp(t, "snapshot-a", provider, root, "shared opening")
			a.client = c
			b := chat.NewSession()
			b.SetID("snapshot-b")
			b.SetWorkDir(root)
			b.SetProvider(provider)
			b.SetHistory([]*genai.Content{genai.NewContentFromText("shared opening", genai.RoleUser), genai.NewContentFromText("session B reply", genai.RoleModel)})
			manager, err := chat.NewHistoryManager()
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.SaveFull(b); err != nil {
				t.Fatal(err)
			}
			if _, err := a.SwitchSession(t.Context(), b.GetState(), false); err != nil {
				t.Fatal(err)
			}
			c.SetTurnContext("PUBLIC-SESSION-B-SNAPSHOT")
			stream, err = c.SendMessageWithHistory(t.Context(), a.session.GetHistory(), "continue session B")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stream.CollectContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			body := <-requests
			if strings.Contains(body, "PRIVATE-SESSION-A-SNAPSHOT") || !strings.Contains(body, "PUBLIC-SESSION-B-SNAPSHOT") {
				t.Fatal("session B replayed session A's private snapshot")
			}
		})
	}
}
