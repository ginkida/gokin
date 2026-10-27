package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gokin/internal/logging"
	"gokin/internal/permission"
	"gokin/internal/tasks"
	"gokin/internal/workspace"
)

// WorkspaceWriteTracker belongs to one executor/agent. Cached snapshots retain
// fingerprints, never file contents. Reset prevents a late background task from
// publishing into a later user turn; its evidence remains on the task itself.
type WorkspaceWriteTracker struct {
	mu       sync.Mutex
	previous *workspace.Snapshot
	current  bool
	version  uint64
	epoch    uint64
	noticed  bool
	partial  bool
	pending  int
}

type workspaceObservation struct {
	before          *workspace.Snapshot
	overlap         bool // guarded by workspaceObservations.Mutex
	epoch           uint64
	native          map[string]bool
	nativePending   int
	nativeAmbiguous bool
	group           *observationGroup
	publish         func(workspace.Delta)
}

type observationGroup struct {
	paths   map[string]bool
	native  map[string]bool
	members []*workspaceObservation
}

// Foreground and child executors may share a workspace. Neither can honestly
// attribute another command's writes when their observation windows overlap.
var workspaceObservations = struct {
	sync.Mutex
	active   map[string]map[*workspaceObservation]bool
	versions map[string]uint64
	native   map[string]int
}{active: make(map[string]map[*workspaceObservation]bool), versions: make(map[string]uint64), native: make(map[string]int)}

func (t *WorkspaceWriteTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.current = false
	t.epoch++
	t.noticed, t.partial, t.pending = false, false, 0
}

func (t *WorkspaceWriteTracker) Partial() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.partial || t.pending > 0
}

func observesWorkspaceWrites(name string) bool {
	return name == "bash" || name == "run_tests" || name == "verify_code"
}

// ObserveTool scans only the built-in tools that actually launch workspace
// processes. A test double or extension reusing a tool name supplies its own
// provenance; it must not acquire a fictitious workspace scan from that name.
func (t *WorkspaceWriteTracker) ObserveTool(ctx context.Context, root string, tool Tool, args map[string]any,
	run func(context.Context) (ToolResult, error), background func(ToolResult, bool)) (ToolResult, error) {
	name := tool.Name()
	switch typed := tool.(type) {
	case *BashTool:
		root = typed.workDir
	case *RunTestsTool:
		root = typed.workDir
	case *VerifyCodeTool:
		root = typed.workDir
	default:
		if observesWorkspaceWrites(name) {
			name = ""
		}
	}
	return t.Observe(ctx, root, name, args, func(ctx context.Context) (ToolResult, error) {
		if isNativeWorkspaceMutation(name) {
			finish := markNativeWorkspaceWindow(root)
			completed := false
			defer func() { finish(!completed) }()
			result, err := run(ctx)
			RecordNativeWorkspaceWrites(root, MutationPathsFromResult(name, args, result))
			completed = result.Success && err == nil
			return result, err
		}
		result, err := run(ctx)
		if !observesWorkspaceWrites(name) {
			paths := WrittenPathsFromResult(result)
			RecordNativeWorkspaceWrites(root, paths)
		}
		return result, err
	}, background)
}

