package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"gokin/internal/auth"
	"gokin/internal/config"
	"gokin/internal/logging"
	"gokin/internal/ratelimit"
	"gokin/internal/security"

	"golang.org/x/sync/singleflight"
	"google.golang.org/genai"
)

// OpenAIOAuthClient implements Client interface using OAuth tokens
// and the ChatGPT Codex Responses API.
type OpenAIOAuthClient struct {
	httpClient   *http.Client
	accessToken  string
	refreshToken string
	expiresAt    time.Time
	email        string
	accountID    string
	config       *config.Config
	model        string
	tools        []*genai.Tool
	rateLimiter  *ratelimit.Limiter

	httpTimeout       time.Duration
	streamIdleTimeout time.Duration

	statusCallback    StatusCallback
	systemInstruction string
	thinkingBudget    int32
	reasoningEffort   string // none/low/medium/high/xhigh — overrides thinkingBudget mapping

	mu           sync.RWMutex
	refreshGroup *singleflight.Group // pointer so WithModel clones share dedup
}

// NewOpenAIOAuthClient creates a new client using OpenAI OAuth tokens.
func NewOpenAIOAuthClient(ctx context.Context, cfg *config.Config) (*OpenAIOAuthClient, error) {
	oauth := cfg.API.OpenAIOAuth
	if oauth == nil {
		return nil, fmt.Errorf("no OpenAI OAuth token configured")
	}

	httpTimeout := cfg.API.Retry.HTTPTimeout
	if httpTimeout == 0 {
		httpTimeout = 120 * time.Second
	}

	streamIdleTimeout := cfg.API.Retry.StreamIdleTimeout
	if streamIdleTimeout == 0 {
		streamIdleTimeout = 60 * time.Second
	}

	httpClient, err := security.CreateSecureHTTPClient(security.DefaultTLSConfig(), httpTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	// Reasoning effort: config override > default "xhigh"
	reasoningEffort := cfg.Model.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = "xhigh"
	}

	client := &OpenAIOAuthClient{
		httpClient:        httpClient,
		accessToken:       oauth.AccessToken,
		refreshToken:      oauth.RefreshToken,
		expiresAt:         time.Unix(oauth.ExpiresAt, 0),
		email:             oauth.Email,
		accountID:         oauth.AccountID,
		config:            cfg,
		model:             cfg.Model.Name,
		reasoningEffort:   reasoningEffort,
		httpTimeout:       httpTimeout,
		streamIdleTimeout: streamIdleTimeout,
		refreshGroup:      &singleflight.Group{},
	}

	// Ensure token is valid
	if err := client.ensureValidToken(ctx); err != nil {
		return nil, fmt.Errorf("failed to validate OpenAI OAuth token: %w", err)
	}

	// Extract account ID from JWT if not cached
	if client.accountID == "" {
		if accountID, err := auth.ExtractOpenAIAccountID(client.accessToken); err == nil {
			client.accountID = accountID
			cfg.API.OpenAIOAuth.AccountID = accountID
			if saveErr := cfg.Save(); saveErr != nil {
				logging.Warn("failed to save OpenAI account ID", "error", saveErr)
			}
		}
	}

	logging.Debug("created OpenAI OAuth client",
		"email", client.email,
		"model", client.model,
		"accountID", client.accountID,
		"expires", client.expiresAt.Format(time.RFC3339))

	return client, nil
}

// SetSystemInstruction sets the system-level instruction for the model.
func (c *OpenAIOAuthClient) SetSystemInstruction(instruction string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.systemInstruction = instruction
}

// SetThinkingBudget configures the thinking/reasoning budget.
func (c *OpenAIOAuthClient) SetThinkingBudget(budget int32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.thinkingBudget = budget
}

// SetTools sets the tools available for function calling.
func (c *OpenAIOAuthClient) SetTools(tools []*genai.Tool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tools = tools
}

// SetRateLimiter sets the rate limiter for API calls.
func (c *OpenAIOAuthClient) SetRateLimiter(limiter interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rl, ok := limiter.(*ratelimit.Limiter); ok {
		c.rateLimiter = rl
	}
}

// SetStatusCallback sets the callback for status updates.
func (c *OpenAIOAuthClient) SetStatusCallback(cb StatusCallback) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statusCallback = cb
}

// SendMessage sends a user message and returns a streaming response.
func (c *OpenAIOAuthClient) SendMessage(ctx context.Context, message string) (*StreamingResponse, error) {
	return c.SendMessageWithHistory(ctx, nil, message)
}

