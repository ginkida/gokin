package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"gokin/internal/logging"
	"google.golang.org/genai"
	"gopkg.in/yaml.v3"
)

type asyncScopeKey struct{}
type asyncWorkerKey struct{}
type asyncRewakeGuardKey struct{}

type asyncWakeRevokedError struct{ cause error }

func (e *asyncWakeRevokedError) Error() string {
	return "Async hook wake authority revoked: " + e.cause.Error()
}
func (e *asyncWakeRevokedError) Unwrap() error          { return e.cause }
func (*asyncWakeRevokedError) AutomaticRetrySafe() bool { return false }
func ContextWithAsyncRewakeGuard(ctx context.Context, guard func() error) context.Context {
	return context.WithValue(ctx, asyncRewakeGuardKey{}, guard)
}
func AsyncRewakeRequestGuard(ctx context.Context) error {
	if guard, ok := ctx.Value(asyncRewakeGuardKey{}).(func() error); ok && guard != nil {
		if err := guard(); err != nil {
			return &asyncWakeRevokedError{err}
		}
	}
	return nil
}

type asyncWorker struct {
	job   *asyncJob
	input []byte
	info  RuntimeInfo
}

func asyncWorkerFromContext(ctx context.Context) *asyncWorker {
	worker, _ := ctx.Value(asyncWorkerKey{}).(*asyncWorker)
	return worker
}

type asyncMessage struct {
	text   string
	rewake bool
	valid  func() error
}

// AsyncScope owns processes and undelivered notes for one conversation loop.
// Close cancels running process groups; closed scopes cannot publish late notes.
type AsyncScope struct {
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closed    bool
	jobs      map[*asyncJob]context.CancelFunc
	messages  []asyncMessage
	bytes     int
	wake      func()
	guard     func() error
	wg        sync.WaitGroup
	closeOnce sync.Once
	done      chan struct{}
}

func NewAsyncScope(ctx context.Context, wake func(), guard ...func() error) *AsyncScope {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	scope := &AsyncScope{ctx: ctx, cancel: cancel, jobs: make(map[*asyncJob]context.CancelFunc), wake: wake, done: make(chan struct{})}
	if len(guard) > 0 {
		scope.guard = guard[0]
	}
	return scope
}
func ContextWithAsyncScope(ctx context.Context, scope *AsyncScope) context.Context {
	ctx = ContextWithSessionStartGate(ctx, nil, nil)
	ctx = ContextWithCwdChanges(ctx, nil)
	return context.WithValue(ctx, asyncScopeKey{}, scope)
}
func AsyncScopeFromContext(ctx context.Context) *AsyncScope {
	scope, _ := ctx.Value(asyncScopeKey{}).(*AsyncScope)
	return scope
}

func (s *AsyncScope) Cancel() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.messages = nil
	s.bytes = 0
	s.cancel()
	s.mu.Unlock()
}
func (s *AsyncScope) Close() {
	if s == nil {
		return
	}
	s.Cancel()
	s.closeOnce.Do(func() { go func() { s.wg.Wait(); close(s.done) }() })
	select {
	case <-s.done:
	case <-time.After(hookTerminationGracePeriod + hookFinalReapWait + time.Second):
		logging.Warn("async hook cleanup exceeded wait limit")
	}
}
func (s *AsyncScope) HasRewake() bool {
	return s.RewakeGuard() != nil
}

// HasPendingWork is a nonblocking UI cancellation probe; authority validation
// stays at delivery and provider boundaries rather than in the event loop.
func (s *AsyncScope) HasPendingWork() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && (len(s.jobs) > 0 || len(s.messages) > 0)
}

func (s *AsyncScope) RewakeGuard() func() error {
	if s == nil || s.ctx.Err() != nil {
		return nil
	}
	s.mu.Lock()
	messages := append([]asyncMessage(nil), s.messages...)
	s.mu.Unlock()
	for _, message := range messages {
		if message.rewake && (message.valid == nil || message.valid() == nil) {
			if message.valid == nil {
				return func() error { return nil }
			}
			return message.valid
		}
	}
	return nil
}

func TakeAsyncNotes(ctx context.Context) string {
	if ctx.Err() != nil {
		return ""
	}
	var notes []string
	if take, _ := ctx.Value(sessionStartNotesKey{}).(func() string); take != nil {
		if text := take(); text != "" {
			notes = append(notes, text)
		}
	}
	if text := TakeScopeNotes(AsyncScopeFromContext(ctx)); text != "" {
		notes = append(notes, text)
	}
	return strings.Join(notes, "\n\n")
}