// Observe wraps the actual, authorized execution, after pre-hooks and permission
// checks. Verification tools execute arbitrary project code and can also write.
func (t *WorkspaceWriteTracker) Observe(ctx context.Context, root, name string, args map[string]any,
	run func(context.Context) (ToolResult, error), background func(ToolResult, bool)) (ToolResult, error) {
	if !observesWorkspaceWrites(name) || name == "bash" && permission.IsReadOnlyBashArgs(args) {
		return run(ctx)
	}
	limits := workspace.DefaultLimits()
	root = canonicalToolWorkDir(root)
	t.mu.Lock()
	o := &workspaceObservation{epoch: t.epoch, native: make(map[string]bool)}
	t.pending++
	previous, current, version := t.previous, t.current, t.version
	t.mu.Unlock()
	o.publish = func(delta workspace.Delta) {
		t.mu.Lock()
		current := o.epoch == t.epoch
		if current {
			t.partial = true
		}
		notice := current && len(delta.Paths) > 0 && !t.noticed
		if notice {
			t.noticed = true
		}
		t.mu.Unlock()
		if background != nil {
			result := withWorkspaceDelta(NewSuccessResult("Overlapping shell workspace changes observed."), delta)
			if notice {
				result.Content += "\nWorkspace changes made by shell commands are not undoable with /undo."
			}
			background(result, current)
		}
	}
	workspaceObservations.Lock()
	active := workspaceObservations.active[root]
	if active == nil {
		active = make(map[*workspaceObservation]bool)
		workspaceObservations.active[root] = active
	}
	for other := range active {
		other.overlap, o.overlap = true, true
		if o.group == nil {
			o.group = other.group
		}
	}
	if o.group == nil {
		o.group = &observationGroup{paths: make(map[string]bool), native: make(map[string]bool)}
	}
	o.group.members = append(o.group.members, o)
	reuse := current && version == workspaceObservations.versions[root] && len(active) == 0 && previous != nil && previous.Root == root
	for nativeRoot, count := range workspaceObservations.native {
		if workspaceRootsOverlap(root, nativeRoot) {
			o.nativePending += count
		}
	}
	active[o] = true
	workspaceObservations.Unlock()
	if reuse {
		o.before = previous
	} else {
		o.before = workspace.CaptureFresh(ctx, root, limits, previous)
	}

	finish := func(result ToolResult, backgroundCompletion bool) ToolResult {
		// Cancellation must not erase writes made before the command stopped.
		probeCtx := context.WithoutCancel(ctx)
		after := o.before
		if !o.before.Partial {
			after = workspace.Capture(probeCtx, root, limits, o.before)
		}
		delta := workspace.Diff(probeCtx, o.before, after)
		workspaceObservations.Lock()
		active := workspaceObservations.active[root]
		delete(active, o)
		// Native writes are known to belong to another tool. A shell may also
		// have written the same path, so subtraction makes attribution partial.
		if o.nativePending > 0 || o.nativeAmbiguous || len(o.native) > 0 {
			filtered := delta.Paths[:0]
			for _, path := range delta.Paths {
				if !o.native[path] {
					filtered = append(filtered, path)
				}
			}
			delta.Paths = filtered
			delta.Partial, delta.Warning = true, "native writes overlapped the shell observation"
		}
		for _, path := range delta.Paths {
			if !o.group.native[path] {
				o.group.paths[path] = true
			}
		}
		var notifications []func()
		if o.overlap {
			delta.Partial, delta.Warning = true, "overlapping workspace commands prevent exact write attribution"
			delta.Paths = nil
			for path := range o.group.paths {
				if !o.group.native[path] {
					delta.Paths = append(delta.Paths, path)
				}
			}
			sort.Strings(delta.Paths)
			// A call can finish before its sibling writes. Publish the expanding
			// union to earlier owners too; their original result remains partial.
			for _, member := range o.group.members {
				if member != o && !active[member] && member.publish != nil {
					d := delta
					d.Paths = append([]string(nil), delta.Paths...)
					notifications = append(notifications, func() { member.publish(d) })
				}
			}
		}
		if len(active) == 0 {
			delete(workspaceObservations.active, root)
		}
		workspaceObservations.versions[root]++
		finishedVersion := workspaceObservations.versions[root]
		workspaceObservations.Unlock()
		for _, notify := range notifications {
			notify()
		}

		t.mu.Lock()
		if o.epoch == t.epoch {
			if !backgroundCompletion {
				t.pending--
			}
			t.previous = after
			t.current = !delta.Partial
			t.version = finishedVersion
			t.partial = t.partial || delta.Partial
		}
		notice := len(delta.Paths) > 0 && !t.noticed && o.epoch == t.epoch
		if notice {
			t.noticed = true
		}
		t.mu.Unlock()
		result = withWorkspaceDelta(result, delta)
		if notice {
			result.Content += "\nWorkspace changes made by shell commands are not undoable with /undo."
		}
		return result
	}

	detached := name == "bash" && GetBoolDefault(args, "run_in_background", false)
	if detached {
		ctx = tasks.ContextWithWriteCompletion(ctx, func(success bool) workspace.Delta {
			published := false
			defer func() {
				t.mu.Lock()
				if o.epoch == t.epoch {
					t.pending--
					t.partial = t.partial || !published
				}
				t.mu.Unlock()
			}()
			result := ToolResult{Success: success, Content: "Background bash completed."}
			if !success {
				result.Error = "Background command did not complete successfully; see task_output for details."
			}
			result = finish(result, true)
			t.mu.Lock()
			current := o.epoch == t.epoch
			t.mu.Unlock()
			if background != nil {
				background(result, current)
			}
			published = true
			return workspaceDeltaFromResult(result)
		})
	}
	result, err := func() (result ToolResult, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logging.Error("tool execution panic", "tool", name, "panic", recovered, "stack", logging.PanicStack())
				result = NewErrorResult(FormatToolPanic(name, recovered))
			}
		}()
		return run(ctx)
	}()
	if err != nil {
		result.Success, result.Error = false, err.Error()
		err = nil
	}
	if detached && result.Success {
		return result, err
	}
	return finish(result, false), err
}

