package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gokin/internal/logging"
)

// Message represents a message exchanged between agents.
type Message struct {
	ID        string         `json:"id"`
	From      string         `json:"from"`    // Sender agent ID
	To        string         `json:"to"`      // Target role (explore, bash, etc.) or agent ID
	Type      string         `json:"type"`    // help_request, response, delegate, etc.
	Content   string         `json:"content"` // The message text
	Data      map[string]any `json:"data"`    // Additional structured data
	Timestamp time.Time      `json:"timestamp"`
}

// AgentMessenger enables communication between agents.
// It implements the tools.Messenger interface.
type AgentMessenger struct {
	ctx         context.Context
	runner      *Runner
	fromAgentID string

	// Message tracking
	inbox      map[string]chan Message // agentID -> incoming messages
	pending    map[string]chan string  // messageID -> response channel
	msgCounter int

	mu sync.RWMutex
}

// NewAgentMessenger creates a messenger for an agent. ctx is the RUNNER's
// (app-lifetime) context — kept only as the fallback parentCtx() uses when
// the owning agent can't be found or hasn't recorded a run context yet
// (e.g. a messenger constructed for tests, or a race at the very top of
// Run() before runCtx is stamped).
func NewAgentMessenger(ctx context.Context, runner *Runner, fromAgentID string) *AgentMessenger {
	return &AgentMessenger{
		ctx:         ctx,
		runner:      runner,
		fromAgentID: fromAgentID,
		inbox:       make(map[string]chan Message),
		pending:     make(map[string]chan string),
	}
}

// parentCtx returns the OWNING agent's live run context (the one Esc/
// task_stop actually cancels via Agent.Cancel -> a.cancelFunc), falling back
// to the app-lifetime ctx captured at construction when the owning agent
// can't be resolved. Without this, every helper agent spawned via
// ask_agent/delegate ran on the app-lifetime ctx regardless of what happened
// to the REQUESTING agent — cancelling the parent left the helper running to
// its own 3-5 minute cap on a context nothing user-facing could reach
// (v0.100.79).
func (m *AgentMessenger) parentCtx() context.Context {
	if m.runner != nil {
		if parent := m.runner.agentByExactID(m.fromAgentID); parent != nil {
			if rc := parent.RunContext(); rc != nil {
				return rc
			}
		}
	}
	return m.ctx
}

// SendMessage sends a message to another agent (by role or ID).
// Returns the message ID for tracking responses.
func (m *AgentMessenger) SendMessage(msgType string, toRole string, content string, data map[string]any) (string, error) {
	m.mu.Lock()
	m.msgCounter++
	msgID := fmt.Sprintf("msg_%s_%d", m.fromAgentID, m.msgCounter)

	// Create response channel for this message
	responseChan := make(chan string, 1)
	m.pending[msgID] = responseChan
	m.mu.Unlock()

	msg := Message{
		ID:        msgID,
		From:      m.fromAgentID,
		To:        toRole,
		Type:      msgType,
		Content:   content,
		Data:      data,
		Timestamp: time.Now(),
	}

	logging.Debug("sending inter-agent message",
		"msg_id", msgID,
		"from", m.fromAgentID,
		"to", toRole,
		"type", msgType)

	// Handle the message based on type
	switch msgType {
	case "help_request":
		// Spawn a sub-agent to handle the request
		go m.handleHelpRequest(msg)
	case "delegate":
		// Delegate task to sub-agent
		go m.handleDelegation(msg)
	default:
		return "", fmt.Errorf("unknown message type: %s", msgType)
	}

	return msgID, nil
}

// ReceiveResponse waits for a response to a specific message.
func (m *AgentMessenger) ReceiveResponse(ctx context.Context, messageID string) (string, error) {
	m.mu.RLock()
	responseChan, ok := m.pending[messageID]
	m.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("no pending message with ID: %s", messageID)
	}

	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()

	select {
	case response := <-responseChan:
		m.mu.Lock()
		delete(m.pending, messageID)
		m.mu.Unlock()
		return response, nil
	case <-ctx.Done():
		m.mu.Lock()
		delete(m.pending, messageID)
		m.mu.Unlock()
		return "", ctx.Err()
	case <-timer.C:
		m.mu.Lock()
		delete(m.pending, messageID)
		m.mu.Unlock()
		return "", fmt.Errorf("timeout waiting for response to message %s", messageID)
	}
}

// spawnResponse builds the ask_agent response text for a spawned sub-agent,
// preferring any partial result.Output over a bare error string — mirrors
// router.go's executeViaSubAgent partial-work preservation (v0.100.34/.45).
// Spawn is synchronous and stores the agent's PARTIAL result (Output +
// Error) into the runner BEFORE returning a non-nil err, so the real "never
// started" failure shape is an EMPTY agentID, not err != nil. Branching on
// spawnErr first (as this code used to) discarded real edits/output whenever
// Spawn returned an error (timeout, max-turn-limit, round timeout, etc.).
func spawnResponse(runner *Runner, agentID string, spawnErr error, errPrefix, noOutputMsg string) string {
	if agentID != "" {
		if result, ok := runner.GetResult(agentID); ok {
			reason := result.Error
			if reason == "" && spawnErr != nil {
				reason = spawnErr.Error()
			}
			if strings.TrimSpace(result.Output) != "" {
				if reason != "" {
					return result.Output + fmt.Sprintf("\n\n⚠ The agent stopped before finishing: %s", reason)
				}
				return result.Output
			}
			if reason != "" {
				return fmt.Sprintf("%s: %s", errPrefix, reason)
			}
			return noOutputMsg
		}
	}
	if spawnErr != nil {
		return fmt.Sprintf("%s: %v", errPrefix, spawnErr)
	}
	return noOutputMsg
}

