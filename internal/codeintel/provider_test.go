package codeintel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/mcp"
)

func TestGoplsProvider_LazyDiscoveryAndReadOnlyBoundary(t *testing.T) {
	workspace := t.TempDir()
	server := newFakeMCPConnection([]*mcp.ToolInfo{
		{Name: "go_search", Description: "search", InputSchema: &mcp.JSONSchema{Type: "object"}},
		{Name: "go_rename_symbol", Description: "mutates files"},
	}, func(params *mcp.CallToolParams) fakeReceive {
		return fakeToolText(params.Name + " ok")
	})
	recorder := &dialRecorder{connections: []Connection{server}}
	provider := newTestProvider(t, workspace, Options{Dialer: recorder.Dial})
	if recorder.Count() != 0 {
		t.Fatal("provider started eagerly")
	}

	capabilities, err := provider.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if recorder.Count() != 1 {
		t.Fatalf("dial count = %d, want 1", recorder.Count())
	}
	spec := recorder.Spec(0)
	if spec.Dir != workspace || spec.Command != "gopls" || len(spec.Args) != 1 || spec.Args[0] != "mcp" {
		t.Fatalf("process spec = %+v", spec)
	}
	if len(capabilities) != 2 || !capabilities[0].ReadOnly || capabilities[1].ReadOnly {
		t.Fatalf("capabilities = %+v", capabilities)
	}

	// Returned schemas are caller-owned snapshots, not aliases into the cache.
	capabilities[0].InputSchema.Type = "mutated"
	again, err := provider.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities cached: %v", err)
	}
	if again[0].InputSchema.Type != "object" || recorder.Count() != 1 {
		t.Fatalf("cached capability mutated or redialed: %+v, dials=%d", again[0], recorder.Count())
	}

	result, err := provider.CallReadOnly(context.Background(), "go_search", map[string]any{"query": "Provider"})
	if err != nil || callResultText(result) != "go_search ok" {
		t.Fatalf("CallReadOnly = %+v, %v", result, err)
	}
	if _, err := provider.CallReadOnly(context.Background(), "go_rename_symbol", nil); !errors.Is(err, ErrToolNotReadOnly) {
		t.Fatalf("mutation capability error = %v", err)
	}
	if server.ToolCallCount() != 1 {
		t.Fatalf("tool calls = %d, mutation reached server", server.ToolCallCount())
	}
}

func TestGoplsProvider_RestartsExactlyOnceForReadOnlyTransportFailure(t *testing.T) {
	tools := []*mcp.ToolInfo{{Name: "go_search"}}
	first := newFakeMCPConnection(tools, func(*mcp.CallToolParams) fakeReceive {
		return fakeReceive{err: io.EOF}
	})
	second := newFakeMCPConnection(tools, func(*mcp.CallToolParams) fakeReceive {
		return fakeToolText("recovered")
	})
	recorder := &dialRecorder{connections: []Connection{first, second}}
	provider := newTestProvider(t, t.TempDir(), Options{Dialer: recorder.Dial})

	result, err := provider.CallReadOnly(context.Background(), "go_search", map[string]any{"query": "x"})
	if err != nil || callResultText(result) != "recovered" {
		t.Fatalf("retry result = %+v, err=%v", result, err)
	}
	if recorder.Count() != 2 || first.CloseCount() != 1 || first.ToolCallCount() != 1 || second.ToolCallCount() != 1 {
		t.Fatalf("restart lifecycle: dials=%d firstClose=%d calls=(%d,%d)",
			recorder.Count(), first.CloseCount(), first.ToolCallCount(), second.ToolCallCount())
	}
}

func TestGoplsProvider_RetryAndOutputAreBounded(t *testing.T) {
	tools := []*mcp.ToolInfo{{Name: "go_search"}}
	makeOversized := func() *fakeMCPConnection {
		return newFakeMCPConnection(tools, func(*mcp.CallToolParams) fakeReceive {
			return fakeToolText(strings.Repeat("x", 2048))
		})
	}
	first, second := makeOversized(), makeOversized()
	recorder := &dialRecorder{connections: []Connection{first, second, makeOversized()}}
	provider := newTestProvider(t, t.TempDir(), Options{
		Dialer: recorder.Dial, MaxOutputBytes: 512,
	})

	_, err := provider.CallReadOnly(context.Background(), "go_search", nil)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversized response error = %v", err)
	}
	if recorder.Count() != 2 {
		t.Fatalf("oversized response dials = %d, want exactly initial + one restart", recorder.Count())
	}
	if first.CloseCount() != 1 || second.CloseCount() != 1 {
		t.Fatalf("broken sessions not closed: (%d,%d)", first.CloseCount(), second.CloseCount())
	}
}