// SendMessageWithHistory sends a message with conversation history.
func (c *OpenAIOAuthClient) SendMessageWithHistory(ctx context.Context, history []*genai.Content, message string) (*StreamingResponse, error) {
	contents := make([]*genai.Content, len(history)+1)
	copy(contents, history)
	contents[len(contents)-1] = genai.NewContentFromText(message, genai.RoleUser)

	return c.doRequest(ctx, contents)
}

// SendFunctionResponse sends function call results back to the model.
func (c *OpenAIOAuthClient) SendFunctionResponse(ctx context.Context, history []*genai.Content, results []*genai.FunctionResponse) (*StreamingResponse, error) {
	var parts []*genai.Part
	for _, result := range results {
		if result == nil {
			continue
		}
		part := genai.NewPartFromFunctionResponse(result.Name, result.Response)
		part.FunctionResponse.ID = result.ID
		parts = append(parts, part)
	}

	if len(parts) == 0 {
		parts = append(parts, genai.NewPartFromText(" "))
	}

	funcContent := &genai.Content{
		Role:  genai.RoleUser,
		Parts: parts,
	}

	contents := make([]*genai.Content, len(history)+1)
	copy(contents, history)
	contents[len(contents)-1] = funcContent

	return c.doRequest(ctx, contents)
}

// CountTokens returns an estimate based on content length.
func (c *OpenAIOAuthClient) CountTokens(ctx context.Context, contents []*genai.Content) (*genai.CountTokensResponse, error) {
	totalChars := 0
	for _, content := range contents {
		for _, part := range content.Parts {
			if part.Text != "" {
				totalChars += len(part.Text)
			}
		}
	}

	estimatedTokens := int32(totalChars / 4)
	return &genai.CountTokensResponse{
		TotalTokens: estimatedTokens,
	}, nil
}

// GetModel returns the model name.
func (c *OpenAIOAuthClient) GetModel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.model
}

// SetModel changes the model for this client.
func (c *OpenAIOAuthClient) SetModel(modelName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = modelName
}

// WithModel returns a new client configured for the specified model.
func (c *OpenAIOAuthClient) WithModel(modelName string) Client {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return &OpenAIOAuthClient{
		httpClient:        c.httpClient,
		accessToken:       c.accessToken,
		refreshToken:      c.refreshToken,
		expiresAt:         c.expiresAt,
		email:             c.email,
		accountID:         c.accountID,
		config:            c.config,
		model:             modelName,
		tools:             c.tools,
		rateLimiter:       c.rateLimiter,
		httpTimeout:       c.httpTimeout,
		streamIdleTimeout: c.streamIdleTimeout,
		statusCallback:    c.statusCallback,
		systemInstruction: c.systemInstruction,
		thinkingBudget:    c.thinkingBudget,
		reasoningEffort:   c.reasoningEffort,
		refreshGroup:      c.refreshGroup, // shared across clones
	}
}

// GetRawClient returns nil since we use HTTP directly.
func (c *OpenAIOAuthClient) GetRawClient() interface{} {
	return nil
}

