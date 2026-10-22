package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxHeaderBytes = 64 << 10
const maxMessageBytes = 32 << 20

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("LSP RPC error %d: %s", e.Code, e.Message) }

// readFrame bounds headers before allocating the advertised body. LSP stdout
// is protocol-only; logging, duplicate lengths and unsupported charsets fail.
func readFrame(reader *bufio.Reader) (rpcMessage, error) {
	var message rpcMessage
	length := -1
	headerBytes := 0
	for {
		rawLine, err := reader.ReadSlice('\n')
		headerBytes += len(rawLine)
		if headerBytes > maxHeaderBytes {
			return message, fmt.Errorf("LSP header exceeds limit")
		}
		if err != nil {
			return message, err
		}
		line := string(rawLine)
		if !strings.HasSuffix(line, "\r\n") {
			return message, fmt.Errorf("invalid LSP header framing")
		}
		line = strings.TrimSuffix(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return message, fmt.Errorf("invalid LSP header")
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(key) {
		case "content-length":
			if length != -1 {
				return message, fmt.Errorf("duplicate LSP Content-Length")
			}
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 || n > maxMessageBytes {
				return message, fmt.Errorf("invalid LSP Content-Length")
			}
			length = n
		case "content-type":
			_, params, err := mime.ParseMediaType(value)
			if err != nil {
				return message, err
			}
			charset := strings.ToLower(params["charset"])
			if charset != "" && charset != "utf-8" && charset != "utf8" {
				return message, fmt.Errorf("unsupported LSP charset")
			}
		}
	}
	if length < 0 {
		return message, fmt.Errorf("missing LSP Content-Length")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(reader, data); err != nil {
		return message, err
	}
	if err := json.Unmarshal(data, &message); err != nil {
		return message, fmt.Errorf("decode LSP frame: %w", err)
	}
	if message.JSONRPC != "2.0" {
		return message, fmt.Errorf("invalid LSP JSON-RPC version")
	}
	return message, nil
}

type process struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderr     io.ReadCloser
	reader     *bufio.Reader
	writeMu    sync.Mutex
	closeOnce  sync.Once
	waitDone   chan struct{}
	stderrDone chan struct{}
	stderrMu   sync.Mutex
	diagnostic []byte
}

func startProcess(ctx context.Context, cfg *Config) (*process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.owner.Root() == "" {
		return nil, fmt.Errorf("LSP process has no plugin authority")
	}
	if data := cfg.Env["CLAUDE_PLUGIN_DATA"]; data != "" {
		if err := os.MkdirAll(data, 0700); err != nil {
			return nil, err
		}
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	setupProcess(cmd)
	cmd.Dir = cfg.WorkspaceFolder
	cmd.Env = processEnv(cfg.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		stdoutWriter.Close()
		return nil, err
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stdoutWriter.Close()
		stderr.Close()
		stderrWriter.Close()
		return nil, fmt.Errorf("start LSP %s: %w", cfg.name, err)
	}
	stdoutWriter.Close()
	stderrWriter.Close()
	p := &process{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr, reader: bufio.NewReaderSize(stdout, maxHeaderBytes), waitDone: make(chan struct{}), stderrDone: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.waitDone) }()
	go func() {
		defer close(p.stderrDone)
		buffer := make([]byte, 4096)
		for {
			n, err := stderr.Read(buffer)
			if n > 0 {
				p.stderrMu.Lock()
				remaining := (64 << 10) - len(p.diagnostic)
				if remaining > n {
					remaining = n
				}
				if remaining > 0 {
					p.diagnostic = append(p.diagnostic, buffer[:remaining]...)
				}
				p.stderrMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return p, nil
}

func (p *process) send(ctx context.Context, message rpcMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	message.JSONRPC = "2.0"
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > maxMessageBytes {
		return fmt.Errorf("LSP request exceeds byte limit")
	}
	stop := context.AfterFunc(ctx, p.forceClose)
	defer stop()
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(p.stdin, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
		return err
	}
	_, err = p.stdin.Write(data)
	return err
}
func (p *process) forceClose() {
	p.closeOnce.Do(func() { killProcess(p.cmd); p.stdin.Close(); p.stdout.Close(); p.stderr.Close() })
}
func (p *process) finish(ctx context.Context) {
	p.stdin.Close()
	select {
	case <-p.waitDone:
	case <-ctx.Done():
	}
	// Kill descendants too, including ones that inherited stdout after the
	// leader exited. Closing owned pipes then unblocks readers independently.
	p.forceClose()
	select {
	case <-p.waitDone:
	case <-time.After(time.Second):
	}
	select {
	case <-p.stderrDone:
	case <-time.After(100 * time.Millisecond):
	}
}

func processEnv(overrides map[string]string) []string {
	values := map[string]string{}
	for _, key := range []string{"PATH", "HOME", "USER", "TMPDIR", "TMP", "TEMP", "SYSTEMROOT", "SystemRoot", "WINDIR", "COMSPEC", "LANG", "LC_ALL", "LC_CTYPE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "GOPATH", "GOROOT", "GOCACHE", "GOMODCACHE", "GOPLSCACHE", "GOPROXY", "GONOPROXY", "GONOSUMDB", "GOPRIVATE", "GOTOOLCHAIN", "GOFLAGS", "CGO_ENABLED", "CC", "CXX", "VIRTUAL_ENV", "CONDA_PREFIX", "PYTHONPATH", "JAVA_HOME", "DOTNET_ROOT", "CARGO_HOME", "RUSTUP_HOME"} {
		if v, ok := os.LookupEnv(key); ok {
			values[key] = v
		}
	}
	for k, v := range overrides {
		values[k] = v
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+values[k])
	}
	return env
}
