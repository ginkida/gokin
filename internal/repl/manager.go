package repl

import (
	"bufio"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed worker.py
var workerSource []byte

type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Code   string `json:"code,omitempty"`
}

type response struct {
	ID string `json:"id"`
	OK bool   `json:"ok"`
	Result
}

// Manager owns exactly one session-scoped worker. Execute calls are serialized
// because Python globals form a single ordered state machine. Cancellation or a
// protocol violation kills the complete worker process group; the next call
// starts a clean generation instead of trusting possibly-corrupt state.
type Manager struct {
	opts Options

	mu                sync.Mutex
	closed            bool
	cmd               *exec.Cmd
	stdin             io.WriteCloser
	stdout            *bufio.Reader
	waitCh            chan error
	stderrDone        chan struct{}
	lifetimeCancel    context.CancelFunc
	runtimeDir        string
	workerPath        string
	generation        uint64
	stderr            *boundedTail
	handlerMu         sync.RWMutex
	callHandler       CallHandler
	executions        uint64
	transportFailures uint64
	timeouts          uint64
	lastError         string
	lastFailureAt     time.Time
	manualResets      uint64
}

// NewManager creates a production manager for an explicitly selected secure
// backend. Call Detect first when implementing auto mode.
func NewManager(opts Options) (*Manager, error) {
	return newManager(opts, false)
}

func newTestManager(opts Options) (*Manager, error) {
	opts.Backend = BackendTest
	return newManager(opts, true)
}

func newManager(opts Options, allowTest bool) (*Manager, error) {
	opts = opts.withDefaults()
	root, err := canonicalWorkDir(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	opts.WorkDir = root
	if strings.TrimSpace(opts.PythonPath) == "" {
		opts.PythonPath, err = exec.LookPath("python3")
		if err != nil {
			return nil, fmt.Errorf("%w: python3 was not found", ErrUnavailable)
		}
	}
	python, err := filepath.Abs(opts.PythonPath)
	if err != nil {
		return nil, fmt.Errorf("resolve python path: %w", err)
	}
	if info, statErr := os.Stat(python); statErr != nil || info.IsDir() {
		if statErr == nil {
			statErr = fmt.Errorf("path is a directory")
		}
		return nil, fmt.Errorf("invalid python path: %w", statErr)
	}
	python, err = resolvePythonExecutable(python)
	if err != nil {
		return nil, fmt.Errorf("resolve Python runtime: %w", err)
	}
	opts.PythonPath = python
	if strings.TrimSpace(opts.GitPath) == "" {
		opts.GitPath = discoverGitExecutable()
	}
	if opts.Backend == BackendTest && !allowTest {
		return nil, fmt.Errorf("%w: unrestricted test backend is not available in production", ErrUnavailable)
	}
	if opts.Backend == BackendNone || opts.Backend == "" {
		return nil, fmt.Errorf("%w: a secure backend is required", ErrUnavailable)
	}
	if err := backendExecutableAvailable(opts.Backend); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	return &Manager{
		opts:   opts,
		stderr: newBoundedTail(32 * 1024),
	}, nil
}

// resolvePythonExecutable turns platform launcher shims (notably macOS
// /usr/bin/python3 -> xcrun) into the interpreter they would exec. Invoking the
// real binary inside the sandbox avoids granting the shim a writable global
// xcrun cache directory.
func resolvePythonExecutable(candidate string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, candidate, "-I", "-c", "import os,sys; print(os.path.realpath(sys.executable))")
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin",
		"LANG=C", "LC_ALL=C", "PYTHONDONTWRITEBYTECODE=1",
	}
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	resolved := strings.TrimSpace(string(output))
	if !filepath.IsAbs(resolved) {
		return "", fmt.Errorf("interpreter reported non-absolute executable %q", resolved)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("interpreter executable is a directory")
	}
	return resolved, nil
}

func discoverGitExecutable() string {
	var candidate string
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := exec.CommandContext(ctx, "/usr/bin/xcrun", "--find", "git")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
		if output, err := cmd.Output(); err == nil {
			candidate = strings.TrimSpace(string(output))
		}
		cancel()
	}
	if candidate == "" {
		candidate, _ = exec.LookPath("git")
	}
	if candidate == "" {
		return ""
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if info, err := os.Stat(abs); err != nil || info.IsDir() {
		return ""
	}
	return abs
}

