package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"gokin/internal/harness"
	"gokin/internal/logging"
	"gokin/internal/repl"
	"gokin/internal/tools"
)

type hybridComponents struct {
	manager      hybridRuntime
	store        *harness.Store
	replTool     *tools.ReplExecTool
	harnessTool  *tools.HarnessTool
	availability repl.Availability
}

func (c hybridComponents) publish() {
	if c.replTool != nil {
		c.replTool.SetManager(c.manager)
	}
	if c.harnessTool != nil {
		c.harnessTool.SetStore(c.store)
	}
}

// deferredHybridInit is the stable executor installed behind auto mode's
// repl_exec declaration. Intent classification may expose that declaration,
// but the sandboxed Python process is not started until the model actually
// executes it. A cancelled first call remains retryable; a conclusive setup
// failure hides the declaration from later requests.
type deferredHybridInit struct {
	mu sync.Mutex

	registry        *tools.Registry
	opener          func(context.Context, repl.Options) (hybridRuntime, repl.Availability)
	opts            repl.Options
	handler         repl.CallHandler
	onPromptChanged func()

	attempted    bool
	initializing bool
	ready        bool
	closed       bool
	manager      hybridRuntime
	store        *harness.Store
	err          error
	initDone     chan struct{}
	initCancel   context.CancelFunc

	harnessAttempted    bool
	harnessInitializing bool
	harnessErr          error
	harnessDone         chan struct{}
	harnessCancel       context.CancelFunc
	harnessLoader       func(context.Context, string) (*harness.Store, error)

	closeDone chan struct{}
	closeErr  error
}

func (d *deferredHybridInit) SetCallHandler(handler repl.CallHandler) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.handler = handler
	manager := d.manager
	if manager != nil {
		manager.SetCallHandler(handler)
	}
	d.mu.Unlock()
}

func (d *deferredHybridInit) SetPromptChangedCallback(callback func()) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.onPromptChanged = callback
	tool := d.harnessToolLocked()
	if tool != nil {
		tool.SetPromptChangedCallback(callback)
	}
	d.mu.Unlock()
}

func (d *deferredHybridInit) components() (hybridRuntime, *harness.Store) {
	if d == nil {
		return nil, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.ready || d.closed {
		return nil, nil
	}
	return d.manager, d.store
}

func (d *deferredHybridInit) isReady() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ready && !d.closed
}

// canAdvertise is intentionally weaker than isReady: before the first execute
// call the lazy runtime is a valid capability even though no process exists.
// Only close or a conclusive secure-runtime failure makes it unavailable.
func (d *deferredHybridInit) canAdvertise() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.closed && !(d.attempted && !d.ready)
}

func (d *deferredHybridInit) harnessToolLocked() *tools.HarnessTool {
	if d == nil || d.registry == nil {
		return nil
	}
	registered, ok := d.registry.Get("harness")
	if !ok {
		return nil
	}
	tool, _ := registered.(*tools.HarnessTool)
	return tool
}

// ensureHarness loads the optional continual state only when Python actually
// calls rlm.harness. Ordinary analytical cells should not pay for a memory-file
// read, and a corrupt optional memory file must not tear down a verified REPL.
// Explicit engine.mode=hybrid remains eager and fail-closed in the builder.
func (d *deferredHybridInit) ensureHarness(ctx context.Context) (*harness.Store, error) {
	if d == nil {
		return nil, fmt.Errorf("continual harness is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return nil, fmt.Errorf("continual harness is unavailable: hybrid runtime is closed")
		}
		if !d.ready || d.manager == nil {
			d.mu.Unlock()
			return nil, fmt.Errorf("continual harness is unavailable before the secure REPL starts")
		}
		if d.store != nil {
			store := d.store
			d.mu.Unlock()
			return store, nil
		}
		if d.harnessAttempted {
			err := d.harnessErr
			d.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("continual harness initialization failed")
		}
		if err := ctx.Err(); err != nil {
			d.mu.Unlock()
			return nil, err
		}
		if d.harnessInitializing {
			done := d.harnessDone
			d.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				continue
			}
		}

		done := make(chan struct{})
		loadCtx, cancel := context.WithCancel(ctx)
		d.harnessInitializing = true
		d.harnessDone = done
		d.harnessCancel = cancel
		loader := d.harnessLoader
		if loader == nil {
			loader = loadHarnessStore
		}
		workDir := d.opts.WorkDir
		d.mu.Unlock()

		tool := d.harnessTool()
		var store *harness.Store
		var err error
		if tool == nil {
			err = fmt.Errorf("continual harness is unavailable in this invocation")
		} else {
			store, err = loader(loadCtx, workDir)
			if err != nil {
				err = fmt.Errorf("initialize continual harness: %w", err)
			}
		}
		if err == nil && loadCtx.Err() != nil {
			err = loadCtx.Err()
		}
		cancel()

		d.mu.Lock()
		d.harnessInitializing = false
		d.harnessDone = nil
		d.harnessCancel = nil
		closed := d.closed
		if closed {
			err = fmt.Errorf("continual harness is unavailable: hybrid runtime is closed")
		}
		if err == nil {
			d.harnessAttempted = true
			tool.SetStore(store)
			tool.SetPromptChangedCallback(d.onPromptChanged)
			d.store = store
			d.harnessErr = nil
		} else {
			// Like secure-runtime initialization, caller cancellation is not a
			// conclusive capability failure. A later cell may retry.
			retryable := !d.closed && (ctx.Err() != nil || errors.Is(err, context.Canceled) ||
				errors.Is(err, context.DeadlineExceeded))
			d.harnessAttempted = !retryable
			if retryable {
				d.harnessErr = nil
			} else {
				d.harnessErr = err
			}
		}
		close(done)
		d.mu.Unlock()
		if err != nil {
			if closed || ctx.Err() != nil || errors.Is(err, context.Canceled) ||
				errors.Is(err, context.DeadlineExceeded) {
				logging.Debug("hybrid auto mode continual harness load cancelled", "error", err)
			} else {
				logging.Warn("hybrid auto mode could not load optional continual harness", "error", err)
			}
			return nil, err
		}
		logging.Debug("hybrid auto mode continual harness activated on first use")
		return store, nil
	}
}

