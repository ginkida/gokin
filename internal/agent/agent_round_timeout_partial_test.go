package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

type partialRoundTimeoutClient struct {
	*testkit.MockClient
	text string
}

func (c *partialRoundTimeoutClient) WithModel(string) client.Client {
	// NewAgent always clones its client, even for the same model. This fixture
	// has no mutable provider state, so retaining it is the faithful clone for
	// the partial-stream behavior under test.
	return c
}

func (c *partialRoundTimeoutClient) SendMessageWithHistory(
	context.Context,
	[]*genai.Content,
	string,
) (*client.StreamingResponse, error) {
	chunks := make(chan client.ResponseChunk, 1)
	chunks <- client.ResponseChunk{Text: c.text, InputTokens: 30, OutputTokens: 8}
	return &client.StreamingResponse{Chunks: chunks, Done: make(chan struct{})}, nil
}

func TestAgentRun_ModelRoundTimeoutPreservesPartialResult(t *testing.T) {
	const partial = "Partial delegated finding."
	cl := &partialRoundTimeoutClient{MockClient: testkit.NewMockClient(), text: partial}
	agent := NewAgent(AgentTypeGeneral, cl, tools.NewRegistry(), t.TempDir(), 2, "", nil, nil)
	agent.SetModelRoundTimeout(20 * time.Millisecond)

	result, err := agent.Run(context.Background(), "inspect the project")
	if !errors.Is(err, client.ErrModelRoundTimeout) {
		t.Fatalf("Run() error = %v, want ErrModelRoundTimeout", err)
	}
	if result == nil || result.Output != partial {
		t.Fatalf("Run() result output = %#v, want %q", result, partial)
	}
	if telemetry := client.DetectFailureTelemetry(err); !telemetry.Partial {
		t.Fatalf("timeout telemetry = %#v, want partial=true", telemetry)
	}

	data, readErr := os.ReadFile(result.OutputFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := string(data); got != partial {
		t.Fatalf("agent transcript = %q, want partial output exactly once", got)
	}

	agent.stateMu.RLock()
	var historyText strings.Builder
	for _, content := range agent.history {
		for _, part := range content.Parts {
			if part != nil {
				historyText.WriteString(part.Text)
			}
		}
	}
	agent.stateMu.RUnlock()
	if !strings.Contains(historyText.String(), partial) {
		t.Fatalf("agent history lost partial output: %q", historyText.String())
	}
}
