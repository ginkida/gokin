package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

var (
	logger *slog.Logger
	// logSink owns the open descriptor and rotates it while the process runs.
	logSink *rotatingFileWriter
	logPath string
	mu      sync.RWMutex
)

func init() {
	// Default: discard logs to avoid TUI interference
	// Use EnableFileLogging() to enable logging to a file
	logger = slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

const (
	maxLogFileSize = 10 * 1024 * 1024 // 10 MB
)

// EnableFileLogging enables logging to a file in the config directory.
// This should be called before the TUI starts.
func EnableFileLogging(configDir string, level Level) error {
	logPath := filepath.Join(configDir, "gokin.log")
	return EnablePathLogging(logPath, level, "")
}

// EnablePathLogging enables JSON logging at an explicit path. The parent
// directory and file are private because diagnostic records can contain
// repository paths and provider error text even after secret redaction.
func EnablePathLogging(path string, level Level, rawFilter string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("log path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve log path: %w", err)
	}
	parsedFilter, err := parseCategoryFilter(rawFilter)
	if err != nil {
		return err
	}
	parent := filepath.Dir(absolute)
	if _, err := os.Stat(parent); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return fmt.Errorf("create log directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect log directory: %w", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Rotate if the log file exceeds the size limit
	if info, err := os.Stat(absolute); err == nil {
		if info.IsDir() {
			return fmt.Errorf("log path is a directory: %s", absolute)
		}
		if info.Size() > maxLogFileSize {
			// Keep one backup
			backupPath := absolute + ".old"
			_ = os.Remove(backupPath)
			if os.Rename(absolute, backupPath) == nil {
				_ = os.Chmod(backupPath, 0o600)
			}
		}
	}

	f, err := os.OpenFile(absolute, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("secure log file: %w", err)
	}

	// Close previous log file if any
	if logSink != nil {
		_ = logSink.Close()
	}
	var existing int64
	if info, statErr := f.Stat(); statErr == nil {
		existing = info.Size()
	}
	logSink = newRotatingFileWriter(f, absolute, existing, maxLogFileSize)
	logPath = absolute

	var slogLevel slog.Level
	switch strings.ToLower(string(level)) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "info":
		slogLevel = slog.LevelInfo
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelWarn
	}

	logger = newSafeLogger(logSink, slogLevel, parsedFilter)

	return nil
}

// DisableLogging disables all logging output.
func DisableLogging() {
	mu.Lock()
	defer mu.Unlock()

	if logSink != nil {
		_ = logSink.Close()
		logSink = nil
	}
	logPath = ""

	logger = newSafeLogger(io.Discard, slog.LevelError, categoryFilter{})
}

// Close closes the log file if open.
func Close() {
	mu.Lock()
	defer mu.Unlock()

	if logSink != nil {
		_ = logSink.Close()
		logSink = nil
	}
	logPath = ""
}

// Level represents a logging level.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Configure configures the global logger with the given level and writer.
func Configure(level Level, w io.Writer) {
	mu.Lock()
	defer mu.Unlock()

	var slogLevel slog.Level
	switch strings.ToLower(string(level)) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "info":
		slogLevel = slog.LevelInfo
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	if w == nil {
		w = os.Stderr
	}

	logPath = ""
	logger = newSafeLogger(w, slogLevel, categoryFilter{})
}

// SetLevel sets the logging level.
func SetLevel(level Level) {
	Configure(level, nil)
}

// Debug logs a debug message.
func Debug(msg string, args ...any) {
	mu.RLock()
	l := logger
	mu.RUnlock()
	l.Debug(msg, args...)
}

// Info logs an info message.
func Info(msg string, args ...any) {
	mu.RLock()
	l := logger
	mu.RUnlock()
	l.Info(msg, args...)
}

// Warn logs a warning message.
func Warn(msg string, args ...any) {
	mu.RLock()
	l := logger
	mu.RUnlock()
	l.Warn(msg, args...)
}

// Error logs an error message.
func Error(msg string, args ...any) {
	mu.RLock()
	l := logger
	mu.RUnlock()
	l.Error(msg, args...)
}

// With returns a new logger with the given attributes.
func With(args ...any) *slog.Logger {
	mu.RLock()
	l := logger
	mu.RUnlock()
	return l.With(args...)
}

// Logger returns the underlying slog.Logger.
func Logger() *slog.Logger {
	mu.RLock()
	defer mu.RUnlock()
	return logger
}