func withWorkspaceDelta(result ToolResult, delta workspace.Delta) ToolResult {
	data := make(map[string]any)
	if original, ok := result.Data.(map[string]any); ok {
		for k, v := range original {
			data[k] = v
		}
	}
	data["written_paths"] = append(WrittenPathsFromResult(result), delta.Paths...)
	data["workspace_observed"] = true
	data["files_changed_partial"] = delta.Partial
	if delta.Partial {
		result.Content += fmt.Sprintf("\nWarning: files_changed is partial: %s (caps: GOKIN_WORKSPACE_SNAPSHOT_MAX_FILES / GOKIN_WORKSPACE_SNAPSHOT_MAX_BYTES).", delta.Warning)
		data["workspace_warning"] = delta.Warning
	}
	result.Data = data
	snapshot := snapshotGoMutation(result)
	result.mutation = &snapshot
	return result
}

func workspaceDeltaFromResult(result ToolResult) workspace.Delta {
	d := workspace.Delta{Paths: WrittenPathsFromResult(result), Partial: FilesChangedPartial(result)}
	if data, ok := result.Data.(map[string]any); ok {
		d.Warning, _ = data["workspace_warning"].(string)
	}
	return d
}

// HasWorkspaceWriteObservation distinguishes observed deltas (including writes
// before a nonzero exit) from legacy failed-tool argument guesses.
func HasWorkspaceWriteObservation(result ToolResult) bool {
	if result.mutation != nil {
		return result.mutation.WorkspaceObserved
	}
	return snapshotGoMutation(result).WorkspaceObserved
}

func FilesChangedPartial(result ToolResult) bool {
	if result.mutation != nil {
		return result.mutation.Partial
	}
	return snapshotGoMutation(result).Partial
}

func (e *Executor) observeWorkspaceExecution(ctx, lifetime context.Context, tool Tool, args map[string]any, run func(context.Context) (ToolResult, error)) (ToolResult, error) {
	return e.workspaceWrites.ObserveTool(ctx, e.workDir, tool, args, run, func(result ToolResult, current bool) {
		e.invalidateWorkspaceWrites(result)
		if current && lifetime.Err() == nil && e.handler != nil && e.handler.OnToolEnd != nil {
			e.handler.OnToolEnd(tool.Name(), args, result)
		}
	})
}

