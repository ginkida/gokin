package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionManagerLoadSessionErrorTaxonomy(t *testing.T) {
	project := t.TempDir()

	tests := []struct {
		name       string
		kind       SessionLoadErrorKind
		sessionID  string
		setup      func(t *testing.T, manager *SessionManager, sessionsDir string)
		disabled   bool
		wantIs     error
		wantAsJSON bool
	}{
		{
			name:      "invalid id",
			kind:      SessionLoadKindInvalidID,
			sessionID: "../escape",
		},
		{
			name:      "not found",
			kind:      SessionLoadKindNotFound,
			sessionID: "missing",
			wantIs:    os.ErrNotExist,
		},
		{
			name:      "corrupt",
			kind:      SessionLoadKindCorrupt,
			sessionID: "corrupt",
			setup: func(t *testing.T, _ *SessionManager, sessionsDir string) {
				// Structurally valid JSON reaches typed unmarshal, whose original
				// *json.UnmarshalTypeError must remain reachable through Unwrap.
				raw := fmt.Sprintf(`{"id":"corrupt","work_dir":%q,"history":"not-an-array"}`, project)
				if err := os.WriteFile(filepath.Join(sessionsDir, "corrupt.json"), []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:      "identity mismatch",
			kind:      SessionLoadKindIdentityMismatch,
			sessionID: "requested",
			setup: func(t *testing.T, _ *SessionManager, sessionsDir string) {
				writeSessionStateFixture(t, sessionsDir, "requested", SessionState{ID: "different", WorkDir: project})
			},
		},
		{
			name:      "foreign project",
			kind:      SessionLoadKindForeignProject,
			sessionID: "foreign",
			setup: func(t *testing.T, manager *SessionManager, _ string) {
				foreign := NewSession()
				foreign.SetID("foreign")
				foreign.SetWorkDir(t.TempDir())
				if err := manager.historyManager.SaveFull(foreign); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:      "storage",
			kind:      SessionLoadKindStorage,
			sessionID: "special",
			setup: func(t *testing.T, _ *SessionManager, sessionsDir string) {
				if err := os.Mkdir(filepath.Join(sessionsDir, "special.json"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:       "persistence disabled",
			kind:       SessionLoadKindPersistenceDisabled,
			sessionID:  "disabled",
			disabled:   true,
			wantAsJSON: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			active := NewSession()
			active.SetWorkDir(project)
			manager, err := NewSessionManager(active, SessionManagerConfig{Enabled: !tc.disabled})
			if err != nil {
				t.Fatal(err)
			}
			sessionsDir, err := getSessionsDir()
			if err != nil {
				t.Fatal(err)
			}
			if err := ensurePrivateDir(sessionsDir); err != nil {
				t.Fatal(err)
			}
			if tc.setup != nil {
				tc.setup(t, manager, sessionsDir)
			}

			_, _, loadErr := manager.LoadSession(tc.sessionID)
			if loadErr == nil {
				t.Fatal("LoadSession unexpectedly succeeded")
			}
			typed, ok := AsSessionLoadError(loadErr)
			if !ok {
				t.Fatalf("error type = %T, want *SessionLoadError: %v", loadErr, loadErr)
			}
			if typed.Kind != tc.kind || typed.SessionID != tc.sessionID {
				t.Fatalf("classification = (%q, %q), want (%q, %q)", typed.Kind, typed.SessionID, tc.kind, tc.sessionID)
			}
			if kind, ok := SessionLoadErrorKindOf(fmt.Errorf("outer: %w", loadErr)); !ok || kind != tc.kind {
				t.Fatalf("wrapped KindOf = (%q, %v), want (%q, true)", kind, ok, tc.kind)
			}
			if tc.wantIs != nil && !errors.Is(loadErr, tc.wantIs) {
				t.Fatalf("errors.Is(%v) = false; chain was not preserved: %v", tc.wantIs, loadErr)
			}
			if tc.wantAsJSON {
				data, err := json.Marshal(typed)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), `"kind":"`+string(tc.kind)+`"`) || strings.Contains(string(data), `"Err"`) {
					t.Fatalf("machine JSON = %s", data)
				}
			}
			if tc.kind == SessionLoadKindCorrupt {
				var typeErr *json.UnmarshalTypeError
				if !errors.As(loadErr, &typeErr) {
					t.Fatalf("underlying JSON error was lost: %v", loadErr)
				}
			}
		})
	}
}

func TestSessionLoadErrorHelpersRejectUntypedErrors(t *testing.T) {
	if got, ok := AsSessionLoadError(errors.New("plain")); ok || got != nil {
		t.Fatalf("AsSessionLoadError(plain) = (%v, %v)", got, ok)
	}
	if kind, ok := SessionLoadErrorKindOf(nil); ok || kind != "" {
		t.Fatalf("SessionLoadErrorKindOf(nil) = (%q, %v)", kind, ok)
	}
}

func writeSessionStateFixture(t *testing.T, dir, filenameID string, state SessionState) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filenameID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