// CurrentLogPath returns the active absolute file path, or an empty string
// when logging is disabled or configured to a non-file writer.
func CurrentLogPath() string {
	mu.RLock()
	defer mu.RUnlock()
	return logPath
}

type categoryFilter struct {
	include []string
	exclude []string
}

func newSafeLogger(w io.Writer, level slog.Level, filter categoryFilter) *slog.Logger {
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(&safeHandler{next: base, filter: filter})
}

// safeHandler is the single redaction/filter boundary. Wrapping the handler
// instead of only the convenience functions also protects logging.With(...)
// and logging.Logger() callers.
type safeHandler struct {
	next   slog.Handler
	filter categoryFilter
	attrs  []slog.Attr
}

func (h *safeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *safeHandler) Handle(ctx context.Context, record slog.Record) error {
	searchArgs := make([]any, 0, 2*(len(h.attrs)+record.NumAttrs()))
	for _, attr := range h.attrs {
		searchArgs = append(searchArgs, attr.Key, attr.Value.Any())
	}
	record.Attrs(func(attr slog.Attr) bool {
		searchArgs = append(searchArgs, attr.Key, attr.Value.Any())
		return true
	})
	if !h.filter.allows(record.Message, searchArgs) {
		return nil
	}

	safeRecord := slog.NewRecord(
		record.Time, record.Level, redactString(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		safeRecord.AddAttrs(redactAttr(attr))
		return true
	})
	return h.next.Handle(ctx, safeRecord)
}

func (h *safeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safeAttrs := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		safeAttrs[i] = redactAttr(attr)
	}
	filterAttrs := append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &safeHandler{
		next:   h.next.WithAttrs(safeAttrs),
		filter: h.filter,
		attrs:  filterAttrs,
	}
}

func (h *safeHandler) WithGroup(name string) slog.Handler {
	return &safeHandler{
		next:   h.next.WithGroup(name),
		filter: h.filter,
		attrs:  append([]slog.Attr(nil), h.attrs...),
	}
}

func parseCategoryFilter(raw string) (categoryFilter, error) {
	var result categoryFilter
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" {
		return result, nil
	}
	if len(raw) > 1024 {
		return result, errors.New("debug category filter exceeds 1024 bytes")
	}
	for _, item := range strings.Split(raw, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			return result, errors.New("debug category filter contains an empty category")
		}
		excluded := strings.HasPrefix(item, "!")
		if excluded {
			item = strings.TrimSpace(strings.TrimPrefix(item, "!"))
		}
		if item == "" || !validCategory.MatchString(item) {
			return result, fmt.Errorf("invalid debug category %q", item)
		}
		if item == "*" {
			if excluded {
				result.exclude = append(result.exclude, item)
			}
			continue
		}
		if excluded {
			result.exclude = append(result.exclude, item)
		} else {
			result.include = append(result.include, item)
		}
	}
	return result, nil
}

var validCategory = regexp.MustCompile(`^[a-z0-9_.:+-]+$`)

func (f categoryFilter) allows(msg string, args []any) bool {
	if len(f.include) == 0 && len(f.exclude) == 0 {
		return true
	}
	var searchable strings.Builder
	searchable.WriteString(strings.ToLower(msg))
	for i := 0; i < len(args); i++ {
		if attr, ok := args[i].(slog.Attr); ok {
			key := strings.ToLower(attr.Key)
			searchable.WriteByte(' ')
			searchable.WriteString(key)
			if key == "category" || key == "component" || key == "subsystem" {
				searchable.WriteByte(' ')
				searchable.WriteString(strings.ToLower(fmt.Sprint(attr.Value.Any())))
			}
			continue
		}
		if i%2 != 0 {
			continue
		}
		key, ok := args[i].(string)
		if !ok {
			continue
		}
		key = strings.ToLower(key)
		searchable.WriteByte(' ')
		searchable.WriteString(key)
		if (key == "category" || key == "component" || key == "subsystem") && i+1 < len(args) {
			searchable.WriteByte(' ')
			searchable.WriteString(strings.ToLower(fmt.Sprint(args[i+1])))
		}
	}
	haystack := searchable.String()
	for _, category := range f.exclude {
		if category == "*" || strings.Contains(haystack, category) {
			return false
		}
	}
	if len(f.include) == 0 {
		return true
	}
	for _, category := range f.include {
		if strings.Contains(haystack, category) {
			return true
		}
	}
	return false
}

