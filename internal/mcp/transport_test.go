package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestJSONRPCMessageParsing tests JSON-RPC message parsing
func TestJSONRPCMessageParsing(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *JSONRPCMessage
		wantErr bool
	}{
		{
			name:  "valid request",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			want: &JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/list",
			},
		},
		{
			name:  "valid notification",
			input: `{"jsonrpc":"2.0","method":"initialized"}`,
			want: &JSONRPCMessage{
				JSONRPC: "2.0",
				Method:  "initialized",
			},
		},
		{
			name:  "valid response",
			input: `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
			want: &JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      1,
				Result:  json.RawMessage(`{"tools":[]}`),
			},
		},
		{
			name:  "valid error response",
			input: `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid Request"}}`,
			want: &JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      1,
				Error: &Error{
					Code:    -32600,
					Message: "Invalid Request",
				},
			},
		},
		{
			name:  "missing jsonrpc version",
			input: `{"id":1,"method":"test"}`,
			want: &JSONRPCMessage{
				JSONRPC: "",
				ID:      1,
				Method:  "test",
			},
		},
		{
			name:    "invalid json",
			input:   `{invalid}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg JSONRPCMessage
			err := json.Unmarshal([]byte(tt.input), &msg)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if msg.JSONRPC != tt.want.JSONRPC {
				t.Errorf("JSONRPC = %q, want %q", msg.JSONRPC, tt.want.JSONRPC)
			}
			if msg.Method != tt.want.Method {
				t.Errorf("Method = %q, want %q", msg.Method, tt.want.Method)
			}
		})
	}
}

// TestJSONRPCMessageSerialization tests JSON-RPC message serialization
func TestJSONRPCMessageSerialization(t *testing.T) {
	msg := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      42,
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"test","arguments":{}}`),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded JSONRPCMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.JSONRPC != msg.JSONRPC {
		t.Errorf("JSONRPC = %q, want %q", decoded.JSONRPC, msg.JSONRPC)
	}
	// ID is unmarshaled as json.Number, compare as strings
	if fmt.Sprintf("%v", decoded.ID) != fmt.Sprintf("%v", msg.ID) {
		t.Errorf("ID mismatch: got %v (%T), want %v", decoded.ID, decoded.ID, msg.ID)
	}
	if decoded.Method != msg.Method {
		t.Errorf("Method = %q, want %q", decoded.Method, msg.Method)
	}
}

// TestJSONRPCMessageHelpers tests message type helpers
func TestJSONRPCMessageHelpers(t *testing.T) {
	tests := []struct {
		name     string
		msg      JSONRPCMessage
		wantReq  bool
		wantNoti bool
		wantResp bool
	}{
		{
			name:     "request",
			msg:      JSONRPCMessage{JSONRPC: "2.0", ID: 1, Method: "test"},
			wantReq:  true,
			wantNoti: false,
			wantResp: false,
		},
		{
			name:     "notification",
			msg:      JSONRPCMessage{JSONRPC: "2.0", Method: "initialized"},
			wantReq:  false,
			wantNoti: true,
			wantResp: false,
		},
		{
			name:     "response",
			msg:      JSONRPCMessage{JSONRPC: "2.0", ID: 1, Result: json.RawMessage(`{}`)},
			wantReq:  false,
			wantNoti: false,
			wantResp: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.msg.IsRequest(); got != tt.wantReq {
				t.Errorf("IsRequest() = %v, want %v", got, tt.wantReq)
			}
			if got := tt.msg.IsNotification(); got != tt.wantNoti {
				t.Errorf("IsNotification() = %v, want %v", got, tt.wantNoti)
			}
			if got := tt.msg.IsResponse(); got != tt.wantResp {
				t.Errorf("IsResponse() = %v, want %v", got, tt.wantResp)
			}
		})
	}
}

