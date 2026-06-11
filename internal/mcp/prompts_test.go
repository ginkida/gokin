package mcp

import (
	"strings"
	"testing"
)

func samplePrompts() []*Prompt {
	return []*Prompt{
		{
			Name:        "greet",
			Description: "Greet someone",
			Arguments: []*PromptArgument{
				{Name: "who", Required: true},
				{Name: "lang"},
			},
		},
		{Name: "summarize", Description: "Summarize text"},
	}
}

// newReadyPromptClient spins up a fake server advertising prompts and an
// initialized client wired to it.
func newReadyPromptClient(t *testing.T) (*Client, *fakeMCPServer, *ServerConfig) {
	t.Helper()
	tr := newFakeTransport()
	server := startFakeServer(t, tr, nil)
	server.SetPrompts(samplePrompts())
	t.Cleanup(func() { server.cancel() })

	cfg := &ServerConfig{Name: "fake"}
	client := newClientWithTransport(t.Context(), cfg, tr)
	t.Cleanup(func() {
		_ = tr.Close()
		_ = client.Close()
	})
	if err := client.Initialize(t.Context()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return client, server, cfg
}

func TestPrompts_ClientListAndGet(t *testing.T) {
	client, _, _ := newReadyPromptClient(t)

	prompts, err := client.ListPrompts(t.Context())
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(prompts) != 2 {
		t.Fatalf("ListPrompts returned %d, want 2", len(prompts))
	}
	if prompts[0].Name != "greet" || prompts[1].Name != "summarize" {
		t.Errorf("unexpected prompt names: %q, %q", prompts[0].Name, prompts[1].Name)
	}

	res, err := client.GetPrompt(t.Context(), "greet", map[string]any{"who": "world"})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	if res.Description != "rendered greet" {
		t.Errorf("description = %q, want %q", res.Description, "rendered greet")
	}
	if len(res.Messages) != 1 || res.Messages[0].Content == nil {
		t.Fatalf("unexpected messages: %+v", res.Messages)
	}
	if !strings.Contains(res.Messages[0].Content.Text, "name=greet") {
		t.Errorf("message body = %q, want it to echo the requested name", res.Messages[0].Content.Text)
	}
}

func TestPrompts_ManagerAggregateAndGet(t *testing.T) {
	client, _, cfg := newReadyPromptClient(t)

	m := NewManager([]*ServerConfig{cfg})
	m.mu.Lock()
	m.clients["fake"] = client
	m.mu.Unlock()

	listed := m.ListPrompts(t.Context())
	if len(listed) != 2 {
		t.Fatalf("manager ListPrompts returned %d, want 2", len(listed))
	}
	for _, p := range listed {
		if p.Server != "fake" {
			t.Errorf("prompt %q tagged server %q, want fake", p.Name, p.Server)
		}
	}

	text, server, err := m.GetPrompt(t.Context(), "greet", map[string]any{"who": "x"})
	if err != nil {
		t.Fatalf("manager GetPrompt: %v", err)
	}
	if server != "fake" {
		t.Errorf("owning server = %q, want fake", server)
	}
	if !strings.Contains(text, "rendered greet") || !strings.Contains(text, "name=greet") {
		t.Errorf("rendered prompt missing expected content: %q", text)
	}
	if !strings.Contains(text, "[user]") {
		t.Errorf("rendered prompt should label the message role: %q", text)
	}
}

func TestPrompts_ManagerGetNoServers(t *testing.T) {
	m := NewManager(nil)
	if _, _, err := m.GetPrompt(t.Context(), "x", nil); err == nil {
		t.Fatal("expected error with no connected servers")
	}
}

func TestFormatPrompts(t *testing.T) {
	if got := FormatPrompts(nil); !strings.Contains(got, "not initialised") {
		t.Errorf("FormatPrompts(nil) = %q", got)
	}

	client, _, cfg := newReadyPromptClient(t)
	m := NewManager([]*ServerConfig{cfg})
	m.mu.Lock()
	m.clients["fake"] = client
	m.mu.Unlock()

	out := FormatPrompts(m)
	for _, want := range []string{"MCP prompts (2)", "[fake]", "greet", "who*", "summarize", "action:prompts"} {
		if !strings.Contains(out, want) {
			t.Errorf("FormatPrompts missing %q in:\n%s", want, out)
		}
	}
}

func TestFormatPrompts_Empty(t *testing.T) {
	tr := newFakeTransport()
	server := startFakeServer(t, tr, nil) // no prompts set
	t.Cleanup(func() { server.cancel() })
	cfg := &ServerConfig{Name: "fake"}
	client := newClientWithTransport(t.Context(), cfg, tr)
	t.Cleanup(func() {
		_ = tr.Close()
		_ = client.Close()
	})
	if err := client.Initialize(t.Context()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	m := NewManager([]*ServerConfig{cfg})
	m.mu.Lock()
	m.clients["fake"] = client
	m.mu.Unlock()

	if got := FormatPrompts(m); !strings.Contains(got, "No MCP prompts available") {
		t.Errorf("expected empty-prompts hint, got: %q", got)
	}
}

// TestPrompts_ManagerCatalogOrderDeterministic pins two review fixes: (1)
// snapshotClients sorts by server name, so catalog order and GetPrompt's
// first-success owner resolution under name collisions are stable across calls
// (map iteration order used to pick a different owner per call); (2) the
// fan-out aggregation preserves that order while querying servers concurrently.
func TestPrompts_ManagerCatalogOrderDeterministic(t *testing.T) {
	mkClient := func(name, promptName string) (*Client, *ServerConfig) {
		t.Helper()
		tr := newFakeTransport()
		server := startFakeServer(t, tr, nil)
		server.SetPrompts([]*Prompt{{Name: promptName}})
		t.Cleanup(func() { server.cancel() })
		cfg := &ServerConfig{Name: name}
		client := newClientWithTransport(t.Context(), cfg, tr)
		t.Cleanup(func() {
			_ = tr.Close()
			_ = client.Close()
		})
		if err := client.Initialize(t.Context()); err != nil {
			t.Fatalf("Initialize(%s): %v", name, err)
		}
		return client, cfg
	}

	// Register in reverse-alphabetical order to prove sorting, not map luck.
	zClient, zCfg := mkClient("zeta", "from-zeta")
	aClient, aCfg := mkClient("alpha", "from-alpha")

	m := NewManager([]*ServerConfig{zCfg, aCfg})
	m.mu.Lock()
	m.clients["zeta"] = zClient
	m.clients["alpha"] = aClient
	m.mu.Unlock()

	for range 5 { // stable across repeated calls, not one lucky ordering
		listed := m.ListPrompts(t.Context())
		if len(listed) != 2 {
			t.Fatalf("ListPrompts returned %d, want 2", len(listed))
		}
		if listed[0].Server != "alpha" || listed[1].Server != "zeta" {
			t.Fatalf("catalog not in sorted server order: %s, %s", listed[0].Server, listed[1].Server)
		}
	}
}
