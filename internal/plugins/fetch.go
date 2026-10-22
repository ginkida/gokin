package plugins

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"gokin/internal/fileutil"
	"gokin/internal/pluginpolicy"
	"gokin/internal/security"
)

const maxTreeBytes = 256 << 20
const maxDownloadBytes = 64 << 20
const maxTreeEntries = 20000
const maxFileBytes = 32 << 20

func validateGitURL(raw string) error {
	if strings.HasPrefix(raw, "git@") {
		host, path, ok := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !ok || host == "" || path == "" || strings.ContainsAny(raw, "\x00\r\n \t") {
			return fmt.Errorf("invalid SSH git URL")
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || strings.ContainsAny(raw, "\x00\r\n") || u.Fragment != "" || u.RawQuery != "" {
		return fmt.Errorf("invalid git URL")
	}
	switch u.Scheme {
	case "https", "http", "ssh":
		if u.Hostname() == "" || u.User != nil && u.Scheme != "ssh" {
			return fmt.Errorf("git URL must not contain credentials")
		}
		if u.Scheme == "http" && !loopbackHost(u.Hostname()) {
			return fmt.Errorf("remote git sources require HTTPS or SSH")
		}
	case "file":
		if u.Host != "" && u.Host != "localhost" || !filepath.IsAbs(filepath.FromSlash(u.Path)) {
			return fmt.Errorf("file git source must be local and absolute")
		}
	default:
		return fmt.Errorf("git URL must use HTTPS, SSH or file://")
	}
	return nil
}
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func validateHTTPURL(raw string, archive bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("download URL must be an absolute HTTP(S) URL without credentials or fragment")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !loopbackHost(u.Hostname()) || archive) {
		return fmt.Errorf("remote downloads require HTTPS")
	}
	if archive {
		ip := net.ParseIP(u.Hostname())
		if loopbackHost(u.Hostname()) || strings.EqualFold(u.Hostname(), "metadata.google.internal") || ip != nil && blockedArchiveIP(ip) {
			return fmt.Errorf("archive source must not target a local or metadata address")
		}
	}
	return nil
}
func blockedArchiveIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() || ip.String() == "100.100.100.200" || ip.String() == "fd00:ec2::254"
}
func download(ctx context.Context, raw string, headers map[string]string, archive bool, writer io.Writer, limit int64) error {
	if err := validateHTTPURL(raw, archive); err != nil {
		return err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if archive {
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if blockedArchiveIP(ip.IP) {
					return nil, fmt.Errorf("archive host resolves to a local or metadata address")
				}
			}
			var last error
			for _, ip := range ips {
				conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		}
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 60 * time.Second, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return fmt.Errorf("download redirect limit exceeded")
		}
		if err := validateHTTPURL(request.URL.String(), archive); err != nil {
			return err
		}
		if len(via) > 0 && request.URL.Host != via[0].URL.Host {
			for key := range request.Header {
				request.Header.Del(key)
			}
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	for key, value := range headers {
		if key == "" || strings.ContainsAny(key+value, "\r\n\x00") {
			return fmt.Errorf("invalid download header")
		}
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return fmt.Errorf("download exceeds byte limit")
	}
	count, err := io.Copy(writer, io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	if count > limit {
		return fmt.Errorf("download exceeds byte limit")
	}
	return nil
}

func commandEnv() []string {
	var out []string
	for _, key := range []string{"PATH", "HOME", "USER", "TMPDIR", "TMP", "TEMP", "SystemRoot", "SYSTEMROOT", "COMSPEC", "LANG", "LC_ALL", "SSH_AUTH_SOCK", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "NPM_CONFIG_USERCONFIG", "npm_config_userconfig", "NPM_CONFIG_REGISTRY", "npm_config_registry"} {
		if value, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+value)
		}
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_LFS_SKIP_SMUDGE=1", "npm_config_ignore_scripts=true")
}
func runCommand(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	configureProcess(cmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = dir
	cmd.Env = commandEnv()
	var stdout, stderr boundedOutput
	stdout.limit = 1 << 20
	stderr.limit = 64 << 10
	stdout.stop = func() {
		if cmd.Cancel != nil {
			_ = cmd.Cancel()
		}
	}
	stderr.stop = stdout.stop
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s failed: %w: %s", name, err, security.NewSecretRedactor().Redact(stderr.String()))
	}
	return stdout.Bytes(), nil
}

type boundedOutput struct {
	bytes.Buffer
	limit int
	stop  func()
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > b.limit {
		if b.stop != nil {
			b.stop()
		}
		return 0, fmt.Errorf("command output exceeds limit")
	}
	return b.Buffer.Write(data)
}

func gitArgs(args ...string) []string {
	return append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "protocol.ext.allow=never", "-c", "protocol.file.allow=always"}, args...)
}
func fetchGit(ctx context.Context, source Source, stage string) (string, string, error) {
	raw := pluginpolicy.GitURL(source)
	if err := validateGitURL(raw); err != nil {
		return "", "", err
	}
	repo := filepath.Join(stage, "repo")
	args := []string{"clone", "--no-checkout", "--no-local", "--no-hardlinks", "--depth=1"}
	if source.Ref != "" {
		args = append(args, "--branch", source.Ref)
	}
	args = append(args, "--", raw, repo)
	if _, err := runCommand(ctx, stage, "git", gitArgs(args...)...); err != nil {
		return "", "", err
	}
	ref := "HEAD"
	if source.SHA != "" {
		if !hexSHA.MatchString(source.SHA) {
			return "", "", fmt.Errorf("invalid git sha")
		}
		if _, err := runCommand(ctx, stage, "git", gitArgs("-C", repo, "fetch", "--depth=1", "origin", source.SHA)...); err != nil {
			return "", "", err
		}
		ref = source.SHA
	}
	output, err := runCommand(ctx, stage, "git", gitArgs("-C", repo, "rev-parse", "--verify", ref+"^{commit}")...)
	if err != nil {
		return "", "", err
	}
	commit := strings.TrimSpace(string(output))
	if !hexSHA.MatchString(commit) {
		return "", "", fmt.Errorf("git returned invalid commit")
	}
	dest := filepath.Join(stage, "tree")
	if err := os.Mkdir(dest, 0700); err != nil {
		return "", "", err
	}
	archiveArgs := gitArgs("-C", repo, "archive", "--format=tar", commit)
	if source.Kind == "git-subdir" {
		path, err := safeRelative(source.Path, false)
		if err != nil {
			return "", "", err
		}
		archiveArgs = append(archiveArgs, "--", filepath.ToSlash(path))
	}
	archiveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(archiveCtx, "git", archiveArgs...)
	configureProcess(cmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = commandEnv()
	cmd.Dir = stage
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", err
	}
	var stderr boundedOutput
	stderr.limit = 64 << 10
	cmd.Stderr = &stderr
	stderr.stop = cancel
	if err := cmd.Start(); err != nil {
		return "", "", err
	}
	extractErr := extractTar(stdout, dest)
	if extractErr != nil {
		cancel()
		stdout.Close()
	}
	waitErr := cmd.Wait()
	if extractErr != nil {
		return "", "", extractErr
	}
	if waitErr != nil {
		return "", "", fmt.Errorf("git archive failed: %w", waitErr)
	}
	if source.Kind == "git-subdir" {
		dest = filepath.Join(dest, source.Path)
	}
	return dest, commit, nil
}