// TestStdioTransportCreation tests stdio transport creation
func TestStdioTransportCreation(t *testing.T) {
	// Test with a simple command
	transport, err := NewStdioTransport("true", nil, nil)
	if err != nil {
		t.Fatalf("NewStdioTransport() error = %v", err)
	}
	defer transport.Close()

	if transport == nil {
		t.Fatal("transport is nil")
	}
}

// TestStdioTransportClose tests that closing transport works correctly
func TestStdioTransportClose(t *testing.T) {
	transport, err := NewStdioTransport("true", nil, nil)
	if err != nil {
		t.Fatalf("NewStdioTransport() error = %v", err)
	}

	// Close should not panic
	err = transport.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

// TestHTTPTransport tests HTTP transport functionality
func TestHTTPTransport(t *testing.T) {
	// Create a mock HTTP server
	var receivedMsg JSONRPCMessage
	var msgMu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var msg JSONRPCMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		msgMu.Lock()
		receivedMsg = msg
		msgMu.Unlock()

		// Send response
		response := JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Result:  json.RawMessage(`{"success":true}`),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	ctx := context.Background()
	transport, err := NewHTTPTransport(ctx, server.URL, nil, 0)
	if err != nil {
		t.Fatalf("NewHTTPTransport() error = %v", err)
	}
	defer transport.Close()

	// Test Send and Receive
	msg := &JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/list",
	}

	if err := transport.Send(msg); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	received, err := transport.Receive()
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}

	msgMu.Lock()
	if receivedMsg.Method != "tools/list" {
		t.Errorf("received method = %q, want %q", receivedMsg.Method, "tools/list")
	}
	msgMu.Unlock()

	if received.JSONRPC != "2.0" {
		t.Errorf("response JSONRPC = %q, want %q", received.JSONRPC, "2.0")
	}
}

// TestHTTPTransportClose tests HTTP transport close
func TestHTTPTransportClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simple handler
	}))
	defer server.Close()

	ctx := context.Background()
	transport, err := NewHTTPTransport(ctx, server.URL, nil, 0)
	if err != nil {
		t.Fatalf("NewHTTPTransport() error = %v", err)
	}

	if err := transport.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

// TestHTTPTransportNotFound tests HTTP transport with 404 response
func TestHTTPTransportNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Not Found", http.StatusNotFound)
	}))
	defer server.Close()

	ctx := context.Background()
	transport, err := NewHTTPTransport(ctx, server.URL, nil, 0)
	if err != nil {
		t.Fatalf("NewHTTPTransport() error = %v", err)
	}
	defer transport.Close()

	msg := &JSONRPCMessage{JSONRPC: "2.0", Method: "test"}
	err = transport.Send(msg)
	if err == nil {
		t.Error("expected error for 404 response")
	}
}

// TestHTTPTransportServerError tests HTTP transport with server error
func TestHTTPTransportServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx := context.Background()
	transport, err := NewHTTPTransport(ctx, server.URL, nil, 0)
	if err != nil {
		t.Fatalf("NewHTTPTransport() error = %v", err)
	}
	defer transport.Close()

	msg := &JSONRPCMessage{JSONRPC: "2.0", Method: "test"}
	err = transport.Send(msg)
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

