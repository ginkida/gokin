package app

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/genai"

	"gokin/internal/client"
	"gokin/internal/hooks"
	"gokin/internal/security"
)

// sdkJSONPresenter consumes model/history events, rather than UI callbacks:
// UI callbacks do not retain call IDs or distinguish model text from notices.
type sdkJSONPresenter struct {
	*stdoutPresenter
	mu            sync.Mutex
	encoder       *json.Encoder
	sessionID     string
	partial       bool
	hostState     bool
	writeErr      error
	apiDuration   time.Duration
	cacheCreation int
	redactor      *security.SecretRedactor
	rounds        map[string]*sdkMessageRound
	pending       map[string]sdkPendingCall
	pendingOrder  []string
	lastText      string
}

type sdkMessageRound struct {
	text      bool
	nextIndex int
}
type sdkPendingCall struct{ id, parent string }

func newSDKJSONPresenter(writer io.Writer, sessionID string, partial, hostState bool) *sdkJSONPresenter {
	return &sdkJSONPresenter{stdoutPresenter: newStdoutPresenter(io.Discard), encoder: json.NewEncoder(writer),
		sessionID: sessionID, partial: partial, hostState: hostState,
		redactor: security.NewSecretRedactor(), rounds: map[string]*sdkMessageRound{}, pending: map[string]sdkPendingCall{}}
}

func sdkParent(parent string) any {
	if parent == "" {
		return nil
	}
	return parent
}

// Caller holds mu, including across encoding: delegated agents share stdout.
func (p *sdkJSONPresenter) emit(frame map[string]any) {
	if p.writeErr != nil {
		return
	}
	frame["session_id"], frame["uuid"] = p.sessionID, uuid.NewString()
	p.writeErr = p.encoder.Encode(frame)
}

func (p *sdkJSONPresenter) Initialize(cwd, model, permissionMode string, names []string, replay any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emit(map[string]any{"type": "system", "subtype": "init", "cwd": cwd, "model": model,
		"tools": names, "mcp_servers": []any{}, "permissionMode": permissionMode})
	if replay != nil {
		p.emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": replay}, "parent_tool_use_id": nil})
	}
	if p.hostState {
		p.emit(map[string]any{"type": "system", "subtype": "session_state_changed", "state": "running", "sdk_host_only": true})
	}
}

func sdkUsage(response *client.Response) map[string]any {
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}
	if response != nil {
		usage["input_tokens"], usage["output_tokens"] = max(0, response.InputTokens), max(0, response.OutputTokens)
		usage["cache_creation_input_tokens"], usage["cache_read_input_tokens"] = max(0, response.CacheCreationInputTokens), max(0, response.CacheReadInputTokens)
	}
	return usage
}

func sdkStopReason(response *client.Response) string {
	if response == nil {
		return "end_turn"
	}
	if len(response.FunctionCalls) > 0 {
		return "tool_use"
	}
	if response.FinishReason == genai.FinishReasonMaxTokens {
		return "max_tokens"
	}
	return "end_turn"
}

func (p *sdkJSONPresenter) stream(event client.MessageEvent, body map[string]any) {
	p.emit(map[string]any{"type": "stream_event", "event": body, "parent_tool_use_id": sdkParent(event.ParentToolUseID)})
}

