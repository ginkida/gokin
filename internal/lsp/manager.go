package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"gokin/internal/hooks"
	"gokin/internal/security"
)

var ErrPolicyChanged = errors.New("LSP plugin authority or configuration changed; run /reload-plugins")
var ErrUnavailable = errors.New("no enabled LSP server handles this file")
var ErrClosed = errors.New("LSP manager is closed")

type Request struct {
	Operation string
	File      string
	Line      int // one-based
	Character int // one-based Unicode code point column
	Query     string
}
type Result struct {
	Server           string          `json:"server"`
	Operation        string          `json:"operation"`
	Value            json.RawMessage `json:"value"`
	PositionEncoding string          `json:"position_encoding"`
}
type DiagnosticReport struct {
	File        string       `json:"file"`
	Server      string       `json:"server"`
	Version     int          `json:"version"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	Available   bool         `json:"available"`
}
type Status struct {
	Name       string
	Extensions []string
	Running    bool
	Restarts   int
	Error      string
}

type document struct {
	text    string
	version int
	touched time.Time
}
type server struct {
	cfg       *Config
	ctx       context.Context
	cancel    context.CancelFunc
	gate      chan struct{}
	mu        sync.Mutex
	client    *client
	restarts  int
	started   bool
	lastError string
	documents map[string]document
}

type Manager struct {
	workDir   string
	authority *hooks.Manager
	mu        sync.Mutex
	reloadMu  sync.Mutex
	servers   map[string]*server
	order     []string
	closed    bool
	ctx       context.Context
	cancel    context.CancelFunc
	watchDone chan struct{}
	forks     map[string]*Manager
}

func NewManager(ctx context.Context, authority *hooks.Manager, workDir string) (*Manager, error) {
	if strings.TrimSpace(workDir) == "" {
		return nil, fmt.Errorf("LSP workspace is required")
	}
	root, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("LSP workspace must be a directory")
	}
	lifetime, cancel := context.WithCancel(ctx)
	m := &Manager{workDir: root, authority: authority, servers: map[string]*server{}, ctx: lifetime, cancel: cancel, watchDone: make(chan struct{}), forks: map[string]*Manager{}}
	if _, err = m.Reload(ctx); err != nil {
		cancel()
		return nil, err
	}
	go m.watch()
	return m, nil
}

func (m *Manager) Reload(ctx context.Context) (int, error) {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	configs, err := LoadPluginConfigs(m.authority, m.workDir)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0, ErrClosed
	}
	next := make(map[string]*server, len(configs))
	order := make([]string, 0, len(configs))
	var retired []*server
	changed := 0
	for _, cfg := range configs {
		s := m.servers[cfg.name]
		dead := false
		if s != nil {
			s.mu.Lock()
			dead = s.started && (s.client == nil || !s.client.alive())
			s.mu.Unlock()
		}
		if s == nil || s.cfg.fingerprint != cfg.fingerprint || s.ctx.Err() != nil || dead {
			lifetime, cancel := context.WithCancel(m.ctx)
			s = &server{cfg: cfg, ctx: lifetime, cancel: cancel, gate: make(chan struct{}, 1), documents: map[string]document{}}
			s.gate <- struct{}{}
			changed++
		}
		next[cfg.name] = s
		order = append(order, cfg.name)
	}
	for name, s := range m.servers {
		if next[name] != s {
			retired = append(retired, s)
			if next[name] == nil {
				changed++
			}
		}
	}
	m.servers = next
	m.order = order
	forks := make([]*Manager, 0, len(m.forks))
	for _, fork := range m.forks {
		forks = append(forks, fork)
	}
	m.mu.Unlock()
	for _, s := range retired {
		s.close()
	}
	var errs []error
	for _, fork := range forks {
		if _, err := fork.Reload(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return changed, errors.Join(errs...)
}

func (s *server) close() {
	s.cancel()
	s.mu.Lock()
	c := s.client
	s.client = nil
	s.mu.Unlock()
	if c != nil {
		c.close()
	}
}
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	servers := m.servers
	forks := m.forks
	m.servers = map[string]*server{}
	m.order = nil
	m.mu.Unlock()
	m.cancel()
	select {
	case <-m.watchDone:
	case <-time.After(2 * time.Second):
		// Discovery may be stuck in a plugin filesystem read. The closed
		// manager has withdrawn all servers, so late discovery cannot revive one.
	}
	for _, s := range servers {
		s.close()
	}
	for _, fork := range forks {
		_ = fork.Close()
	}
	return nil
}

// ForWorkDir owns a separate lazy server/index for an isolated agent. The
// parent closes these managers too, so cloned registries never leak processes.
func (m *Manager) ForWorkDir(workDir string) (*Manager, error) {
	root, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	if root == m.workDir {
		return m, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if fork := m.forks[root]; fork != nil {
		return fork, nil
	}
	if len(m.forks) >= 32 {
		return nil, fmt.Errorf("LSP isolated-workspace limit reached")
	}
	fork, err := NewManager(m.ctx, m.authority, root)
	if err != nil {
		return nil, err
	}
	m.forks[root] = fork
	return fork, nil
}
func (m *Manager) watch() {
	defer close(m.watchDone)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			configs, err := LoadPluginConfigs(m.authority, m.workDir)
			current := map[string]string{}
			if err == nil {
				for _, cfg := range configs {
					current[cfg.name] = cfg.fingerprint
				}
			}
			m.mu.Lock()
			var retired []*server
			for name, s := range m.servers {
				if s.cfg.fingerprint != current[name] && s.ctx.Err() == nil {
					retired = append(retired, s)
				}
			}
			m.mu.Unlock()
			for _, s := range retired {
				s.close()
			}
		}
	}
}
func (m *Manager) check(s *server) error {
	if s.ctx.Err() != nil {
		return ErrPolicyChanged
	}
	if err := s.cfg.owner.CheckSelected(m.authority); err != nil {
		return fmt.Errorf("%w: %v", ErrPolicyChanged, err)
	}
	configs, err := LoadPluginConfigs(m.authority, m.workDir)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPolicyChanged, err)
	}
	for _, cfg := range configs {
		if cfg.name == s.cfg.name && cfg.fingerprint == s.cfg.fingerprint {
			return nil
		}
	}
	return ErrPolicyChanged
}

func (m *Manager) route(file string) (*server, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	var chosen *server
	length := 0
	for _, name := range m.order {
		s := m.servers[name]
		for ext := range s.cfg.ExtensionToLanguage {
			if strings.HasSuffix(file, ext) && len(ext) > length {
				chosen = s
				length = len(ext)
			}
		}
	}
	if chosen == nil {
		return nil, ErrUnavailable
	}
	return chosen, nil
}
func (m *Manager) HasFile(file string) bool { _, err := m.route(file); return err == nil }
func (m *Manager) Status() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.order))
	for _, name := range m.order {
		s := m.servers[name]
		row := Status{Name: name}
		for ext := range s.cfg.ExtensionToLanguage {
			row.Extensions = append(row.Extensions, ext)
		}
		sort.Strings(row.Extensions)
		s.mu.Lock()
		row.Running = s.ctx.Err() == nil && s.client != nil && s.client.alive()
		row.Restarts = s.restarts
		row.Error = s.lastError
		if s.ctx.Err() != nil {
			row.Error = ErrPolicyChanged.Error()
		}
		s.mu.Unlock()
		out = append(out, row)
	}
	return out
}

func (m *Manager) withServer(ctx context.Context, file string, run func(context.Context, *server, *client, string) error) error {
	path, err := m.validateFile(file)
	if err != nil {
		return err
	}
	s, err := m.route(path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.timeout("request"))
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.gate:
	}
	defer func() { s.gate <- struct{}{} }()
	if err := m.check(s); err != nil {
		return err
	}
	c, err := m.ensureClient(ctx, s)
	if err != nil {
		return err
	}
	if err := m.check(s); err != nil {
		s.close()
		return err
	}
	err = run(ctx, s, c, path)
	if policyErr := m.check(s); policyErr != nil {
		return policyErr
	}
	return err
}

func (m *Manager) ensureClient(ctx context.Context, s *server) (*client, error) {
	s.mu.Lock()
	c := s.client
	started := s.started
	restarts := s.restarts
	s.mu.Unlock()
	if c != nil && c.alive() {
		return c, nil
	}
	if c != nil {
		c.close()
	}
	max := 3
	if s.cfg.MaxRestarts != nil {
		max = *s.cfg.MaxRestarts
	}
	if started && ((s.cfg.RestartOnCrash != nil && !*s.cfg.RestartOnCrash) || restarts >= max) {
		return nil, fmt.Errorf("LSP %s stopped; restart limit reached or restartOnCrash is false", s.cfg.name)
	}
	startup, cancel := context.WithTimeout(ctx, s.cfg.timeout("startup"))
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	c, err := newClient(startup, s.cfg)
	s.mu.Lock()
	defer s.mu.Unlock()
	if started {
		s.restarts++
	}
	if err != nil {
		if c != nil {
			s.started = true
		}
		s.lastError = err.Error()
		return nil, err
	}
	if s.ctx.Err() != nil {
		c.close()
		return nil, ErrPolicyChanged
	}
	s.started = true
	s.lastError = ""
	s.client = c
	s.documents = map[string]document{}
	// The process lifetime belongs to this selected server, not the startup
	// or single-query deadline. Revocation interrupts blocked pipe operations.
	context.AfterFunc(s.ctx, c.process.forceClose)
	return c, nil
}

func (m *Manager) validateFile(file string) (string, error) {
	if file == "" {
		return "", fmt.Errorf("LSP filePath is required")
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(m.workDir, file)
	}
	return security.NewPathValidator([]string{m.workDir}, false).Validate(file)
}
func (m *Manager) readFile(path string) (string, error) {
	rel, err := filepath.Rel(m.workDir, path)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(m.workDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return "", fmt.Errorf("LSP source must be a regular file of at most 2 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 2<<20 || !utf8.Valid(data) {
		return "", fmt.Errorf("LSP source must be bounded UTF-8")
	}
	return string(data), nil
}

func (m *Manager) syncDocument(ctx context.Context, s *server, c *client, path string) (document, error) {
	if _, err := security.NewPathValidator([]string{s.cfg.WorkspaceFolder}, false).Validate(path); err != nil {
		return document{}, fmt.Errorf("file is outside LSP workspaceFolder")
	}
	text, err := m.readFile(path)
	if err != nil {
		return document{}, err
	}
	uri := fileURI(path)
	old, open := s.documents[uri]
	if open && old.text == text {
		old.touched = time.Now()
		s.documents[uri] = old
		return old, nil
	}
	doc := document{text: text, version: old.version + 1, touched: time.Now()}
	if !open && len(s.documents) >= 64 {
		var oldest string
		var at time.Time
		for uri, item := range s.documents {
			if oldest == "" || item.touched.Before(at) {
				oldest, at = uri, item.touched
			}
		}
		if c.openClose {
			if err := c.notify(ctx, "textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": oldest}}); err != nil {
				return document{}, err
			}
		}
		delete(s.documents, oldest)
		c.mu.Lock()
		delete(c.versions, oldest)
		delete(c.diagnostics, oldest)
		c.mu.Unlock()
	}
	c.mu.Lock()
	c.versions[uri] = doc.version
	delete(c.diagnostics, uri)
	c.mu.Unlock()
	language := ""
	longest := 0
	for ext, id := range s.cfg.ExtensionToLanguage {
		if strings.HasSuffix(path, ext) && len(ext) > longest {
			language = id
			longest = len(ext)
		}
	}
	if !open && c.openClose {
		err = c.notify(ctx, "textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": language, "version": doc.version, "text": text}})
	} else if c.syncKind != 0 {
		change := map[string]any{"text": text}
		if c.syncKind == 2 && open {
			change["range"] = Range{Start: Position{}, End: endPosition(old.text, c.encoding)}
		}
		err = c.notify(ctx, "textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": doc.version}, "contentChanges": []any{change}})
	}
	if err != nil {
		return document{}, err
	}
	if c.save {
		if err := c.notify(ctx, "textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}, "text": text}); err != nil {
			return document{}, err
		}
	}
	s.documents[uri] = doc
	return doc, nil
}

var operations = map[string]string{
	"goToDefinition": "textDocument/definition", "findReferences": "textDocument/references", "hover": "textDocument/hover",
	"documentSymbol": "textDocument/documentSymbol", "workspaceSymbol": "workspace/symbol", "goToImplementation": "textDocument/implementation",
	"prepareCallHierarchy": "textDocument/prepareCallHierarchy", "incomingCalls": "callHierarchy/incomingCalls", "outgoingCalls": "callHierarchy/outgoingCalls",
}

func ValidOperation(operation string) bool { _, ok := operations[operation]; return ok }

func (m *Manager) Query(ctx context.Context, req Request) (Result, error) {
	result := Result{Operation: req.Operation}
	method, ok := operations[req.Operation]
	if !ok {
		return result, fmt.Errorf("unsupported LSP operation %q", req.Operation)
	}
	err := m.withServer(ctx, req.File, func(ctx context.Context, s *server, c *client, path string) error {
		doc, err := m.syncDocument(ctx, s, c, path)
		if err != nil {
			return err
		}
		result.Server = s.cfg.name
		result.PositionEncoding = c.encoding
		params := map[string]any{"textDocument": map[string]any{"uri": fileURI(path)}}
		if req.Operation == "workspaceSymbol" {
			params = map[string]any{"query": req.Query}
		} else if req.Operation != "documentSymbol" {
			position, err := sourcePosition(doc.text, req.Line, req.Character, c.encoding)
			if err != nil {
				return err
			}
			params["position"] = position
		}
		if req.Operation == "findReferences" {
			params["context"] = map[string]any{"includeDeclaration": true}
		}
		if req.Operation == "incomingCalls" || req.Operation == "outgoingCalls" {
			var items []json.RawMessage
			if err := c.request(ctx, "textDocument/prepareCallHierarchy", params, &items); err != nil {
				return err
			}
			if len(items) > 128 {
				return fmt.Errorf("LSP call hierarchy exceeds limit")
			}
			all := []json.RawMessage{}
			for _, item := range items {
				var calls []json.RawMessage
				if err := c.request(ctx, method, map[string]any{"item": item}, &calls); err != nil {
					return err
				}
				all = append(all, calls...)
				if len(all) > 10000 {
					return fmt.Errorf("LSP call hierarchy exceeds limit")
				}
			}
			result.Value, _ = json.Marshal(all)
			return nil
		}
		return c.request(ctx, method, params, &result.Value)
	})
	return result, err
}

// Diagnose synchronizes the current file, then uses pull diagnostics when
// advertised, or waits for an explicit current publishDiagnostics batch.
// Silence/timeouts never count as a clean answer.
func (m *Manager) Diagnose(ctx context.Context, file string) (DiagnosticReport, error) {
	report := DiagnosticReport{File: file}
	err := m.withServer(ctx, file, func(ctx context.Context, s *server, c *client, path string) error {
		report.File = path
		report.Server = s.cfg.name
		if s.cfg.Diagnostics != nil && !*s.cfg.Diagnostics {
			return nil
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			uri := fileURI(path)
			if _, open := s.documents[uri]; open && c.openClose {
				_ = c.notify(ctx, "textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": uri}})
			}
			delete(s.documents, uri)
			c.mu.Lock()
			delete(c.versions, uri)
			delete(c.diagnostics, uri)
			c.mu.Unlock()
			return nil
		}
		doc, err := m.syncDocument(ctx, s, c, path)
		if err != nil {
			return err
		}
		report.Version = doc.version
		uri := fileURI(path)
		if raw := c.capabilities["diagnosticProvider"]; len(raw) > 0 && string(raw) != "false" && string(raw) != "null" {
			var pulled struct {
				Kind  string       `json:"kind"`
				Items []Diagnostic `json:"items"`
			}
			if err := c.request(ctx, "textDocument/diagnostic", map[string]any{"textDocument": map[string]any{"uri": uri}}, &pulled); err != nil {
				return err
			}
			if pulled.Kind != "full" {
				return fmt.Errorf("LSP diagnostics returned no full report")
			}
			if len(pulled.Items) > 10000 {
				return fmt.Errorf("LSP diagnostics exceed limit")
			}
			report.Diagnostics = pulled.Items
			report.Available = true
			return nil
		}
		wait, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		for {
			c.mu.Lock()
			batch, known := c.diagnostics[uri]
			c.mu.Unlock()
			if known {
				report.Diagnostics = append([]Diagnostic(nil), batch.Diagnostics...)
				report.Available = true
				return nil
			}
			select {
			case <-wait.Done():
				return fmt.Errorf("LSP did not publish current diagnostics: %w", wait.Err())
			case <-c.done:
				return fmt.Errorf("LSP stopped before publishing diagnostics")
			case <-c.updates:
			}
		}
	})
	return report, err
}

func fileURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
func FilePath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("LSP location is not a local file URI")
	}
	p := u.Path
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p), nil
}
func sourcePosition(text string, line, column int, encoding string) (Position, error) {
	if line < 1 || column < 1 {
		return Position{}, fmt.Errorf("LSP line and character must be one-based positive integers")
	}
	lines := strings.Split(text, "\n")
	if line > len(lines) {
		return Position{}, fmt.Errorf("LSP line exceeds source")
	}
	content := strings.TrimSuffix(lines[line-1], "\r")
	runes := []rune(content)
	if column-1 > len(runes) {
		return Position{}, fmt.Errorf("LSP character exceeds source line")
	}
	prefix := runes[:column-1]
	units := len(prefix)
	if encoding == "utf-16" {
		units = len(utf16.Encode(prefix))
	} else if encoding == "utf-8" {
		units = len(string(prefix))
	}
	return Position{Line: line - 1, Character: units}, nil
}
func endPosition(text, encoding string) Position {
	lines := strings.Split(text, "\n")
	position, _ := sourcePosition(text, len(lines), len([]rune(strings.TrimSuffix(lines[len(lines)-1], "\r")))+1, encoding)
	return position
}
