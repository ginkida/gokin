package client

import (
	"context"
	"fmt"
)

// directHealthTrackingSetter is implemented by built-in provider clients.
// FallbackClient disables their direct observer because it already scores each
// candidate itself; otherwise one fallback request would be counted twice.
type directHealthTrackingSetter interface {
	setDirectHealthTracking(bool)
}

func disableDirectHealthTracking(c Client) {
	if setter, ok := c.(directHealthTrackingSetter); ok {
		setter.setDirectHealthTracking(false)
	}
}

// observeProviderStream mirrors a provider stream while recording its terminal
// outcome. Request success is not provider success yet: HTTP 200 streams can
// still deliver an SSE error, end empty, or be cancelled by the user.
func observeProviderStream(ctx context.Context, provider string, stream *StreamingResponse) *StreamingResponse {
	out := make(chan ResponseChunk, 16)
	done := make(chan struct{})

	go func() {
		defer close(out)
		defer close(done)

		if stream == nil || stream.Chunks == nil {
			err := fmt.Errorf("provider returned a streaming response with nil chunks")
			if ContextErr(ctx) == nil {
				recordProviderFailure(provider, true)
			}
			emitObservedChunk(ctx, out, ResponseChunk{Error: err, Done: true})
			return
		}

		meaningful := false
		for {
			select {
			case <-ctx.Done():
				// Caller cancellation is not evidence about provider health.
				return
			case chunk, ok := <-stream.Chunks:
				if !ok {
					if ContextErr(ctx) != nil {
						return
					}
					if meaningful {
						recordProviderSuccess(provider)
					} else {
						recordProviderFailure(provider, true)
					}
					return
				}

				if chunk.Text != "" || chunk.Thinking != "" ||
					len(chunk.FunctionCalls) > 0 || len(chunk.Parts) > 0 {
					meaningful = true
				}
				// Publish terminal completion after health bookkeeping. Callers may
				// start the next request or release their workspace on Done; no
				// persistence should still race that lifecycle transition.
				if chunk.Error != nil {
					if ContextErr(ctx) == nil && shouldFallbackToNextProvider(chunk.Error) {
						recordProviderFailure(provider, IsRetryableError(chunk.Error))
					}
				} else if chunk.Done && ContextErr(ctx) == nil {
					if meaningful {
						recordProviderSuccess(provider)
					} else {
						recordProviderFailure(provider, true)
					}
				}
				if !emitObservedChunk(ctx, out, chunk) || chunk.Error != nil || chunk.Done {
					return
				}
			}
		}
	}()

	return &StreamingResponse{Chunks: out, Done: done}
}

func emitObservedChunk(ctx context.Context, out chan<- ResponseChunk, chunk ResponseChunk) bool {
	select {
	case out <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}
