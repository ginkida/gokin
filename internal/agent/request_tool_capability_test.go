package agent

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/tools"
)

func requestToolCapabilityRegistry(t *testing.T) (*tools.Registry, string) {
	t.Helper()
	workDir := t.TempDir()
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewRequestToolTool())
	registry.MustRegister(tools.NewWriteTool(workDir))
	registry.MustRegister(tools.NewSSHTool())
	return registry, workDir
}

func TestRestrictedAgentRequestToolCannotExpandOriginalCapabilities(t *testing.T) {
	registry, workDir := requestToolCapabilityRegistry(t)
	agent := NewAgent(AgentTypeExplore, nil, registry, workDir, 3, "", nil, nil)

	requestAny, ok := agent.registry.Get("request_tool")
	if !ok {
		t.Fatal("restricted agent registry has no request_tool")
	}
	request, ok := requestAny.(*tools.RequestToolTool)
	if !ok {
		t.Fatalf("request_tool type = %T, want *tools.RequestToolTool", requestAny)
	}

	result, err := request.Execute(context.Background(), map[string]any{"tool_name": "ssh"})
	if err != nil {
		t.Fatalf("request_tool Execute: %v", err)
	}
	if result.Success {
		t.Fatalf("restricted agent expanded its capability set: %#v", result)
	}
	if !strings.Contains(result.Error, "outside this agent's authorized capabilities") {
		t.Fatalf("request_tool error = %q, want capability denial", result.Error)
	}
	if _, ok := agent.registry.Get("ssh"); ok {
		t.Fatal("denied ssh tool was added to restricted agent registry")
	}
}

func TestDynamicRestrictedAgentRequestToolCannotEscapeAllowlist(t *testing.T) {
	registry, workDir := requestToolCapabilityRegistry(t)
	agent := NewAgentWithDynamicType(&DynamicAgentType{
		Name:         "read-only-reviewer",
		AllowedTools: []string{"request_tool"},
	}, nil, registry, workDir, 3, "", nil, nil)

	if err := agent.RequestTool("write"); err == nil {
		t.Fatal("dynamic restricted agent unexpectedly acquired write")
	}
	if _, ok := agent.registry.Get("write"); ok {
		t.Fatal("denied write tool was added to dynamic agent registry")
	}
}

func TestRequestToolRequiresNarrowExplicitAuthority(t *testing.T) {
	registry, workDir := requestToolCapabilityRegistry(t)
	agent := NewAgent(AgentTypeExplore, nil, registry, workDir, 3, "", nil, nil)

	agent.SetAllowedRequestedTools([]string{"write"})
	if err := agent.RequestTool("write"); err != nil {
		t.Fatalf("explicitly authorized write request: %v", err)
	}
	if _, ok := agent.registry.Get("write"); !ok {
		t.Fatal("explicitly authorized write tool was not added")
	}
	if err := agent.RequestTool("ssh"); err == nil {
		t.Fatal("narrow write authority unexpectedly authorized ssh")
	}

	second := NewAgent(AgentTypeExplore, nil, registry, workDir, 3, "", nil, nil)
	second.SetAllowedRequestedTools(nil)
	if err := second.RequestTool("write"); err == nil {
		t.Fatal("empty explicit authority unexpectedly disabled capability checks")
	}
}

func TestRequestToolCanResolveLateToolWithinOriginalCapabilities(t *testing.T) {
	workDir := t.TempDir()
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewRequestToolTool())
	agent := NewAgentWithDynamicType(&DynamicAgentType{
		Name:         "late-bound-writer",
		AllowedTools: []string{"request_tool", "write"},
	}, nil, registry, workDir, 3, "", nil, nil)

	registry.MustRegister(tools.NewWriteTool(workDir))
	if err := agent.RequestTool("write"); err != nil {
		t.Fatalf("late tool declared in original capabilities was denied: %v", err)
	}
	if _, ok := agent.registry.Get("write"); !ok {
		t.Fatal("late tool declared in original capabilities was not added")
	}
}
