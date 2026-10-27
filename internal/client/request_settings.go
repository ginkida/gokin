package client

import (
	"context"
	"fmt"
	"sync"

	"gokin/internal/toolschema"

	"google.golang.org/genai"
)

// RequestSettings is ephemeral state owned by a model loop. Empty fields keep
// the session's selection. It must never be stored in user configuration.
type RequestSettings struct {
	Model  string
	Effort string
}

// requestSettingsClient selects isolated model clones at request time. A
// skill loaded by a tool can change the next request without changing a pooled
// prototype, a sibling agent, or the saved session model.
type requestSettingsClient struct {
	Client
	settings func() RequestSettings
	guard    func(context.Context) error
	mu       sync.Mutex
	models   map[string]Client
}

func WithRequestSettings(base Client, settings func() RequestSettings) Client {
	if isNilClient(base) || settings == nil {
		return base
	}
	return &requestSettingsClient{Client: base, settings: settings, models: make(map[string]Client)}
}

// WithRequestGuard checks live execution authority before every provider
// request, including auxiliary clones and model selections.
func WithRequestGuard(base Client, guard func(context.Context) error) Client {
	if isNilClient(base) || guard == nil {
		return base
	}
	return &requestSettingsClient{Client: base, settings: func() RequestSettings { return RequestSettings{} }, guard: guard, models: make(map[string]Client)}
}

// RequestSettingsBase exposes the session selection without a previous turn's
// temporary overrides. The underlying budget/fallback decorators stay intact.
func RequestSettingsBase(c Client) Client {
	for {
		wrapped, ok := c.(*requestSettingsClient)
		if !ok {
			return c
		}
		c = wrapped.Client
	}
}

// ReleaseRequestSettings closes only this loop's model overrides. The session
// base remains owned by its caller and usable on the next turn.
func ReleaseRequestSettings(cl Client) {
	if c, ok := cl.(*requestSettingsClient); ok {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, clone := range c.models {
			_ = clone.Close()
		}
		c.models = make(map[string]Client)
	}
}