// Close closes the client connection and releases transport resources.
func (c *OpenAIOAuthClient) Close() error {
	if c.httpClient != nil {
		if transport, ok := c.httpClient.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
	return nil
}

// ensureValidToken checks if the token is valid and refreshes if needed.
// Uses singleflight to prevent concurrent callers from racing on refresh.
func (c *OpenAIOAuthClient) ensureValidToken(ctx context.Context) error {
	c.mu.RLock()
	if c.refreshToken == "" {
		c.mu.RUnlock()
		return fmt.Errorf("missing OpenAI OAuth refresh token; run /oauth-login openai again")
	}
	if c.accessToken != "" && !c.expiresAt.IsZero() && time.Now().Before(c.expiresAt.Add(-auth.TokenRefreshBuffer)) {
		c.mu.RUnlock()
		return nil
	}
	c.mu.RUnlock()

	// Serialize concurrent refresh attempts via singleflight
	_, err, _ := c.refreshGroup.Do("refresh", func() (interface{}, error) {
		// Re-check under lock (another goroutine may have refreshed)
		c.mu.RLock()
		if c.accessToken != "" && !c.expiresAt.IsZero() && time.Now().Before(c.expiresAt.Add(-auth.TokenRefreshBuffer)) {
			c.mu.RUnlock()
			return nil, nil
		}
		refreshTok := c.refreshToken
		c.mu.RUnlock()

		logging.Debug("OpenAI OAuth token expired, refreshing")

		manager := auth.NewOpenAIOAuthManager()
		newToken, err := manager.RefreshToken(ctx, refreshTok)
		if err != nil {
			return nil, fmt.Errorf("failed to refresh OpenAI OAuth token: %w (try /oauth-login openai)", err)
		}

		// Update client state under lock
		c.mu.Lock()
		c.accessToken = newToken.AccessToken
		c.expiresAt = newToken.ExpiresAt
		if newToken.RefreshToken != "" {
			c.refreshToken = newToken.RefreshToken
		}
		if newToken.Email != "" {
			c.email = newToken.Email
		}
		if accountID, aErr := auth.ExtractOpenAIAccountID(newToken.AccessToken); aErr == nil {
			c.accountID = accountID
		}
		c.mu.Unlock()

		// Persist to config (snapshot under lock, write outside)
		c.mu.RLock()
		snap := struct {
			accessToken, refreshToken, email, accountID string
			expiresAt                                   int64
		}{
			accessToken:  c.accessToken,
			refreshToken: c.refreshToken,
			email:        c.email,
			accountID:    c.accountID,
			expiresAt:    c.expiresAt.Unix(),
		}
		c.mu.RUnlock()

		oauth := c.config.API.OpenAIOAuth
		if oauth != nil {
			oauth.AccessToken = snap.accessToken
			oauth.ExpiresAt = snap.expiresAt
			if snap.refreshToken != "" {
				oauth.RefreshToken = snap.refreshToken
			}
			if snap.email != "" {
				oauth.Email = snap.email
			}
			if snap.accountID != "" {
				oauth.AccountID = snap.accountID
			}
			if err := c.config.Save(); err != nil {
				logging.Warn("failed to save refreshed OpenAI OAuth token", "error", err)
			}
		}

		logging.Debug("OpenAI OAuth token refreshed",
			"expiresAt", newToken.ExpiresAt.Format(time.RFC3339))

		return nil, nil
	})

	return err
}

// doRequest performs a streaming request to the Codex Responses API.
func (c *OpenAIOAuthClient) doRequest(ctx context.Context, contents []*genai.Content) (*StreamingResponse, error) {
	contents = sanitizeContents(contents)

	if err := c.ensureValidToken(ctx); err != nil {
		return nil, err
	}

	// Snapshot rate limiter under lock
	c.mu.RLock()
	rl := c.rateLimiter
	c.mu.RUnlock()

	// Rate limiting
	var estimatedTokens int64
	if rl != nil {
		estimatedTokens = ratelimit.EstimateTokensFromContents(len(contents), 500)
		if err := rl.AcquireWithContext(ctx, estimatedTokens); err != nil {
			return nil, fmt.Errorf("rate limit: %w", err)
		}
	}

	// Build request body
	reqBody := c.buildRequest(contents)

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	c.mu.RLock()
	token := c.accessToken
	accountID := c.accountID
	c.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, "POST", auth.OpenAICodexAPI, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}
	for k, v := range auth.OpenAICodexHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if rl != nil && estimatedTokens > 0 {
			rl.ReturnTokens(1, estimatedTokens)
		}
		err = WrapProviderHTTPTimeout(err, "openai", c.httpTimeout)
		var netErr net.Error
		if errors.As(err, &netErr) {
			logging.Warn("OpenAI OAuth HTTP request failed",
				"timeout", netErr.Timeout(),
				"error", err)
		} else {
			logging.Warn("OpenAI OAuth HTTP request failed", "error", err)
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}

	// Map non-standard error codes
	if resp.StatusCode != http.StatusOK {
		retryAfter := ParseRetryAfter(resp)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if rl != nil && estimatedTokens > 0 {
			rl.ReturnTokens(1, estimatedTokens)
		}

		statusCode := resp.StatusCode
		bodyStr := string(body)

		// HTTP 404 with usage_limit_reached or rate_limit_exceeded → treat as 429
		if statusCode == http.StatusNotFound {
			if strings.Contains(bodyStr, "usage_limit_reached") || strings.Contains(bodyStr, "rate_limit_exceeded") {
				statusCode = http.StatusTooManyRequests
			}
		}

		return nil, &HTTPError{
			StatusCode: statusCode,
			RetryAfter: retryAfter,
			Message:    fmt.Sprintf("OpenAI API error (%d): %s", statusCode, bodyStr),
		}
	}

	// Create streaming response
	chunks := make(chan ResponseChunk, 10)
	done := make(chan struct{})

	go c.processSSEStream(ctx, resp.Body, chunks, done, estimatedTokens, rl)

	return &StreamingResponse{
		Chunks: chunks,
		Done:   done,
	}, nil
}

