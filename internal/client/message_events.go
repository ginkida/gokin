package client

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/genai"

	"gokin/internal/logging"
)

// MessageEvent observes actual model streams and committed conversation turns.
// It is independent of UI text, which may also contain retry/status messages.
type MessageEvent struct {
	Kind, ID, Model, AgentID, ParentToolUseID string
	Text                                      string
	Content                                   *genai.Content
	Response                                  *Response
	Duration                                  time.Duration
	Err                                       error
}

type messageObserverKey struct{}
type messageRoundKey struct{}
type messageObserver struct {
	observe           func(MessageEvent)
	agentID, parentID string
}
type messageRound struct {
	id, model string
	started   time.Time
	observer  messageObserver
}

func ContextWithMessageObserver(ctx context.Context, observe func(MessageEvent)) context.Context {
	return context.WithValue(ctx, messageObserverKey{}, messageObserver{observe: observe})
}

func ContextWithMessageParent(ctx context.Context, agentID, parentToolUseID string) context.Context {
	binding, _ := ctx.Value(messageObserverKey{}).(messageObserver)
	binding.agentID, binding.parentID = agentID, parentToolUseID
	return context.WithValue(ctx, messageObserverKey{}, binding)
}

func (observer messageObserver) emit(event MessageEvent) {
	if observer.observe == nil {
		return
	}
	event.AgentID, event.ParentToolUseID = observer.agentID, observer.parentID
	defer func() {
		if recover() != nil {
			logging.Warn("conversation message observer panicked")
		}
	}()
	observer.observe(event)
}

func BeginObservedMessage(ctx context.Context, model string) (context.Context, func(*Response, error)) {
	observer, _ := ctx.Value(messageObserverKey{}).(messageObserver)
	if observer.observe == nil {
		return ctx, func(*Response, error) {}
	}
	round := &messageRound{id: "msg_" + uuid.NewString(), model: model, started: time.Now(), observer: observer}
	observer.emit(MessageEvent{Kind: "start", ID: round.id, Model: model})
	return context.WithValue(ctx, messageRoundKey{}, round), func(response *Response, err error) {
		if response != nil {
			response.MessageID = round.id
			ensureObservedToolIDs(response.FunctionCalls)
		}
		observer.emit(MessageEvent{Kind: "end", ID: round.id, Model: model, Response: response, Duration: time.Since(round.started), Err: err})
	}
}

func observeMessageText(ctx context.Context, text string) {
	if round, _ := ctx.Value(messageRoundKey{}).(*messageRound); round != nil {
		round.observer.emit(MessageEvent{Kind: "delta", ID: round.id, Model: round.model, Text: text})
	}
}

func ObserveConversationMessage(ctx context.Context, content *genai.Content, model string, response *Response) {
	observer, _ := ctx.Value(messageObserverKey{}).(messageObserver)
	if observer.observe == nil {
		return
	}
	if response != nil {
		ensureObservedToolIDs(response.FunctionCalls)
	}
	if content != nil {
		for _, part := range content.Parts {
			if part != nil && part.FunctionCall != nil {
				ensureObservedToolIDs([]*genai.FunctionCall{part.FunctionCall})
			}
		}
	}
	id := ""
	if response != nil {
		id = response.MessageID
	}
	if id == "" {
		id = "msg_" + uuid.NewString()
	}
	observer.emit(MessageEvent{Kind: "content", ID: id, Model: model, Content: content, Response: response})
}

// Providers without native call IDs still need stable call/result correlation.
// Assign before execution and only when a message observer is installed.
func ensureObservedToolIDs(calls []*genai.FunctionCall) {
	for _, call := range calls {
		if call != nil && call.ID == "" {
			call.ID = "toolu_" + uuid.NewString()
		}
	}
}