func TestGoplsProvider_CallTimeoutAndCloseInterruptReceive(t *testing.T) {
	tools := []*mcp.ToolInfo{{Name: "go_search"}}
	blockedA := newFakeMCPConnection(tools, func(*mcp.CallToolParams) fakeReceive {
		return fakeReceive{block: true}
	})
	blockedB := newFakeMCPConnection(tools, func(*mcp.CallToolParams) fakeReceive {
		return fakeReceive{block: true}
	})
	recorder := &dialRecorder{connections: []Connection{blockedA, blockedB}}
	provider := newTestProvider(t, t.TempDir(), Options{
		Dialer: recorder.Dial, CallTimeout: 20 * time.Millisecond, ShutdownTimeout: 20 * time.Millisecond,
	})
	started := time.Now()
	_, err := provider.CallReadOnly(context.Background(), "go_search", nil)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("bounded call = %v after %v", err, time.Since(started))
	}
	if recorder.Count() != 2 || blockedA.CloseCount() != 1 || blockedB.CloseCount() != 1 {
		t.Fatalf("timeout restart lifecycle: dials=%d closes=(%d,%d)",
			recorder.Count(), blockedA.CloseCount(), blockedB.CloseCount())
	}

	// A separate provider proves Close cancels a long foreground Receive before
	// waiting for the serialization mutex.
	blocking := newFakeMCPConnection(tools, func(*mcp.CallToolParams) fakeReceive {
		return fakeReceive{block: true}
	})
	provider = newTestProvider(t, t.TempDir(), Options{
		Dialer:      (&dialRecorder{connections: []Connection{blocking}}).Dial,
		CallTimeout: 5 * time.Second, ShutdownTimeout: 30 * time.Millisecond,
	})
	callDone := make(chan error, 1)
	go func() {
		_, err := provider.CallReadOnly(context.Background(), "go_search", nil)
		callDone <- err
	}()
	select {
	case <-blocking.callStarted:
	case <-time.After(time.Second):
		t.Fatal("blocking call did not start")
	}
	started = time.Now()
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("Close did not interrupt foreground receive: %v", time.Since(started))
	}
	select {
	case err := <-callDone:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("interrupted call error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground call remained blocked after Close")
	}
}

func TestGoplsProvider_DiagnoseNormalizesFilesAndRequiresExplicitClean(t *testing.T) {
	workspace := t.TempDir()
	var callNumber atomic.Int32
	argsSeen := make(chan *mcp.CallToolParams, 2)
	server := newFakeMCPConnection([]*mcp.ToolInfo{{Name: "go_diagnostics"}}, func(params *mcp.CallToolParams) fakeReceive {
		argsSeen <- params
		if callNumber.Add(1) == 1 {
			return fakeToolText("No diagnostics.")
		}
		return fakeToolText("File `broken.go` has diagnostics")
	})
	provider := newTestProvider(t, workspace, Options{Dialer: (&dialRecorder{connections: []Connection{server}}).Dial})

	report, err := provider.Diagnose(context.Background(), []string{"broken.go", "broken.go"})
	if err != nil || !report.Clean || report.Source != DiagnosticsSource {
		t.Fatalf("clean report = %+v, err=%v", report, err)
	}
	params := <-argsSeen
	files, ok := params.Arguments["files"].([]any)
	if !ok || len(files) != 1 || files[0] != filepath.Join(workspace, "broken.go") {
		t.Fatalf("normalized diagnostic files = %#v", params.Arguments["files"])
	}

	report, err = provider.Diagnose(context.Background(), nil)
	if err != nil || report.Clean || !strings.Contains(report.Content, "diagnostics") {
		t.Fatalf("dirty report = %+v, err=%v", report, err)
	}
	if _, err := provider.Diagnose(context.Background(), []string{"../outside.go"}); err == nil {
		t.Fatal("Diagnose accepted a path outside the workspace")
	}
	outside := t.TempDir()
	link := filepath.Join(workspace, "outside-link")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := provider.Diagnose(context.Background(), []string{filepath.Join(link, "outside.go")}); err == nil {
			t.Fatal("Diagnose accepted a symlink escape from the workspace")
		}
	}
}