// buildRequest builds the Responses API request body.
func (c *OpenAIOAuthClient) buildRequest(contents []*genai.Content) map[string]any {
	c.mu.RLock()
	model := c.model
	sysInstruction := c.systemInstruction
	toolsSnap := c.tools
	thinkingBudget := c.thinkingBudget
	cfgEffort := c.reasoningEffort
	c.mu.RUnlock()

	reqBody := map[string]any{
		"model":  model,
		"stream": true,
	}

	// Convert contents to OpenAI Responses API input format
	input := c.convertContentsToInput(contents)
	if len(input) > 0 {
		reqBody["input"] = input
	}

	// System instructions
	if sysInstruction != "" {
		reqBody["instructions"] = sysInstruction
	}

	// Tools
	if len(toolsSnap) > 0 {
		openaiTools := c.convertToolsToOpenAI(toolsSnap)
		if len(openaiTools) > 0 {
			reqBody["tools"] = openaiTools
		}
	}

	// Reasoning effort: use configured effort level, or fall back to thinkingBudget mapping.
	// Config takes priority ("xhigh" by default for OpenAI).
	effort := cfgEffort
	if effort == "" {
		effort = thinkingBudgetToEffort(thinkingBudget)
	}
	if effort != "" && effort != "none" {
		reqBody["reasoning"] = map[string]any{
			"effort": effort,
		}
	}

	return reqBody
}

// thinkingBudgetToEffort maps the router's thinkingBudget (int32 token count)
// to an OpenAI Responses API reasoning effort level.
// Returns "" when reasoning should be omitted (budget=0).
func thinkingBudgetToEffort(budget int32) string {
	switch {
	case budget <= 0:
		return "" // omit — API uses its default
	case budget <= 1024:
		return "low"
	case budget <= 2048:
		return "medium"
	case budget <= 4096:
		return "high"
	default:
		return "xhigh" // 8192+
	}
}

// convertContentsToInput converts genai.Content history to OpenAI Responses API input items.
func (c *OpenAIOAuthClient) convertContentsToInput(contents []*genai.Content) []map[string]any {
	var input []map[string]any
	callCounter := 0 // counter for generating unique fallback call IDs

	for _, content := range contents {
		for _, part := range content.Parts {
			switch {
			case part.FunctionCall != nil:
				// Model made a function call
				argsJSON, _ := json.Marshal(part.FunctionCall.Args)
				callID := part.FunctionCall.ID
				if callID == "" {
					callID = fmt.Sprintf("call_%s_%d", part.FunctionCall.Name, callCounter)
					callCounter++
				}
				input = append(input, map[string]any{
					"type":      "function_call",
					"name":      part.FunctionCall.Name,
					"arguments": string(argsJSON),
					"call_id":   callID,
				})

			case part.FunctionResponse != nil:
				// User returning function result
				outputJSON, _ := json.Marshal(part.FunctionResponse.Response)
				callID := part.FunctionResponse.ID
				if callID == "" {
					callID = fmt.Sprintf("call_%s_%d", part.FunctionResponse.Name, callCounter)
					callCounter++
				}
				input = append(input, map[string]any{
					"type":    "function_call_output",
					"call_id": callID,
					"output":  string(outputJSON),
				})

			case part.Text != "":
				role := "user"
				if content.Role == genai.RoleModel {
					role = "assistant"
				}
				input = append(input, map[string]any{
					"role":    role,
					"content": part.Text,
				})
			}
		}
	}

	return input
}

// convertToolsToOpenAI converts genai tools to OpenAI Responses API tool format.
func (c *OpenAIOAuthClient) convertToolsToOpenAI(tools []*genai.Tool) []map[string]any {
	var result []map[string]any

	for _, tool := range tools {
		if tool.FunctionDeclarations == nil {
			continue
		}
		for _, fd := range tool.FunctionDeclarations {
			toolDef := map[string]any{
				"type": "function",
				"name": fd.Name,
			}
			if fd.Description != "" {
				toolDef["description"] = fd.Description
			}
			if fd.Parameters != nil {
				toolDef["parameters"] = convertSchemaToOpenAI(fd.Parameters)
			}
			result = append(result, toolDef)
		}
	}

	return result
}

