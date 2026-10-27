package security

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// SandboxStatus describes effective OS enforcement, independently of permission
// prompts. An unavailable optional sandbox runs commands on the host.
type SandboxStatus struct {
	Status  string `json:"status"`
	Backend string `json:"backend"`
	Network string `json:"network"`
	Reason  string `json:"reason,omitempty"`
}

// CommandScope is captured by the command's owner, never supplied by shell text.
// Channels exposes only the exact cwd-probe / SessionStart environment files.
type CommandScope struct {
	WorkDir     string
	AllowedDirs []string
	ReadFiles   []string
	WriteFiles  []string
}

// SandboxPolicy is shared by command runners and agent clones. Configure swaps
// future policy atomically; Snapshot pins an invocation to its approved policy.
type SandboxPolicy struct {
	mu    sync.RWMutex
	state *sandboxPolicyState
}

type sandboxPolicyState struct {
	config   SandboxConfig
	root     string
	once     sync.Once
	warning  sync.Once
	status   SandboxStatus
	unsafe   error
	launcher string
	cacheMu  sync.Mutex
	caches   map[string]string
}

var errUnsafeSandboxScope = errors.New("unsafe sandbox scope")

func NewSandboxPolicy(root string, config SandboxConfig) *SandboxPolicy {
	p := &SandboxPolicy{}
	p.Configure(root, config)
	return p
}

func (p *SandboxPolicy) Configure(root string, config SandboxConfig) {
	config.SensitivePaths = append([]string(nil), config.SensitivePaths...)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != nil && p.state.root == root && reflect.DeepEqual(p.state.config, config) {
		return
	}
	p.state = &sandboxPolicyState{root: root, config: config, caches: make(map[string]string)}
}

func (p *SandboxPolicy) Snapshot() *SandboxPolicy {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return &SandboxPolicy{state: p.state}
}

func (p *SandboxPolicy) Status(ctx context.Context) SandboxStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	if p == nil {
		return SandboxStatus{Status: "disabled", Backend: "none", Network: "host"}
	}
	s := p.Snapshot().state
	// A cancelled tool invocation must not cache "unavailable" and weaken the
	// next command. The run-level probe has its own bounded lifetime.
	s.once.Do(func() { s.probe(context.WithoutCancel(ctx)) })
	return s.status
}

func (p *SandboxPolicy) Required() bool {
	return p != nil && p.Snapshot().state.config.RequireSandbox
}

func (p *SandboxPolicy) Enabled() bool {
	return p != nil && (p.Config().Enabled || p.Required())
}

func (p *SandboxPolicy) Config() SandboxConfig {
	config := p.Snapshot().state.config
	config.SensitivePaths = append([]string(nil), config.SensitivePaths...)
	return config
}

// Identity is opaque but stable for the lifetime of an immutable policy. It
// invalidates permission leases on live network/sandbox policy changes.
func (p *SandboxPolicy) Identity() string {
	if p == nil {
		return "disabled"
	}
	return fmt.Sprintf("%p", p.Snapshot().state)
}

func (p *SandboxPolicy) Warn(ctx context.Context, writer io.Writer) {
	if p == nil {
		return
	}
	p = p.Snapshot()
	status := p.Status(ctx)
	if status.Status != "unavailable" || p.Required() {
		return
	}
	p.state.warning.Do(func() {
		if status.Network == "blocked" {
			fmt.Fprintf(writer, "WARNING: bash command execution refused: %s\n", status.Reason)
			return
		}
		fmt.Fprintf(writer, "WARNING: bash sandbox unavailable (%s): %s. Commands run UNSANDBOXED with host filesystem and network access. Use --require-sandbox to refuse this fallback.\n", status.Backend, status.Reason)
	})
}

