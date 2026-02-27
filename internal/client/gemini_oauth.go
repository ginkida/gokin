package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"gokin/internal/auth"
	"gokin/internal/config"
	"gokin/internal/logging"
	"gokin/internal/ratelimit"
	"gokin/internal/security"

	"google.golang.org/genai"
)

// GeminiOAuthClient implements Client interface using OAuth tokens
// and the Code Assist API (cloudcode-pa.googleapis.com)
type GeminiOAuthClient struct {
	httpClient   *http.Client
	accessToken  string
	refreshToken string
	expiresAt    time.Time
	email        string
	projectID    string
	config       *config.Config
	model        string
	tools        []*genai.Tool
	rateLimiter  *ratelimit.Limiter
	maxRetries   int
	retryDelay   time.Duration
	genConfig    *genai.GenerateContentConfig

	httpTimeout       time.Duration
	streamIdleTimeout time.Duration

	statusCallback    StatusCallback
	systemInstruction string
	thinkingBudget    int32
	reasoningEffort   string

	refreshGroup *singleflight.Group // serializes concurrent token refreshes

	mu sync.RWMutex
}

// NewGeminiOAuthClient creates a new client using OAuth tokens
func NewGeminiOAuthClient(ctx context.Context, cfg *config.Config) (*GeminiOAuthClient, error) {
	oauth := cfg.API.GeminiOAuth
	if oauth == nil {
		return nil, fmt.Errorf("no OAuth token configured")
	}

	// Request retries are orchestrated at App layer to avoid retry multiplication.
	maxRetries := 0
	retryDelay := cfg.API.Retry.RetryDelay
	if retryDelay == 0 {
		retryDelay = 1 * time.Second
	}

	httpTimeout := cfg.API.Retry.HTTPTimeout
	if httpTimeout == 0 {
		httpTimeout = 120 * time.Second
	}

	streamIdleTimeout := cfg.API.Retry.StreamIdleTimeout
	if streamIdleTimeout == 0 {
		streamIdleTimeout = 30 * time.Second
	}

	httpClient, err := security.CreateSecureHTTPClient(security.DefaultTLSConfig(), httpTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	client := &GeminiOAuthClient{
		httpClient:   httpClient,
		accessToken:  oauth.AccessToken,
		refreshToken: oauth.RefreshToken,
		expiresAt:    time.Unix(oauth.ExpiresAt, 0),
		email:        oauth.Email,
		projectID:    oauth.ProjectID,
		config:       cfg,
		model:        cfg.Model.Name,
		maxRetries:   maxRetries,
		retryDelay:   retryDelay,
		genConfig: &genai.GenerateContentConfig{
			Temperature:     Ptr(cfg.Model.Temperature),
			MaxOutputTokens: cfg.Model.MaxOutputTokens,
		},
		httpTimeout:       httpTimeout,
		streamIdleTimeout: streamIdleTimeout,
		reasoningEffort:   cfg.Model.ReasoningEffort,
		refreshGroup:      &singleflight.Group{},
	}

	// Ensure token is valid
	if err := client.ensureValidToken(ctx); err != nil {
		return nil, fmt.Errorf("failed to validate OAuth token: %w", err)
	}

	// Load project ID: check env vars first, then API
	if client.projectID == "" {
		for _, env := range []string{"GOKIN_GEMINI_PROJECT_ID", "GOOGLE_CLOUD_PROJECT"} {
			if v := os.Getenv(env); v != "" {
				client.projectID = v
				logging.Debug("using project ID from env", "env", env, "projectID", v)
				break
			}
		}
	}
	if client.projectID == "" {
		if err := client.loadProjectID(ctx); err != nil {
			return nil, fmt.Errorf("failed to load project ID: %w", err)
		}
	}

	logging.Debug("created Gemini OAuth client",
		"email", client.email,
		"model", client.model,
		"projectID", client.projectID,
		"expires", client.expiresAt.Format(time.RFC3339))

	return client, nil
}

// loadCodeAssistResponse represents the full response from /v1internal:loadCodeAssist
type loadCodeAssistResponse struct {
	CloudaicompanionProject interface{} `json:"cloudaicompanionProject"` // string or {"id": string}
	CurrentTier             *struct {
		ID string `json:"id"`
	} `json:"currentTier"`
	AllowedTiers []struct {
		ID        string `json:"id"`
		IsDefault bool   `json:"isDefault"`
	} `json:"allowedTiers"`
}

// extractProjectID extracts the project ID from the cloudaicompanionProject field,
// which may be a plain string or an object with an "id" field.
func (r *loadCodeAssistResponse) extractProjectID() string {
	if r.CloudaicompanionProject == nil {
		return ""
	}
	// Try string first
	if s, ok := r.CloudaicompanionProject.(string); ok {
		return s
	}
	// Try object with "id" field
	if m, ok := r.CloudaicompanionProject.(map[string]interface{}); ok {
		if id, ok := m["id"].(string); ok {
			return id
		}
	}
	return ""
}

// selectTierID picks the best tier from the response: isDefault=true, then first, then fallback.
func (r *loadCodeAssistResponse) selectTierID() string {
	for _, t := range r.AllowedTiers {
		if t.IsDefault {
			return t.ID
		}
	}
	if len(r.AllowedTiers) > 0 {
		return r.AllowedTiers[0].ID
	}
	return "free-tier"
}

// loadProjectID loads the project ID from Code Assist API.
// If the API returns no project (new free-tier user), it triggers onboardProject().
func (c *GeminiOAuthClient) loadProjectID(ctx context.Context) error {
	c.mu.RLock()
	token := c.accessToken
	c.mu.RUnlock()

	apiURL := auth.GeminiCodeAssistAPI + "/v1internal:loadCodeAssist"

	// Build request body with metadata (matching reference implementation)
	reqBody := map[string]interface{}{
		"metadata": map[string]string{
			"ideType":    "IDE_UNSPECIFIED",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal loadCodeAssist request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(reqJSON))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range auth.CodeAssistHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to load code assist: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("loadCodeAssist failed (%d): %s", resp.StatusCode, string(body))
	}

	var result loadCodeAssistResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to parse loadCodeAssist response: %w", err)
	}

	projectID := result.extractProjectID()
	if projectID == "" {
		// New user without a project — trigger provisioning via onboardUser
		tierID := result.selectTierID()
		logging.Debug("no project ID from loadCodeAssist, onboarding", "tierID", tierID)
		if err := c.onboardProject(ctx, tierID); err != nil {
			return fmt.Errorf("project onboarding failed: %w", err)
		}
		return nil
	}

	c.mu.Lock()
	c.projectID = projectID
	c.mu.Unlock()

	// Save to config
	c.config.API.GeminiOAuth.ProjectID = projectID
	if err := c.config.Save(); err != nil {
		logging.Warn("failed to save project ID to config", "error", err)
	}

	logging.Debug("loaded Code Assist project ID", "projectID", projectID)
	return nil
}