// TestHTTPTransportAuth tests HTTP transport with authentication
func TestHTTPTransportAuth(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		response := JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{}`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	headers := map[string]string{"Authorization": "Bearer test-token"}
	ctx := context.Background()
	transport, err := NewHTTPTransport(ctx, server.URL, headers, 0)
	if err != nil {
		t.Fatalf("NewHTTPTransport() error = %v", err)
	}
	defer transport.Close()

	msg := &JSONRPCMessage{JSONRPC: "2.0", ID: 1, Method: "test"}
	if err := transport.Send(msg); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if authHeader != "Bearer test-token" {
		t.Errorf("Authorization header = %q, want %q", authHeader, "Bearer test-token")
	}
}

// TestProtocolVersion tests protocol version constants
func TestProtocolVersion(t *testing.T) {
	if ProtocolVersion != "2024-11-05" {
		t.Errorf("ProtocolVersion = %q, want %q", ProtocolVersion, "2024-11-05")
	}

	// Verify JSON-RPC version in messages
	msg := JSONRPCMessage{JSONRPC: "2.0"}
	if msg.JSONRPC != "2.0" {
		t.Errorf("JSONRPC version = %q, want %q", msg.JSONRPC, "2.0")
	}
}

// TestErrorCodes tests JSON-RPC error codes
func TestErrorCodes(t *testing.T) {
	tests := []struct {
		code    int
		message string
	}{
		{ErrCodeParseError, "Parse error"},
		{ErrCodeInvalidRequest, "Invalid Request"},
		{ErrCodeMethodNotFound, "Method not found"},
		{ErrCodeInvalidParams, "Invalid params"},
		{ErrCodeInternalError, "Internal error"},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			// Verify error codes are within valid JSON-RPC range
			if tt.code >= -32768 && tt.code <= -32000 {
				// Valid JSON-RPC error code range
			} else {
				t.Errorf("error code %d out of range", tt.code)
			}
		})
	}
}

// TestErrorMethod tests Error type Error() method
func TestErrorMethod(t *testing.T) {
	err := &Error{
		Code:    -32600,
		Message: "Test error message",
	}

	if got := err.Error(); got != err.Message {
		t.Errorf("Error() = %q, want %q", got, err.Message)
	}
}

// TestConcurrencySendReceive tests concurrent send and receive operations
func TestConcurrencySendReceive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg JSONRPCMessage
		json.NewDecoder(r.Body).Decode(&msg)

		response := JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Result:  json.RawMessage(`{"processed":true}`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	ctx := context.Background()
	transport, err := NewHTTPTransport(ctx, server.URL, nil, 0)
	if err != nil {
		t.Fatalf("NewHTTPTransport() error = %v", err)
	}
	defer transport.Close()

	var wg sync.WaitGroup
	errChan := make(chan error, 10)

	// Concurrent sends
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			msg := &JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      id,
				Method:  "test/concurrent",
			}

			if err := transport.Send(msg); err != nil {
				errChan <- err
				return
			}

			_, err := transport.Receive()
			if err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		t.Errorf("concurrent operation error: %v", err)
	}
}

// TestTransportReconnect tests transport reconnection
func TestTransportReconnect(t *testing.T) {
	// This tests that a transport can be closed and we can work with it
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{}`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	ctx := context.Background()
	transport, err := NewHTTPTransport(ctx, server.URL, nil, 0)
	if err != nil {
		t.Fatalf("NewHTTPTransport() error = %v", err)
	}

	// First connection
	msg1 := &JSONRPCMessage{JSONRPC: "2.0", ID: 1, Method: "test1"}
	if err := transport.Send(msg1); err != nil {
		t.Fatalf("first Send() error = %v", err)
	}

	// Close
	if err := transport.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// Create new transport to same server
	transport2, err := NewHTTPTransport(ctx, server.URL, nil, 0)
	if err != nil {
		t.Fatalf("NewHTTPTransport() error = %v", err)
	}
	defer transport2.Close()

	// Second connection
	msg2 := &JSONRPCMessage{JSONRPC: "2.0", ID: 2, Method: "test2"}
	if err := transport2.Send(msg2); err != nil {
		t.Fatalf("second Send() error = %v", err)
	}
}

// TestToolInfoValidation tests ToolInfo structure
func TestToolInfoValidation(t *testing.T) {
	tool := ToolInfo{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: &JSONSchema{
			Type:     "object",
			Required: []string{"arg1"},
			Properties: map[string]*JSONSchema{
				"arg1": {Type: "string"},
			},
		},
	}

	if tool.Name != "test_tool" {
		t.Errorf("Name = %q, want %q", tool.Name, "test_tool")
	}
}