func (s *sandboxPolicyState) probe(ctx context.Context) {
	s.status = SandboxStatus{Status: "disabled", Backend: "none", Network: "host"}
	if !s.config.Enabled && !s.config.RequireSandbox {
		return
	}
	s.status.Status = "unavailable"
	switch runtime.GOOS {
	case "linux":
		s.status.Backend = "bubblewrap"
		s.launcher, _ = exec.LookPath("bwrap")
	case "darwin":
		s.status.Backend = "sandbox-exec"
		s.launcher, _ = exec.LookPath("sandbox-exec")
	default:
		s.status.Reason = "no bash isolation backend for " + runtime.GOOS
		return
	}
	if s.launcher == "" {
		s.status.Reason = s.status.Backend + " executable not found"
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "bash", "-c", "printf gokin-bash-probe")
	cmd.Dir = s.root
	cleanup, err := s.wrap(cmd, CommandScope{WorkDir: s.root}, false)
	if errors.Is(err, errUnsafeSandboxScope) {
		s.unsafe = err
		s.status.Network = "blocked"
	}
	if err == nil {
		defer cleanup()
		configureSandboxProcessGroup(cmd)
		cmd.Cancel = func() error { terminateSandboxProcessTree(cmd); return nil }
		cmd.WaitDelay = time.Second
		var output []byte
		output, err = cmd.CombinedOutput()
		if err == nil && string(output) != "gokin-bash-probe" {
			err = fmt.Errorf("unexpected probe output")
		}
		if err != nil {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	if err != nil {
		s.status.Reason = "bash policy probe failed: " + err.Error()
		return
	}
	s.status.Status = "enforced"
	s.status.Network = "loopback"
	if runtime.GOOS == "darwin" {
		s.status.Network = "loopback-host"
		s.status.Reason = "sandbox-exec allows connections to all addresses of this host (loopback and local interfaces); other hosts are blocked"
	}
	if s.config.AllowNetwork {
		s.status.Network = "host"
		s.status.Reason = ""
	}
}

// Prepare wraps a command once. A subsequent launcher failure is an ordinary
// execution failure: callers must never retry that command outside the sandbox.
func (p *SandboxPolicy) Prepare(ctx context.Context, cmd *exec.Cmd, scope CommandScope) (func(), error) {
	if p == nil {
		return func() {}, nil
	}
	p = p.Snapshot()
	status := p.Status(ctx)
	if p.state.unsafe != nil {
		return nil, p.state.unsafe
	}
	if status.Status == "unavailable" {
		if p.Required() {
			return nil, fmt.Errorf("required bash sandbox unavailable: %s", status.Reason)
		}
		p.Warn(ctx, os.Stderr)
		return func() {}, nil
	}
	if status.Status == "disabled" {
		return func() {}, nil
	}
	return p.state.wrap(cmd, scope, true)
}

type sandboxMount struct {
	path     string
	writable bool
}

func sandboxSystemPaths() []string {
	paths := []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc/ld.so.cache", "/etc/ld.so.conf", "/etc/ld.so.conf.d", "/etc/ssl/certs", "/etc/ssl/cert.pem", "/etc/ssl/openssl.cnf", "/etc/ca-certificates", "/etc/passwd", "/etc/group", "/etc/nsswitch.conf", "/etc/resolv.conf", "/etc/hosts", "/etc/localtime"}
	if runtime.GOOS == "darwin" {
		paths = append(paths, "/System", "/Library/Developer", "/Library/Apple", "/opt/homebrew", "/private/etc/ssl/certs", "/private/etc/ssl/cert.pem", "/private/etc/ssl/openssl.cnf", "/private/etc/hosts", "/private/etc/localtime", "/private/var/db/timezone")
	}
	paths = append(paths, runtime.GOROOT())
	for _, path := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(path) && (filepath.Base(path) == "bin" || filepath.Base(path) == "sbin") {
			paths = append(paths, path)
		}
	}
	return paths
}

func sandboxPathMounted(path string, mounts []sandboxMount) bool {
	for _, mount := range mounts {
		if pathWithin(path, mount.path) {
			return true
		}
	}
	for _, system := range sandboxSystemPaths() {
		if pathWithin(path, system) {
			return true
		}
	}
	return false
}

func canonicalSandboxPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func sandboxSecrets(extra []string) []string {
	paths := []string{}
	for _, path := range extra {
		if strings.TrimSpace(path) != "" {
			paths = append(paths, path)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, rel := range []string{".ssh", ".aws", ".gnupg", ".config/gcloud", ".kube", ".docker/config.json", ".netrc", ".config/gokin", ".claude", "Library/Application Support/gokin"} {
			paths = append(paths, filepath.Join(home, filepath.FromSlash(rel)))
		}
	}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		paths = append(paths, dir)
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		paths = append(paths, filepath.Join(dir, "gokin"))
	}
	if dir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(dir, "gokin"))
	}
	for _, path := range append([]string(nil), paths...) {
		if resolved, err := resolveCacheBase(path); err == nil && resolved != path {
			paths = append(paths, resolved)
		}
	}
	return paths
}

