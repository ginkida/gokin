package hooks

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedPluginDependencyFailureBlocksExecution(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("hooks-enabled=%v", enabled), func(t *testing.T) {
			m, _, user, managed := managedTestManager(t, enabled, false)
			guard := pluginTestRoot(t, "guard", "printf guard")
			helper := pluginTestRoot(t, "helper", "printf helper")
			writeClaudeSettingsTestFile(t, filepath.Join(guard, ".claude-plugin", "plugin.json"), map[string]any{"name": "guard", "dependencies": []string{"helper"}})
			pluginTestRegistry(t, user, map[string][]claudePluginInstall{"guard@org": {{Scope: "user", InstallPath: guard}}, "helper@org": {{Scope: "user", InstallPath: helper}}})
			writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"enabledPlugins": map[string]bool{"guard@org": true}})
			if enabled {
				if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 2 {
					t.Fatalf("baseline hooks=%+v", results)
				}
				writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{"helper@org": false}})
			}
			err := m.CheckReady()
			if err == nil || !strings.Contains(err.Error(), "managed plugin guard@org") || !strings.Contains(err.Error(), "helper") {
				t.Fatalf("required dependency silently disappeared: %v", err)
			}
			if results := m.RunPreTool(t.Context(), "write", nil); len(results) == 0 || results[0].StopReason == "" {
				t.Fatalf("execution did not fail closed: %+v", results)
			}
		})
	}
}

func TestCapturedEventOnceClaimIsShared(t *testing.T) {
	for _, event := range []string{"DirectoryAdded", "SessionStart", "SessionEnd"} {
		t.Run(event, func(t *testing.T) {
			root := t.TempDir()
			manager := NewManager(true, root)
			owned := &Scope{}
			kind := claudeEventTypes[event]
			registration, err := owned.Prepare("guard", []ClaudeCommand{{Event: event, Handler: "mcp_tool", MCPServer: "fixture", MCPTool: "observe", Once: true, Timeout: time.Second}})
			if err != nil {
				t.Fatal(err)
			}
			registration.Commit()
			first, second := owned.CaptureEvent(kind), owned.CaptureEvent(kind)
			started, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			manager.SetMCPHookInvoker(func(context.Context, string, string, map[string]any) (MCPHookOutput, error) {
				if calls.Add(1) == 1 {
					close(started)
					<-release
				}
				return MCPHookOutput{}, nil
			})
			done := make(chan []Result, 1)
			go func() {
				done <- manager.RunScoped(ContextWithScope(t.Context(), first), kind, NewContext("", nil, root))
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				close(release)
				t.Fatal("first captured hook did not start")
			}
			duplicate := manager.RunScoped(ContextWithScope(t.Context(), second), kind, NewContext("", nil, root))
			close(release)
			results := <-done
			if len(results) != 1 || results[0].Error != nil || len(duplicate) != 0 || calls.Load() != 1 || !registration.Consumed()[0] {
				t.Fatalf("once ran more than once across captures: first=%+v duplicate=%+v calls=%d consumed=%v", results, duplicate, calls.Load(), registration.Consumed())
			}
			if again := manager.RunScoped(ContextWithScope(t.Context(), second), kind, NewContext("", nil, root)); len(again) != 0 {
				t.Fatal("stale capture rearmed consumed hook", again)
			}
		})
	}
}