// TestJSONSchemaValidation tests JSON schema validation
func TestJSONSchemaValidation(t *testing.T) {
	schema := &JSONSchema{
		Type:     "object",
		Required: []string{"name"},
		Properties: map[string]*JSONSchema{
			"name": {Type: "string"},
			"age":  {Type: "integer"},
		},
	}

	// Test valid input
	validArgs := map[string]any{
		"name": "test",
		"age":  25,
	}

	for name, propSchema := range schema.Properties {
		val, ok := validArgs[name]
		if !ok {
			continue
		}
		if err := validateValue(name, val, propSchema); err != nil {
			t.Errorf("unexpected validation error for valid input: %v", err)
		}
	}

	// Test invalid input - wrong type
	invalidArgs := map[string]any{
		"name": 123, // should be string
	}

	for name, propSchema := range schema.Properties {
		val, ok := invalidArgs[name]
		if !ok {
			continue
		}
		if err := validateValue(name, val, propSchema); err == nil {
			t.Errorf("expected validation error for invalid input: name=%v", val)
		}
	}
}

// TestFormatContentBlocks tests content block formatting
func TestFormatContentBlocks(t *testing.T) {
	tests := []struct {
		name   string
		blocks []*ContentBlock
		want   string
	}{
		{
			name:   "empty blocks",
			blocks: []*ContentBlock{},
			want:   "(no output)",
		},
		{
			name: "single text block",
			blocks: []*ContentBlock{
				{Type: "text", Text: "Hello World"},
			},
			want: "Hello World",
		},
		{
			name: "multiple text blocks",
			blocks: []*ContentBlock{
				{Type: "text", Text: "Line 1"},
				{Type: "text", Text: "Line 2"},
			},
			want: "Line 1\nLine 2",
		},
		{
			name: "image block",
			blocks: []*ContentBlock{
				{Type: "image", MIMEType: "image/png"},
			},
			want: "[Image: image/png]",
		},
		{
			name: "resource block",
			blocks: []*ContentBlock{
				{Type: "resource", URI: "file:///path/to/file"},
			},
			want: "[Resource: file:///path/to/file]",
		},
		{
			name: "mixed blocks",
			blocks: []*ContentBlock{
				{Type: "text", Text: "Header"},
				{Type: "image", MIMEType: "image/jpeg"},
				{Type: "text", Text: "Footer"},
			},
			want: "Header\n[Image: image/jpeg]\nFooter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatContentBlocks(tt.blocks)
			if got != tt.want {
				t.Errorf("formatContentBlocks() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestMCPToolBasic tests MCPTool basic functionality
func TestMCPToolBasic(t *testing.T) {
	tool := &MCPTool{
		client:      nil, // Will be set in integration tests
		serverName:  "test_server",
		toolName:    "test_tool",
		displayName: "test_server_test_tool",
		description: "A test tool",
	}

	if tool.Name() != "test_server_test_tool" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "test_server_test_tool")
	}

	if tool.Description() != "A test tool" {
		t.Errorf("Description() = %q, want %q", tool.Description(), "A test tool")
	}

	if tool.GetServerName() != "test_server" {
		t.Errorf("GetServerName() = %q, want %q", tool.GetServerName(), "test_server")
	}

	if tool.GetOriginalToolName() != "test_tool" {
		t.Errorf("GetOriginalToolName() = %q, want %q", tool.GetOriginalToolName(), "test_tool")
	}
}

// TestMCPToolValidation tests MCPTool validation
func TestMCPToolValidation(t *testing.T) {
	tool := &MCPTool{
		client:      nil,
		serverName:  "test",
		toolName:    "tool",
		displayName: "test_tool",
		description: "A test tool",
		inputSchema: &JSONSchema{
			Type:     "object",
			Required: []string{"required_arg"},
			Properties: map[string]*JSONSchema{
				"required_arg": {Type: "string"},
				"optional_arg": {Type: "integer"},
			},
		},
	}

	// Test missing required argument
	err := tool.Validate(map[string]any{})
	if err == nil {
		t.Error("expected error for missing required argument")
	}

	// Test valid arguments
	err = tool.Validate(map[string]any{
		"required_arg": "value",
		"optional_arg": 42,
	})
	if err != nil {
		t.Errorf("unexpected error for valid args: %v", err)
	}

	// Test wrong type
	err = tool.Validate(map[string]any{
		"required_arg": 123, // should be string
	})
	if err == nil {
		t.Error("expected error for wrong type")
	}
}

// TestMCPToolExecuteNoClient tests MCPTool execution without client
func TestMCPToolExecuteNoClient(t *testing.T) {
	tool := &MCPTool{
		client:      nil,
		serverName:  "test",
		toolName:    "tool",
		displayName: "test_tool",
		description: "A test tool",
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() error = %v, want nil", err)
	}

	if result.Success {
		t.Error("expected error result when client is nil")
	}
}

// TestHTTPTransportContextTimeout tests HTTP transport with context timeout
func TestHTTPTransportContextTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slow response
		time.Sleep(2 * time.Second)
	}))
	defer server.Close()

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	transport, err := NewHTTPTransport(ctx, server.URL, nil, 0)
	if err != nil {
		t.Fatalf("NewHTTPTransport() error = %v", err)
	}
	defer transport.Close()

	msg := &JSONRPCMessage{JSONRPC: "2.0", Method: "test"}
	err = transport.Send(msg)
	if err == nil {
		// Send succeeded, receive should timeout
		_, err = transport.Receive()
	}
	if err == nil {
		t.Error("expected timeout error from Send or Receive, but both succeeded")
	}
}