// onboardProject provisions a new Code Assist project via /v1internal:onboardUser.
// It polls the returned long-running operation until completion.
func (c *GeminiOAuthClient) onboardProject(ctx context.Context, tierID string) error {
	c.mu.RLock()
	token := c.accessToken
	c.mu.RUnlock()

	apiURL := auth.GeminiCodeAssistAPI + "/v1internal:onboardUser"

	reqBody := map[string]interface{}{
		"tierId": tierID,
		"metadata": map[string]string{
			"ideType":    "IDE_UNSPECIFIED",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal onboardUser request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(reqJSON))
	if err != nil {
		return fmt.Errorf("failed to create onboard request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range auth.CodeAssistHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("onboardUser request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("onboardUser failed (%d): %s", resp.StatusCode, string(body))
	}

	var operation struct {
		Name     string `json:"name"`
		Done     bool   `json:"done"`
		Response *struct {
			CloudaicompanionProject interface{} `json:"cloudaicompanionProject"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&operation); err != nil {
		return fmt.Errorf("failed to parse onboardUser response: %w", err)
	}

	// If the operation completed immediately, extract the project ID
	if operation.Done {
		return c.extractOnboardResult(&operation)
	}

	// Poll the operation until completion
	const maxPolls = 10
	const pollInterval = 5 * time.Second

	logging.Info("provisioning Code Assist project, this may take a moment...")

	for i := 0; i < maxPolls; i++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("onboarding cancelled: %w", ctx.Err())
		case <-time.After(pollInterval):
		}

		// Refresh token if needed — polling can take up to 50s
		if err := c.ensureValidToken(ctx); err != nil {
			return fmt.Errorf("token expired during onboarding: %w", err)
		}

		pollURL := auth.GeminiCodeAssistAPI + "/v1internal/" + operation.Name

		c.mu.RLock()
		token = c.accessToken
		c.mu.RUnlock()

		pollReq, err := http.NewRequestWithContext(ctx, "GET", pollURL, nil)
		if err != nil {
			return fmt.Errorf("failed to create poll request: %w", err)
		}

		pollReq.Header.Set("Authorization", "Bearer "+token)
		for k, v := range auth.CodeAssistHeaders {
			pollReq.Header.Set(k, v)
		}

		pollResp, err := c.httpClient.Do(pollReq)
		if err != nil {
			logging.Warn("onboard poll failed, retrying", "attempt", i+1, "error", err)
			continue
		}

		if pollResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(pollResp.Body)
			pollResp.Body.Close()
			logging.Warn("onboard poll non-200", "status", pollResp.StatusCode, "body", string(body))
			continue
		}

		if err := json.NewDecoder(pollResp.Body).Decode(&operation); err != nil {
			pollResp.Body.Close()
			logging.Warn("onboard poll parse failed", "attempt", i+1, "error", err)
			continue
		}
		pollResp.Body.Close()

		if operation.Done {
			return c.extractOnboardResult(&operation)
		}

		logging.Debug("onboarding in progress", "attempt", i+1, "operation", operation.Name)
	}

	return fmt.Errorf("onboarding timed out after %d polls", maxPolls)
}

// extractOnboardResult extracts the project ID from a completed onboard operation
// and saves it to the client and config.
func (c *GeminiOAuthClient) extractOnboardResult(operation *struct {
	Name     string `json:"name"`
	Done     bool   `json:"done"`
	Response *struct {
		CloudaicompanionProject interface{} `json:"cloudaicompanionProject"`
	} `json:"response"`
}) error {
	if operation.Response == nil {
		return fmt.Errorf("onboard operation completed but response is nil")
	}

	// Reuse the same extraction logic
	r := &loadCodeAssistResponse{
		CloudaicompanionProject: operation.Response.CloudaicompanionProject,
	}
	projectID := r.extractProjectID()
	if projectID == "" {
		return fmt.Errorf("onboard operation completed but no project ID in response")
	}

	c.mu.Lock()
	c.projectID = projectID
	c.mu.Unlock()

	// Save to config
	c.config.API.GeminiOAuth.ProjectID = projectID
	if err := c.config.Save(); err != nil {
		logging.Warn("failed to save onboarded project ID to config", "error", err)
	}

	logging.Debug("onboarded Code Assist project", "projectID", projectID)
	return nil
}

// SetSystemInstruction sets the system-level instruction for the model.
func (c *GeminiOAuthClient) SetSystemInstruction(instruction string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.systemInstruction = instruction
}

// SetThinkingBudget configures the thinking/reasoning budget.
func (c *GeminiOAuthClient) SetThinkingBudget(budget int32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.thinkingBudget = budget
}

// SetTools sets the tools available for function calling
func (c *GeminiOAuthClient) SetTools(tools []*genai.Tool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tools = tools
}

// SetRateLimiter sets the rate limiter for API calls
func (c *GeminiOAuthClient) SetRateLimiter(limiter interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rl, ok := limiter.(*ratelimit.Limiter); ok {
		c.rateLimiter = rl
	}
}

// SetStatusCallback sets the callback for status updates
func (c *GeminiOAuthClient) SetStatusCallback(cb StatusCallback) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statusCallback = cb
}

// SendMessage sends a user message and returns a streaming response
func (c *GeminiOAuthClient) SendMessage(ctx context.Context, message string) (*StreamingResponse, error) {
	return c.SendMessageWithHistory(ctx, nil, message)
}

// SendMessageWithHistory sends a message with conversation history
func (c *GeminiOAuthClient) SendMessageWithHistory(ctx context.Context, history []*genai.Content, message string) (*StreamingResponse, error) {
	contents := make([]*genai.Content, len(history)+1)
	copy(contents, history)
	contents[len(contents)-1] = genai.NewContentFromText(message, genai.RoleUser)

	return c.generateContentStream(ctx, contents)
}

// SendFunctionResponse sends function call results back to the model
func (c *GeminiOAuthClient) SendFunctionResponse(ctx context.Context, history []*genai.Content, results []*genai.FunctionResponse) (*StreamingResponse, error) {
	var parts []*genai.Part
	for _, result := range results {
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

	return c.generateContentStream(ctx, contents)
}

// CountTokens counts tokens for the given contents
func (c *GeminiOAuthClient) CountTokens(ctx context.Context, contents []*genai.Content) (*genai.CountTokensResponse, error) {
	// The Code Assist API doesn't have a separate count tokens endpoint
	// Return an estimate based on content length
	totalChars := 0
	for _, content := range contents {
		for _, part := range content.Parts {
			if part.Text != "" {
				totalChars += len(part.Text)
			}
		}
	}

	// Rough estimate: ~4 chars per token
	estimatedTokens := int32(totalChars / 4)
	return &genai.CountTokensResponse{
		TotalTokens: estimatedTokens,
	}, nil
}

// GetModel returns the model name
func (c *GeminiOAuthClient) GetModel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.model
}

// SetModel changes the model for this client
func (c *GeminiOAuthClient) SetModel(modelName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = modelName
}

// WithModel returns a new client configured for the specified model
func (c *GeminiOAuthClient) WithModel(modelName string) Client {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return &GeminiOAuthClient{
		httpClient:        c.httpClient,
		accessToken:       c.accessToken,
		refreshToken:      c.refreshToken,
		expiresAt:         c.expiresAt,
		email:             c.email,
		projectID:         c.projectID,
		config:            c.config,
		model:             modelName,
		tools:             c.tools,
		rateLimiter:       c.rateLimiter,
		maxRetries:        c.maxRetries,
		retryDelay:        c.retryDelay,
		genConfig:         c.genConfig,
		httpTimeout:       c.httpTimeout,
		streamIdleTimeout: c.streamIdleTimeout,
		statusCallback:    c.statusCallback,
		systemInstruction: c.systemInstruction,
		thinkingBudget:    c.thinkingBudget,
		reasoningEffort:   c.reasoningEffort,
		refreshGroup:      c.refreshGroup, // share to serialize refreshes across clones
	}
}

// GetRawClient returns nil since we use HTTP directly
func (c *GeminiOAuthClient) GetRawClient() interface{} {
	return nil
}

// Close closes the client connection and releases transport resources.
func (c *GeminiOAuthClient) Close() error {
	if c.httpClient != nil {
		if transport, ok := c.httpClient.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
	return nil
}

// ensureValidToken checks if the token is valid and refreshes if needed.
// Uses singleflight to serialize concurrent refresh attempts.
func (c *GeminiOAuthClient) ensureValidToken(ctx context.Context) error {
	c.mu.RLock()

	if c.refreshToken == "" {
		c.mu.RUnlock()
		return fmt.Errorf("missing OAuth refresh token; run /oauth-login again")
	}

	// Check if token is still valid (with 5 minute buffer).
	// We also require a non-empty access token; otherwise force refresh.
	if c.accessToken != "" && !c.expiresAt.IsZero() && time.Now().Before(c.expiresAt.Add(-auth.TokenRefreshBuffer)) {
		c.mu.RUnlock()
		return nil
	}
	c.mu.RUnlock()

	// Use singleflight to ensure only one refresh happens at a time
	_, err, _ := c.refreshGroup.Do("refresh", func() (interface{}, error) {
		// Re-check under lock — another goroutine may have already refreshed
		c.mu.RLock()
		if c.accessToken != "" && !c.expiresAt.IsZero() && time.Now().Before(c.expiresAt.Add(-auth.TokenRefreshBuffer)) {
			c.mu.RUnlock()
			return nil, nil
		}
		refreshTok := c.refreshToken
		c.mu.RUnlock()

		logging.Debug("OAuth token expired, refreshing",
			"expiresAt", c.expiresAt.Format(time.RFC3339))

		// Refresh the token (network I/O — outside lock)
		manager := auth.NewGeminiOAuthManager()
		newToken, err := manager.RefreshToken(ctx, refreshTok)
		if err != nil {
			return nil, fmt.Errorf("failed to refresh OAuth token: %w (try /oauth-login)", err)
		}

		// Update client state under lock
		c.mu.Lock()
		c.accessToken = newToken.AccessToken
		c.expiresAt = newToken.ExpiresAt
		if newToken.RefreshToken != "" {
			c.refreshToken = newToken.RefreshToken
		}
		c.mu.Unlock()

		// Persist to config (disk I/O — outside lock).
		// Snapshot OAuth pointer to avoid race with concurrent logout.
		if oauth := c.config.API.GeminiOAuth; oauth != nil {
			oauth.AccessToken = newToken.AccessToken
			oauth.ExpiresAt = newToken.ExpiresAt.Unix()
			if newToken.RefreshToken != "" {
				oauth.RefreshToken = newToken.RefreshToken
			}
			if err := c.config.Save(); err != nil {
				logging.Warn("failed to save refreshed OAuth token", "error", err)
			}
		}

		logging.Debug("OAuth token refreshed",
			"expiresAt", newToken.ExpiresAt.Format(time.RFC3339))

		return nil, nil
	})

	return err
}

// generateContentStream handles the streaming content generation
func (c *GeminiOAuthClient) generateContentStream(ctx context.Context, contents []*genai.Content) (*StreamingResponse, error) {
	contents = sanitizeContents(contents)

	var lastErr error
	var lastStatusCode int
	maxDelay := 30 * time.Second

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			// Calculate backoff with jitter
			delay := CalculateBackoff(c.retryDelay, attempt-1, maxDelay)

			// Respect Retry-After header from server (typically on 429)
			var httpErr *HTTPError
			if errors.As(lastErr, &httpErr) {
				lastStatusCode = httpErr.StatusCode
				if httpErr.RetryAfter > 0 && httpErr.RetryAfter > delay {
					delay = httpErr.RetryAfter
				}
			}

			logging.Info("retrying OAuth Gemini request",
				"attempt", attempt, "delay", delay, "status", lastStatusCode)

			if c.statusCallback != nil {
				reason := "API error"
				if lastStatusCode == 429 {
					reason = "rate limit"
				} else if lastErr != nil {
					reason = lastErr.Error()
					if strings.Contains(reason, "connection") {
						reason = "connection error"
					} else if strings.Contains(reason, "timeout") || strings.Contains(reason, "deadline exceeded") {
						reason = "timeout"
					} else if len(reason) > 50 {
						reason = reason[:47] + "..."
					}
				}
				c.statusCallback.OnRetry(attempt, c.maxRetries, delay, reason)
			}

			backoffTimer := time.NewTimer(delay)
			select {
			case <-backoffTimer.C:
			case <-ctx.Done():
				backoffTimer.Stop()
				return nil, ContextErr(ctx)
			}
		}

		// Per-attempt timeout is handled at the Transport level:
		// - DialContext.Timeout (30s) for TCP connect
		// - TLSHandshakeTimeout (10s) for TLS
		// - ResponseHeaderTimeout (httpTimeout) for waiting for first header
		// Each http.Client.Do() gets fresh transport timeouts automatically.
		// The parent ctx governs the overall lifetime including SSE streaming.
		response, err := c.doGenerateContentStream(ctx, contents)
		if err == nil {
			return response, nil
		}

		// Don't retry if parent context is cancelled
		if ctx.Err() != nil {
			return nil, err
		}

		lastErr = err
		if !c.isRetryableError(err) {
			return nil, err
		}

		// Close idle connections on EOF or timeout to force a fresh TCP
		// connection on retry — stale/broken connections shouldn't be reused.
		if isEOFError(err) || errors.Is(err, context.DeadlineExceeded) {
			c.httpClient.CloseIdleConnections()
		}

		logging.Warn("OAuth Gemini request failed, will retry", "attempt", attempt, "error", err)
	}

	return nil, fmt.Errorf("max retries (%d) exceeded: %w", c.maxRetries, lastErr)
}

// doGenerateContentStream performs a single streaming request.
// Per-attempt timeouts are enforced at the Transport level (DialContext,
// TLSHandshakeTimeout, ResponseHeaderTimeout). The ctx governs the overall
// lifetime of the request and SSE stream.
func (c *GeminiOAuthClient) doGenerateContentStream(ctx context.Context, contents []*genai.Content) (*StreamingResponse, error) {
	if err := c.ensureValidToken(ctx); err != nil {
		return nil, err
	}

	// Rate limiting
	var estimatedTokens int64
	var rateLimiterAcquired bool
	if c.rateLimiter != nil {
		estimatedTokens = ratelimit.EstimateTokensFromContents(len(contents), 500)
		if err := c.rateLimiter.AcquireWithContext(ctx, estimatedTokens); err != nil {
			if c.statusCallback != nil {
				c.statusCallback.OnRateLimit(5 * time.Second)
			}
			return nil, fmt.Errorf("rate limit: %w", err)
		}
		rateLimiterAcquired = true
	}

	// Helper to return tokens on error paths before HTTP request
	returnTokens := func() {
		if rateLimiterAcquired && c.rateLimiter != nil && estimatedTokens > 0 {
			c.rateLimiter.ReturnTokens(1, estimatedTokens)
		}
	}

	// Build request body in Code Assist format
	reqBody := c.buildRequest(contents)

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		returnTokens()
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Use v1internal endpoint with streaming
	url := auth.GeminiCodeAssistAPI + "/v1internal:streamGenerateContent?alt=sse"

	c.mu.RLock()
	token := c.accessToken
	projectID := c.projectID
	c.mu.RUnlock()

	if projectID == "" {
		returnTokens()
		return nil, fmt.Errorf("no project ID available")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqJSON))
	if err != nil {
		returnTokens()
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range auth.CodeAssistHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if c.rateLimiter != nil && estimatedTokens > 0 {
			c.rateLimiter.ReturnTokens(1, estimatedTokens)
		}
		err = WrapProviderHTTPTimeout(err, "gemini", c.httpTimeout)
		// Log detailed error info for debugging connection issues
		var netErr net.Error
		if errors.As(err, &netErr) {
			logging.Warn("OAuth HTTP request failed",
				"timeout", netErr.Timeout(),
				"error", err)
		} else {
			logging.Warn("OAuth HTTP request failed", "error", err)
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		retryAfter := ParseRetryAfter(resp)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if c.rateLimiter != nil && estimatedTokens > 0 {
			c.rateLimiter.ReturnTokens(1, estimatedTokens)
		}
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			RetryAfter: retryAfter,
			Message:    fmt.Sprintf("API error (%d): %s", resp.StatusCode, string(body)),
		}
	}

	// Create streaming response
	chunks := make(chan ResponseChunk, 10)
	done := make(chan struct{})

	go c.processSSEStream(ctx, resp.Body, chunks, done, estimatedTokens)

	return &StreamingResponse{
		Chunks: chunks,
		Done:   done,
	}, nil
}

// buildRequest builds the API request body in Code Assist format
func (c *GeminiOAuthClient) buildRequest(contents []*genai.Content) map[string]interface{} {
	// Convert contents to API format
	apiContents := make([]map[string]interface{}, 0, len(contents))
	for _, content := range contents {
		parts := make([]map[string]interface{}, 0, len(content.Parts))
		for _, part := range content.Parts {
			if part.Text != "" {
				parts = append(parts, map[string]interface{}{"text": part.Text})
			}
			if part.FunctionCall != nil {
				parts = append(parts, map[string]interface{}{
					"functionCall": map[string]interface{}{
						"name": part.FunctionCall.Name,
						"args": part.FunctionCall.Args,
					},
					"thoughtSignature": "skip_thought_signature_validator",
				})
			}
			if part.FunctionResponse != nil {
				parts = append(parts, map[string]interface{}{
					"functionResponse": map[string]interface{}{
						"name":     part.FunctionResponse.Name,
						"response": part.FunctionResponse.Response,
					},
				})
			}
			if part.InlineData != nil {
				parts = append(parts, map[string]interface{}{
					"inlineData": map[string]interface{}{
						"mimeType": part.InlineData.MIMEType,
						"data":     base64.StdEncoding.EncodeToString(part.InlineData.Data),
					},
				})
			}
		}
		apiContents = append(apiContents, map[string]interface{}{
			"role":  string(content.Role),
			"parts": parts,
		})
	}

	// Inner request payload
	requestPayload := map[string]interface{}{
		"contents": apiContents,
	}

	// Snapshot all mutable fields under lock to avoid data races.
	c.mu.RLock()
	sysInstruction := c.systemInstruction
	thinkingBudget := c.thinkingBudget
	cfgEffort := c.reasoningEffort
	model := c.model
	projectID := c.projectID
	genCfg := c.genConfig
	toolsSnap := c.tools
	c.mu.RUnlock()
	if sysInstruction != "" {
		requestPayload["systemInstruction"] = map[string]interface{}{
			"parts": []map[string]interface{}{
				{"text": sysInstruction},
			},
		}
	}

	// Add generation config
	if genCfg != nil {
		genConfig := map[string]interface{}{}
		if genCfg.Temperature != nil {
			genConfig["temperature"] = *genCfg.Temperature
		}
		if genCfg.MaxOutputTokens > 0 {
			genConfig["maxOutputTokens"] = genCfg.MaxOutputTokens
		}
		tc := geminiThinkingConfig(model, cfgEffort, thinkingBudget)
		if tc != nil {
			thinkingJSON := map[string]interface{}{}
			if tc.ThinkingLevel != "" {
				thinkingJSON["thinkingLevel"] = string(tc.ThinkingLevel)
			}
			if tc.ThinkingBudget != nil {
				thinkingJSON["thinkingBudget"] = *tc.ThinkingBudget
				if *tc.ThinkingBudget > 0 {
					thinkingJSON["includeThoughts"] = true
					// Ensure MaxOutputTokens accommodates thinking + response text
					minRequired := *tc.ThinkingBudget + 4096
					if maxTokens, ok := genConfig["maxOutputTokens"].(int32); ok && maxTokens > 0 && maxTokens < minRequired {
						genConfig["maxOutputTokens"] = minRequired
					}
				}
			}
			if tc.IncludeThoughts {
				thinkingJSON["includeThoughts"] = true
			}
			genConfig["thinkingConfig"] = thinkingJSON
		}
		if len(genConfig) > 0 {
			requestPayload["generationConfig"] = genConfig
		}
	}

	// Add tools
	if len(toolsSnap) > 0 {
		toolDefs := make([]map[string]interface{}, 0)
		for _, tool := range toolsSnap {
			if tool.FunctionDeclarations != nil {
				funcs := make([]map[string]interface{}, 0, len(tool.FunctionDeclarations))
				for _, fd := range tool.FunctionDeclarations {
					funcDef := map[string]interface{}{
						"name":        fd.Name,
						"description": fd.Description,
					}
					if fd.Parameters != nil {
						funcDef["parameters"] = fd.Parameters
					}
					funcs = append(funcs, funcDef)
				}
				toolDefs = append(toolDefs, map[string]interface{}{
					"functionDeclarations": funcs,
				})
			}
		}
		if len(toolDefs) > 0 {
			requestPayload["tools"] = toolDefs
		}
	}

	// Wrap in Code Assist format
	return map[string]interface{}{
		"project": projectID,
		"model":   model,
		"request": requestPayload,
	}
}

// processSSEStream processes Server-Sent Events from the API.
// Uses a scanner goroutine + ctx→body.Close() pattern to prevent hangs
// when the server stops sending data (matching anthropic.go approach).
func (c *GeminiOAuthClient) processSSEStream(ctx context.Context, body io.ReadCloser, chunks chan<- ResponseChunk, doneCh chan<- struct{}, estimatedTokens int64) {
	defer close(chunks)
	defer close(doneCh)
	defer body.Close()

	hasError := false

	// Snapshot status callback under lock
	c.mu.RLock()
	statusCb := c.statusCallback
	streamIdleTimeout := c.streamIdleTimeout
	c.mu.RUnlock()

	// Monitor context cancellation to force-close response body,
	// unblocking any blocked scanner.Scan() call.
	localDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = body.Close()
		case <-localDone:
		}
	}()
	defer close(localDone)

	scanner := bufio.NewScanner(body)
	// Large tool payloads can produce long SSE lines; default scanner limit is too small.
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024) // 8MB max line size

	// Channel for scanner results — single goroutine reads all lines.
	// It will exit when resp.Body is closed (by defer or ctx goroutine above).
	type scanResult struct {
		line string
		ok   bool
		err  error
	}
	scanCh := make(chan scanResult)
	stopScan := make(chan struct{})
	go func() {
		defer close(scanCh)
		for {
			ok := scanner.Scan()
			select {
			case scanCh <- scanResult{line: scanner.Text(), ok: ok, err: scanner.Err()}:
				if !ok {
					return
				}
			case <-ctx.Done():
				return
			case <-stopScan:
				return
			}
		}
	}()

	contentReceived := false
	idleTimer := time.NewTimer(streamIdleTimeout)
	defer idleTimer.Stop()

	// Warning timer for UI feedback (fires at half the idle timeout)
	streamIdleWarning := streamIdleTimeout / 2
	warningTimer := time.NewTimer(streamIdleWarning)
	defer warningTimer.Stop()
	lastWarningAt := time.Duration(0)

	var currentLine string

scanLoop:
	for {
	waitLoop:
		for {
			select {
			case <-ctx.Done():
				logging.Debug("context cancelled, stopping OAuth SSE stream")
				select {
				case chunks <- ResponseChunk{Error: ContextErr(ctx), Done: true}:
				default:
				}
				hasError = true
				break scanLoop

			case <-warningTimer.C:
				lastWarningAt += streamIdleWarning
				if statusCb != nil {
					statusCb.OnStreamIdle(lastWarningAt)
				}
				warningTimer.Reset(10 * time.Second)
				continue waitLoop

			case <-idleTimer.C:
				logging.Warn("OAuth SSE stream idle timeout exceeded",
					"timeout", streamIdleTimeout, "partial", contentReceived)
				chunks <- ResponseChunk{
					Error: &ErrStreamIdleTimeout{Timeout: streamIdleTimeout, Partial: contentReceived},
					Done:  true,
				}
				hasError = true
				break scanLoop

			case result, ok := <-scanCh:
				// Got data — notify resume if we had warned
				if lastWarningAt > 0 && statusCb != nil {
					statusCb.OnStreamResume()
				}
				lastWarningAt = 0

				// Reset timers
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(streamIdleTimeout)

				if !warningTimer.Stop() {
					select {
					case <-warningTimer.C:
					default:
					}
				}
				warningTimer.Reset(streamIdleWarning)

				if !ok {
					if result.err != nil {
						hasError = true
						chunks <- ResponseChunk{Error: result.err, Done: true}
					}
					break scanLoop
				}

				if result.err != nil {
					hasError = true
					chunks <- ResponseChunk{Error: result.err, Done: true}
					break scanLoop
				}

				currentLine = result.line
				break waitLoop
			}
		}

		// Skip empty lines and comments
		if currentLine == "" || strings.HasPrefix(currentLine, ":") {
			continue
		}

		// Parse SSE data
		if !strings.HasPrefix(currentLine, "data: ") {
			continue
		}

		data := strings.TrimPrefix(currentLine, "data: ")

		// Check for end of stream
		if data == "[DONE]" {
			break scanLoop
		}

		chunk, err := c.parseSSEData(data)
		if err != nil {
			logging.Debug("failed to parse SSE data", "error", err, "data", data)
			continue
		}

		if chunk.Text != "" || chunk.Thinking != "" || len(chunk.FunctionCalls) > 0 {
			contentReceived = true
		}

		select {
		case chunks <- chunk:
		case <-ctx.Done():
			hasError = true
			select {
			case chunks <- ResponseChunk{Error: ContextErr(ctx), Done: true}:
			default:
			}
			break scanLoop
		}

		if chunk.Done {
			break scanLoop
		}
	}
	close(stopScan) // Signal scanner goroutine to exit

	if hasError && c.rateLimiter != nil && estimatedTokens > 0 {
		c.rateLimiter.ReturnTokens(1, estimatedTokens)
	}
}

// parseSSEData parses a single SSE data chunk (Code Assist format)
func (c *GeminiOAuthClient) parseSSEData(data string) (ResponseChunk, error) {
	// Code Assist wraps response in { "response": { ... } }
	var wrapper struct {
		Response *struct {
			Candidates []struct {
				Content struct {
					Role  string `json:"role"`
					Parts []struct {
						Text         string `json:"text,omitempty"`
						Thought      bool   `json:"thought,omitempty"`
						FunctionCall *struct {
							Name string                 `json:"name"`
							Args map[string]interface{} `json:"args"`
						} `json:"functionCall,omitempty"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason,omitempty"`
			} `json:"candidates"`
			UsageMetadata *struct {
				PromptTokenCount     int `json:"promptTokenCount"`
				CandidatesTokenCount int `json:"candidatesTokenCount"`
			} `json:"usageMetadata,omitempty"`
		} `json:"response"`
	}

	if err := json.Unmarshal([]byte(data), &wrapper); err != nil {
		return ResponseChunk{}, err
	}

	chunk := ResponseChunk{}

	// Handle case where response is not wrapped
	resp := wrapper.Response
	if resp == nil {
		// Try parsing as direct response
		var direct struct {
			Candidates []struct {
				Content struct {
					Role  string `json:"role"`
					Parts []struct {
						Text         string `json:"text,omitempty"`
						Thought      bool   `json:"thought,omitempty"`
						FunctionCall *struct {
							Name string                 `json:"name"`
							Args map[string]interface{} `json:"args"`
						} `json:"functionCall,omitempty"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason,omitempty"`
			} `json:"candidates"`
			UsageMetadata *struct {
				PromptTokenCount     int `json:"promptTokenCount"`
				CandidatesTokenCount int `json:"candidatesTokenCount"`
			} `json:"usageMetadata,omitempty"`
		}
		if err := json.Unmarshal([]byte(data), &direct); err == nil && len(direct.Candidates) > 0 {
			// Process direct format
			if direct.UsageMetadata != nil {
				chunk.InputTokens = direct.UsageMetadata.PromptTokenCount
				chunk.OutputTokens = direct.UsageMetadata.CandidatesTokenCount
			}

			candidate := direct.Candidates[0]
			if candidate.FinishReason != "" {
				chunk.Done = true
				chunk.FinishReason = genai.FinishReason(candidate.FinishReason)
			}

			for _, part := range candidate.Content.Parts {
				if part.Thought {
					chunk.Thinking += part.Text
					continue
				}
				if part.Text != "" {
					chunk.Text += part.Text
					chunk.Parts = append(chunk.Parts, genai.NewPartFromText(part.Text))
				}
				if part.FunctionCall != nil {
					fc := &genai.FunctionCall{
						Name: part.FunctionCall.Name,
						Args: part.FunctionCall.Args,
					}
					chunk.FunctionCalls = append(chunk.FunctionCalls, fc)
					chunk.Parts = append(chunk.Parts, &genai.Part{FunctionCall: fc})
				}
			}
			return chunk, nil
		}

		chunk.Done = true
		return chunk, nil
	}

	if resp.UsageMetadata != nil {
		chunk.InputTokens = resp.UsageMetadata.PromptTokenCount
		chunk.OutputTokens = resp.UsageMetadata.CandidatesTokenCount
	}

	if len(resp.Candidates) == 0 {
		logging.Warn("gemini oauth: response has no candidates")
		chunk.Done = true
		return chunk, nil
	}

	candidate := resp.Candidates[0]
	if candidate.FinishReason != "" {
		chunk.Done = true
		chunk.FinishReason = genai.FinishReason(candidate.FinishReason)
	}

	for _, part := range candidate.Content.Parts {
		if part.Thought {
			chunk.Thinking += part.Text
			continue
		}
		if part.Text != "" {
			chunk.Text += part.Text
			chunk.Parts = append(chunk.Parts, genai.NewPartFromText(part.Text))
		}
		if part.FunctionCall != nil {
			fc := &genai.FunctionCall{
				Name: part.FunctionCall.Name,
				Args: part.FunctionCall.Args,
			}
			chunk.FunctionCalls = append(chunk.FunctionCalls, fc)
			chunk.Parts = append(chunk.Parts, &genai.Part{FunctionCall: fc})
		}
	}

	return chunk, nil
}

// isRetryableError returns true if the error should trigger a retry.
func (c *GeminiOAuthClient) isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Typed checks first — preferred over string matching
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if IsStreamIdleTimeout(err) {
		return true
	}

	errStr := err.Error()

	// Check for retryable HTTP status codes
	retryableCodes := []string{"429", "500", "502", "503", "504"}
	for _, code := range retryableCodes {
		if strings.Contains(errStr, code) {
			return true
		}
	}

	// String fallback for untyped errors
	networkPatterns := []string{
		"connection refused",
		"connection reset",
		"no such host",
		"timeout",
		"temporary failure",
		"unavailable",
		"resource_exhausted",
		"eof",
	}
	lowerErr := strings.ToLower(errStr)
	for _, pattern := range networkPatterns {
		if strings.Contains(lowerErr, pattern) {
			return true
		}
	}

	return false
}

// GetEmail returns the authenticated user's email
func (c *GeminiOAuthClient) GetEmail() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.email
}

// GetExpiresAt returns when the token expires
func (c *GeminiOAuthClient) GetExpiresAt() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.expiresAt
}

// GetProjectID returns the Code Assist project ID
func (c *GeminiOAuthClient) GetProjectID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.projectID
}