func TestCapturedEventFailedOnceClaimCanRetry(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(true, root)
	scope := &Scope{}
	registration, err := scope.Prepare("guard", []ClaudeCommand{{Event: "DirectoryAdded", Handler: "mcp_tool", MCPServer: "fixture", MCPTool: "observe", Once: true, Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	first, second := scope.CaptureEvent(DirectoryAdded), scope.CaptureEvent(DirectoryAdded)
	calls := 0
	manager.SetMCPHookInvoker(func(context.Context, string, string, map[string]any) (MCPHookOutput, error) {
		calls++
		if calls == 1 {
			return MCPHookOutput{}, fmt.Errorf("retryable failure")
		}
		return MCPHookOutput{}, nil
	})
	for i, captured := range []*Scope{first, second} {
		results := manager.RunScoped(ContextWithScope(t.Context(), captured), DirectoryAdded, NewContext("", nil, root))
		if len(results) != 1 || (results[0].Error == nil) != (i == 1) || registration.Consumed()[0] != (i == 1) {
			t.Fatalf("failed claim not released: %+v consumed=%v", results, registration.Consumed())
		}
	}
}

func TestPluginCommandFingerprintExcludesTransientAuthority(t *testing.T) {
	original := &claudePluginRuntime{ID: "suite@inline", Root: "/plugin", Data: "/data", Options: map[string]string{"value": "literal"}, Sensitive: map[string]bool{"token": true}, Project: true}
	definition := ClaudeCommand{Event: "PreToolUse", Command: "exit 0", plugin: original}
	durable := CommandFingerprint([]ClaudeCommand{definition})
	live := pluginRuntimeFingerprint(original)
	for _, flags := range []struct{ forced, explicit bool }{{true, false}, {false, true}, {true, true}} {
		changed := *original
		changed.Forced, changed.Explicit = flags.forced, flags.explicit
		next := definition
		next.plugin = &changed
		if CommandFingerprint([]ClaudeCommand{next}) != durable {
			t.Error("authority-only change invalidated durable definition", flags)
		}
		if pluginRuntimeFingerprint(&changed) == live {
			t.Error("authority change escaped live identity check", flags)
		}
	}
	for _, field := range []string{"id", "root", "data", "options", "sensitive", "project"} {
		changed := *original
		switch field {
		case "id":
			changed.ID = "other@inline"
		case "root":
			changed.Root = "/other"
		case "data":
			changed.Data = "/other-data"
		case "options":
			changed.Options = map[string]string{"value": "changed"}
		case "sensitive":
			changed.Sensitive = nil
		case "project":
			changed.Project = false
		}
		next := definition
		next.plugin = &changed
		if CommandFingerprint([]ClaudeCommand{next}) == durable {
			t.Error("executable input escaped durable fingerprint", field)
		}
	}
}

func TestScopeProvenanceRefreshPreservesOnceClaimAndReservation(t *testing.T) {
	root := t.TempDir()
	runtime := &claudePluginRuntime{ID: "suite@inline", Root: root, Data: root, Explicit: true}
	current := *runtime
	current.Forced = true
	definitions := []ClaudeCommand{{Event: "PreToolUse", Handler: "mcp_tool", MCPServer: "fixture", MCPTool: "observe", Once: true, Timeout: time.Second, plugin: runtime}}
	refreshed := CloneClaudeCommands(definitions)
	refreshed[0].plugin = &current
	scope := &Scope{}
	registration, err := scope.Prepare("guard", definitions)
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	manager := NewManager(true, root)
	manager.claudePlugins = []ClaudePlugin{{Name: "suite", runtime: runtime}}
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	manager.SetMCPHookInvoker(func(context.Context, string, string, map[string]any) (MCPHookOutput, error) {
		close(started)
		<-release
		return MCPHookOutput{}, nil
	})
	observed := make(chan int, 1)
	registration.ObserveOnce(func(index int) { observed <- index })
	done := make(chan []Result, 1)
	ctx := ContextWithScope(t.Context(), scope)
	go func() { done <- manager.RunScoped(ctx, PreTool, NewContext("write", nil, root)) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("once hook did not start")
	}
	pending, err := scope.Prepare("pending", []ClaudeCommand{{Event: "PostToolUse", Command: "exit 0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer pending.Cancel()
	original := scope.groups["guard"][0]
	if err := scope.RefreshPluginProvenance(map[string][]ClaudeCommand{"guard": refreshed}); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.claudePlugins = []ClaudePlugin{{Name: "suite", runtime: &current}}
	manager.mu.Unlock()
	if scope.groups["guard"][0] != original || runtime.Forced {
		t.Fatal("refresh replaced claim state or mutated old provenance")
	}
	if err := scope.PolicyError(manager); err != nil {
		t.Fatal("new authority was not published", err)
	}
	if results := manager.RunScoped(ctx, PreTool, NewContext("write", nil, root)); len(results) != 0 {
		t.Fatal("refresh rearmed in-flight once claim", results)
	}
	pending.Commit()
	if !scope.HasSource("pending") {
		t.Fatal("refresh discarded render reservation")
	}
	releaseOnce.Do(func() { close(release) })
	results := <-done
	if len(results) != 1 || results[0].Error != nil || !registration.Consumed()[0] {
		t.Fatal("refresh lost completion", results, registration.Consumed())
	}
	select {
	case index := <-observed:
		if index != 0 {
			t.Fatal("wrong once observer index", index)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh lost once observer")
	}
	if err := scope.RefreshPluginProvenance(map[string][]ClaudeCommand{"guard": {{Event: "PreToolUse", Command: "changed", plugin: &current}}}); err == nil {
		t.Fatal("refresh accepted changed executable definitions")
	}
}