// TakeScopeNotes delivers a conversation-owned inbox without inheriting a
// root request gate. Callers validate the owner before attaching its notes.
func TakeScopeNotes(s *AsyncScope) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ""
	}
	messages := s.messages
	s.messages = nil
	s.bytes = 0
	s.mu.Unlock()
	var texts []string
	for _, message := range messages {
		if message.valid == nil || message.valid() == nil {
			texts = append(texts, message.text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// InjectAsyncNotes clones only the current user turn, preserving tool pairing
// and recording delivered notes in the same history that the model consumes.
func InjectAsyncNotes(ctx context.Context, history []*genai.Content) []*genai.Content {
	if len(history) == 0 || history[len(history)-1] == nil || history[len(history)-1].Role != genai.RoleUser {
		return history
	}
	return InjectAsyncText(history, TakeAsyncNotes(ctx))
}

func InjectAsyncText(history []*genai.Content, notes string) []*genai.Content {
	if notes == "" || len(history) == 0 || history[len(history)-1] == nil || history[len(history)-1].Role != genai.RoleUser {
		return history
	}
	result := append([]*genai.Content(nil), history...)
	last := *result[len(result)-1]
	last.Parts = append([]*genai.Part(nil), last.Parts...)
	result[len(result)-1] = &last
	for i, part := range last.Parts {
		if part != nil && part.FunctionResponse != nil {
			copyPart := *part
			response := *part.FunctionResponse
			fields := make(map[string]any, len(response.Response)+1)
			for key, value := range response.Response {
				fields[key] = value
			}
			fields["async_hook_context"] = notes
			response.Response = fields
			copyPart.FunctionResponse = &response
			last.Parts[i] = &copyPart
			return result
		}
	}
	for i, part := range last.Parts {
		if part != nil && part.Text != "" {
			copy := *part
			copy.Text += "\n\n" + notes
			last.Parts[i] = &copy
			return result
		}
	}
	last.Parts = append(last.Parts, genai.NewPartFromText(notes))
	return result
}

type asyncJob struct {
	started        chan error
	mu             sync.Mutex
	finished       bool
	result         Result
	observers      []func(Result)
	stdout, stderr string
	exitCode       int
}

func (j *asyncJob) observe(observer func(Result)) {
	j.mu.Lock()
	if !j.finished {
		j.observers = append(j.observers, observer)
		j.mu.Unlock()
		return
	}
	result := j.result
	j.mu.Unlock()
	notifyAsyncObserver(observer, result)
}
func (j *asyncJob) finish(result Result) {
	j.mu.Lock()
	j.finished = true
	j.result = result
	observers := j.observers
	j.observers = nil
	j.mu.Unlock()
	for _, observer := range observers {
		notifyAsyncObserver(observer, result)
	}
}

func notifyAsyncObserver(observer func(Result), result Result) {
	defer func() {
		if value := recover(); value != nil {
			logging.Warn("async hook observer panicked", "panic", value)
		}
	}()
	observer(result)
}

func (m *Manager) launchAsyncHook(ctx context.Context, hook *Hook, hctx *Context) Result {
	scope := AsyncScopeFromContext(ctx)
	if scope == nil {
		return Result{Hook: hook, Error: fmt.Errorf("async hooks require an owning conversation scope")}
	}
	if err := ctx.Err(); err != nil {
		return Result{Hook: hook, Error: err}
	}
	input, info, err := claudeInput(ctx, hook, hctx)
	if err != nil {
		return Result{Hook: hook, Error: err}
	}
	binding, _ := ctx.Value(promptClientKey{}).(promptClientBinding)
	fingerprint := CommandFingerprint([]ClaudeCommand{*hook.Claude})
	valid := func() error {
		if err := scope.ctx.Err(); err != nil {
			return err
		}
		if scope.guard != nil {
			if err := scope.guard(); err != nil {
				return err
			}
		}
		if err := m.CheckReady(); err != nil {
			return err
		}
		if !m.HookEnabled(hook) {
			return fmt.Errorf("async hook disabled by current policy")
		}
		if hook.claudeSettings && !m.asyncHookConfigured(hook, fingerprint) {
			return fmt.Errorf("async hook was removed or changed")
		}
		if binding.guard != nil {
			return binding.guard()
		}
		return nil
	}
	if err := valid(); err != nil {
		return Result{Hook: hook, Error: err}
	}
	job := &asyncJob{started: make(chan error, 1)}
	scope.mu.Lock()
	if scope.closed || len(scope.jobs) >= 32 {
		scope.mu.Unlock()
		return Result{Hook: hook, Error: fmt.Errorf("async hook scope is closed or has 32 running processes")}
	}
	workerCtx, cancel := context.WithCancel(scope.ctx)
	scope.jobs[job] = cancel
	scope.wg.Add(1)
	scope.mu.Unlock()
	workerCtx = context.WithValue(workerCtx, asyncWorkerKey{}, &asyncWorker{job: job, input: input, info: info})
	workerCtx = ContextWithRuntimeInfo(workerCtx, info)
	frozen := &Context{ToolName: hctx.ToolName, WorkDir: hctx.WorkDir}
	go func() {
		defer scope.wg.Done()
		defer cancel()
		defer func() { scope.mu.Lock(); delete(scope.jobs, job); scope.mu.Unlock() }()
		finished := make(chan struct{})
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-finished:
					return
				case <-workerCtx.Done():
					return
				case <-ticker.C:
					if valid() != nil {
						cancel()
						return
					}
				}
			}
		}()
		limit := time.Duration(0)
		if hook.Claude.AsyncRewake {
			limit = effectiveClaudeTimeout(workerCtx, hook)
		}
		result := func() (result Result) {
			defer func() {
				if value := recover(); value != nil {
					result = Result{Hook: hook, Error: fmt.Errorf("async hook panicked: %v", value)}
				}
			}()
			return m.executeHandler(workerCtx, hook, frozen, limit)
		}()
		close(finished)
		select {
		case job.started <- result.Error:
		default:
		}
		job.finish(result)
		text := parseAsyncOutput(job.stdout, hook.Claude.Event)
		if hook.Type == FileChanged || hook.Type == CwdChanged || hook.Type == DirectoryAdded {
			text = ""
		}
		if receive, _ := ctx.Value(deferredHookResultKey{}).(func(Result)); receive != nil && valid() == nil {
			observed := result
			if hook.Type == SessionStart || hook.Type == FileChanged || hook.Type == CwdChanged || hook.Type == DirectoryAdded {
				stdout := strings.TrimSpace(job.stdout)
				if !strings.HasPrefix(stdout, "{") {
					lines := strings.Split(stdout, "\n")
					for i := len(lines) - 1; i >= 0; i-- {
						if validHTTPHookOutput([]byte(strings.TrimSpace(lines[i]))) {
							stdout = strings.TrimSpace(lines[i])
							break
						}
					}
				}
				observed = interpretClaudeResult(result, stdout, job.stderr, job.exitCode)
			}
			notifyAsyncObserver(receive, observed)
		}
		if hook.Type == SessionStart && text == "" && job.exitCode == 0 && result.Error == nil && !strings.HasPrefix(strings.TrimSpace(job.stdout), "{") {
			text = strings.TrimSpace(job.stdout)
		}
		rewake := hook.Type != DirectoryAdded && hook.Claude.AsyncRewake && job.exitCode == 2 && workerCtx.Err() == nil
		if rewake {
			reason := strings.TrimSpace(job.stderr)
			if reason == "" {
				reason = strings.TrimSpace(job.stdout)
			}
			if reason == "" {
				reason = "Background hook requested attention"
			}
			if text != "" {
				text += "\n"
			}
			text += reason
		}
		if text != "" && valid() == nil {
			note := asyncMessage{text: "[Async hook " + hook.DisplayName() + "]\n" + text, rewake: rewake, valid: valid}
			scope.mu.Lock()
			if !scope.closed {
				for len(scope.messages) > 0 && (len(scope.messages) >= 256 || scope.bytes+len(note.text) > 4<<20) {
					scope.bytes -= len(scope.messages[0].text)
					scope.messages = scope.messages[1:]
					logging.Debug("old async hook note evicted at queue limit")
				}
				scope.messages = append(scope.messages, note)
				scope.bytes += len(note.text)
			}
			wake := scope.wake
			scope.mu.Unlock()
			if rewake && wake != nil {
				func() {
					defer func() {
						if value := recover(); value != nil {
							logging.Warn("async hook wake callback panicked", "panic", value)
						}
					}()
					wake()
				}()
			}
		}
		logging.Debug("async hook completed", "hook", hook.DisplayName(), "error", result.Error)
	}()
	select {
	case err := <-job.started:
		if err != nil {
			return Result{Hook: hook, Error: err}
		}
		return Result{Hook: hook, asyncJob: job}
	case <-ctx.Done():
		cancel()
		return Result{Hook: hook, Error: ctx.Err()}
	}
}