var (
	bearerSecret = regexp.MustCompile(`(?i)(bearer[[:space:]]+)[a-z0-9._~+/=-]{8,}`)
	commonSecret = regexp.MustCompile(`(?i)\b(sk-[a-z0-9_-]{8,}|gh[pousr]_[a-z0-9_]{8,}|AKIA[0-9A-Z]{12,})\b`)
	inlineSecret = regexp.MustCompile(`(?i)((?:api[_-]?key|x-api-key|access[_-]?token|token|secret|password)["']?[[:space:]]*[=:][[:space:]]*["']?)[^"'[:space:],;}]+`)
)

func redactString(value string) string {
	value = bearerSecret.ReplaceAllString(value, "${1}[REDACTED]")
	value = commonSecret.ReplaceAllString(value, "[REDACTED]")
	return inlineSecret.ReplaceAllString(value, "${1}[REDACTED]")
}

func sensitiveLogKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, marker := range []string{"api_key", "apikey", "authorization", "password", "secret", "token", "credential"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func redactLogArgs(args []any) []any {
	if len(args) == 0 {
		return nil
	}
	out := make([]any, len(args))
	copy(out, args)
	for i := 0; i < len(out); i++ {
		if attr, ok := out[i].(slog.Attr); ok {
			out[i] = redactAttr(attr)
			continue
		}
		if i%2 == 0 {
			if _, isKey := out[i].(string); !isKey {
				out[i] = redactLogValue(out[i])
			}
			continue
		}
		key, _ := out[i-1].(string)
		if sensitiveLogKey(key) {
			out[i] = "[REDACTED]"
		} else {
			out[i] = redactLogValue(out[i])
		}
	}
	return out
}

func redactAttr(attr slog.Attr) slog.Attr {
	if sensitiveLogKey(attr.Key) {
		return slog.String(attr.Key, "[REDACTED]")
	}
	if attr.Value.Kind() == slog.KindGroup {
		group := attr.Value.Group()
		for i := range group {
			group[i] = redactAttr(group[i])
		}
		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(group...)}
	}
	return slog.Any(attr.Key, redactLogValue(attr.Value.Any()))
}

func redactLogValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return redactString(typed)
	case error:
		return redactString(typed.Error())
	case fmt.Stringer:
		return redactString(typed.String())
	case []string:
		out := make([]string, len(typed))
		for i, item := range typed {
			out[i] = redactString(item)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, item := range typed {
			if sensitiveLogKey(key) {
				out[key] = "[REDACTED]"
			} else {
				out[key] = redactString(item)
			}
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveLogKey(key) {
				out[key] = "[REDACTED]"
			} else {
				out[key] = redactLogValue(item)
			}
		}
		return out
	default:
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() {
			return nil
		}
		switch reflected.Kind() {
		case reflect.String:
			return redactString(reflected.String())
		case reflect.Map:
			if reflected.Type().Key().Kind() != reflect.String {
				return value
			}
			out := make(map[string]any, reflected.Len())
			iter := reflected.MapRange()
			for iter.Next() {
				key := iter.Key().String()
				if sensitiveLogKey(key) {
					out[key] = "[REDACTED]"
				} else {
					out[key] = redactLogValue(iter.Value().Interface())
				}
			}
			return out
		case reflect.Array, reflect.Slice:
			out := make([]any, reflected.Len())
			for i := range reflected.Len() {
				out[i] = redactLogValue(reflected.Index(i).Interface())
			}
			return out
		default:
			return value
		}
	}
}

// PanicStack captures up to 4096 bytes of the current goroutine's stack
// trace as a string. Intended for use immediately after `recover()` so that
// post-mortem debugging doesn't require reproducing the panic — the stack
// snapshot points at the exact line that faulted.
//
// Idiomatic usage:
//
//	defer func() {
//	    if r := recover(); r != nil {
//	        logging.Error("panic in foo",
//	            "panic", r,
//	            "stack", logging.PanicStack())
//	    }
//	}()
//
// 4096 bytes is enough to capture ~30-50 stack frames in typical Go code,
// which covers any realistic panic path. We don't grow the buffer because
// that would require a second runtime.Stack call that itself could allocate
// during a panic recovery.
func PanicStack() string {
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	return string(buf[:n])
}

// ParseLevel parses a level string to Level.
func ParseLevel(s string) Level {
	switch strings.ToLower(s) {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}
