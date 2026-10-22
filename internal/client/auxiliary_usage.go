package client

import (
	"context"
	"fmt"

	"google.golang.org/genai"
)

type AuxiliaryRequestPolicy struct {
	Client  func(Client) Client
	Record  func(provider, model string, response *Response)
	Bounded bool // one-shot work must join its caller and retain its deadline
}

type auxiliaryRequestKey struct{}

func ContextWithAuxiliaryRequests(ctx context.Context, policy AuxiliaryRequestPolicy) context.Context {
	return context.WithValue(ctx, auxiliaryRequestKey{}, policy)
}

func ContextWithBoundedAuxiliaryRequests(ctx context.Context) context.Context {
	policy, _ := ctx.Value(auxiliaryRequestKey{}).(AuxiliaryRequestPolicy)
	policy.Bounded = true
	return ContextWithAuxiliaryRequests(ctx, policy)
}

func AuxiliaryRequestsBounded(ctx context.Context) bool {
	policy, ok := ctx.Value(auxiliaryRequestKey{}).(AuxiliaryRequestPolicy)
	return ok && policy.Bounded
}

func auxiliaryRequestClient(ctx context.Context, base Client) Client {
	if policy, ok := ctx.Value(auxiliaryRequestKey{}).(AuxiliaryRequestPolicy); ok && policy.Client != nil {
		return policy.Client(base)
	}
	return base
}

func SendAuxiliaryMessage(ctx context.Context, base Client, prompt string) (*StreamingResponse, error) {
	if err := AuxiliaryModelError(base); err != nil {
		return nil, err
	}
	return auxiliaryRequestClient(ctx, base).SendMessage(ctx, prompt)
}

func SendAuxiliaryMessageWithHistory(ctx context.Context, base Client, history []*genai.Content, prompt string) (*StreamingResponse, error) {
	if err := AuxiliaryModelError(base); err != nil {
		return nil, err
	}
	return auxiliaryRequestClient(ctx, base).SendMessageWithHistory(ctx, history, prompt)
}

func CollectAuxiliaryResponse(ctx context.Context, base Client, stream *StreamingResponse) (*Response, error) {
	return ProcessAuxiliaryStream(ctx, base, stream, &StreamHandler{})
}

func SendAuxiliaryFunctionResponse(ctx context.Context, base Client, history []*genai.Content, results []*genai.FunctionResponse) (*StreamingResponse, error) {
	if err := AuxiliaryModelError(base); err != nil {
		return nil, err
	}
	return auxiliaryRequestClient(ctx, base).SendFunctionResponse(ctx, history, results)
}

func ProcessAuxiliaryStream(ctx context.Context, base Client, stream *StreamingResponse, handler *StreamHandler) (*Response, error) {
	if stream == nil {
		return nil, fmt.Errorf("auxiliary model returned no response")
	}
	response, err := ProcessStream(ctx, stream, handler)
	if response != nil {
		RecordContextTokenUsage(ctx, response.TotalInputTokens(), response.OutputTokens)
		if policy, ok := ctx.Value(auxiliaryRequestKey{}).(AuxiliaryRequestPolicy); ok && policy.Record != nil {
			provider := ""
			if identified, ok := base.(ProviderIdentity); ok {
				provider = identified.GetProvider()
			}
			policy.Record(provider, base.GetModel(), response)
		}
	}
	return response, err
}
