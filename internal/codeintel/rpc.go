package codeintel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sync/atomic"

	"gokin/internal/mcp"
)

type rpcSession struct {
	conn   Connection
	nextID atomic.Int64
}

type receiveResult struct {
	message *mcp.JSONRPCMessage
	err     error
}

func (s *rpcSession) request(ctx context.Context, method string, params, out any, maxOutput int) error {
	if err := ctx.Err(); err != nil {
		return retryable(err)
	}
	id := s.nextID.Add(1)
	if err := s.conn.Send(&mcp.JSONRPCMessage{
		JSONRPC: "2.0", ID: id, Method: method, Params: params,
	}); err != nil {
		return retryable(fmt.Errorf("send %s: %w", method, err))
	}

	received := make(chan receiveResult, 1)
	go func() {
		for {
			message, err := s.conn.Receive()
			if err != nil {
				received <- receiveResult{err: err}
				return
			}
			if message == nil {
				received <- receiveResult{err: fmt.Errorf("nil MCP message")}
				return
			}
			if message.IsRequest() {
				_ = s.conn.Send(&mcp.JSONRPCMessage{
					JSONRPC: "2.0", ID: message.ID,
					Error: &mcp.Error{Code: mcp.ErrCodeMethodNotFound, Message: "client method not supported"},
				})
				continue
			}
			if !message.IsResponse() || !sameRPCID(message.ID, id) {
				continue
			}
			received <- receiveResult{message: message}
			return
		}
	}()

	select {
	case response := <-received:
		if response.err != nil {
			if response.err == io.EOF {
				return retryable(fmt.Errorf("receive %s: server exited: %w", method, response.err))
			}
			return retryable(fmt.Errorf("receive %s: %w", method, response.err))
		}
		if response.message.Error != nil {
			return fmt.Errorf("%s RPC error %d: %s", method,
				response.message.Error.Code, response.message.Error.Message)
		}
		data, err := json.Marshal(response.message.Result)
		if err != nil {
			return retryable(fmt.Errorf("encode %s response: %w", method, err))
		}
		if len(data) > maxOutput {
			return retryable(fmt.Errorf("%w: %s returned %d > %d bytes",
				ErrResponseTooLarge, method, len(data), maxOutput))
		}
		if err := json.Unmarshal(data, out); err != nil {
			return retryable(fmt.Errorf("decode %s response: %w", method, err))
		}
		return nil
	case <-ctx.Done():
		return retryable(fmt.Errorf("%s: %w", method, ctx.Err()))
	}
}

func (s *rpcSession) notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return retryable(err)
	}
	if err := s.conn.Send(&mcp.JSONRPCMessage{JSONRPC: "2.0", Method: method, Params: params}); err != nil {
		return retryable(fmt.Errorf("send %s: %w", method, err))
	}
	return nil
}

func (s *rpcSession) close(ctx context.Context) error {
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.Close(ctx)
}

func sameRPCID(raw any, want int64) bool {
	switch value := raw.(type) {
	case int:
		return int64(value) == want
	case int32:
		return int64(value) == want
	case int64:
		return value == want
	case float64:
		return value >= math.MinInt64 && value <= math.MaxInt64 && value == math.Trunc(value) && int64(value) == want
	case json.Number:
		parsed, err := value.Int64()
		return err == nil && parsed == want
	default:
		return false
	}
}