// TestHTTPTransportTimeout tests HTTP transport timeout handling
func TestHTTPTransportTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // Simulate slow server
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx := context.Background()
	transport, err := NewHTTPTransport(ctx, server.URL, nil, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("NewHTTPTransport() error = %v", err)
	}
	defer transport.Close()

	msg := &JSONRPCMessage{JSONRPC: "2.0", Method: "test"}
	err = transport.Send(msg)
	if err == nil {
		t.Error("expected timeout error")
	}
}

// BenchmarkJSONRPCMessageParsing benchmarks JSON-RPC message parsing
func BenchmarkJSONRPCMessageParsing(b *testing.B) {
	data := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"cursor":"test-cursor"}}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var msg JSONRPCMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkJSONRPCMessageSerialization benchmarks JSON-RPC message serialization
func BenchmarkJSONRPCMessageSerialization(b *testing.B) {
	msg := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/list",
		Params:  json.RawMessage(`{"cursor":"test-cursor","limit":100}`),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, err := json.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
		_ = data
	}
}

// BenchmarkContentBlockFormatting benchmarks content block formatting
func BenchmarkContentBlockFormatting(b *testing.B) {
	blocks := []*ContentBlock{
		{Type: "text", Text: "This is a test message with some content"},
		{Type: "text", Text: "Another line of text here"},
		{Type: "image", MIMEType: "image/png"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = formatContentBlocks(blocks)
	}
}

// BenchmarkSchemaValidation benchmarks schema validation
func BenchmarkSchemaValidation(b *testing.B) {
	schema := &JSONSchema{
		Type:     "object",
		Required: []string{"name", "age", "active"},
		Properties: map[string]*JSONSchema{
			"name":     {Type: "string"},
			"age":      {Type: "integer"},
			"active":   {Type: "boolean"},
			"tags":     {Type: "array"},
			"metadata": {Type: "object"},
		},
	}

	args := map[string]any{
		"name":     "test",
		"age":      25,
		"active":   true,
		"tags":     []any{"a", "b"},
		"metadata": map[string]any{"key": "value"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for name, propSchema := range schema.Properties {
			if val, ok := args[name]; ok {
				_ = validateValue(name, val, propSchema)
			}
		}
	}
}