func (c *requestSettingsClient) selected(settings RequestSettings) Client {
	if settings.Model == "" || settings.Model == c.Client.GetModel() {
		return c.Client
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.models[settings.Model]; existing != nil {
		return existing
	}
	clone := WithModelOnCurrentProvider(c.Client, settings.Model)
	if isNilClient(clone) || sameClientInstance(clone, c.Client) || clone.GetModel() != settings.Model {
		return nil
	}
	c.models[settings.Model] = clone
	return clone
}

func (c *requestSettingsClient) request(ctx context.Context) (context.Context, Client, error) {
	if c.guard != nil {
		if err := c.guard(ctx); err != nil {
			return ctx, nil, err
		}
	}
	settings := c.settings()
	selected := c.selected(settings)
	if selected == nil {
		return ctx, nil, fmt.Errorf("cannot create isolated client for skill model %q", settings.Model)
	}
	if settings.Effort != "" {
		ctx = ContextWithReasoningEffort(ctx, settings.Effort)
	}
	return ctx, selected, nil
}

func (c *requestSettingsClient) SendMessage(ctx context.Context, message string) (*StreamingResponse, error) {
	ctx, selected, err := c.request(ctx)
	if err != nil {
		return nil, err
	}
	return selected.SendMessage(ctx, message)
}

func (c *requestSettingsClient) SendMessageWithHistory(ctx context.Context, history []*genai.Content, message string) (*StreamingResponse, error) {
	ctx, selected, err := c.request(ctx)
	if err != nil {
		return nil, err
	}
	return selected.SendMessageWithHistory(ctx, history, message)
}

func (c *requestSettingsClient) SendFunctionResponse(ctx context.Context, history []*genai.Content, results []*genai.FunctionResponse) (*StreamingResponse, error) {
	ctx, selected, err := c.request(ctx)
	if err != nil {
		return nil, err
	}
	return selected.SendFunctionResponse(ctx, history, results)
}

func (c *requestSettingsClient) CountTokens(ctx context.Context, contents []*genai.Content) (*genai.CountTokensResponse, error) {
	ctx, selected, err := c.request(ctx)
	if err != nil {
		return nil, err
	}
	return selected.CountTokens(ctx, contents)
}

func (c *requestSettingsClient) CountTokensWithAccuracy(ctx context.Context, contents []*genai.Content) (*genai.CountTokensResponse, bool, error) {
	ctx, selected, err := c.request(ctx)
	if err != nil {
		return nil, false, err
	}
	if detailed, ok := selected.(TokenCountWithAccuracy); ok {
		return detailed.CountTokensWithAccuracy(ctx, contents)
	}
	response, err := selected.CountTokens(ctx, contents)
	accuracy, ok := selected.(TokenCountAccuracy)
	return response, ok && accuracy.TokenCountIsEstimate(), err
}

func (c *requestSettingsClient) GetModel() string {
	if selected := c.selected(c.settings()); selected != nil {
		return selected.GetModel()
	}
	return c.Client.GetModel()
}

func (c *requestSettingsClient) GetProvider() string {
	if selected := c.selected(c.settings()); selected != nil {
		if identity, ok := selected.(ProviderIdentity); ok {
			return identity.GetProvider()
		}
	}
	if identity, ok := c.Client.(ProviderIdentity); ok {
		return identity.GetProvider()
	}
	return ""
}

func (c *requestSettingsClient) EffectiveReasoningEffort(ctx context.Context) string {
	ctx, selected, err := c.request(ctx)
	if err != nil {
		return ""
	}
	return EffectiveReasoningEffort(ctx, selected)
}

func (c *requestSettingsClient) TokenCountCacheKey() string {
	if selected := c.selected(c.settings()); selected != nil {
		if keyer, ok := selected.(TokenCountCacheKey); ok {
			return keyer.TokenCountCacheKey()
		}
		return selected.GetModel()
	}
	return c.Client.GetModel()
}

func (c *requestSettingsClient) TokenCountIsEstimate() bool {
	selected := c.selected(c.settings())
	accuracy, ok := selected.(TokenCountAccuracy)
	return ok && accuracy.TokenCountIsEstimate()
}

func (c *requestSettingsClient) NeedsToolCallFallback() bool {
	selected := c.selected(c.settings())
	fallback, ok := selected.(interface{ NeedsToolCallFallback() bool })
	return ok && fallback.NeedsToolCallFallback()
}

func (c *requestSettingsClient) ResetFallbackPosition() {
	c.update(ResetClientFallback)
}

// WithModel drops temporary model/effort selection. Execution guards stay
// bound to their component when a provider or auxiliary path selects a model.
func (c *requestSettingsClient) WithModel(model string) Client {
	return WithRequestGuard(c.Client.WithModel(model), c.guard)
}

func (c *requestSettingsClient) WithModelOnCurrentProvider(model string) Client {
	return WithRequestGuard(WithModelOnCurrentProvider(c.Client, model), c.guard)
}

// Auxiliary clones stay within the same owner but isolate schemas, prompts,
// budgets and callbacks. Future scope changes still reach their requests.
func (c *requestSettingsClient) CloneForAuxiliaryClient() (Client, bool) {
	base, isolated := CloneForAuxiliary(c.Client)
	if !isolated {
		return nil, false
	}
	return &requestSettingsClient{Client: base, settings: c.settings, guard: c.guard, models: make(map[string]Client)}, true
}

func (c *requestSettingsClient) CloneForAuxiliaryModel(model string) (Client, bool, error) {
	base, isolated, err := CloneForAuxiliaryWithModel(c.Client, model)
	if !isolated {
		return nil, false, err
	}
	settings := c.settings
	if model != "" {
		settings = func() RequestSettings { return RequestSettings{} }
	}
	return &requestSettingsClient{Client: base, settings: settings, guard: c.guard, models: make(map[string]Client)}, true, err
}

func (c *requestSettingsClient) update(apply func(Client)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	apply(c.Client)
	for _, clone := range c.models {
		apply(clone)
	}
}

func (c *requestSettingsClient) SetTools(value []*genai.Tool) {
	c.update(func(cl Client) { cl.SetTools(value) })
}
func (c *requestSettingsClient) SetRateLimiter(value any) {
	c.update(func(cl Client) { cl.SetRateLimiter(value) })
}
func (c *requestSettingsClient) SetSystemInstruction(value string) {
	c.update(func(cl Client) { cl.SetSystemInstruction(value) })
}
func (c *requestSettingsClient) SetTurnContext(value string) {
	c.update(func(cl Client) { cl.SetTurnContext(value) })
}
func (c *requestSettingsClient) ResetConversationCache() {
	c.update(ResetConversationCache)
}
func (c *requestSettingsClient) SetThinkingBudget(value int32) {
	c.update(func(cl Client) { cl.SetThinkingBudget(value) })
}
func (c *requestSettingsClient) SetStatusCallback(value StatusCallback) {
	c.update(func(cl Client) {
		if setter, ok := cl.(interface{ SetStatusCallback(StatusCallback) }); ok {
			setter.SetStatusCallback(value)
		}
	})
}
func (c *requestSettingsClient) setDirectHealthTracking(value bool) {
	c.update(func(cl Client) {
		if setter, ok := cl.(directHealthTrackingSetter); ok {
			setter.setDirectHealthTracking(value)
		}
	})
}

func (c *requestSettingsClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.Client.Close()
	for _, clone := range c.models {
		if closeErr := clone.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}

func (c *requestSettingsClient) SetToolSchemaProfile(profile *toolschema.Profile) {
	c.update(func(cl Client) { toolschema.Install(cl, profile) })
}
