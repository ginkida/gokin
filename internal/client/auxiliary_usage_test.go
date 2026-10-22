package client

import (
	"errors"
	"testing"
)

type auxiliaryUsageFixture struct{ Client }

func (*auxiliaryUsageFixture) GetModel() string    { return "fixture-model" }
func (*auxiliaryUsageFixture) GetProvider() string { return "fixture-provider" }

func TestAuxiliaryPartialResponseKeepsCacheUsageAndOwnerPolicy(t *testing.T) {
	partialErr := errors.New("partial stream failure")
	chunks := make(chan ResponseChunk, 2)
	chunks <- ResponseChunk{Text: "partial proof", InputTokens: 11, OutputTokens: 3, CacheReadInputTokens: 7, CacheCreationInputTokens: 5}
	chunks <- ResponseChunk{Error: partialErr}
	close(chunks)
	counter := &TokenUsageCounter{}
	reports := 0
	ctx := ContextWithAuxiliaryRequests(ContextWithTokenUsageCounter(t.Context(), counter), AuxiliaryRequestPolicy{Record: func(provider, model string, response *Response) {
		reports++
		if provider != "fixture-provider" || model != "fixture-model" || response.TotalInputTokens() != 23 {
			t.Errorf("wrong auxiliary identity/usage: %s %s %+v", provider, model, response)
		}
	}})
	ctx = ContextWithBoundedAuxiliaryRequests(ctx)
	response, err := CollectAuxiliaryResponse(ctx, &auxiliaryUsageFixture{}, &StreamingResponse{Chunks: chunks})
	input, output := counter.Snapshot()
	if !errors.Is(err, partialErr) || response.Text != "partial proof" || input != 23 || output != 3 || reports != 1 || !AuxiliaryRequestsBounded(ctx) {
		t.Fatalf("partial usage or owner was lost: %+v err=%v usage=%d/%d reports=%d", response, err, input, output, reports)
	}
}