// Execute evaluates one Python cell, preserving globals from earlier successful
// or failed cells in the same generation.
func (m *Manager) Execute(ctx context.Context, code string) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len([]byte(code)) > m.opts.MaxCodeBytes {
		return Result{}, fmt.Errorf("REPL code exceeds %d-byte limit", m.opts.MaxCodeBytes)
	}
	if strings.TrimSpace(code) == "" {
		return Result{}, fmt.Errorf("REPL code must not be empty")
	}
	requestID, err := newProtocolID("req-")
	if err != nil {
		return Result{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Result{}, fmt.Errorf("REPL manager is closed")
	}
	if err := m.startLocked(ctx); err != nil {
		m.recordTransportFailureLocked(err)
		return Result{}, err
	}

	resp, err := m.roundTripLocked(ctx, request{
		ID: requestID, Method: "exec", Code: code,
	}, m.opts.CellTimeout)
	if err != nil {
		m.recordTransportFailureLocked(err)
		m.stopLocked()
		return Result{}, err
	}
	m.executions++
	return resp.Result, nil
}

func (m *Manager) recordTransportFailureLocked(err error) {
	if err == nil {
		return
	}
	m.transportFailures++
	if errors.Is(err, context.DeadlineExceeded) {
		m.timeouts++
	}
	m.lastError = truncateUTF8Bytes(err.Error(), 1024)
	m.lastFailureAt = time.Now().UTC()
}