func loadHarnessStore(ctx context.Context, workDir string) (*harness.Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store, err := harness.NewStore(workDir)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return store, nil
}

func (d *deferredHybridInit) harnessTool() *tools.HarnessTool {
	if d == nil || d.registry == nil {
		return nil
	}
	registered, ok := d.registry.Get("harness")
	if !ok {
		return nil
	}
	tool, _ := registered.(*tools.HarnessTool)
	return tool
}

func (d *deferredHybridInit) activate(ctx context.Context) (hybridRuntime, error) {
	if d == nil {
		return nil, fmt.Errorf("%w: lazy runtime is not configured", repl.ErrUnavailable)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return nil, fmt.Errorf("%w: lazy runtime is closed", repl.ErrUnavailable)
		}
		if d.ready && d.manager != nil {
			manager := d.manager
			d.mu.Unlock()
			return manager, nil
		}
		if d.attempted {
			err := d.err
			d.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w: lazy runtime initialization failed", repl.ErrUnavailable)
		}
		if err := ctx.Err(); err != nil {
			d.mu.Unlock()
			return nil, err
		}
		if d.initializing {
			done := d.initDone
			d.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				continue
			}
		}

		// The first execute owns this attempt. Publish a completion channel before
		// opening the sandbox, then release d.mu so schema/status/shutdown remain
		// responsive and concurrent executes can wait with their own contexts.
		initCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		d.initializing = true
		d.initDone = done
		d.initCancel = cancel
		registry, opener, opts := d.registry, d.opener, d.opts
		d.mu.Unlock()

		components, err := initializeHybridComponents(initCtx, registry, opener, opts, false)
		if err == nil && initCtx.Err() != nil {
			_ = components.manager.Close()
			components = hybridComponents{}
			err = initCtx.Err()
		}
		cancel()

		var closeManager hybridRuntime
		d.mu.Lock()
		d.initializing = false
		d.initDone = nil
		d.initCancel = nil
		if d.closed {
			closeManager = components.manager
			err = fmt.Errorf("%w: lazy runtime is closed", repl.ErrUnavailable)
		} else if err != nil {
			// Cancellation is request-scoped and says nothing about host support.
			// Leave attempted=false so a concurrent or later request can retry.
			if ctx.Err() == nil && !errors.Is(err, context.Canceled) &&
				!errors.Is(err, context.DeadlineExceeded) {
				if !errors.Is(err, repl.ErrUnavailable) {
					err = fmt.Errorf("%w: %v", repl.ErrUnavailable, err)
				}
				d.attempted = true
				d.err = err
				logging.Debug("hybrid auto mode fell back to structured tools", "error", err)
			}
		} else {
			components.manager.SetCallHandler(d.handler)
			d.manager = components.manager
			d.attempted = true
			d.ready = true
			d.err = nil
			logging.Info("hybrid auto mode activated on first repl_exec call",
				"backend", components.availability.Backend,
				"python", components.availability.PythonPath)
		}
		manager := d.manager
		d.mu.Unlock()
		if closeManager != nil {
			_ = closeManager.Close()
		}
		// Completion means all process cleanup is done, not merely that the state
		// mutex was released. Shutdown and concurrent executes rely on this edge.
		close(done)
		if err != nil {
			return nil, err
		}
		return manager, nil
	}
}

