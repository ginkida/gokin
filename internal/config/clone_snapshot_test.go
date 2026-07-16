package config

import (
	"testing"
)

// ===========================================================================
// ResolveThinkingMode (config.go:593) — normalizes empty/unknown → "auto"
// ===========================================================================

func TestResolveThinkingMode_On(t *testing.T) {
	if got := ResolveThinkingMode("on"); got != ThinkingModeOn {
		t.Fatalf("got %q, want %q", got, ThinkingModeOn)
	}
}

func TestResolveThinkingMode_Off(t *testing.T) {
	if got := ResolveThinkingMode("off"); got != ThinkingModeOff {
		t.Fatalf("got %q, want %q", got, ThinkingModeOff)
	}
}

func TestResolveThinkingMode_EmptyDefaultsAuto(t *testing.T) {
	if got := ResolveThinkingMode(""); got != ThinkingModeAuto {
		t.Fatalf("got %q, want %q for empty", got, ThinkingModeAuto)
	}
}

func TestResolveThinkingMode_UnknownDefaultsAuto(t *testing.T) {
	if got := ResolveThinkingMode("maybe"); got != ThinkingModeAuto {
		t.Fatalf("got %q, want %q for unknown", got, ThinkingModeAuto)
	}
}

func TestResolveThinkingMode_CaseInsensitive(t *testing.T) {
	if got := ResolveThinkingMode("ON"); got != ThinkingModeOn {
		t.Fatalf("got %q, want %q (case-insensitive)", got, ThinkingModeOn)
	}
	if got := ResolveThinkingMode("Off"); got != ThinkingModeOff {
		t.Fatalf("got %q, want %q (case-insensitive)", got, ThinkingModeOff)
	}
}

func TestResolveThinkingMode_Trimmed(t *testing.T) {
	if got := ResolveThinkingMode("  on  "); got != ThinkingModeOn {
		t.Fatalf("got %q, want %q (trimmed)", got, ThinkingModeOn)
	}
}

// ===========================================================================
// Snapshot lifecycle: SetSnapshotRevision / SnapshotRevision / SnapshotMCP /
// ClearSnapshotRevision (config.go:49-87)
// ===========================================================================

func TestSnapshotRevision_NilReceiverSafe(t *testing.T) {
	var c *Config
	rev, tracked := c.SnapshotRevision()
	if rev != 0 || tracked {
		t.Fatal("nil receiver should return 0,false")
	}
}

func TestSnapshotRevision_InitiallyUntracked(t *testing.T) {
	c := DefaultConfig()
	rev, tracked := c.SnapshotRevision()
	if rev != 0 || tracked {
		t.Fatalf("initial state = (%d,%v), want (0,false)", rev, tracked)
	}
}

func TestSetSnapshotRevision_MarksTracked(t *testing.T) {
	c := DefaultConfig()
	c.SetSnapshotRevision(42)
	rev, tracked := c.SnapshotRevision()
	if rev != 42 || !tracked {
		t.Fatalf("after SetSnapshotRevision(42) = (%d,%v), want (42,true)", rev, tracked)
	}
}

func TestSetSnapshotRevision_NilReceiverSafe(t *testing.T) {
	var c *Config
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SetSnapshotRevision on nil receiver panicked: %v", r)
		}
	}()
	c.SetSnapshotRevision(1)
}

func TestSnapshotMCP_InitiallyFalse(t *testing.T) {
	c := DefaultConfig()
	_, ok := c.SnapshotMCP()
	if ok {
		t.Fatal("SnapshotMCP should return false before SetSnapshotRevision")
	}
}

func TestSnapshotMCP_NilReceiverSafe(t *testing.T) {
	var c *Config
	_, ok := c.SnapshotMCP()
	if ok {
		t.Fatal("nil receiver should return false")
	}
}

func TestSnapshotMCP_CapturesMCPAtSetTime(t *testing.T) {
	c := DefaultConfig()
	c.MCP.Servers = []MCPServerConfig{{Name: "alpha", Command: "x"}}
	c.SetSnapshotRevision(1)

	snap, ok := c.SnapshotMCP()
	if !ok {
		t.Fatal("SnapshotMCP should return true after SetSnapshotRevision")
	}
	if len(snap.Servers) != 1 || snap.Servers[0].Name != "alpha" {
		t.Fatalf("snapshot = %+v, want alpha server", snap)
	}

	// Mutate the live MCP after snapshot — the snapshot must be independent.
	c.MCP.Servers[0].Name = "beta"
	snap2, _ := c.SnapshotMCP()
	if snap2.Servers[0].Name != "alpha" {
		t.Fatalf("snapshot mutated after live change: %q", snap2.Servers[0].Name)
	}
}

func TestClearSnapshotRevision_ResetsAll(t *testing.T) {
	c := DefaultConfig()
	c.MCP.Servers = []MCPServerConfig{{Name: "x"}}
	c.SetSnapshotRevision(99)
	c.ClearSnapshotRevision()

	rev, tracked := c.SnapshotRevision()
	if rev != 0 || tracked {
		t.Fatalf("after clear = (%d,%v), want (0,false)", rev, tracked)
	}
	_, ok := c.SnapshotMCP()
	if ok {
		t.Fatal("SnapshotMCP should return false after clear")
	}
}

func TestClearSnapshotRevision_NilReceiverSafe(t *testing.T) {
	var c *Config
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ClearSnapshotRevision on nil receiver panicked: %v", r)
		}
	}()
	c.ClearSnapshotRevision()
}

// ===========================================================================
// Clone (config.go:93) + cloneMCPConfig (config.go:119)
// ===========================================================================