type treeBudget struct {
	count int
	bytes int64
	seen  map[string]bool
}

func (b *treeBudget) entry(path string, size int64) error {
	b.count++
	b.bytes += size
	if b.count > maxTreeEntries || size < 0 || size > maxFileBytes || b.bytes > maxTreeBytes {
		return fmt.Errorf("plugin tree exceeds entry or byte limit")
	}
	if b.seen == nil {
		b.seen = map[string]bool{}
	}
	if b.seen[path] {
		return fmt.Errorf("duplicate archive path")
	}
	b.seen[path] = true
	return nil
}
func archivePath(raw string) (string, error) { return safeRelative(strings.TrimSuffix(raw, "/"), true) }
func extractTar(reader io.Reader, dest string) error {
	tr := tar.NewReader(reader)
	budget := treeBudget{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		path, err := archivePath(header.Name)
		if err != nil {
			return err
		}
		if err := budget.entry(path, header.Size); err != nil {
			return err
		}
		if header.Typeflag == tar.TypeXGlobalHeader || header.Typeflag == tar.TypeXHeader {
			continue
		}
		target := filepath.Join(dest, path)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			mode := os.FileMode(0600)
			if header.Mode&0111 != 0 {
				mode = 0700
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, tr)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("archive links and special files are unsupported")
		}
	}
}
func extractZIP(path, dest string) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer archive.Close()
	budget := treeBudget{}
	for _, entry := range archive.File {
		relative, err := archivePath(entry.Name)
		if err != nil {
			return err
		}
		if entry.UncompressedSize64 > maxFileBytes {
			return fmt.Errorf("archive file exceeds limit")
		}
		if err := budget.entry(relative, int64(entry.UncompressedSize64)); err != nil {
			return err
		}
		target := filepath.Join(dest, relative)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			return fmt.Errorf("archive links and special files are unsupported")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		in, err := entry.Open()
		if err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if entry.Mode()&0111 != 0 {
			mode = 0700
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			in.Close()
			return err
		}
		n, copyErr := io.Copy(out, io.LimitReader(in, maxFileBytes+1))
		closeErr := out.Close()
		in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n > maxFileBytes || uint64(n) != entry.UncompressedSize64 {
			return fmt.Errorf("archive member size mismatch")
		}
	}
	return nil
}

