package router

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

// TestRestoreOriginalUserMessage is a direct unit test of the pure helper,
// mirroring the style of TestExtendConversation.
func TestRestoreOriginalUserMessage(t *testing.T) {
	original := "fix the bug in payments.go"

	t.Run("restores augmented text at priorLen", func(t *testing.T) {
		prior := []*genai.Content{genai.NewContentFromText("earlier turn", genai.RoleUser)}
		augmented := "For this task, prefer edit over write.\n\nBefore acting, analyze the problem step by step.\n\n" + original
		newHistory := append(append([]*genai.Content{}, prior...),
			genai.NewContentFromText(augmented, genai.RoleUser),
			genai.NewContentFromText("model reply", genai.RoleModel),
		)

		got := restoreOriginalUserMessage(newHistory, len(prior), original)

		if len(got) != len(newHistory) {
			t.Fatalf("length changed: got %d, want %d", len(got), len(newHistory))
		}
		if got[len(prior)].Parts[0].Text != original {
			t.Fatalf("restored text = %q, want %q", got[len(prior)].Parts[0].Text, original)
		}
		if strings.Contains(got[len(prior)].Parts[0].Text, "prefer edit over write") {
			t.Fatal("augmentation leaked into the persisted turn")
		}
		// Untouched turns must survive.
		if got[0] != prior[0] {
			t.Error("prior turn was not preserved")
		}
		if got[len(prior)+1].Parts[0].Text != "model reply" {
			t.Error("trailing turn was not preserved")
		}
	})

	t.Run("no-op when priorLen out of range", func(t *testing.T) {
		h := []*genai.Content{genai.NewContentFromText("x", genai.RoleUser)}
		if got := restoreOriginalUserMessage(h, 5, original); len(got) != 1 {
			t.Fatalf("expected unchanged history, got len=%d", len(got))
		}
		if got := restoreOriginalUserMessage(h, -1, original); len(got) != 1 {
			t.Fatalf("expected unchanged history for negative priorLen, got len=%d", len(got))
		}
	})

	t.Run("no-op when the turn is not model role", func(t *testing.T) {
		h := []*genai.Content{genai.NewContentFromText("hint\n\n"+original, genai.RoleModel)}
		got := restoreOriginalUserMessage(h, 0, original)
		if got[0].Parts[0].Text == original {
			t.Fatal("should not rewrite a non-user-role turn")
		}
	})

	t.Run("no-op when the turn has a function call, not text", func(t *testing.T) {
		h := []*genai.Content{{
			Role:  genai.RoleUser,
			Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "read"}}},
		}}
		got := restoreOriginalUserMessage(h, 0, original)
		if got[0].Parts[0].FunctionCall == nil {
			t.Fatal("should not have touched a function-call part")
		}
	})
}

// TestRouterExecute_HandlerExecutor_PersistsOriginalMessageNotHint
// (round 5) pins the fix: Router.Execute augments `message` with
// tool-usage/thinking hints before sending it to the model, but
// HandlerExecutor (and HandlerDirect/default) returned that AUGMENTED text
// baked into the persisted history's user turn — unlike HandlerSubAgent/
// HandlerCoordinated, which already used originalMessage via
// extendConversation. This is an end-to-end test through the real
// Router.Execute call chain (real TaskAnalyzer classification + a real
// tools.Executor backed by a MockClient) proving: (1) the model still
// RECEIVES the augmented, hint-prefixed message (functional behavior
// unchanged), but (2) the history that gets PERSISTED holds the user's
// actual original words.
func TestRouterExecute_HandlerExecutor_PersistsOriginalMessageNotHint(t *testing.T) {
	original := "explore the authentication module and find all usages, then investigate how it works"

	mock := testkit.NewMockClient()
	mock.EnqueueText("Understood, exploring now.")

	registry := tools.NewRegistry()
	exec := tools.NewExecutor(registry, mock, time.Second)

	// A very high DecomposeThreshold keeps this test isolated to
	// HandlerExecutor — without it, the analyzer's auto-decomposition can
	// route a multi-clause message to HandlerCoordinated instead.
	cfg := &RouterConfig{Enabled: true, DecomposeThreshold: 100, ParallelThreshold: 100}
	r := NewRouter(cfg, exec, nil, mock, registry, false, t.TempDir())

	newHistory, resp, err := r.Execute(context.Background(), nil, original)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if resp != "Understood, exploring now." {
		t.Fatalf("response = %q", resp)
	}
	if len(newHistory) == 0 {
		t.Fatal("expected non-empty history")
	}

	// The PERSISTED user turn must be the user's actual words.
	persistedText := newHistory[0].Parts[0].Text
	if persistedText != original {
		t.Fatalf("persisted user turn = %q, want the original message %q (routing scaffolding leaked into history)", persistedText, original)
	}
	if strings.Contains(persistedText, "Before acting, analyze") || strings.Contains(persistedText, "prefer read, glob, grep") {
		t.Fatalf("persisted user turn contains routing hint scaffolding: %q", persistedText)
	}

	// The MODEL must still have RECEIVED the augmented, hint-prefixed
	// message — the fix must not change what's actually sent, only what's
	// persisted.
	calls := mock.Calls()
	if len(calls) == 0 {
		t.Fatal("expected at least one recorded model call")
	}
	sentMessage := calls[0].Message
	if !strings.Contains(sentMessage, original) {
		t.Fatalf("model call message = %q, want it to still contain the original task", sentMessage)
	}
	if sentMessage == original {
		t.Fatalf("expected the model to receive hint augmentation (test setup should trigger it), got the bare original: %q", sentMessage)
	}
}