func TestClone_NilReturnsNil(t *testing.T) {
	var c *Config
	if got := c.Clone(); got != nil {
		t.Fatalf("nil.Clone() = %v, want nil", got)
	}
}

func TestClone_DeepCopiesSlicesAndMaps(t *testing.T) {
	c := DefaultConfig()
	c.MCP.Servers = []MCPServerConfig{{
		Name: "s1", Command: "cmd",
		Args:    []string{"a", "b"},
		Env:     map[string]string{"K": "V"},
		Headers: map[string]string{"H": "1"},
	}}
	c.Model.FallbackProviders = []string{"glm", "kimi"}
	c.Tools.AllowedDirs = []string{"/tmp"}
	c.Tools.Formatters = map[string]string{"go": "gofmt"}
	c.Permission.Rules = map[string]string{"bash": "ask"}

	clone := c.Clone()

	// Mutate the clone's nested structures — original must be unaffected.
	clone.MCP.Servers[0].Args[0] = "MUTATED"
	clone.MCP.Servers[0].Env["K"] = "MUTATED"
	clone.MCP.Servers[0].Headers["H"] = "MUTATED"
	clone.Model.FallbackProviders[0] = "MUTATED"
	clone.Tools.AllowedDirs[0] = "MUTATED"
	clone.Tools.Formatters["go"] = "MUTATED"
	clone.Permission.Rules["bash"] = "MUTATED"

	if c.MCP.Servers[0].Args[0] != "a" {
		t.Errorf("original MCP args mutated: %q", c.MCP.Servers[0].Args[0])
	}
	if c.MCP.Servers[0].Env["K"] != "V" {
		t.Errorf("original MCP env mutated: %q", c.MCP.Servers[0].Env["K"])
	}
	if c.MCP.Servers[0].Headers["H"] != "1" {
		t.Errorf("original MCP headers mutated: %q", c.MCP.Servers[0].Headers["H"])
	}
	if c.Model.FallbackProviders[0] != "glm" {
		t.Errorf("original FallbackProviders mutated: %q", c.Model.FallbackProviders[0])
	}
	if c.Tools.AllowedDirs[0] != "/tmp" {
		t.Errorf("original AllowedDirs mutated: %q", c.Tools.AllowedDirs[0])
	}
	if c.Tools.Formatters["go"] != "gofmt" {
		t.Errorf("original Formatters mutated: %q", c.Tools.Formatters["go"])
	}
	if c.Permission.Rules["bash"] != "ask" {
		t.Errorf("original Permission.Rules mutated: %q", c.Permission.Rules["bash"])
	}
}

func TestClone_PreservesSnapshotMCP(t *testing.T) {
	c := DefaultConfig()
	c.MCP.Servers = []MCPServerConfig{{Name: "snap-src"}}
	c.SetSnapshotRevision(1) // captures snapshotMCP

	clone := c.Clone()

	// The clone should carry an independent copy of snapshotMCP.
	snap, ok := clone.SnapshotMCP()
	if !ok {
		t.Fatal("clone should have snapshotMCP set")
	}
	if len(snap.Servers) != 1 || snap.Servers[0].Name != "snap-src" {
		t.Fatalf("clone snapshotMCP = %+v, want snap-src", snap)
	}

	// Mutating clone's snapshot must not affect original's snapshot.
	clone.snapshotMCP.Servers[0].Name = "cloned-mut"
	snapOrig, _ := c.SnapshotMCP()
	if snapOrig.Servers[0].Name != "snap-src" {
		t.Fatalf("original snapshotMCP mutated via clone: %q", snapOrig.Servers[0].Name)
	}
}

func TestClone_PreservesVerifyPolicyProfiles(t *testing.T) {
	c := DefaultConfig()
	c.Plan.VerifyPolicy.Profiles = map[string]PlanVerifyPolicyProfileConfig{
		"strict": {
			AllowContains: []string{"pass"},
			DenyContains:  []string{"fail"},
		},
	}
	clone := c.Clone()
	clone.Plan.VerifyPolicy.Profiles["strict"].AllowContains[0] = "MUTATED"
	if c.Plan.VerifyPolicy.Profiles["strict"].AllowContains[0] != "pass" {
		t.Errorf("original VerifyProfile.AllowContains mutated via clone")
	}
}

func TestCloneMCPConfig_EmptyServers(t *testing.T) {
	src := MCPConfig{}
	got := cloneMCPConfig(src)
	if len(got.Servers) != 0 {
		t.Fatalf("got %d servers, want 0", len(got.Servers))
	}
}

func TestCloneMCPConfig_DeepCopiesNestedMaps(t *testing.T) {
	src := MCPConfig{
		Servers: []MCPServerConfig{{
			Name:    "x",
			Env:     map[string]string{"A": "1"},
			Headers: map[string]string{"B": "2"},
			Args:    []string{"arg"},
		}},
	}
	got := cloneMCPConfig(src)
	got.Servers[0].Env["A"] = "MUTATED"
	got.Servers[0].Headers["B"] = "MUTATED"
	got.Servers[0].Args[0] = "MUTATED"

	if src.Servers[0].Env["A"] != "1" {
		t.Errorf("source env mutated: %q", src.Servers[0].Env["A"])
	}
	if src.Servers[0].Headers["B"] != "2" {
		t.Errorf("source headers mutated: %q", src.Servers[0].Headers["B"])
	}
	if src.Servers[0].Args[0] != "arg" {
		t.Errorf("source args mutated: %q", src.Servers[0].Args[0])
	}
}