func (d *deferredHybridInit) Execute(ctx context.Context, code string) (repl.Result, error) {
	manager, err := d.activate(ctx)
	if err != nil {
		return repl.Result{}, err
	}
	return manager.Execute(ctx, code)
}

// Reset before first use is a process-free no-op: no globals or artifacts
// exist yet. A ready runtime retains the ordinary reset semantics.
func (d *deferredHybridInit) Reset(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	manager := d.manager
	ready := d.ready && !d.closed
	attempted := d.attempted
	err := d.err
	d.mu.Unlock()
	if attempted && !ready {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: lazy runtime initialization failed", repl.ErrUnavailable)
	}
	if !ready || manager == nil {
		return nil
	}
	return manager.Reset(ctx)
}

func (d *deferredHybridInit) Stats() repl.Stats {
	if d == nil {
		return repl.Stats{}
	}
	d.mu.Lock()
	manager := d.manager
	ready := d.ready && !d.closed
	err := d.err
	d.mu.Unlock()
	if ready && manager != nil {
		return manager.Stats()
	}
	stats := repl.Stats{}
	if err != nil {
		stats.LastError = err.Error()
	}
	return stats
}

func (d *deferredHybridInit) close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	if d.closed {
		done := d.closeDone
		d.mu.Unlock()
		if done != nil {
			<-done
		}
		d.mu.Lock()
		err := d.closeErr
		d.mu.Unlock()
		return err
	}
	d.closed = true
	d.ready = false
	closeDone := make(chan struct{})
	d.closeDone = closeDone
	manager := d.manager
	initCancel, initDone := d.initCancel, d.initDone
	harnessCancel, harnessDone := d.harnessCancel, d.harnessDone
	d.mu.Unlock()
	if initCancel != nil {
		initCancel()
	}
	if harnessCancel != nil {
		harnessCancel()
	}
	if initDone != nil {
		<-initDone
	}
	if harnessDone != nil {
		<-harnessDone
	}
	var err error
	// Do not wait for a worker while holding d.mu. A worker callback may
	// refresh the prompt and snapshot these components under the same lock.
	if manager != nil {
		manager.SetCallHandler(nil)
		err = manager.Close()
	}
	if tool := d.harnessTool(); tool != nil {
		tool.SetStore(nil)
		tool.SetPromptChangedCallback(nil)
	}
	d.mu.Lock()
	d.manager = nil
	d.store = nil
	d.handler = nil
	d.onPromptChanged = nil
	d.closeErr = err
	close(closeDone)
	d.mu.Unlock()
	return err
}

func containsToolName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func initializeHybridComponents(
	ctx context.Context,
	registry *tools.Registry,
	opener func(context.Context, repl.Options) (hybridRuntime, repl.Availability),
	opts repl.Options,
	loadHarness bool,
) (hybridComponents, error) {
	if registry == nil {
		return hybridComponents{}, fmt.Errorf("hybrid tool registry is unavailable")
	}
	registeredREPL, ok := registry.Get("repl_exec")
	if !ok {
		return hybridComponents{}, fmt.Errorf("repl_exec is not registered")
	}
	replTool, ok := registeredREPL.(*tools.ReplExecTool)
	if !ok {
		return hybridComponents{}, fmt.Errorf("repl_exec has unexpected implementation %T", registeredREPL)
	}
	var harnessTool *tools.HarnessTool
	var store *harness.Store
	if registeredHarness, harnessRegistered := registry.Get("harness"); loadHarness && harnessRegistered {
		harnessTool, ok = registeredHarness.(*tools.HarnessTool)
		if !ok {
			return hybridComponents{}, fmt.Errorf("harness has unexpected implementation %T", registeredHarness)
		}
		var storeErr error
		store, storeErr = harness.NewStore(opts.WorkDir)
		if storeErr != nil {
			return hybridComponents{}, fmt.Errorf("initialize continual harness: %w", storeErr)
		}
	}

	// Validate every process-free dependency before opening the sandbox. Explicit
	// hybrid mode therefore fails on bad harness state without starting and then
	// immediately discarding a Python worker.
	manager, availability := opener(ctx, opts)
	if !availability.Available {
		if manager != nil {
			_ = manager.Close()
		}
		return hybridComponents{}, availability.Error()
	}
	if manager == nil {
		return hybridComponents{}, fmt.Errorf("hybrid runtime opener returned no manager")
	}

	return hybridComponents{
		manager: manager, store: store, replTool: replTool,
		harnessTool: harnessTool, availability: availability,
	}, nil
}