// Settings refresh replaces scope entries, so pointer identity cannot establish
// authority. Unrelated settings edits may keep an unchanged handler running.
func (m *Manager) asyncHookConfigured(hook *Hook, fingerprint string) bool {
	m.mu.RLock()
	current := m.claudeScope
	m.mu.RUnlock()
	if current == nil {
		return false
	}
	for _, entry := range current.snapshotEntries() {
		currentHook := entry.snapshotHook()
		if currentHook.Source == hook.Source && CommandFingerprint([]ClaudeCommand{*currentHook.Claude}) == fingerprint {
			return true
		}
	}
	return false
}

func parseAsyncOutput(stdout, event string) string {
	parse := func(value string) (string, bool) {
		if !json.Valid([]byte(value)) || !strings.HasPrefix(strings.TrimSpace(value), "{") {
			return "", false
		}
		var document yaml.Node
		if yaml.Unmarshal([]byte(value), &document) != nil || validateSettingsNode(&document, 0) != nil {
			return "", false
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(value), &fields) != nil {
			return "", false
		}
		var texts []string
		var system string
		if json.Unmarshal(fields["systemMessage"], &system) == nil && system != "" {
			texts = append(texts, system)
		}
		var specific map[string]json.RawMessage
		if json.Unmarshal(fields["hookSpecificOutput"], &specific) == nil {
			var name, text string
			_ = json.Unmarshal(specific["hookEventName"], &name)
			if name == "" || name == event {
				if json.Unmarshal(specific["additionalContext"], &text) == nil && text != "" {
					texts = append(texts, text)
				}
			}
		}
		return strings.Join(texts, "\n"), true
	}
	if text, ok := parse(strings.TrimSpace(stdout)); ok {
		return text
	}
	lines := strings.Split(stdout, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if text, ok := parse(lines[i]); ok {
			return text
		}
	}
	return ""
}