// handleHelpRequest spawns a sub-agent to answer a help request.
func (m *AgentMessenger) handleHelpRequest(msg Message) {
	ctx, cancel := context.WithTimeout(m.parentCtx(), 3*time.Minute)
	defer cancel()

	// Map role to agent type
	agentType := msg.To

	// Build prompt for the helper agent
	prompt := fmt.Sprintf(
		"Another agent (ID: %s) is asking for help:\n\n%s\n\n"+
			"Please provide a helpful response to assist them.",
		msg.From, msg.Content)

	logging.Info("spawning helper agent",
		"agent_type", agentType,
		"for_message", msg.ID,
		"requester", msg.From)

	// Spawn the helper agent
	agentID, err := m.runner.Spawn(ctx, agentType, prompt, 15, "")

	response := spawnResponse(m.runner, agentID, err,
		fmt.Sprintf("Error from %s agent", agentType),
		fmt.Sprintf("No response from %s agent", agentType))

	// Send response back (non-blocking to prevent goroutine leak)
	m.mu.RLock()
	responseChan, ok := m.pending[msg.ID]
	m.mu.RUnlock()

	if ok {
		select {
		case responseChan <- response:
			// Response sent successfully
		default:
			// Channel full or closed - response already timed out
			logging.Debug("response channel full, receiver likely timed out", "msg_id", msg.ID)
		}
	}
}

// handleDelegation spawns a sub-agent to handle a delegated task.
func (m *AgentMessenger) handleDelegation(msg Message) {
	ctx, cancel := context.WithTimeout(m.parentCtx(), 5*time.Minute)
	defer cancel()

	agentType := msg.To

	// Extract options from data
	maxTurns := 30
	model := ""
	delegationDepth := 0
	if msg.Data != nil {
		if mt, ok := msg.Data["max_turns"].(int); ok {
			maxTurns = mt
		} else if mt, ok := msg.Data["max_turns"].(float64); ok {
			maxTurns = int(mt)
		}
		if mdl, ok := msg.Data["model"].(string); ok {
			model = mdl
		}
		if depth, ok := msg.Data["delegation_depth"].(int); ok {
			delegationDepth = depth
		} else if depth, ok := msg.Data["delegation_depth"].(float64); ok {
			delegationDepth = int(depth)
		}
	}

	// Increment delegation depth for the spawned agent
	delegationDepth++

	// Check if we've exceeded the maximum delegation depth
	if delegationDepth > MaxDelegationDepth {
		logging.Warn("delegation depth exceeded",
			"depth", delegationDepth,
			"max", MaxDelegationDepth,
			"from", msg.From)

		m.mu.RLock()
		responseChan, ok := m.pending[msg.ID]
		m.mu.RUnlock()

		if ok {
			select {
			case responseChan <- fmt.Sprintf("Delegation failed: maximum depth (%d) exceeded", MaxDelegationDepth):
				// Sent successfully
			default:
				// Channel full or closed - receiver already timed out
				logging.Debug("response channel full, receiver likely timed out", "msg_id", msg.ID)
			}
		}
		return
	}

	logging.Info("delegating to sub-agent",
		"agent_type", agentType,
		"from", msg.From,
		"msg_id", msg.ID)

	// Spawn the delegate agent with delegation depth propagated via context
	spawnCtx := WithDelegationDepth(ctx, delegationDepth)
	agentID, err := m.runner.Spawn(spawnCtx, agentType, msg.Content, maxTurns, model)

	response := spawnResponse(m.runner, agentID, err,
		"Delegation failed",
		"Delegated task completed (no output)")

	// Send response back (non-blocking to prevent goroutine leak)
	m.mu.RLock()
	responseChan, ok := m.pending[msg.ID]
	m.mu.RUnlock()

	if ok {
		select {
		case responseChan <- response:
			// Response sent successfully
		default:
			// Channel full or closed - response already timed out
			logging.Debug("response channel full, receiver likely timed out", "msg_id", msg.ID)
		}
	}
}

// Broadcast sends a message to all agents of a given type.
func (m *AgentMessenger) Broadcast(msgType string, targetType string, content string) error {
	at := ParseAgentType(targetType)

	// Snapshot matching agent IDs under runner lock to avoid nested locking
	m.runner.mu.RLock()
	var targetIDs []string
	for _, agent := range m.runner.agents {
		if agent.Type == at && agent.GetStatus() == AgentStatusRunning {
			targetIDs = append(targetIDs, agent.ID)
		}
	}
	m.runner.mu.RUnlock()

	count := 0
	m.mu.Lock()
	for _, agentID := range targetIDs {
		if inbox, ok := m.inbox[agentID]; ok {
			msg := Message{
				ID:        fmt.Sprintf("broadcast_%d", m.msgCounter),
				From:      m.fromAgentID,
				To:        agentID,
				Type:      msgType,
				Content:   content,
				Timestamp: time.Now(),
			}
			m.msgCounter++
			select {
			case inbox <- msg:
				count++
			default:
				// Inbox full, skip
			}
		}
	}
	m.mu.Unlock()

	logging.Debug("broadcast sent", "type", targetType, "recipients", count)
	return nil
}

// RegisterInbox creates an inbox for an agent to receive messages.
func (m *AgentMessenger) RegisterInbox(agentID string) <-chan Message {
	m.mu.Lock()
	defer m.mu.Unlock()

	inbox := make(chan Message, 10)
	m.inbox[agentID] = inbox
	return inbox
}

// UnregisterInbox removes an agent's inbox.
func (m *AgentMessenger) UnregisterInbox(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if inbox, ok := m.inbox[agentID]; ok {
		close(inbox)
		delete(m.inbox, agentID)
	}
}