// copyTree opens source paths through os.Root. Contained symlink files are
// copied as files; escaping links, directory links and special files fail.
func copyTree(source, dest string) (string, error) {
	root, err := os.OpenRoot(source)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := os.MkdirAll(dest, 0700); err != nil {
		return "", err
	}
	budget := treeBudget{}
	hash := sha256.New()
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := root.Stat(path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 && info.IsDir() {
			return fmt.Errorf("plugin directory symlinks are unsupported")
		}
		target := filepath.Join(dest, filepath.FromSlash(path))
		if info.IsDir() {
			if err := budget.entry(path, 0); err != nil {
				return err
			}
			return os.MkdirAll(target, 0700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin tree contains special file")
		}
		// npm/Bun bin links must retain their location relative to the package
		// entry point (flattening them changes __dirname and breaks CLIs).
		if entry.Type()&os.ModeSymlink != 0 && (strings.Contains("/"+path, "/node_modules/.bin/") || filepath.Base(source) == "node_modules" && strings.HasPrefix(path, ".bin/")) {
			link, err := root.Readlink(path)
			if err != nil {
				return err
			}
			resolved := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(path), link)))
			if filepath.IsAbs(link) || resolved == ".." || strings.HasPrefix(resolved, "../") {
				return fmt.Errorf("package bin link escapes node_modules")
			}
			// Opening through os.Root additionally rejects escaping symlink chains.
			file, err := root.Open(path)
			if err != nil {
				return err
			}
			file.Close()
			if err := budget.entry(path, int64(len(link))); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			fmt.Fprintf(hash, "%d:%s:link:%d:%s", len(path), path, len(link), link)
			return os.Symlink(link, target)
		}
		if err := budget.entry(path, info.Size()); err != nil {
			return err
		}
		in, err := root.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if info.Mode()&0111 != 0 {
			mode = 0700
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%d:%s:%o:%d:", len(path), path, mode, info.Size())
		n, copyErr := io.Copy(io.MultiWriter(out, hash), io.LimitReader(in, maxFileBytes+1))
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != info.Size() {
			return fmt.Errorf("plugin file changed while copying")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateNPMSource(s Source) error {
	if strings.HasPrefix(s.Package, "https://") {
		if err := validateHTTPURL(s.Package, true); err != nil {
			return err
		}
		u, _ := url.Parse(s.Package)
		host := strings.ToLower(u.Hostname())
		if host == "github.com" || host == "gist.github.com" || host == "bitbucket.org" || host == "git.sr.ht" || host == "gitlab.com" && !strings.HasPrefix(u.Path, "/api/v4/") {
			return fmt.Errorf("npm tarball must not be a git-hosted download")
		}
	} else if !regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9_.-]*/)?[a-z0-9][a-z0-9_.-]*(?:@[a-zA-Z0-9*^~<>=. |+-]+)?$`).MatchString(s.Package) {
		return fmt.Errorf("npm source must name a registry package, not a path, alias or git source")
	}
	if s.Version != "" && !regexp.MustCompile(`^[a-zA-Z0-9*^~<>=. |+-]{1,128}$`).MatchString(s.Version) {
		return fmt.Errorf("invalid npm version")
	}
	if s.Registry != "" {
		return validateHTTPURL(s.Registry, false)
	}
	return nil
}
func fetchNPM(ctx context.Context, s Source, stage string) (string, error) {
	if err := validateNPMSource(s); err != nil {
		return "", err
	}
	spec := s.Package
	if s.Version != "" && !strings.HasPrefix(spec, "https://") && strings.LastIndex(spec, "@") <= 0 {
		spec += "@" + s.Version
	}
	args := []string{"pack", "--ignore-scripts", "--json", "--cache", filepath.Join(stage, "npm-cache"), "--pack-destination", stage}
	if s.Registry != "" {
		args = append(args, "--registry", s.Registry)
	}
	args = append(args, "--", spec)
	output, err := runCommand(ctx, stage, "npm", args...)
	if err != nil {
		return "", err
	}
	var packed []struct {
		Filename string `json:"filename"`
	}
	if json.Unmarshal(output, &packed) != nil || len(packed) != 1 {
		return "", fmt.Errorf("npm returned invalid pack output")
	}
	name, err := safeRelative(packed[0].Filename, false)
	if err != nil {
		return "", err
	}
	path := filepath.Join(stage, name)
	file, err := fileutil.OpenRegularRead(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > maxDownloadBytes {
		return "", fmt.Errorf("npm package exceeds download limit")
	}
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return "", err
	}
	defer gzipReader.Close()
	dest := filepath.Join(stage, "package-tree")
	os.Mkdir(dest, 0700)
	if err := extractTar(gzipReader, dest); err != nil {
		return "", err
	}
	return filepath.Join(dest, "package"), nil
}

func acceptedCommand(command string, accept []string) error {
	// Validate before showing or accepting remote helper text. An exact hash
	// cannot make terminal controls safe to review or execute.
	if command == "" || len(command) > 500 {
		return fmt.Errorf("reviewed command must contain 1..500 printable ASCII characters")
	}
	for _, c := range command {
		if c < 32 || c > 126 {
			return fmt.Errorf("reviewed command must be printable ASCII")
		}
	}
	sum := sha256.Sum256([]byte(command))
	digest := hex.EncodeToString(sum[:])
	for _, hash := range accept {
		if strings.EqualFold(hash, digest) {
			return nil
		}
	}
	return &CommandApprovalError{ShownCommand{Command: command, SHA256: digest}}
}
func runReviewedCommand(ctx context.Context, command string, timeout int, accept []string) ([]byte, error) {
	if err := acceptedCommand(command, accept); err != nil {
		return nil, err
	}
	if timeout == 0 {
		timeout = 60
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" {
		return runCommand(ctx, home, "cmd.exe", "/d", "/s", "/c", command)
	}
	return runCommand(ctx, home, "sh", "-c", command)
}
func headersFor(ctx context.Context, source Source, entry Entry, accept []string) (map[string]string, error) {
	headers := map[string]string{}
	for k, v := range source.Headers {
		headers[k] = v
	}
	for k, v := range entry.Headers {
		headers[k] = v
	}
	helper := entry.HeadersHelper
	if helper == "" {
		helper = source.HeadersHelper
	}
	if helper != "" {
		output, err := runReviewedCommand(ctx, helper, 60, accept)
		if err != nil {
			return nil, err
		}
		var generated map[string]string
		if pluginpolicy.ValidateJSON(output) != nil || json.Unmarshal(output, &generated) != nil || len(generated) > 128 {
			return nil, fmt.Errorf("headers helper must print one bounded JSON object")
		}
		for k, v := range generated {
			headers[k] = v
		}
	}
	return headers, nil
}

func findPluginRoot(directory string) (string, error) {
	if pluginContent(directory) {
		return directory, nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	if len(entries) == 1 && entries[0].IsDir() {
		child := filepath.Join(directory, entries[0].Name())
		if pluginContent(child) {
			return child, nil
		}
	}
	return "", fmt.Errorf("source has no plugin content at its root or one directory below")
}
func pluginContent(path string) bool {
	for _, name := range []string{".claude-plugin", "skills", "commands", "agents", "hooks", ".mcp.json", ".lsp.json"} {
		if _, err := os.Stat(filepath.Join(path, name)); err == nil {
			return true
		}
	}
	return false
}
