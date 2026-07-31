package app

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"gokin/internal/security"
	"gokin/internal/tools"
)

// headlessOutputPresenter is the presentation contract additionally needed by
// RunHeadlessWithOptions to collect the final answer and observe output errors.
// Both plain text and JSONL streaming use the same agent execution callbacks.
type headlessOutputPresenter interface {
	agentPresenter
	Finish()
	Result() string
	Err() error
}

// HeadlessStreamEvent is one non-terminal JSONL record. The final line is
// always the ordinary HeadlessResult (Type == "result"), so consumers can use
// one terminal parser for both json and stream-json modes.
type HeadlessStreamEvent struct {
	SchemaVersion int            `json:"schema_version"`
	Type          string         `json:"type"`
	Sequence      uint64         `json:"sequence"`
	SessionID     string         `json:"session_id,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
}

// HeadlessStreamState is a connection-scoped sequence counter. A caller that
// sends several headless turns over one JSONL stream should reuse one state so
// event ordering remains globally monotonic across terminal result boundaries.
type HeadlessStreamState struct {
	mu       sync.Mutex
	sequence uint64
}

func NewHeadlessStreamState() *HeadlessStreamState {
	return &HeadlessStreamState{}
}

func (s *HeadlessStreamState) sequenceWrite(write func(uint64) error) error {
	if s == nil {
		return write(0)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	return write(s.sequence)
}

// streamJSONPresenter emits progress as independent JSON values, one per
// newline. A single mutex covers sequencing, encoding, and answer collection:
// callbacks from tool progress and delegated agents may arrive concurrently,
// but downstream JSONL readers must never observe interleaved records.
type streamJSONPresenter struct {
	mu        sync.Mutex
	encoder   *json.Encoder
	sessionID string
	stream    *HeadlessStreamState
	result    strings.Builder
	writeErr  error
	redactor  *security.SecretRedactor
}

func newStreamJSONPresenter(writer io.Writer, sessionID string, stream *HeadlessStreamState) *streamJSONPresenter {
	if writer == nil {
		writer = io.Discard
	}
	if stream == nil {
		stream = NewHeadlessStreamState()
	}
	return &streamJSONPresenter{
		encoder:   json.NewEncoder(writer),
		sessionID: sessionID,
		stream:    stream,
		redactor:  security.NewSecretRedactor(),
	}
}

func (p *streamJSONPresenter) redact(text string) string {
	if p.redactor == nil {
		return text
	}
	return p.redactor.Redact(text)
}

func (p *streamJSONPresenter) redactMap(value map[string]any) map[string]any {
	if p.redactor == nil {
		return value
	}
	return p.redactor.RedactMap(value)
}

func (p *streamJSONPresenter) emit(eventType string, data map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.writeErr != nil {
		return
	}
	p.writeErr = p.stream.sequenceWrite(func(sequence uint64) error {
		return p.encoder.Encode(HeadlessStreamEvent{
			SchemaVersion: HeadlessSchemaVersion,
			Type:          eventType,
			Sequence:      sequence,
			SessionID:     p.sessionID,
			Data:          data,
		})
	})
}

func (p *streamJSONPresenter) StreamText(text string) {
	if text == "" {
		return
	}
	p.mu.Lock()
	_, _ = p.result.WriteString(text)
	p.mu.Unlock()
	p.emit("assistant_delta", map[string]any{"text": text})
}

// Thinking is intentionally omitted. Streaming internal reasoning would make
// the machine-readable mode leak content that plain and final-JSON modes
// deliberately keep private.
func (p *streamJSONPresenter) StreamThinking(string) {}

func (p *streamJSONPresenter) StreamTokenEstimate(tokens int) {
	p.emit("token_estimate", map[string]any{"output_tokens": tokens})
}

func (p *streamJSONPresenter) ToolStart(name string, args map[string]any) {
	p.emit("tool_start", map[string]any{
		"tool":      name,
		"arguments": p.redactMap(args),
	})
}

func (p *streamJSONPresenter) ToolEnd(name string, _ map[string]any, result tools.ToolResult) {
	data := map[string]any{
		"tool":    name,
		"success": result.Success,
	}
	if result.Content != "" {
		data["content"] = p.redact(result.Content)
	}
	if result.Error != "" {
		data["error"] = p.redact(result.Error)
	}
	if result.Duration != "" {
		data["duration"] = result.Duration
	}
	if result.PolicyBlock != nil {
		data["policy_kind"] = string(result.PolicyBlock.Kind)
		data["policy_reason"] = p.redact(result.PolicyBlock.Reason)
	}
	p.emit("tool_result", data)
}

func (p *streamJSONPresenter) ToolProgress(name string, elapsed time.Duration, step string) {
	data := map[string]any{
		"tool":       name,
		"elapsed_ms": elapsed.Milliseconds(),
	}
	if step != "" {
		data["step"] = step
	}
	p.emit("tool_progress", data)
}

func (p *streamJSONPresenter) ToolDetailedProgress(name string, progress float64, step string) {
	data := map[string]any{
		"tool":     name,
		"progress": progress,
	}
	if step != "" {
		data["step"] = step
	}
	p.emit("tool_progress", data)
}

func (p *streamJSONPresenter) ToolError(err error) {
	if err == nil {
		return
	}
	p.emit("tool_error", map[string]any{"message": p.redact(err.Error())})
}

func (p *streamJSONPresenter) Warning(warning string) {
	if warning != "" {
		p.emit("warning", map[string]any{"message": p.redact(warning)})
	}
}

func (p *streamJSONPresenter) InlineDiff(filePath, oldText, newText string) {
	p.emit("file_change", map[string]any{
		"path":      filePath,
		"old_bytes": len(oldText),
		"new_bytes": len(newText),
	})
}

func (p *streamJSONPresenter) LoopIteration(iteration, toolsUsed int) {
	p.emit("loop_iteration", map[string]any{
		"iteration":  iteration,
		"tools_used": toolsUsed,
	})
}

func (p *streamJSONPresenter) TokenUsage(inputTokens, maxTokens int, percentUsed float64) {
	p.emit("context_usage", map[string]any{
		"input_tokens": inputTokens,
		"max_tokens":   maxTokens,
		"percent_used": percentUsed,
	})
}

func (p *streamJSONPresenter) FilePeek(filePath, title, content, action string) {
	p.emit("file_peek", map[string]any{
		"path":          filePath,
		"title":         title,
		"action":        action,
		"content_bytes": len(content),
	})
}

func (p *streamJSONPresenter) MemoryNotify(message string) {
	if message != "" {
		p.emit("memory", map[string]any{"message": p.redact(message)})
	}
}

func (p *streamJSONPresenter) SubAgentActivity(
	agentID, agentType, prompt, toolName string,
	args map[string]any,
	status string,
	success bool,
	summary string,
) {
	data := map[string]any{
		"agent_id":   agentID,
		"agent_type": agentType,
		"status":     status,
		"success":    success,
	}
	if prompt != "" {
		data["task"] = p.redact(prompt)
	}
	if toolName != "" {
		data["tool"] = toolName
	}
	if len(args) > 0 {
		data["arguments"] = p.redactMap(args)
	}
	if summary != "" {
		data["summary"] = p.redact(summary)
	}
	p.emit("subagent", data)
}

func (p *streamJSONPresenter) Finish() {}

func (p *streamJSONPresenter) Result() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result.String()
}

func (p *streamJSONPresenter) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.writeErr == nil {
		return nil
	}
	return fmt.Errorf("encode stream-json event: %w", p.writeErr)
}