// convertSchemaToOpenAI converts a genai.Schema to OpenAI-compatible JSON Schema.
// The main difference is that genai uses uppercase types ("STRING", "OBJECT")
// while OpenAI expects lowercase ("string", "object").
func convertSchemaToOpenAI(s *genai.Schema) map[string]any {
	if s == nil {
		return nil
	}

	result := map[string]any{}

	// Convert type to lowercase (genai: "STRING" -> OpenAI: "string")
	if s.Type != "" {
		result["type"] = strings.ToLower(string(s.Type))
	}

	if s.Description != "" {
		result["description"] = s.Description
	}

	if len(s.Enum) > 0 {
		result["enum"] = s.Enum
	}

	if s.Format != "" {
		result["format"] = s.Format
	}

	// Array items
	if s.Items != nil {
		result["items"] = convertSchemaToOpenAI(s.Items)
	}

	// Object properties
	if len(s.Properties) > 0 {
		props := make(map[string]any, len(s.Properties))
		for name, prop := range s.Properties {
			props[name] = convertSchemaToOpenAI(prop)
		}
		result["properties"] = props
	}

	if len(s.Required) > 0 {
		result["required"] = s.Required
	}

	if s.Nullable != nil && *s.Nullable {
		result["nullable"] = true
	}

	if s.Default != nil {
		result["default"] = s.Default
	}

	// AnyOf
	if len(s.AnyOf) > 0 {
		anyOf := make([]map[string]any, 0, len(s.AnyOf))
		for _, sub := range s.AnyOf {
			anyOf = append(anyOf, convertSchemaToOpenAI(sub))
		}
		result["anyOf"] = anyOf
	}

	return result
}

// pendingFunctionCall tracks an in-progress function call during SSE streaming.
type pendingFunctionCall struct {
	name   string
	callID string
	args   strings.Builder
}

// processSSEStream processes the SSE stream from the Codex Responses API.
func (c *OpenAIOAuthClient) processSSEStream(ctx context.Context, body io.ReadCloser, chunks chan<- ResponseChunk, doneCh chan<- struct{}, estimatedTokens int64, rl *ratelimit.Limiter) {
	defer close(chunks)
	defer close(doneCh)
	defer body.Close()

	// Ensure rate limiter tokens are returned on any exit path.
	tokenReturned := false
	defer func() {
		if !tokenReturned && rl != nil && estimatedTokens > 0 {
			rl.ReturnTokens(1, estimatedTokens)
		}
	}()

	hasError := false

	// Scanner goroutine reads lines; ctx cancellation closes body to unblock.
	lineCh := make(chan string, 100)
	go func() {
		defer close(lineCh)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			select {
			case lineCh <- line:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Track pending function calls by call_id
	pendingCalls := make(map[string]*pendingFunctionCall)

	idleTimeout := c.streamIdleTimeout
	if idleTimeout == 0 {
		idleTimeout = 60 * time.Second
	}
	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			if !hasError {
				chunks <- ResponseChunk{Error: ContextErr(ctx), Done: true}
			}
			return

		case <-idleTimer.C:
			if !hasError {
				chunks <- ResponseChunk{Error: fmt.Errorf("OpenAI SSE stream idle timeout (%v)", idleTimeout), Done: true}
				hasError = true
			}
			return

		case line, ok := <-lineCh:
			if !ok {
				// Scanner done — emit final chunk
				c.flushPendingCalls(pendingCalls, chunks)
				if !hasError {
					chunks <- ResponseChunk{Done: true, FinishReason: genai.FinishReasonStop}
				}
				return
			}

			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idleTimeout)

			// Parse SSE lines
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				c.flushPendingCalls(pendingCalls, chunks)
				if !hasError {
					chunks <- ResponseChunk{Done: true, FinishReason: genai.FinishReasonStop}
				}
				return
			}

			var event map[string]any
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				logging.Debug("openai SSE: failed to parse event JSON", "error", err)
				continue
			}

			c.handleSSEEvent(event, pendingCalls, chunks, estimatedTokens, rl, &hasError, &tokenReturned)
		}
	}
}

