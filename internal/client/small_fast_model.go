package client

import (
	"context"

	"gokin/internal/config"
	"google.golang.org/genai"
)

func ResolveSmallFastModel(c Client) (string, error) {
	provider := ""
	if identified, ok := c.(ProviderIdentity); ok {
		provider = identified.GetProvider()
	}
	return config.SmallFastModel(provider, c.GetModel())
}

// WithModelOnCurrentProvider preserves the chosen transport even when a local
// or gateway model's name resembles a different configured fallback provider.
func WithModelOnCurrentProvider(c Client, model string) Client {
	if cloner, ok := c.(interface{ WithModelOnCurrentProvider(string) Client }); ok {
		return cloner.WithModelOnCurrentProvider(model)
	}
	return c.WithModel(model)
}

// Invalid overrides fail at the next auxiliary request, without modifying the
// foreground client or losing the configured provider's transport and auth.
type auxiliaryModelErrorClient struct {
	Client
	err error
}

func AuxiliaryModelError(c Client) error {
	if failed, ok := c.(*auxiliaryModelErrorClient); ok {
		return failed.err
	}
	return nil
}

func (c *auxiliaryModelErrorClient) SendMessage(context.Context, string) (*StreamingResponse, error) {
	return nil, c.err
}
func (c *auxiliaryModelErrorClient) SendMessageWithHistory(context.Context, []*genai.Content, string) (*StreamingResponse, error) {
	return nil, c.err
}
func (c *auxiliaryModelErrorClient) SendFunctionResponse(context.Context, []*genai.Content, []*genai.FunctionResponse) (*StreamingResponse, error) {
	return nil, c.err
}
func (c *auxiliaryModelErrorClient) WithModel(string) Client { return c }
func (c *auxiliaryModelErrorClient) GetProvider() string {
	if identified, ok := c.Client.(ProviderIdentity); ok {
		return identified.GetProvider()
	}
	return ""
}
