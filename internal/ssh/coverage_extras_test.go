package ssh

import (
	"testing"
	"time"
)

// --- SetMaxIdle (exercised indirectly, but test explicitly) ---

func TestSessionManager_SetMaxIdle(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	sm.SetMaxIdle(5 * time.Minute)

	sm.mu.RLock()
	got := sm.maxIdle
	sm.mu.RUnlock()
	if got != 5*time.Minute {
		t.Fatalf("maxIdle = %v, want 5m", got)
	}
}

// --- CleanupIdle with no sessions ---

func TestSessionManager_CleanupIdle_Empty(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	if cleaned := sm.CleanupIdle(); cleaned != 0 {
		t.Fatalf("CleanupIdle on empty = %d, want 0", cleaned)
	}
}

// --- Stop is idempotent (sync.Once guard) ---

func TestSessionManager_StopIdempotent(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("double Stop panicked: %v", r)
		}
	}()
	sm := NewSessionManager()
	sm.Stop()
	sm.Stop() // sync.Once guard — must not panic
}

// --- Count after manual session injection ---

func TestSessionManager_Count(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	if sm.Count() != 0 {
		t.Fatalf("Count = %d, want 0", sm.Count())
	}

	// Inject a fake session entry directly
	sm.mu.Lock()
	sm.sessions["test@host:22"] = &SSHClient{config: &SSHConfig{Host: "host", Port: 22, User: "test"}}
	sm.mu.Unlock()

	if sm.Count() != 1 {
		t.Fatalf("Count = %d, want 1", sm.Count())
	}
}

// --- List with injected session ---

func TestSessionManager_List_WithSession(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	sm.mu.Lock()
	sm.sessions["test@host:22"] = &SSHClient{config: &SSHConfig{Host: "host", Port: 22, User: "test"}}
	sm.mu.Unlock()

	infos := sm.List()
	if len(infos) != 1 {
		t.Fatalf("List = %d items, want 1", len(infos))
	}
	if infos[0].Host != "host" || infos[0].Port != 22 || infos[0].User != "test" {
		t.Fatalf("List[0] = %+v, want host/port/user", infos[0])
	}
}

// --- CloseAll with sessions ---

func TestSessionManager_CloseAll_WithSessions(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	// Inject sessions
	sm.mu.Lock()
	sm.sessions["a@host:22"] = &SSHClient{config: &SSHConfig{Host: "host", Port: 22, User: "a"}}
	sm.sessions["b@host:22"] = &SSHClient{config: &SSHConfig{Host: "host", Port: 22, User: "b"}}
	sm.mu.Unlock()

	sm.CloseAll()

	if sm.Count() != 0 {
		t.Fatalf("Count after CloseAll = %d, want 0", sm.Count())
	}
}

// --- Close existing session (error path) ---

func TestSessionManager_Close_Existing(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	// Inject a session — Close will call client.Close() which on a zero-value
	// SSHClient returns nil (no real connection)
	sm.mu.Lock()
	sm.sessions["test@host:22"] = &SSHClient{config: &SSHConfig{Host: "host", Port: 22, User: "test"}}
	sm.mu.Unlock()

	if err := sm.Close("test@host:22"); err != nil {
		t.Fatalf("Close existing: %v", err)
	}
	if sm.Count() != 0 {
		t.Fatalf("Count after Close = %d, want 0", sm.Count())
	}
}

// --- Get with disconnected session ---

func TestSessionManager_Get_DisconnectedSession(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	// Inject a session that reports as not connected
	client := &SSHClient{config: &SSHConfig{Host: "host", Port: 22, User: "test"}}
	// SSHClient.connected is atomic.Bool, defaults to false
	sm.mu.Lock()
	sm.sessions["test@host:22"] = client
	sm.mu.Unlock()

	_, ok := sm.Get("test@host:22")
	if ok {
		t.Fatal("Get on disconnected session should return false")
	}
}

// --- cleanupLoop stopCh path ---
// cleanupLoop selects on ticker.C and stopCh. Stop() closes stopCh which
// causes cleanupLoop to return. We can't easily test the ticker.C path
// (5-minute interval), but Stop() exercises the stopCh path.

func TestSessionManager_CleanupLoop_Stops(t *testing.T) {
	sm := NewSessionManager()
	sm.Stop()
	// cleanupLoop exits via stopCh — if it didn't, Stop() would deadlock
	// on CloseAll waiting for sessions the loop is iterating. Reaching here
	// proves the loop terminated.
	if sm.Count() != 0 {
		t.Fatalf("Count after Stop = %d, want 0", sm.Count())
	}
}

// --- GetOrCreate error path (can't connect) ---

func TestSessionManager_GetOrCreate_ConnectFails(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	// Try to create a session to a non-existent server
	_, err := sm.GetOrCreate(nil, &SSHConfig{
		Host:    "127.0.0.1",
		Port:    1, // port 1 should fail to connect
		User:    "test",
		Timeout: 100 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected error connecting to invalid server")
	}
}
