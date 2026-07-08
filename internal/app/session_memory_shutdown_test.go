package app

import (
	"context"
	"testing"
	"time"

	appcontext "gokin/internal/context"

	"google.golang.org/genai"
)

type shutdownSessionSummarizer struct {
	started  chan struct{}
	canceled chan struct{}
}

func (s *shutdownSessionSummarizer) Summarize(ctx context.Context, history []*genai.Content, prompt string) (string, error) {
	close(s.started)
	<-ctx.Done()
	close(s.canceled)
	return "", ctx.Err()
}

func shutdownSessionHistory() []*genai.Content {
	history := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "continue the task"}}},
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "working"}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "read internal/app/signals.go"}}},
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "checked it"}}},
	}
	for i := 0; i < 8; i++ {
		history = append(history,
			&genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "continue"}}},
			&genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "done"}}},
		)
	}
	return history
}

func TestGracefulShutdown_ClosesSessionMemory(t *testing.T) {
	sm := appcontext.NewSessionMemoryManager(t.TempDir(), appcontext.DefaultSessionMemoryConfig())
	summarizer := &shutdownSessionSummarizer{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	sm.SetSummarizer(summarizer)

	history := shutdownSessionHistory()
	sm.Extract(history, 20000)
	sm.Extract(history, 20001)
	sm.Extract(history, 20002)

	select {
	case <-summarizer.started:
	case <-time.After(time.Second):
		t.Fatal("LLM session-memory extraction did not start")
	}

	a := &App{sessionMemory: sm}
	a.gracefulShutdown(context.Background())

	select {
	case <-summarizer.canceled:
	default:
		t.Fatal("gracefulShutdown returned without closing session memory")
	}
}