func (s *sandboxPolicyState) wrap(cmd *exec.Cmd, scope CommandScope, seed bool) (func(), error) {
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	root, err := canonicalSandboxPath(scope.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("sandbox workspace: %w", err)
	}
	if cmd.Dir == "" {
		cmd.Dir = root
	} else {
		cmd.Dir, err = canonicalSandboxPath(cmd.Dir)
		if err != nil {
			return nil, fmt.Errorf("sandbox cwd: %w", err)
		}
	}
	secrets := sandboxSecrets(s.config.SensitivePaths)
	mounts := []sandboxMount{}
	add := func(path string, writable bool) error {
		resolved, err := canonicalSandboxPath(path)
		if err != nil {
			return err
		}
		for _, secret := range secrets {
			if pathWithin(resolved, secret) {
				return fmt.Errorf("%w: sandbox scope includes sensitive path %s", errUnsafeSandboxScope, resolved)
			}
		}
		mounts = append(mounts, sandboxMount{resolved, writable})
		return nil
	}
	for _, path := range append([]string{root}, scope.AllowedDirs...) {
		if err := add(path, true); err != nil {
			return nil, err
		}
	}
	var gitProtected []sandboxMount
	for _, path := range append([]string{root}, scope.AllowedDirs...) {
		data, protected, err := sandboxGitMounts(path)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, data...)
		gitProtected = append(gitProtected, protected...)
	}
	for _, path := range scope.ReadFiles {
		if err := add(path, false); err != nil {
			return nil, err
		}
	}
	for _, path := range scope.WriteFiles {
		if err := add(path, true); err != nil {
			return nil, err
		}
	}
	// A host-selected tool may live outside the standard system roots. Expose
	// only its executable, never its enclosing directory as an implicit grant.
	if err := add(cmd.Path, false); err != nil {
		return nil, err
	}
	// Linked worktrees need external metadata for status/diff. It is read-only;
	// neither the main checkout nor its enclosing directory becomes a grant.
	runtimeDir, err := os.MkdirTemp("", "gokin-bash-*")
	if err != nil {
		return nil, err
	}
	initialFiles := len(cmd.ExtraFiles)
	cleanup := func() {
		for _, file := range cmd.ExtraFiles[initialFiles:] {
			_ = file.Close()
		}
		_ = os.RemoveAll(runtimeDir)
	}
	runtimeDir, err = canonicalSandboxPath(runtimeDir)
	if err != nil {
		cleanup()
		return nil, err
	}
	for _, dir := range []string{"home", "tmp"} {
		if err := os.Mkdir(filepath.Join(runtimeDir, dir), 0700); err != nil {
			cleanup()
			return nil, err
		}
	}
	cacheDir, cacheMounts, err := s.workspaceCaches(root, seed)
	if err != nil {
		cleanup()
		return nil, err
	}
	mounts = append(mounts, cacheMounts...)
	mounts = append(mounts, sandboxMount{runtimeDir, true})
	mounts = append(mounts, gitProtected...)
	moduleCache := filepath.Join(cacheDir, "go-mod")
	// A complete downloaded module tree works offline without copying a huge
	// cache. Network-enabled downloads use the writable managed cache instead.
	if !s.config.AllowNetwork {
		if host, err := canonicalSandboxPath(hostGoModuleCache()); err == nil {
			moduleCache = host
		}
	}
	env := safeEnvironment(root)
	if cmd.Env != nil {
		env = cmd.Env
	}
	// Go test and automatic toolchain selection can export a GOROOT belonging
	// to a different Go than the one on PATH. Let the selected executable find
	// its own installation instead of mixing a driver's version with another
	// installation's compiler and standard library.
	cleanEnv := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, "GOROOT=") {
			cleanEnv = append(cleanEnv, entry)
		}
	}
	env = cleanEnv
	path := sandboxPATH()
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			path = strings.TrimPrefix(entry, "PATH=")
		}
	}
	env = append(append([]string(nil), env...),
		"PATH="+path+string(os.PathListSeparator)+filepath.Join(cacheDir, "toolchains"),
		"HOME="+filepath.Join(runtimeDir, "home"), "TMPDIR="+filepath.Join(runtimeDir, "tmp"),
		"TMP="+filepath.Join(runtimeDir, "tmp"), "TEMP="+filepath.Join(runtimeDir, "tmp"),
		"GOCACHE="+filepath.Join(cacheDir, "go-build"), "GOMODCACHE="+moduleCache,
		"GOPATH="+filepath.Join(cacheDir, "go"), "npm_config_cache="+filepath.Join(cacheDir, "npm"),
		"PIP_CACHE_DIR="+filepath.Join(cacheDir, "pip"))
	if !s.config.AllowNetwork {
		env = append(env, "GOPROXY=off", "GOSUMDB=off", "PIP_NO_INDEX=1", "npm_config_offline=true")
	} else if host, err := canonicalSandboxPath(filepath.Join(hostGoModuleCache(), "cache", "download")); err == nil {
		proxy := os.Getenv("GOPROXY")
		if proxy == "" {
			proxy = "https://proxy.golang.org,direct"
		}
		local := (&url.URL{Scheme: "file", Path: filepath.ToSlash(host)}).String()
		env = append(env, "GOPROXY="+local+","+proxy)
	}
	cmd.Env = env
	if err := wrapBashSandbox(cmd, s.launcher, mounts, secrets, runtimeDir, s.config.AllowNetwork); err != nil {
		cleanup()
		return nil, err
	}
	return cleanup, nil
}

