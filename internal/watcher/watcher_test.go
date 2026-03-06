package watcher

import (
	"testing"
	"time"
)

func TestOperationString(t *testing.T) {
	tests := []struct {
		op   Operation
		want string
	}{
		{OpCreate, "create"},
		{OpModify, "modify"},
		{OpDelete, "delete"},
		{OpRename, "rename"},
		{Operation(99), "unknown"},
	}

	for _, tt := range tests {
		got := tt.op.String()
		if got != tt.want {
			t.Errorf("Operation(%d).String() = %q, want %q", tt.op, got, tt.want)
		}
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Enabled {
		t.Error("should be disabled by default")
	}
	if cfg.DebounceMs != 500 {
		t.Errorf("DebounceMs = %d, want 500", cfg.DebounceMs)
	}
	if cfg.MaxWatches != 1000 {
		t.Errorf("MaxWatches = %d, want 1000", cfg.MaxWatches)
	}
}

func TestNewFileChangeMsg(t *testing.T) {
	msg := NewFileChangeMsg("/src/main.go", OpModify)
	if msg.Path != "/src/main.go" {
		t.Errorf("Path = %q", msg.Path)
	}
	if msg.Operation != OpModify {
		t.Errorf("Operation = %v", msg.Operation)
	}
	if msg.Time.IsZero() {
		t.Error("Time should be set")
	}
}

// --- EventBuffer tests ---

func TestEventBufferNew(t *testing.T) {
	b := NewEventBuffer(10)
	if b.Len() != 0 {
		t.Errorf("initial Len = %d", b.Len())
	}

	// Zero capacity defaults to 100
	b = NewEventBuffer(0)
	if b.size != 100 {
		t.Errorf("zero capacity size = %d, want 100", b.size)
	}

	// Negative capacity defaults to 100
	b = NewEventBuffer(-5)
	if b.size != 100 {
		t.Errorf("negative capacity size = %d, want 100", b.size)
	}
}

func TestEventBufferAdd(t *testing.T) {
	b := NewEventBuffer(5)

	b.Add(Event{Path: "/a", Operation: OpCreate, Time: time.Now()})
	if b.Len() != 1 {
		t.Errorf("Len = %d, want 1", b.Len())
	}

	b.Add(Event{Path: "/b", Operation: OpModify, Time: time.Now()})
	if b.Len() != 2 {
		t.Errorf("Len = %d, want 2", b.Len())
	}
}

func TestEventBufferRecent(t *testing.T) {
	b := NewEventBuffer(10)

	b.Add(Event{Path: "/a"})
	b.Add(Event{Path: "/b"})
	b.Add(Event{Path: "/c"})

	// Get last 2
	recent := b.Recent(2)
	if len(recent) != 2 {
		t.Fatalf("Recent(2) = %d", len(recent))
	}
	if recent[0].Path != "/b" {
		t.Errorf("recent[0].Path = %q, want /b", recent[0].Path)
	}
	if recent[1].Path != "/c" {
		t.Errorf("recent[1].Path = %q, want /c", recent[1].Path)
	}

	// Get more than available
	recent = b.Recent(10)
	if len(recent) != 3 {
		t.Errorf("Recent(10) = %d, want 3", len(recent))
	}

	// Get zero
	recent = b.Recent(0)
	if recent != nil {
		t.Error("Recent(0) should return nil")
	}

	// Get negative
	recent = b.Recent(-1)
	if recent != nil {
		t.Error("Recent(-1) should return nil")
	}
}

func TestEventBufferCircular(t *testing.T) {
	b := NewEventBuffer(3)

	// Fill and overflow
	b.Add(Event{Path: "/a"})
	b.Add(Event{Path: "/b"})
	b.Add(Event{Path: "/c"})
	b.Add(Event{Path: "/d"}) // Overwrites /a

	if b.Len() != 3 {
		t.Errorf("Len = %d, want 3 (capped at capacity)", b.Len())
	}

	recent := b.Recent(3)
	if len(recent) != 3 {
		t.Fatalf("Recent(3) = %d", len(recent))
	}
	// Should be /b, /c, /d (oldest /a was overwritten)
	if recent[0].Path != "/b" {
		t.Errorf("recent[0] = %q, want /b", recent[0].Path)
	}
	if recent[2].Path != "/d" {
		t.Errorf("recent[2] = %q, want /d", recent[2].Path)
	}
}

func TestEventBufferClear(t *testing.T) {
	b := NewEventBuffer(10)
	b.Add(Event{Path: "/a"})
	b.Add(Event{Path: "/b"})
	b.Clear()

	if b.Len() != 0 {
		t.Errorf("Len after Clear = %d", b.Len())
	}

	recent := b.Recent(10)
	if recent != nil {
		t.Error("Recent after Clear should return nil")
	}
}

func TestNewWatcherDisabled(t *testing.T) {
	w, err := NewWatcher("/tmp", nil, Config{Enabled: false})
	if err != nil {
		t.Fatalf("NewWatcher disabled: %v", err)
	}
	if w.fsWatcher != nil {
		t.Error("disabled watcher should not create fsWatcher")
	}

	// Start/Stop should be no-ops
	if err := w.Start(); err != nil {
		t.Errorf("Start on disabled: %v", err)
	}
	if err := w.Stop(); err != nil {
		t.Errorf("Stop on disabled: %v", err)
	}
}

func TestNewWatcherEnabled(t *testing.T) {
	w, err := NewWatcher(t.TempDir(), nil, Config{Enabled: true})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	if w.fsWatcher == nil {
		t.Error("enabled watcher should create fsWatcher")
	}
	if w.debounceMs != 500 {
		t.Errorf("default debounceMs = %d", w.debounceMs)
	}
	if w.maxWatches != 1000 {
		t.Errorf("default maxWatches = %d", w.maxWatches)
	}
}

func TestNewWatcherCustomConfig(t *testing.T) {
	w, err := NewWatcher(t.TempDir(), nil, Config{
		Enabled:    true,
		DebounceMs: 200,
		MaxWatches: 50,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	if w.debounceMs != 200 {
		t.Errorf("debounceMs = %d, want 200", w.debounceMs)
	}
	if w.maxWatches != 50 {
		t.Errorf("maxWatches = %d, want 50", w.maxWatches)
	}
}
