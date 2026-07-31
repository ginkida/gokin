package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestInterAgentResponseContextPreservesParentDeadline(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer parentCancel()

	waitCtx, cancel := interAgentResponseContext(parent)
	defer cancel()
	deadline, ok := waitCtx.Deadline()
	if !ok {
		t.Fatal("wait context lost parent deadline")
	}
	if remaining := time.Until(deadline); remaining > 200*time.Millisecond {
		t.Fatalf("wait context extended parent deadline: %v", remaining)
	}
}

func TestInterAgentResponseContextAddsOnlyUndeadlinedSafetyCap(t *testing.T) {
	waitCtx, cancel := interAgentResponseContext(context.Background())
	defer cancel()
	deadline, ok := waitCtx.Deadline()
	if !ok {
		t.Fatal("undeadlined inter-agent wait has no safety cap")
	}
	remaining := time.Until(deadline)
	if remaining < maxInterAgentResponseWait-time.Minute ||
		remaining > maxInterAgentResponseWait+time.Minute {
		t.Fatalf("fallback wait = %v, want ~%v", remaining, maxInterAgentResponseWait)
	}
}

func TestReceiveResponseUsesCallerCancellationAndCleansPending(t *testing.T) {
	messenger := NewAgentMessenger(context.Background(), nil, "parent")
	messenger.pending["message"] = make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := messenger.ReceiveResponse(ctx, "message")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReceiveResponse error = %v, want context.Canceled", err)
	}
	messenger.mu.RLock()
	_, leaked := messenger.pending["message"]
	messenger.mu.RUnlock()
	if leaked {
		t.Fatal("cancelled response waiter leaked pending entry")
	}
}

func TestSendMessageRejectsUnknownTypeWithoutLeakingPending(t *testing.T) {
	messenger := NewAgentMessenger(context.Background(), nil, "parent")
	if _, err := messenger.SendMessage("unknown", "bash", "work", nil); err == nil {
		t.Fatal("unknown message type was accepted")
	}
	messenger.mu.RLock()
	pending := len(messenger.pending)
	messenger.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("unknown message type leaked %d pending response(s)", pending)
	}
}

func TestInterAgentHandlerPanicBecomesResponseInsteadOfCrashingProcess(t *testing.T) {
	// A nil runner makes handleHelpRequest panic at runner.Spawn. The handler
	// boundary must recover and turn that into a response for the requester.
	messenger := NewAgentMessenger(context.Background(), nil, "parent")
	messageID, err := messenger.SendMessage("help_request", "bash", "work", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := messenger.ReceiveResponse(ctx, messageID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response, "internal panic") {
		t.Fatalf("panic response = %q", response)
	}
}