func truncateUTF8Bytes(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

func (m *Manager) startLocked(ctx context.Context) error {
	if m.cmd != nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.prepareRuntimeLocked(); err != nil {
		return err
	}
	m.generation++
	lifetimeCtx, lifetimeCancel := context.WithCancel(context.Background())
	cmd, err := buildWorkerCommand(lifetimeCtx, m.opts, m.runtimeDir, m.workerPath, m.generation)
	if err != nil {
		lifetimeCancel()
		return fmt.Errorf("start REPL generation %d: %w", m.generation, err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		lifetimeCancel()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		lifetimeCancel()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		lifetimeCancel()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		lifetimeCancel()
		return fmt.Errorf("launch REPL generation %d: %w", m.generation, err)
	}

	m.stderr.Reset()
	m.cmd = cmd
	m.stdin = stdin
	m.stdout = bufio.NewReaderSize(stdout, 64*1024)
	m.waitCh = make(chan error, 1)
	stderrDone := make(chan struct{})
	m.stderrDone = stderrDone
	m.lifetimeCancel = lifetimeCancel
	go func(done chan struct{}) {
		defer close(done)
		_, _ = io.Copy(m.stderr, stderr)
	}(stderrDone)
	go func() { m.waitCh <- cmd.Wait() }()

	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	pingID, idErr := newProtocolID("ping-")
	if idErr != nil {
		m.stopLocked()
		return idErr
	}
	resp, err := m.roundTripLocked(probeCtx, request{
		ID: pingID, Method: "ping",
	}, 2*time.Second)
	if err != nil || !resp.OK || resp.Value != "pong" || resp.Generation != m.generation {
		if err == nil {
			err = fmt.Errorf("invalid ping response")
		}
		m.stopLocked()
		// stopLocked joins the worker, which closes stderr and lets the drain
		// goroutine publish its final diagnostic bytes before we snapshot them.
		stderrTail := m.stderr.String()
		if stderrTail != "" {
			return fmt.Errorf("REPL startup probe failed: %w (stderr: %s)", err, stderrTail)
		}
		return fmt.Errorf("REPL startup probe failed: %w", err)
	}
	return nil
}

func (m *Manager) prepareRuntimeLocked() error {
	if m.runtimeDir != "" && m.workerPath != "" {
		return nil
	}
	runtimeDir, err := os.MkdirTemp("", "gokin-repl-")
	if err != nil {
		return fmt.Errorf("create REPL runtime directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(runtimeDir) }
	if err := os.Chmod(runtimeDir, 0700); err != nil {
		cleanup()
		return fmt.Errorf("secure REPL runtime directory: %w", err)
	}
	// macOS exposes /var as a symlink to /private/var. Seatbelt evaluates the
	// canonical path, so an allow rule for the spelling returned by TMPDIR does
	// not override the deny on /private/var/folders. Canonicalize before writing
	// the worker or generating the sandbox profile.
	canonicalRuntimeDir, err := filepath.EvalSymlinks(runtimeDir)
	if err != nil {
		cleanup()
		return fmt.Errorf("resolve REPL runtime directory: %w", err)
	}
	runtimeDir = canonicalRuntimeDir
	workerPath := filepath.Join(runtimeDir, "worker.py")
	if err := os.WriteFile(workerPath, workerSource, 0600); err != nil {
		cleanup()
		return fmt.Errorf("write REPL worker: %w", err)
	}
	m.runtimeDir = runtimeDir
	m.workerPath = workerPath
	return nil
}

func (m *Manager) roundTripLocked(ctx context.Context, req request, inactivity time.Duration) (response, error) {
	var zero response
	encoded, err := json.Marshal(req)
	if err != nil {
		return zero, err
	}
	encoded = append(encoded, '\n')
	if _, err := m.stdin.Write(encoded); err != nil {
		return zero, fmt.Errorf("write REPL request: %w", err)
	}

	callbacks := 0
	for {
		read, err := m.readFrameWithInactivity(ctx, inactivity)
		if err != nil {
			return zero, err
		}
		var envelope struct {
			Type   string         `json:"type"`
			ID     string         `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(read, &envelope); err != nil {
			return zero, fmt.Errorf("decode REPL frame: %w", err)
		}
		if envelope.Type == "call" {
			if err := validateCallbackEnvelope(envelope.ID, envelope.Method); err != nil {
				return zero, err
			}
			callbacks++
			if callbacks > m.opts.MaxCallbacks {
				return zero, fmt.Errorf("REPL cell exceeded %d orchestrator callbacks", m.opts.MaxCallbacks)
			}
			result, callErr := m.invokeCallHandler(ctx, Call{
				ID: envelope.ID, Method: envelope.Method, Params: envelope.Params,
			})
			callResponse := map[string]any{
				"type": "call_result", "id": envelope.ID, "ok": callErr == nil,
			}
			if callErr != nil {
				callResponse["error"] = callErr.Error()
			} else {
				callResponse["result"] = result
			}
			if err := m.writeFrameLocked(callResponse); err != nil {
				return zero, err
			}
			continue
		}
		if envelope.Type != "response" {
			return zero, fmt.Errorf("unexpected REPL frame type %q", envelope.Type)
		}
		if err := json.Unmarshal(read, &zero); err != nil {
			return response{}, fmt.Errorf("decode REPL response: %w", err)
		}
		if zero.ID != req.ID {
			return response{}, fmt.Errorf("REPL protocol response id %q does not match request %q", zero.ID, req.ID)
		}
		if zero.Generation != m.generation {
			return response{}, fmt.Errorf("REPL protocol response generation %d does not match active generation %d", zero.Generation, m.generation)
		}
		return zero, nil
	}
}

func newProtocolID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate REPL protocol id: %w", err)
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}

func validateCallbackEnvelope(id, method string) error {
	if len(id) != len("call_")+32 || !strings.HasPrefix(id, "call_") {
		return fmt.Errorf("invalid REPL callback id")
	}
	if _, err := hex.DecodeString(id[len("call_"):]); err != nil {
		return fmt.Errorf("invalid REPL callback id")
	}
	if len(method) == 0 || len(method) > 128 {
		return fmt.Errorf("invalid REPL callback method")
	}
	for i, r := range method {
		valid := r >= 'a' && r <= 'z'
		if i > 0 {
			valid = valid || r >= '0' && r <= '9' || r == '_' || r == '.'
		}
		if !valid {
			return fmt.Errorf("invalid REPL callback method")
		}
	}
	return nil
}

func (m *Manager) readFrameWithInactivity(ctx context.Context, inactivity time.Duration) ([]byte, error) {
	type readResult struct {
		data []byte
		err  error
	}
	readCh := make(chan readResult, 1)
	reader := m.stdout
	limit := m.opts.MaxResponseBytes
	go func() {
		data, err := readFrame(reader, limit)
		readCh <- readResult{data: data, err: err}
	}()
	var timer *time.Timer
	var timeout <-chan time.Time
	if inactivity > 0 {
		timer = time.NewTimer(inactivity)
		timeout = timer.C
		defer timer.Stop()
	}
	select {
	case <-ctx.Done():
		terminateProcessTree(m.cmd)
		return nil, fmt.Errorf("REPL cell interrupted: %w", ctx.Err())
	case <-timeout:
		terminateProcessTree(m.cmd)
		return nil, fmt.Errorf("REPL cell inactive for %v: %w", inactivity, context.DeadlineExceeded)
	case read := <-readCh:
		if read.err != nil {
			return nil, fmt.Errorf("read REPL response: %w", read.err)
		}
		return read.data, nil
	}
}

func (m *Manager) writeFrameLocked(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode REPL callback response: %w", err)
	}
	if len(encoded) > m.opts.MaxResponseBytes {
		return fmt.Errorf("REPL callback response exceeds %d-byte limit", m.opts.MaxResponseBytes)
	}
	encoded = append(encoded, '\n')
	if _, err := m.stdin.Write(encoded); err != nil {
		return fmt.Errorf("write REPL callback response: %w", err)
	}
	return nil
}

func (m *Manager) invokeCallHandler(ctx context.Context, call Call) (result any, err error) {
	m.handlerMu.RLock()
	handler := m.callHandler
	m.handlerMu.RUnlock()
	if handler == nil {
		return nil, fmt.Errorf("orchestrator callback %q is unavailable", call.Method)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			result = nil
			err = fmt.Errorf("orchestrator callback %q panicked: %v", call.Method, recovered)
		}
	}()
	return handler(ctx, call)
}

// SetCallHandler installs the typed worker-to-Go callback boundary. It is safe
// to wire after Manager construction and before or during ordinary cell use.
func (m *Manager) SetCallHandler(handler CallHandler) {
	if m == nil {
		return
	}
	m.handlerMu.Lock()
	m.callHandler = handler
	m.handlerMu.Unlock()
}

func readFrame(reader *bufio.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = DefaultMaxResponseBytes
	}
	frame := make([]byte, 0, min(limit, 64*1024))
	for {
		part, err := reader.ReadSlice('\n')
		if len(frame)+len(part) > limit {
			return nil, fmt.Errorf("REPL response exceeds %d-byte limit", limit)
		}
		frame = append(frame, part...)
		if err == nil {
			return frame, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

func (m *Manager) stopLocked() {
	if m.cmd == nil {
		return
	}
	if m.lifetimeCancel != nil {
		m.lifetimeCancel()
	}
	terminateProcessTree(m.cmd)
	if m.stdin != nil {
		_ = m.stdin.Close()
	}
	if m.waitCh != nil {
		select {
		case <-m.waitCh:
		case <-time.After(2 * time.Second):
		}
	}
	if m.stderrDone != nil {
		select {
		case <-m.stderrDone:
		case <-time.After(500 * time.Millisecond):
		}
	}
	m.cmd = nil
	m.stdin = nil
	m.stdout = nil
	m.waitCh = nil
	m.stderrDone = nil
	m.lifetimeCancel = nil
}

// Close terminates the worker and removes all ephemeral runtime artifacts.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.stopLocked()
	runtimeDir := m.runtimeDir
	m.mu.Unlock()
	if runtimeDir == "" {
		return nil
	}
	return os.RemoveAll(runtimeDir)
}

// Reset discards Python globals and artifacts without closing the session
// owner. The next Execute lazily starts a fresh generation.
func (m *Manager) Reset(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("REPL manager is closed")
	}
	m.stopLocked()
	m.manualResets++
	return nil
}

// Stats returns a concurrency-safe operational snapshot.
func (m *Manager) Stats() Stats {
	if m == nil {
		return Stats{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	restarts := uint64(0)
	if m.generation > 1 {
		restarts = m.generation - 1
	}
	return Stats{
		Generation: m.generation, Running: m.cmd != nil,
		Restarts: restarts, ManualResets: m.manualResets,
		Executions: m.executions, TransportFailures: m.transportFailures,
		Timeouts: m.timeouts, LastError: m.lastError, LastFailureAt: m.lastFailureAt,
	}
}

// Generation reports the current kernel generation. It is zero until the
// first successful or attempted start.
func (m *Manager) Generation() uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.generation
}

type boundedTail struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newBoundedTail(limit int) *boundedTail { return &boundedTail{limit: limit} }

func (b *boundedTail) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if n >= b.limit {
		b.data = append(b.data[:0], p[n-b.limit:]...)
		return n, nil
	}
	if excess := len(b.data) + n - b.limit; excess > 0 {
		copy(b.data, b.data[excess:])
		b.data = b.data[:len(b.data)-excess]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (b *boundedTail) Reset() {
	b.mu.Lock()
	b.data = b.data[:0]
	b.mu.Unlock()
}

func (b *boundedTail) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(append([]byte(nil), b.data...)))
}