func (p *sdkJSONPresenter) Observe(event client.MessageEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch event.Kind {
	case "start":
		if !p.partial {
			return
		}
		p.rounds[event.ID] = &sdkMessageRound{}
		p.stream(event, map[string]any{"type": "message_start", "message": map[string]any{
			"id": event.ID, "type": "message", "role": "assistant", "model": event.Model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": sdkUsage(nil)}})
	case "delta":
		if !p.partial || event.Text == "" {
			return
		}
		round := p.rounds[event.ID]
		if round == nil {
			return
		}
		if !round.text {
			round.text = true
			round.nextIndex = 1
			p.stream(event, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		}
		p.stream(event, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": p.redactor.Redact(event.Text)}})
	case "end":
		p.apiDuration += event.Duration
		if event.Response != nil {
			p.cacheCreation += max(0, event.Response.CacheCreationInputTokens)
		}
		if round := p.rounds[event.ID]; round != nil {
			if round.text {
				p.stream(event, map[string]any{"type": "content_block_stop", "index": 0})
			}
			if event.Response != nil {
				for _, call := range event.Response.FunctionCalls {
					if call == nil {
						continue
					}
					index := round.nextIndex
					round.nextIndex++
					p.stream(event, map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "tool_use", "id": call.ID, "name": hooks.ClaudeToolName(call.Name), "input": map[string]any{}}})
					args := call.Args
					if args == nil {
						args = map[string]any{}
					}
					data, err := json.Marshal(p.redactor.RedactMap(args))
					if err != nil {
						p.writeErr = err
						break
					}
					p.stream(event, map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(data)}})
					p.stream(event, map[string]any{"type": "content_block_stop", "index": index})
				}
			}
			p.stream(event, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": sdkStopReason(event.Response), "stop_sequence": nil}, "usage": sdkUsage(event.Response)})
			p.stream(event, map[string]any{"type": "message_stop"})
			delete(p.rounds, event.ID)
		}
		if event.Err != nil && event.Response != nil {
			event.Content = &genai.Content{Role: genai.RoleModel, Parts: client.ResponsePartsForHistory(event.Response)}
			p.content(event)
		}
	case "content":
		p.content(event)
	}
}

func (p *sdkJSONPresenter) content(event client.MessageEvent) {
	if event.Content == nil {
		return
	}
	blocks := []any{}
	var text strings.Builder
	for _, part := range event.Content.Parts {
		if part == nil || part.Thought {
			continue
		}
		if part.FunctionCall != nil {
			call := part.FunctionCall
			args := call.Args
			if args == nil {
				args = map[string]any{}
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": hooks.ClaudeToolName(call.Name), "input": p.redactor.RedactMap(args)})
			key := event.ParentToolUseID + "\x00" + call.ID
			if _, ok := p.pending[key]; !ok {
				p.pendingOrder = append(p.pendingOrder, key)
			}
			p.pending[key] = sdkPendingCall{call.ID, event.ParentToolUseID}
		} else if part.FunctionResponse != nil {
			response := part.FunctionResponse
			isError := false
			content := ""
			if response.Response != nil {
				content, _ = response.Response["content"].(string)
				if failure, ok := response.Response["error"].(string); ok && failure != "" {
					isError = true
					if content != "" {
						content += "\n"
					}
					content += failure
				}
				if success, ok := response.Response["success"].(bool); ok && !success {
					isError = true
				}
				if content == "" {
					encoded, _ := json.Marshal(p.redactor.RedactMap(response.Response))
					content = string(encoded)
				}
			}
			blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": response.ID, "content": p.redactor.Redact(content), "is_error": isError})
			delete(p.pending, event.ParentToolUseID+"\x00"+response.ID)
		} else if part.Text != "" {
			value := p.redactor.Redact(part.Text)
			text.WriteString(value)
			blocks = append(blocks, map[string]any{"type": "text", "text": value})
		} else if part.InlineData != nil && strings.HasPrefix(part.InlineData.MIMEType, "image/") && len(blocks) > 0 {
			// Images belong to the preceding tool result, not a new user turn.
			if result, ok := blocks[len(blocks)-1].(map[string]any); ok && result["type"] == "tool_result" {
				var contents []any
				if previous, ok := result["content"].([]any); ok {
					contents = previous
				} else {
					contents = []any{map[string]any{"type": "text", "text": result["content"]}}
				}
				result["content"] = append(contents, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": part.InlineData.MIMEType, "data": base64.StdEncoding.EncodeToString(part.InlineData.Data)}})
			}
		}
	}
	if len(blocks) == 0 {
		return
	}
	if event.Content.Role == genai.RoleModel {
		p.emit(map[string]any{"type": "assistant", "message": map[string]any{"id": event.ID, "type": "message", "role": "assistant", "model": event.Model, "content": blocks, "stop_reason": sdkStopReason(event.Response), "stop_sequence": nil, "usage": sdkUsage(event.Response)}, "parent_tool_use_id": sdkParent(event.ParentToolUseID)})
		if event.ParentToolUseID == "" && text.Len() > 0 {
			p.lastText = text.String()
		}
	} else {
		p.emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": blocks}, "parent_tool_use_id": sdkParent(event.ParentToolUseID)})
	}
}

func (p *sdkJSONPresenter) Result() string {
	p.mu.Lock()
	value := p.lastText
	p.mu.Unlock()
	if value != "" {
		return value
	}
	return p.stdoutPresenter.Result()
}
func (p *sdkJSONPresenter) Err() error { p.mu.Lock(); defer p.mu.Unlock(); return p.writeErr }

// SDKResult maps terminal outcomes without claiming unsupported cost tariffs.
// Extra usage fields remain provider-reported; total input is split into the
// disjoint Anthropic cache/uncached counters expected by the SDK.
func SDKResult(result HeadlessResult, apiDuration time.Duration, cacheCreation int) map[string]any {
	subtype := "success"
	if result.Status != "success" {
		subtype = "error_during_execution"
		if result.Error != nil {
			switch result.Error.Kind {
			case "max_turns":
				subtype = "error_max_turns"
			case "budget":
				subtype = "error_max_budget_usd"
			case "structured_output":
				subtype = "error_max_structured_output_retries"
			}
		}
	}
	creation := min(max(0, cacheCreation), max(0, result.Usage.InputTokens-result.Usage.CacheReadInputTokens))
	frame := map[string]any{"type": "result", "subtype": subtype, "duration_ms": result.DurationMS, "duration_api_ms": apiDuration.Milliseconds(), "is_error": result.Status != "success", "num_turns": result.Usage.ModelRounds, "session_id": result.SessionID, "uuid": uuid.NewString(), "result": result.Result, "total_cost_usd": result.Cost.EstimatedUSD, "cost_tracked": result.Cost.Tracked, "permission_denials": []any{}, "usage": map[string]any{"input_tokens": max(0, result.Usage.InputTokens-result.Usage.CacheReadInputTokens-creation), "output_tokens": result.Usage.OutputTokens, "cache_read_input_tokens": result.Usage.CacheReadInputTokens, "cache_creation_input_tokens": creation}}
	if result.Error != nil {
		frame["errors"] = []string{result.Error.Message}
		frame["error"] = result.Error
	}
	if result.StructuredOutput != nil {
		frame["structured_output"] = *result.StructuredOutput
	}
	return frame
}

func (p *sdkJSONPresenter) Complete(result HeadlessResult) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessionID = result.SessionID
	for _, key := range p.pendingOrder {
		if call, ok := p.pending[key]; ok {
			p.emit(map[string]any{"type": "user", "parent_tool_use_id": sdkParent(call.parent), "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": call.id, "is_error": true, "content": "Tool result unavailable: turn ended before a result was recorded."}}}})
		}
	}
	frame := SDKResult(result, p.apiDuration, p.cacheCreation)
	frame["result"] = p.redactor.Redact(result.Result)
	p.emit(frame)
	if p.hostState {
		p.emit(map[string]any{"type": "system", "subtype": "session_state_changed", "state": "idle", "sdk_host_only": true})
	}
	return p.writeErr
}