func (e *Executor) invalidateWorkspaceWrites(result ToolResult) {
	if !result.Success && !HasWorkspaceWriteObservation(result) {
		return
	}
	if !HasWorkspaceWriteObservation(result) {
		RecordNativeWorkspaceWrites(e.workDir, WrittenPathsFromResult(result))
	}
	// A capped/overlapping scan cannot name every stale entry. Drop all caches
	// rather than return a pre-command read after a possible unobserved write.
	if FilesChangedPartial(result) {
		if e.readTracker != nil {
			e.readTracker.Reset()
		}
	}
	if FilesChangedPartial(result) || HasWorkspaceWriteObservation(result) && len(WrittenPathsFromResult(result)) > 0 {
		if e.toolCache != nil {
			e.toolCache.Clear()
		}
		if e.searchCache != nil {
			e.searchCache.Clear()
		}
	}
	for _, path := range WrittenPathsFromResult(result) {
		if e.readTracker != nil {
			e.readTracker.InvalidateFile(path)
			e.readTracker.InvalidateFile(e.readTrackerPath(path))
		}
		if e.writeTracker != nil {
			e.writeTracker.Record(path)
		}
		if e.toolCache != nil {
			e.toolCache.InvalidateByFile(path)
		}
		if e.searchCache != nil {
			e.searchCache.InvalidateByPath(path)
		}
	}
}

func isNativeWorkspaceMutation(name string) bool {
	switch name {
	case "write", "edit", "copy", "move", "delete", "mkdir", "batch", "refactor":
		return true
	}
	return false
}

func markNativeWorkspaceWindow(root string) func(bool) {
	root = canonicalToolWorkDir(root)
	workspaceObservations.Lock()
	workspaceObservations.native[root]++
	for observedRoot, active := range workspaceObservations.active {
		if workspaceRootsOverlap(root, observedRoot) {
			for o := range active {
				o.nativePending++
			}
		}
	}
	workspaceObservations.Unlock()
	return func(ambiguous bool) {
		workspaceObservations.Lock()
		defer workspaceObservations.Unlock()
		workspaceObservations.native[root]--
		if workspaceObservations.native[root] == 0 {
			delete(workspaceObservations.native, root)
		}
		for observedRoot, active := range workspaceObservations.active {
			if workspaceRootsOverlap(root, observedRoot) {
				for o := range active {
					o.nativePending--
					o.nativeAmbiguous = o.nativeAmbiguous || ambiguous
				}
			}
		}
	}
}

func workspaceRootsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

// RecordNativeWorkspaceWrites shares confirmed native-tool provenance across
// executors/agents observing this workspace. Never infer writes from read args.
func RecordNativeWorkspaceWrites(root string, paths []string) {
	if len(paths) == 0 {
		return
	}
	root = canonicalToolWorkDir(root)
	workspaceObservations.Lock()
	defer workspaceObservations.Unlock()
	for knownRoot := range workspaceObservations.versions {
		if workspaceRootsOverlap(root, knownRoot) {
			workspaceObservations.versions[knownRoot]++
		}
	}
	if _, ok := workspaceObservations.versions[root]; !ok {
		workspaceObservations.versions[root] = 1
	}
	for observedRoot, active := range workspaceObservations.active {
		if !workspaceRootsOverlap(root, observedRoot) {
			continue
		}
		for _, path := range paths {
			if !filepath.IsAbs(path) {
				path = filepath.Join(root, path)
			}
			path = filepath.Clean(path)
			if !strings.HasPrefix(path, observedRoot+string(filepath.Separator)) {
				continue
			}
			for o := range active {
				o.native[path] = true
				o.group.native[path] = true
				delete(o.group.paths, path)
			}
		}
	}
}

// WorkspaceWritesPartial includes commands whose background completion has not
// yet published a delta. The gate must not treat an empty ledger as no writes.
func (e *Executor) WorkspaceWritesPartial() bool { return e.workspaceWrites.Partial() }

type workspaceWriteLifetimeKey struct{}

// ContextWithWorkspaceWriteLifetime masks the parent's lifecycle for a child.
func ContextWithWorkspaceWriteLifetime(ctx context.Context) context.Context {
	return context.WithValue(ctx, workspaceWriteLifetimeKey{}, ctx)
}

func WorkspaceWriteLifetimeActive(ctx context.Context) bool {
	if lifetime, ok := ctx.Value(workspaceWriteLifetimeKey{}).(context.Context); ok {
		return lifetime.Err() == nil
	}
	return ctx.Err() == nil
}
