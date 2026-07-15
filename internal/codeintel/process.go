package codeintel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"gokin/internal/mcp"
)

type processConnection struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	encoder *json.Encoder
	scanner *bufio.Scanner

	writeMu sync.Mutex
	stateMu sync.Mutex
	closed  bool

	waitDone chan struct{}
	waitErr  error

	stderrDone chan struct{}
	diagnostic boundedCapture
}

func dialProcess(ctx context.Context, spec ProcessSpec) (Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = append([]string(nil), spec.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open gopls stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open gopls stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("open gopls stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("start %s: %w", spec.Command, err)
	}

	connection := &processConnection{
		cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr,
		encoder:    json.NewEncoder(stdin),
		scanner:    bufio.NewScanner(stdout),
		waitDone:   make(chan struct{}),
		stderrDone: make(chan struct{}),
		diagnostic: boundedCapture{limit: spec.MaxStderrBytes},
	}
	connection.scanner.Buffer(make([]byte, 0, 64<<10), spec.MaxMessageBytes)
	go connection.wait()
	go connection.drainStderr()
	return connection, nil
}

func (c *processConnection) Send(message *mcp.JSONRPCMessage) error {
	if message == nil {
		return fmt.Errorf("send nil MCP message")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.stateMu.Lock()
	closed := c.closed
	c.stateMu.Unlock()
	if closed {
		return io.ErrClosedPipe
	}
	message.JSONRPC = "2.0"
	if err := c.encoder.Encode(message); err != nil {
		return fmt.Errorf("write gopls MCP message: %w", err)
	}
	return nil
}

func (c *processConnection) Receive() (*mcp.JSONRPCMessage, error) {
	for c.scanner.Scan() {
		line := c.scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var message mcp.JSONRPCMessage
		if err := json.Unmarshal(line, &message); err != nil {
			return nil, fmt.Errorf("decode gopls MCP message: %w", err)
		}
		return &message, nil
	}
	if err := c.scanner.Err(); err != nil {
		return nil, fmt.Errorf("read gopls MCP message: %w", err)
	}
	return nil, io.EOF
}

func (c *processConnection) Close(ctx context.Context) error {
	ctx = nonNilContext(ctx)
	c.stateMu.Lock()
	firstClose := !c.closed
	c.closed = true
	c.stateMu.Unlock()
	if firstClose {
		// stdin EOF is the graceful MCP-stdio shutdown signal. Closing stdout and
		// stderr before the child exits can turn an otherwise clean shutdown into
		// SIGPIPE, so leave those read ends open. If the child ignores EOF, the
		// bounded branch below kills it, which closes both pipes and unblocks any
		// in-flight receiver/drainer.
		_ = c.stdin.Close()
	}

	forced := false
	select {
	case <-c.waitDone:
	case <-ctx.Done():
		forced = true
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		killTimer := time.NewTimer(time.Second)
		select {
		case <-c.waitDone:
			killTimer.Stop()
		case <-killTimer.C:
			return fmt.Errorf("gopls process did not exit after kill: %w", ctx.Err())
		}
	}
	_ = c.stdout.Close()
	_ = c.stderr.Close()

	stderrTimer := time.NewTimer(100 * time.Millisecond)
	select {
	case <-c.stderrDone:
		stderrTimer.Stop()
	case <-stderrTimer.C:
	}
	c.stateMu.Lock()
	waitErr := c.waitErr
	c.stateMu.Unlock()
	if forced {
		return fmt.Errorf("gopls graceful shutdown timed out; process killed: %w", ctx.Err())
	}
	if waitErr != nil {
		return fmt.Errorf("gopls process exit: %w%s", waitErr, diagnosticSuffix(c.Diagnostic()))
	}
	return nil
}

func (c *processConnection) Diagnostic() string {
	return c.diagnostic.String()
}

func (c *processConnection) wait() {
	err := c.cmd.Wait()
	c.stateMu.Lock()
	c.waitErr = err
	c.stateMu.Unlock()
	close(c.waitDone)
}

func (c *processConnection) drainStderr() {
	_, _ = io.Copy(&c.diagnostic, c.stderr)
	close(c.stderrDone)
}

type boundedCapture struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	truncated bool
}

func (b *boundedCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	remaining := b.limit - len(b.data)
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		b.data = append(b.data, p[:remaining]...)
	}
	if remaining < len(p) {
		b.truncated = true
	}
	return written, nil
}

func (b *boundedCapture) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	value := strings.TrimSpace(string(b.data))
	if b.truncated {
		value += " [truncated]"
	}
	return value
}

func managedGoEnv() []string {
	// gopls is a trusted, fixed child process. Pass only execution/cache/toolchain
	// variables it needs, never API keys or arbitrary user secrets. Go-specific
	// entries are local to this provider; the generic MCP safe-env policy remains
	// unchanged.
	keys := []string{
		"PATH", "HOME", "USER", "TMPDIR", "TMP", "TEMP",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
		"GOPATH", "GOROOT", "GOCACHE", "GOMODCACHE", "GOENV", "GOFLAGS",
		"GOPLSCACHE",
		"GOPROXY", "GONOPROXY", "GONOSUMDB", "GOPRIVATE", "GOTOOLCHAIN",
		"CGO_ENABLED", "CC", "CXX", "LANG", "LC_ALL", "LC_CTYPE",
	}
	env := make([]string, 0, len(keys)+1)
	hasPath := false
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok && value != "" {
			env = append(env, key+"="+value)
			if key == "PATH" {
				hasPath = true
			}
		}
	}
	if !hasPath {
		env = append(env, "PATH=/usr/local/bin:/usr/bin:/bin")
	}
	return env
}
