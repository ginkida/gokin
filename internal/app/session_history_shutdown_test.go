package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/config"

	"google.golang.org/genai"
)

func TestGracefulShutdownFallbackRespectsDisabledSessionPersistence(t *testing.T) {
	for _, mode := range []string{"disabled", "enabled", "unspecified"} {
		t.Run(mode, func(t *testing.T) {
			data := t.TempDir()
			t.Setenv("XDG_DATA_HOME", data)
			session := chat.NewSession()
			session.SetWorkDir(t.TempDir())
			session.AddUserMessage("keep this conversation in its configured scope")
			session.AddContent(&genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{ID: "read-call", Name: "read", Args: map[string]any{"file_path": "notes.txt"}},
			}}})
			a := &App{session: session}
			if mode != "unspecified" {
				a.config = config.DefaultConfig()
				a.config.Session.Enabled = mode == "enabled"
			}
			a.gracefulShutdown(t.Context())
			path := filepath.Join(data, "gokin", "sessions", session.GetID()+".json")
			if mode == "disabled" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("ephemeral conversation was saved at shutdown: %v", err)
				}
				return
			}
			history, err := chat.NewHistoryManager()
			if err != nil {
				t.Fatal(err)
			}
			state, err := history.LoadFull(session.GetID())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(state.History, session.GetState().History) {
				t.Fatal("shutdown fallback lost conversation or tool-call contents")
			}
		})
	}
}