// handleSSEEvent processes a single parsed SSE event from the Responses API.
func (c *OpenAIOAuthClient) handleSSEEvent(event map[string]any, pendingCalls map[string]*pendingFunctionCall, chunks chan<- ResponseChunk, estimatedTokens int64, rl *ratelimit.Limiter, hasError *bool, tokenReturned *bool) {
	eventType, _ := event["type"].(string)

	// After response.completed or response.failed, ignore further content events.
	if *hasError {
		return
	}

	switch eventType {
	case "response.output_text.delta":
		// Text content delta
		if delta, ok := event["delta"].(string); ok && delta != "" {
			chunks <- ResponseChunk{Text: delta}
		}

	case "response.function_call_arguments.delta":
		// Function call argument delta — accumulate
		callID, _ := event["call_id"].(string)
		if callID == "" {
			callID, _ = event["item_id"].(string)
		}
		delta, _ := event["delta"].(string)
		if callID != "" && delta != "" {
			pc, exists := pendingCalls[callID]
			if !exists {
				name, _ := event["name"].(string)
				pc = &pendingFunctionCall{name: name, callID: callID}
				pendingCalls[callID] = pc
			}
			pc.args.WriteString(delta)
		}

	case "response.output_item.added":
		// A new output item is added — could be a function call
		if item, ok := event["item"].(map[string]any); ok {
			itemType, _ := item["type"].(string)
			if itemType == "function_call" {
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				if callID != "" {
					pendingCalls[callID] = &pendingFunctionCall{name: name, callID: callID}
				}
			}
		}

	case "response.function_call_arguments.done":
		// Function call complete
		callID, _ := event["call_id"].(string)
		if callID == "" {
			callID, _ = event["item_id"].(string)
		}
		if pc, exists := pendingCalls[callID]; exists {
			c.emitFunctionCall(pc, chunks)
			delete(pendingCalls, callID)
		}

	case "response.output_text.done":
		// Text output complete — nothing special needed

	case "response.completed":
		// Response completed — extract usage and signal done
		chunk := ResponseChunk{Done: true, FinishReason: genai.FinishReasonStop}
		if resp, ok := event["response"].(map[string]any); ok {
			if usage, ok := resp["usage"].(map[string]any); ok {
				if inputTokens, ok := usage["input_tokens"].(float64); ok {
					chunk.InputTokens = int(inputTokens)
				}
				if outputTokens, ok := usage["output_tokens"].(float64); ok {
					chunk.OutputTokens = int(outputTokens)
				}
			}
		}
		// Return tokens to rate limiter
		if rl != nil && estimatedTokens > 0 {
			actualTokens := int64(chunk.InputTokens + chunk.OutputTokens)
			if actualTokens < estimatedTokens {
				rl.ReturnTokens(0, estimatedTokens-actualTokens)
			}
			*tokenReturned = true
		}
		chunks <- chunk
		*hasError = true // Prevent duplicate Done from [DONE] or scanner close

	case "response.failed":
		// Response failed
		errMsg := "OpenAI response failed"
		if resp, ok := event["response"].(map[string]any); ok {
			if status, ok := resp["status_details"].(map[string]any); ok {
				if reason, ok := status["error"].(map[string]any); ok {
					if msg, ok := reason["message"].(string); ok {
						errMsg = msg
					}
				}
			}
		}
		if !*hasError {
			chunks <- ResponseChunk{Error: fmt.Errorf("%s", errMsg), Done: true}
			*hasError = true
		}

	case "error":
		// Top-level error event
		errMsg := "OpenAI stream error"
		if msg, ok := event["message"].(string); ok {
			errMsg = msg
		}
		if !*hasError {
			chunks <- ResponseChunk{Error: fmt.Errorf("%s", errMsg), Done: true}
			*hasError = true
		}
	}
}

// emitFunctionCall converts a pending function call to a ResponseChunk.
func (c *OpenAIOAuthClient) emitFunctionCall(pc *pendingFunctionCall, chunks chan<- ResponseChunk) {
	argsStr := pc.args.String()
	var args map[string]any
	if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
		logging.Debug("OpenAI function call args parse failed, using raw fallback", "name", pc.name, "error", err)
		args = map[string]any{"raw": argsStr}
	}

	fc := &genai.FunctionCall{
		Name: pc.name,
		Args: args,
		ID:   pc.callID,
	}

	chunks <- ResponseChunk{
		FunctionCalls: []*genai.FunctionCall{fc},
		Parts:         []*genai.Part{{FunctionCall: fc}},
	}
}

// flushPendingCalls emits any remaining pending function calls.
func (c *OpenAIOAuthClient) flushPendingCalls(pendingCalls map[string]*pendingFunctionCall, chunks chan<- ResponseChunk) {
	for id, pc := range pendingCalls {
		c.emitFunctionCall(pc, chunks)
		delete(pendingCalls, id)
	}
}