func TestGoplsProvider_ConcurrentCallsShareOneForegroundProcess(t *testing.T) {
	server := newFakeMCPConnection([]*mcp.ToolInfo{{Name: "go_search"}}, func(*mcp.CallToolParams) fakeReceive {
		return fakeToolText("ok")
	})
	recorder := &dialRecorder{connections: []Connection{server}}
	provider := newTestProvider(t, t.TempDir(), Options{Dialer: recorder.Dial})

	const callers = 24
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := provider.CallReadOnly(context.Background(), "go_search", map[string]any{"query": fmt.Sprint(i)})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent call: %v", err)
		}
	}
	if recorder.Count() != 1 || server.ToolCallCount() != callers {
		t.Fatalf("foreground serialization: dials=%d calls=%d", recorder.Count(), server.ToolCallCount())
	}
}

func TestGoplsProvider_ManagedProcessUsesWorkspaceAndShutsDown(t *testing.T) {
	workspace := t.TempDir()
	provider := newTestProvider(t, workspace, Options{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagedGoplsHelperProcess", "--", "--codeintel-helper"},
	})
	result, err := provider.CallReadOnly(context.Background(), "go_workspace", nil)
	if err != nil {
		t.Fatalf("managed helper call: %v", err)
	}
	got := callResultText(result)
	physicalWorkspace, _ := filepath.EvalSymlinks(workspace)
	physicalGot, _ := filepath.EvalSymlinks(got)
	if physicalGot != physicalWorkspace {
		t.Fatalf("managed process cwd = %q, want workspace %q", got, workspace)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("managed process Close: %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
}

func TestGoplsProvider_CloseKillsHungManagedProcess(t *testing.T) {
	provider := newTestProvider(t, t.TempDir(), Options{
		Command:         os.Args[0],
		Args:            []string{"-test.run=TestManagedGoplsHelperProcess", "--", "--codeintel-helper", "--hang-after-eof"},
		ShutdownTimeout: 30 * time.Millisecond,
	})
	if _, err := provider.CallReadOnly(context.Background(), "go_workspace", nil); err != nil {
		t.Fatalf("managed helper call: %v", err)
	}
	started := time.Now()
	err := provider.Close()
	if err == nil || !strings.Contains(err.Error(), "process killed") {
		t.Fatalf("hung process Close error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("hung process kill exceeded bound: %v", time.Since(started))
	}
}

func TestGoplsProvider_InstalledGoplsIntegration(t *testing.T) {
	if os.Getenv("GOKIN_TEST_REAL_GOPLS_MCP") != "1" {
		t.Skip("set GOKIN_TEST_REAL_GOPLS_MCP=1 to exercise the installed gopls")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skipf("gopls not installed: %v", err)
	}
	workspace, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	provider := newTestProvider(t, workspace, Options{
		StartupTimeout: 15 * time.Second,
		CallTimeout:    15 * time.Second,
	})
	capabilities, err := provider.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("real gopls capabilities: %v", err)
	}
	if !hasCapability(capabilities, "go_search") || !hasCapability(capabilities, "go_diagnostics") {
		t.Fatalf("real gopls missing expected capabilities: %+v", capabilities)
	}
	workspaceResult, err := provider.CallReadOnly(context.Background(), "go_workspace", nil)
	if err != nil || !strings.Contains(callResultText(workspaceResult), "go.mod") {
		t.Fatalf("real gopls go_workspace = %q, err=%v", callResultText(workspaceResult), err)
	}
}

// TestManagedGoplsHelperProcess is re-executed as a fake stdio MCP child by
// the two managed-process tests above.
func TestManagedGoplsHelperProcess(t *testing.T) {
	if !hasProcessArg("--codeintel-helper") {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request mcp.JSONRPCMessage
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		if request.ID == nil {
			continue
		}
		response := &mcp.JSONRPCMessage{JSONRPC: "2.0", ID: request.ID}
		switch request.Method {
		case mcp.MethodInitialize:
			response.Result = mcp.InitializeResult{
				ProtocolVersion: mcp.ProtocolVersion,
				ServerInfo:      &mcp.ServerInfo{Name: "fake-gopls", Version: "test"},
			}
		case mcp.MethodToolsList:
			response.Result = mcp.ListToolsResult{Tools: []*mcp.ToolInfo{{Name: "go_workspace"}}}
		case mcp.MethodToolsCall:
			cwd, _ := os.Getwd()
			response.Result = mcp.CallToolResult{Content: []*mcp.ContentBlock{{Type: "text", Text: cwd}}}
		default:
			response.Error = &mcp.Error{Code: mcp.ErrCodeMethodNotFound, Message: "unsupported"}
		}
		if err := encoder.Encode(response); err != nil {
			os.Exit(3)
		}
	}
	if hasProcessArg("--hang-after-eof") {
		for {
			time.Sleep(time.Second)
		}
	}
}

func hasProcessArg(want string) bool {
	for _, arg := range os.Args {
		if arg == want {
			return true
		}
	}
	return false
}

func newTestProvider(t *testing.T, workspace string, opts Options) *GoplsProvider {
	t.Helper()
	provider, err := NewGoplsProvider(workspace, opts)
	if err != nil {
		t.Fatalf("NewGoplsProvider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}

type dialRecorder struct {
	mu          sync.Mutex
	specs       []ProcessSpec
	connections []Connection
}

func (d *dialRecorder) Dial(_ context.Context, spec ProcessSpec) (Connection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.specs = append(d.specs, spec)
	index := len(d.specs) - 1
	if index >= len(d.connections) {
		return nil, fmt.Errorf("unexpected dial %d", index+1)
	}
	return d.connections[index], nil
}

func (d *dialRecorder) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.specs)
}

func (d *dialRecorder) Spec(index int) ProcessSpec {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.specs[index]
}

type fakeReceive struct {
	message *mcp.JSONRPCMessage
	err     error
	block   bool
}

type fakeMCPConnection struct {
	tools  []*mcp.ToolInfo
	onCall func(*mcp.CallToolParams) fakeReceive

	recv        chan fakeReceive
	closed      chan struct{}
	closeOnce   sync.Once
	closeCount  atomic.Int32
	toolCalls   atomic.Int32
	callStarted chan struct{}
	startedOnce sync.Once
}

func newFakeMCPConnection(tools []*mcp.ToolInfo, onCall func(*mcp.CallToolParams) fakeReceive) *fakeMCPConnection {
	return &fakeMCPConnection{
		tools: tools, onCall: onCall,
		recv: make(chan fakeReceive, 16), closed: make(chan struct{}), callStarted: make(chan struct{}),
	}
}

func (f *fakeMCPConnection) Send(request *mcp.JSONRPCMessage) error {
	select {
	case <-f.closed:
		return io.ErrClosedPipe
	default:
	}
	if request.ID == nil {
		return nil
	}
	response := &mcp.JSONRPCMessage{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case mcp.MethodInitialize:
		response.Result = mcp.InitializeResult{
			ProtocolVersion: mcp.ProtocolVersion,
			ServerInfo:      &mcp.ServerInfo{Name: "fake-gopls", Version: "test"},
		}
		f.recv <- fakeReceive{message: response}
	case mcp.MethodToolsList:
		response.Result = mcp.ListToolsResult{Tools: f.tools}
		f.recv <- fakeReceive{message: response}
	case mcp.MethodToolsCall:
		f.toolCalls.Add(1)
		f.startedOnce.Do(func() { close(f.callStarted) })
		params, _ := request.Params.(*mcp.CallToolParams)
		result := f.onCall(params)
		if result.block {
			return nil
		}
		if result.err != nil {
			f.recv <- result
			return nil
		}
		if result.message == nil {
			result.message = response
		} else {
			result.message.ID = request.ID
		}
		f.recv <- result
	default:
		response.Error = &mcp.Error{Code: mcp.ErrCodeMethodNotFound, Message: "unsupported"}
		f.recv <- fakeReceive{message: response}
	}
	return nil
}

func (f *fakeMCPConnection) Receive() (*mcp.JSONRPCMessage, error) {
	select {
	case result := <-f.recv:
		return result.message, result.err
	case <-f.closed:
		return nil, io.EOF
	}
}

func (f *fakeMCPConnection) Close(context.Context) error {
	f.closeCount.Add(1)
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeMCPConnection) Diagnostic() string { return "" }
func (f *fakeMCPConnection) CloseCount() int    { return int(f.closeCount.Load()) }
func (f *fakeMCPConnection) ToolCallCount() int { return int(f.toolCalls.Load()) }

func fakeToolText(text string) fakeReceive {
	return fakeReceive{message: &mcp.JSONRPCMessage{
		JSONRPC: "2.0",
		Result:  mcp.CallToolResult{Content: []*mcp.ContentBlock{{Type: "text", Text: text}}},
	}}
}
