package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/permission"
	"gokin/internal/ui"
)

func TestPromptPermission_HeadlessFailsClosed(t *testing.T) {
	a := &App{headlessDirect: true}
	decision, err := a.promptPermission(context.Background(), permission.NewRequest("bash", map[string]any{"command": "go test ./..."}))
	if decision != permission.DecisionDeny || err == nil {
		t.Fatalf("headless ask = decision %v, err %v; want deny with error", decision, err)
	}
	if !strings.Contains(err.Error(), "permission.rules.bash: allow") {
		t.Errorf("headless error is not actionable: %v", err)
	}
}

// TestHandlePermissionDecision_RoutesByRequestID pins the fix for the
// misattribution bug: with a single shared response channel, a decision
// meant for one in-flight promptPermission call could resolve a DIFFERENT
// concurrent one (e.g. the coordinate tool's parallel sub-agents each
// hitting a LevelAsk tool, or the "auto-deny the second prompt while a
// modal is already showing" path in tui.go). Each request now gets its own
// channel keyed by ID, and handlePermissionDecision must deliver to ONLY
// the matching one.
func TestHandlePermissionDecision_RoutesByRequestID(t *testing.T) {
	a := &App{permPending: make(map[string]chan permission.Decision)}

	idA, chA, cleanupA := a.registerPermRequest()
	defer cleanupA()
	idB, chB, cleanupB := a.registerPermRequest()
	defer cleanupB()

	if idA == idB {
		t.Fatal("two registered requests got the same ID")
	}

	// Resolve ONLY request B (e.g. the currently-displayed modal's own
	// decision, or an auto-deny fired for B specifically).
	a.handlePermissionDecision(idB, ui.PermissionDeny)

	select {
	case d := <-chB:
		if d != permission.DecisionDeny {
			t.Errorf("chB got %v, want DecisionDeny", d)
		}
	case <-time.After(time.Second):
		t.Fatal("chB never received the decision meant for request B")
	}

	// Request A's channel must be completely untouched — the exact
	// misattribution the fix closes: A must NOT have silently received B's
	// decision (or anything at all).
	select {
	case d := <-chA:
		t.Fatalf("request A's channel received a decision (%v) meant for a DIFFERENT request — misattribution bug reproduced", d)
	default:
	}
}

// TestHandlePermissionDecision_UnknownRequestIsANoOp: a decision for an
// already-timed-out or already-resolved request (cleanup already ran) must
// not panic and must not affect any other pending request.
func TestHandlePermissionDecision_UnknownRequestIsANoOp(t *testing.T) {
	a := &App{permPending: make(map[string]chan permission.Decision)}

	_, chA, cleanupA := a.registerPermRequest()
	defer cleanupA()

	a.handlePermissionDecision("nonexistent-request-id", ui.PermissionAllow)

	select {
	case d := <-chA:
		t.Fatalf("unrelated request A received a decision (%v) meant for an unknown/expired request", d)
	default:
	}
}

// TestRegisterPermRequest_CleanupRemovesEntry: cleanup() must actually
// unregister the request so a stale decision after the caller has already
// returned (timed out) can't leak into a reused ID slot.
func TestRegisterPermRequest_CleanupRemovesEntry(t *testing.T) {
	a := &App{permPending: make(map[string]chan permission.Decision)}

	id, _, cleanup := a.registerPermRequest()

	a.permPendingMu.Lock()
	_, ok := a.permPending[id]
	a.permPendingMu.Unlock()
	if !ok {
		t.Fatal("request not registered")
	}

	cleanup()

	a.permPendingMu.Lock()
	_, ok = a.permPending[id]
	a.permPendingMu.Unlock()
	if ok {
		t.Fatal("cleanup() did not remove the request from permPending")
	}
}

// TestHandlePermissionDecision_ConcurrentRequestsNoCrossTalk drives many
// concurrent registrations and resolves each by its own ID, verifying every
// channel receives exactly its own decision — a fuzz-ish version of the
// misattribution scenario under -race.
func TestHandlePermissionDecision_ConcurrentRequestsNoCrossTalk(t *testing.T) {
	a := &App{permPending: make(map[string]chan permission.Decision)}

	const n = 20
	ids := make([]string, n)
	chans := make([]chan permission.Decision, n)
	for i := range n {
		id, ch, cleanup := a.registerPermRequest()
		ids[i] = id
		chans[i] = ch
		defer cleanup()
	}

	done := make(chan struct{})
	for i := range n {
		go func(i int) {
			decision := ui.PermissionAllow
			if i%2 == 0 {
				decision = ui.PermissionDeny
			}
			a.handlePermissionDecision(ids[i], decision)
			done <- struct{}{}
		}(i)
	}
	for range n {
		<-done
	}

	for i := range n {
		want := permission.DecisionAllow
		if i%2 == 0 {
			want = permission.DecisionDeny
		}
		select {
		case got := <-chans[i]:
			if got != want {
				t.Errorf("request %d got %v, want %v", i, got, want)
			}
		case <-time.After(time.Second):
			t.Errorf("request %d never received its decision", i)
		}
	}
}