func worktreeMetadata(root string) []string {
	file, err := os.Open(filepath.Join(root, ".git"))
	if err != nil {
		return nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 || !strings.HasPrefix(string(data), "gitdir: ") {
		return nil
	}
	dir := strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir: "))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	paths := []string{dir}
	// A repository-controlled gitfile is not an arbitrary directory grant.
	// Accept only a registered linked worktree with a matching backlink and
	// the standard common/.git/worktrees/<name> relationship.
	dir, err = canonicalSandboxPath(dir)
	if err != nil {
		return nil
	}
	common, err := readSandboxMetadata(filepath.Join(dir, "commondir"))
	if err != nil {
		return nil
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	common, err = canonicalSandboxPath(common)
	if err != nil || filepath.Base(common) != ".git" || filepath.Dir(filepath.Dir(dir)) != common || filepath.Base(filepath.Dir(dir)) != "worktrees" {
		return nil
	}
	backlink, err := readSandboxMetadata(filepath.Join(dir, "gitdir"))
	if err != nil {
		return nil
	}
	backlink, err = canonicalSandboxPath(backlink)
	if err != nil || backlink != filepath.Join(root, ".git") {
		return nil
	}
	paths = []string{dir}
	for _, name := range []string{"HEAD", "config", "objects", "refs", "logs", "packed-refs", "shallow", "info"} {
		path := filepath.Join(common, name)
		if resolved, err := canonicalSandboxPath(path); err == nil && pathWithin(resolved, common) {
			paths = append(paths, resolved)
		}
	}
	return paths
}

func readSandboxMetadata(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", err
	}
	if len(data) > 4096 {
		return "", fmt.Errorf("git metadata exceeds size limit")
	}
	return strings.TrimSpace(string(data)), nil
}

// Host caches are only mounted read-only. Bounded leaf symlinks seed a writable
// managed cache: misses and atomic replacements stay in the workspace cache,
// while a write through an existing seed cannot modify the host's cache.
func (s *sandboxPolicyState) workspaceCaches(root string, seed bool) (string, []sandboxMount, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	base, err := managedCacheBase(root)
	if err != nil {
		return "", nil, err
	}
	hash := sha256.Sum256([]byte(root))
	parts := []string{"gokin", "bash", fmt.Sprintf("%x", hash[:12])}
	dir, err := privateCacheDirectory(base, parts...)
	if err != nil {
		return "", nil, err
	}
	home, _ := os.UserHomeDir()
	host := map[string]string{"go-mod": hostGoModuleCache(), "go-build": filepath.Join(base, "go-build"), "npm": filepath.Join(home, ".npm"), "pip": filepath.Join(base, "pip")}
	if path := os.Getenv("GOCACHE"); path != "" && path != "off" {
		host["go-build"] = path
	}
	mounts := []sandboxMount{{dir, true}}
	names := make([]string, 0, len(host))
	for name := range host {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := host[name]
		target := filepath.Join(dir, name)
		if _, err := privateCacheDirectory(base, append(append([]string(nil), parts...), name)...); err != nil {
			return "", nil, err
		}
		resolved, err := canonicalSandboxPath(path)
		if err != nil || pathWithin(resolved, dir) {
			continue
		}
		mounts = append(mounts, sandboxMount{resolved, false})
		key := target + "\x00" + resolved
		if _, ok := s.caches[key]; ok || !seed {
			continue
		}
		// Go's build cache contains coupled index/data entries and does not
		// support a partially seeded symlink tree. Rebuild into the managed
		// cache; complete downloaded modules are reused separately above.
		if name != "go-mod" && name != "go-build" {
			seedCache(resolved, target)
		} else if name == "go-mod" {
			toolchains, err := privateCacheDirectory(base, append(append([]string(nil), parts...), "toolchains")...)
			if err != nil {
				return "", nil, err
			}
			if err := seedToolchains(resolved, toolchains); err != nil {
				return "", nil, err
			}
		}
		s.caches[key] = resolved
	}
	return dir, mounts, nil
}

func hostGoModuleCache() string {
	if mod := os.Getenv("GOMODCACHE"); mod != "" {
		return mod
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		home, _ := os.UserHomeDir()
		gopath = filepath.Join(home, "go")
	}
	return filepath.Join(filepath.SplitList(gopath)[0], "pkg", "mod")
}

func seedCache(source, target string) {
	destRoot, err := os.OpenRoot(target)
	if err != nil {
		return
	}
	defer destRoot.Close()
	remaining := 20000
	deadline := time.Now().Add(100 * time.Millisecond)
	_ = filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir
		}
		remaining--
		if remaining < 0 || time.Now().After(deadline) {
			return fs.SkipAll
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == "." {
			return nil
		}
		if entry.IsDir() {
			// Never follow a cache directory symlink created by a prior shell.
			if info, err := destRoot.Lstat(rel); err == nil && !info.IsDir() {
				return fs.SkipDir
			}
			if err := destRoot.Mkdir(rel, 0700); err != nil && !os.IsExist(err) {
				return fs.SkipDir
			}
		} else if entry.Type().IsRegular() {
			_ = destRoot.Symlink(path, rel)
		}
		return nil
	})
}
