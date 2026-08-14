"""Gokin stateful read-only Python worker.

The transport is newline-delimited JSON on stdin/stdout. User stdout/stderr is
captured and returned as data, so it can never forge protocol events.
Filesystem and network isolation are owned by the parent OS sandbox; this file
only provides bounded, workspace-contained convenience APIs.
"""

import ast
import builtins
import contextlib
import fnmatch
import io
import json
import os
import pathlib
import re
import signal
import stat
import sys
import traceback
import types
import uuid
from collections import OrderedDict

try:
    import resource
except ImportError:  # Windows
    resource = None

MAX_REQUEST_BYTES = 64 * 1024
# ToolResult currently permits 30,000 visible characters. Three independently
# bounded ASCII channels plus their labels and artifact metadata must fit below
# that outer limit, otherwise a later transport layer can silently hide the
# artifact handles. 8 KiB per channel leaves a deterministic safety margin.
MAX_INLINE_BYTES = 8 * 1024
MAX_ARTIFACT_CHUNK_BYTES = 4 * 1024
MAX_CAPTURE_BYTES = 4 * 1024 * 1024
MAX_ARTIFACT_BYTES = 4 * 1024 * 1024
MAX_ARTIFACT_TOTAL = 16 * 1024 * 1024
MAX_FILE_BYTES = 2 * 1024 * 1024
MAX_SEARCH_FILES = 20_000
MAX_SEARCH_MATCHES = 500
MAX_SEARCH_QUERY_CHARS = 4_096
MAX_COUNT_QUERIES = 32
MAX_COUNT_SAMPLES = 20
MAX_COUNT_SAMPLE_TEXT_CHARS = 300
MAX_FILE_STAT_GROUPS = 1_024
MAX_READ_LINES = 2_000
MAX_FILE_INDEX_BYTES = 4 * 1024 * 1024
MAX_FILE_INDEX_CACHE_BYTES = MAX_FILE_INDEX_BYTES
MEMORY_POLL_SECONDS = 0.025

# Operational eval evidence belongs to the worker, not to the model-visible
# Context object. The caller-code allowlist prevents cell code from incrementing
# a counter merely by discovering and invoking this private helper.
_operation_counts = {}
_context_operation_callers = {}


class _CellEntrySnapshot:
    """Replay one scope's entries without forcing the first scan to materialise."""

    def __init__(self, entries, truncated):
        self._source = iter(entries)
        self._items = []
        self._complete = False
        self.truncated = bool(truncated)

    def iterate(self):
        index = 0
        while True:
            if index < len(self._items):
                item = self._items[index]
            elif self._complete:
                return
            else:
                try:
                    item = next(self._source)
                except StopIteration:
                    self._source = None
                    self._complete = True
                    return
                if _reserve_cell_entry(item):
                    self._items.append(item)
                elif self._items:
                    # Reuse is an optimization, not permission to retain an
                    # unbounded number of Python Path/tuple objects.
                    self._items.clear()
            index += 1
            yield item


_cell_entry_cache = {}
_cell_entry_cache_enabled = False
_cell_entry_cache_entries = 0
_cell_entry_cache_bytes = 0


def _begin_cell_entry_cache():
    global _cell_entry_cache, _cell_entry_cache_enabled
    global _cell_entry_cache_entries, _cell_entry_cache_bytes
    _cell_entry_cache = {}
    _cell_entry_cache_enabled = True
    _cell_entry_cache_entries = 0
    _cell_entry_cache_bytes = 0


def _invalidate_cell_entry_cache():
    global _cell_entry_cache, _cell_entry_cache_enabled
    global _cell_entry_cache_entries, _cell_entry_cache_bytes
    _cell_entry_cache = {}
    # A callback may launch asynchronous mutation. Do not re-enable reuse until
    # the next cell merely because the synchronous callback itself returned.
    _cell_entry_cache_enabled = False
    _cell_entry_cache_entries = 0
    _cell_entry_cache_bytes = 0


def _reserve_cell_entry(item):
    global _cell_entry_cache_entries, _cell_entry_cache_bytes
    if not _cell_entry_cache_enabled:
        return False
    path_bytes = len(os.fsencode(str(item[0]))) + 1
    if (
        _cell_entry_cache_entries >= MAX_SEARCH_FILES
        or _cell_entry_cache_bytes + path_bytes > MAX_FILE_INDEX_CACHE_BYTES
    ):
        _invalidate_cell_entry_cache()
        return False
    _cell_entry_cache_entries += 1
    _cell_entry_cache_bytes += path_bytes
    return True


def _begin_operation_capture():
    _operation_counts.clear()


def _record_context_operation(name):
    allowed = _context_operation_callers.get(sys._getframe(1).f_code, ())
    if name not in allowed:
        raise PermissionError("operation telemetry is worker-owned")
    _operation_counts[name] = _operation_counts.get(name, 0) + 1


def _operation_snapshot():
    return dict(sorted(_operation_counts.items()))


class _MemoryLimitExceeded(MemoryError):
    pass


def _install_memory_watchdog(limit, generation):
    limit = max(0, int(limit))
    available = bool(
        resource is not None
        and hasattr(signal, "SIGALRM")
        and hasattr(signal, "setitimer")
        and hasattr(signal, "ITIMER_REAL")
    )
    state = [""]

    def set_request(request_id):
        state[0] = str(request_id or "")

    if not available or limit <= 0:
        return available, set_request, lambda: None

    getrusage = resource.getrusage
    rusage_self = resource.RUSAGE_SELF
    rss_scale = 1 if sys.platform == "darwin" else 1024
    encode_json = json.dumps
    write_fd = os.write
    hard_exit = os._exit
    stdout_fd = sys.__stdout__.fileno()

    def check(_signum=None, _frame=None):
        peak = int(getrusage(rusage_self).ru_maxrss) * rss_scale
        if peak <= limit:
            return
        request_id = state[0]
        if request_id:
            response = {
                "type": "response",
                "id": request_id,
                "ok": False,
                "generation": generation,
                "kernel_reset": True,
                "error": {
                    "type": "MemoryLimitExceeded",
                    "message": (
                        "worker peak RSS exceeded %d-byte limit "
                        "(observed %d bytes)" % (limit, peak)
                    ),
                },
            }
            try:
                # This fixed-size frame is well below PIPE_BUF, so the parent
                # sees either one complete fatal response or an ordinary EOF.
                payload = (
                    encode_json(response, ensure_ascii=False, separators=(",", ":"))
                    + "\n"
                ).encode("utf-8")
                write_fd(stdout_fd, payload)
            except BaseException:
                pass
        # A Python exception is catchable by cell code. Exiting is intentional:
        # a hard resource boundary must not be suppressible with `except`.
        hard_exit(86)

    signal.signal(signal.SIGALRM, check)
    signal.setitimer(signal.ITIMER_REAL, MEMORY_POLL_SECONDS, MEMORY_POLL_SECONDS)
    return True, set_request, check


def _utf8_prefix_bytes(value, limit):
    """Return a valid UTF-8 prefix without encoding an unbounded full value."""
    text = str(value)
    limit = max(0, int(limit))
    char_truncated = len(text) > limit
    candidate = text[:limit] if char_truncated else text
    raw = candidate.encode("utf-8", "replace")
    if len(raw) > limit:
        raw = raw[:limit].decode("utf-8", "ignore").encode("utf-8")
        return raw, True
    return raw, char_truncated


def _utf8_tail_bytes(value, limit):
    """Return a valid UTF-8 suffix without encoding an unbounded full value."""
    text = str(value)
    limit = max(0, int(limit))
    if limit == 0:
        return b"", bool(text)
    char_truncated = len(text) > limit
    candidate = text[-limit:] if char_truncated else text
    raw = candidate.encode("utf-8", "replace")
    if len(raw) > limit:
        raw = raw[-limit:].decode("utf-8", "ignore").encode("utf-8")
        return raw, True
    return raw, char_truncated


def _truncate_utf8(value, limit):
    raw, _ = _utf8_prefix_bytes(value, limit)
    return raw.decode("utf-8")


def _apply_resource_limits(backend):
    applied = {}
    if resource is None:
        return {"available": False}

    def apply(name, requested):
        limit = getattr(resource, name, None)
        if limit is None:
            return
        try:
            _, hard = resource.getrlimit(limit)
            target = int(requested)
            if hard != resource.RLIM_INFINITY:
                target = min(target, int(hard))
            resource.setrlimit(limit, (target, target))
            soft, final_hard = resource.getrlimit(limit)
            applied[name] = {"soft": int(soft), "hard": int(final_hard)}
        except (OSError, ValueError) as exc:
            applied[name] = {"error": type(exc).__name__}

    # These limits are safe for the persistent interpreter on every POSIX host.
    apply("RLIMIT_CORE", 0)
    apply("RLIMIT_FSIZE", 16 * 1024 * 1024)
    apply("RLIMIT_NOFILE", 128)
    # bubblewrap runs in isolated namespaces, so per-user process accounting
    # cannot interfere with unrelated desktop/server processes.
    if backend == "bubblewrap":
        apply("RLIMIT_AS", 2 * 1024 * 1024 * 1024)
        apply("RLIMIT_NPROC", 128)
    applied["available"] = True
    return applied


class _BoundedText(io.TextIOBase):
    def __init__(self, limit=MAX_CAPTURE_BYTES):
        self._limit = limit
        self._parts = []
        self._size = 0
        self.truncated = False

    def writable(self):
        return True

    def write(self, value):
        value = str(value)
        if self.truncated:
            return len(value)
        remaining = self._limit - self._size
        if remaining > 0:
            encoded, truncated = _utf8_prefix_bytes(value, remaining)
            chunk = encoded.decode("utf-8")
            if chunk:
                self._parts.append(chunk)
                self._size += len(encoded)
        else:
            encoded = b""
            truncated = bool(value)
        if truncated:
            self.truncated = True
        return len(value)

    def getvalue(self):
        return "".join(self._parts)


class _Artifacts:
    def __init__(self):
        self._items = OrderedDict()
        self._total = 0

    def put(self, value, source_truncated=False):
        raw, bounded = _utf8_prefix_bytes(value, MAX_ARTIFACT_BYTES)
        truncated = source_truncated or bounded
        while self._items and self._total + len(raw) > MAX_ARTIFACT_TOTAL:
            _, old = self._items.popitem(last=False)
            self._total -= len(old)
        artifact_id = "art_" + uuid.uuid4().hex
        self._items[artifact_id] = raw
        self._total += len(raw)
        return {"id": artifact_id, "size": len(raw), "truncated": truncated}

    def get(self, artifact_id, offset=0, limit=MAX_ARTIFACT_CHUNK_BYTES):
        if artifact_id not in self._items:
            raise KeyError("unknown or expired artifact: " + str(artifact_id))
        raw = self._items[artifact_id]
        self._items.move_to_end(artifact_id)
        requested_offset = max(0, min(int(offset), len(raw)))
        limit = max(1, min(int(limit), MAX_ARTIFACT_CHUNK_BYTES))
        offset = requested_offset
        while offset < len(raw) and raw[offset] & 0xC0 == 0x80:
            offset += 1
        end = min(offset + limit, len(raw))
        while end > offset and end < len(raw) and raw[end] & 0xC0 == 0x80:
            end -= 1
        if end == offset and offset < len(raw):
            end = offset + 1
            while end < len(raw) and raw[end] & 0xC0 == 0x80:
                end += 1
        chunk = raw[offset:end]
        return {
            "id": artifact_id,
            "offset": offset,
            "next_offset": end,
            "size": len(raw),
            "content": chunk.decode("utf-8"),
            "has_more": end < len(raw),
        }


_artifacts = _Artifacts()


def _protocol_call(method, params):
    if method != "context.file_index":
        _invalidate_cell_entry_cache()
    call_id = "call_" + uuid.uuid4().hex
    _write({"type": "call", "id": call_id, "method": method, "params": params})
    raw = sys.stdin.buffer.readline()
    if not raw:
        raise RuntimeError("orchestrator closed while handling " + method)
    response = json.loads(raw)
    if response.get("type") != "call_result" or response.get("id") != call_id:
        raise RuntimeError("invalid orchestrator callback response")
    if not response.get("ok"):
        raise RuntimeError(str(response.get("error") or "orchestrator callback failed"))
    result = response.get("result")
    if isinstance(result, dict) and result.get("success") is False:
        raise RuntimeError(str(result.get("error") or "delegated operation failed"))
    return result


class RLMFuture:
    def __init__(self, agent_id):
        self.agent_id = str(agent_id)

    def result(self, timeout=600):
        timeout_ms = max(100, min(int(float(timeout) * 1000), 600_000))
        return _protocol_call(
            "rlm.result",
            {
                "agent_id": self.agent_id,
                "block": True,
                "timeout_ms": timeout_ms,
            },
        )

    def poll(self):
        return _protocol_call(
            "rlm.result",
            {
                "agent_id": self.agent_id,
                "block": False,
            },
        )

    def cancel(self):
        return _protocol_call("rlm.cancel", {"agent_id": self.agent_id})

    def __repr__(self):
        return "RLMFuture(agent_id=%r)" % self.agent_id


class RLM:
    def __call__(
        self,
        instruction,
        dynamic_context=None,
        *,
        agent_type="general",
        max_turns=20,
        model="",
    ):
        return _protocol_call(
            "rlm.call",
            {
                "instruction": str(instruction),
                "dynamic_context": dynamic_context,
                "agent_type": str(agent_type),
                "max_turns": int(max_turns),
                "model": str(model),
                "async": False,
            },
        )

    def async_call(
        self,
        instruction,
        dynamic_context=None,
        *,
        agent_type="general",
        max_turns=20,
        model="",
    ):
        response = _protocol_call(
            "rlm.call",
            {
                "instruction": str(instruction),
                "dynamic_context": dynamic_context,
                "agent_type": str(agent_type),
                "max_turns": int(max_turns),
                "model": str(model),
                "async": True,
            },
        )
        data = response.get("data", {}) if isinstance(response, dict) else {}
        agent_id = data.get("agent_id")
        if not agent_id:
            raise RuntimeError("background delegation returned no agent_id")
        return RLMFuture(agent_id)


class Harness:
    def create_prompt(self, text):
        return _protocol_call("harness.prompt_create", {"text": str(text)})

    def update_prompt(self, text, patch_id=None):
        if patch_id is None:
            return self.create_prompt(text)
        return _protocol_call(
            "harness.prompt_update",
            {
                "id": str(patch_id),
                "text": str(text),
            },
        )

    def list_prompts(self):
        return _protocol_call("harness.prompt_list", {})

    def delete_prompt(self, patch_id):
        return _protocol_call("harness.prompt_delete", {"id": str(patch_id)})

    def put_memory(self, key, value):
        return _protocol_call(
            "harness.memory_put",
            {
                "key": str(key),
                "value": str(value),
            },
        )

    def get_memory(self, key):
        return _protocol_call("harness.memory_get", {"key": str(key)})

    def list_memory(self):
        return _protocol_call("harness.memory_list", {})

    def delete_memory(self, key):
        return _protocol_call("harness.memory_delete", {"key": str(key)})

    def create_skill(self, name, code, description="Proposed harness helper"):
        return _protocol_call(
            "harness.skill_propose",
            {
                "name": str(name),
                "description": str(description),
                "code": str(code),
            },
        )

    def list_skills(self):
        return _protocol_call("harness.skill_list", {})

    def delete_skill(self, name):
        return _protocol_call("harness.skill_delete", {"name": str(name)})


class _ImmutableContextType(type):
    def __getattribute__(cls, name):
        if str(name).startswith("_") and sys._getframe(1).f_code.co_filename == "<gokin-repl>":
            raise PermissionError("private context attributes are disabled in the REPL")
        return type.__getattribute__(cls, name)

    def __setattr__(cls, name, value):
        if sys._getframe(1).f_code.co_filename == "<gokin-repl>":
            raise PermissionError("the context API is immutable")
        type.__setattr__(cls, name, value)


class Context(metaclass=_ImmutableContextType):
    _SKIP_DIRS = {
        ".git",
        ".hg",
        ".svn",
        ".gokin",
        ".agents",
        ".codex",
        ".claude",
        "node_modules",
        "vendor",
        ".idea",
        ".vscode",
    }
    _BINARY_SUFFIXES = {
        ".7z", ".a", ".avi", ".bin", ".bmp", ".class", ".db", ".dll",
        ".dylib", ".eot", ".exe", ".gif", ".gz", ".ico", ".jar", ".jpeg",
        ".jpg", ".mov", ".mp3", ".mp4", ".o", ".obj", ".otf", ".pdf",
        ".png", ".pyc", ".so", ".sqlite", ".sqlite3", ".tar", ".tgz",
        ".ttf", ".wasm", ".webm", ".webp", ".woff", ".woff2", ".xz",
        ".zip",
    }

    def __getattribute__(self, name):
        if str(name).startswith("_") and sys._getframe(1).f_code.co_filename == "<gokin-repl>":
            raise PermissionError("private context attributes are disabled in the REPL")
        return object.__getattribute__(self, name)

    def __setattr__(self, name, value):
        if sys._getframe(1).f_code.co_filename == "<gokin-repl>":
            raise PermissionError("the context API is immutable")
        object.__setattr__(self, name, value)

    def __init__(self, workdir, runtime_limits):
        self._root = pathlib.Path(workdir).resolve(strict=True)
        self._runtime_limits = dict(runtime_limits)
        self._runtime_limits.update(
            {
                "max_file_bytes": MAX_FILE_BYTES,
                "max_search_files": MAX_SEARCH_FILES,
                "max_search_matches": MAX_SEARCH_MATCHES,
                "max_search_query_chars": MAX_SEARCH_QUERY_CHARS,
                "max_count_queries": MAX_COUNT_QUERIES,
                "max_count_samples": MAX_COUNT_SAMPLES,
                "max_count_sample_text_chars": MAX_COUNT_SAMPLE_TEXT_CHARS,
                "max_file_stat_groups": MAX_FILE_STAT_GROUPS,
                "max_read_lines": MAX_READ_LINES,
                "max_file_index_cache_bytes": MAX_FILE_INDEX_CACHE_BYTES,
                "max_inline_bytes": MAX_INLINE_BYTES,
                "max_artifact_chunk_bytes": MAX_ARTIFACT_CHUNK_BYTES,
                "max_artifact_bytes": MAX_ARTIFACT_BYTES,
                "max_artifact_total_bytes": MAX_ARTIFACT_TOTAL,
            }
        )
    @property
    def workspace(self):
        return str(self._root)

    def _resolve(self, path="."):
        candidate = pathlib.Path(path)
        if not candidate.is_absolute():
            candidate = self._root / candidate
        resolved = candidate.resolve(strict=True)
        try:
            resolved.relative_to(self._root)
        except ValueError:
            raise PermissionError("path escapes workspace: " + str(path))
        return resolved

    def read_slice(self, path, start_line=1, end_line=200):
        resolved = self._resolve(path)
        if not resolved.is_file():
            raise ValueError("not a regular file: " + str(path))
        if resolved.stat().st_size > MAX_FILE_BYTES:
            raise ValueError("file exceeds read limit")
        start = max(1, int(start_line))
        end = max(start, int(end_line))
        if end - start + 1 > MAX_READ_LINES:
            end = start + MAX_READ_LINES - 1
        lines = []
        with resolved.open("r", encoding="utf-8", errors="replace") as handle:
            for number, line in enumerate(handle, 1):
                if number < start:
                    continue
                if number > end:
                    break
                lines.append({"line": number, "text": line.rstrip("\n")})
        response = {
            "type": "response",
            "path": str(resolved.relative_to(self._root)),
            "start_line": start,
            "end_line": lines[-1]["line"] if lines else start - 1,
            "lines": lines,
        }
        _record_context_operation("read_slice")
        return response

    @staticmethod
    def _line_matcher(query, case_sensitive=False, regex=False):
        query = str(query)
        if not query:
            raise ValueError("query must not be empty")
        if len(query) > MAX_SEARCH_QUERY_CHARS:
            raise ValueError("query exceeds search limit")
        if regex:
            try:
                pattern = re.compile(query, 0 if case_sensitive else re.IGNORECASE)
            except re.error as exc:
                raise ValueError("invalid regular expression: " + str(exc)) from exc
            def regex_matches(line):
                return pattern.search(line) is not None

            return regex_matches
        needle = query if case_sensitive else query.casefold()

        def literal_matches(line):
            return needle in (line if case_sensitive else line.casefold())

        return literal_matches

    def search_code(
        self, query, path=".", limit=50, case_sensitive=False, regex=False
    ):
        root = self._resolve(path)
        limit = max(1, min(int(limit), MAX_SEARCH_MATCHES))
        matches_line = self._line_matcher(query, case_sensitive, regex)
        matches = []
        scanned = 0
        searched = 0
        skipped = {"binary": 0, "oversized": 0, "unreadable": 0}
        entries, scan_truncated = self._file_entries(root)
        for candidate, size in entries:
            if scanned >= MAX_SEARCH_FILES:
                scan_truncated = True
                break
            scanned += 1
            try:
                if size > MAX_FILE_BYTES:
                    skipped["oversized"] += 1
                    scan_truncated = True
                    continue
                handle = self._open_searchable_text(candidate)
                if handle is None:
                    skipped["binary"] += 1
                    continue
                searched += 1
                with handle:
                    for number, line in enumerate(handle, 1):
                        if matches_line(line):
                            matches.append(
                                {
                                    "path": str(candidate.relative_to(self._root)),
                                    "line": number,
                                    "text": line.rstrip("\n")[:1000],
                                }
                            )
                            if len(matches) >= limit:
                                response = {
                                    "matches": matches,
                                    "scanned_files": scanned,
                                    "searched_files": searched,
                                    "skipped_files": skipped,
                                    "truncated": True,
                                }
                                _record_context_operation("search_code")
                                return response
            except (OSError, UnicodeError):
                skipped["unreadable"] += 1
                scan_truncated = True
                continue
        response = {
            "matches": matches,
            "scanned_files": scanned,
            "searched_files": searched,
            "skipped_files": skipped,
            "truncated": scan_truncated,
        }
        _record_context_operation("search_code")
        return response

    def count_code(
        self,
        query,
        path=".",
        case_sensitive=False,
        regex=False,
        group_by=None,
        sample_limit=0,
    ):
        """Count matching lines without materialising them in the transcript."""
        result = self._count_code_many(
            [query],
            path=path,
            case_sensitive=case_sensitive,
            regex=regex,
            group_by=group_by,
            sample_limit=sample_limit,
        )
        count = result["counts"][0]
        response = {
            "matching_lines": count["matching_lines"],
            "matching_files": count["matching_files"],
            "groups": count["groups"],
            "scanned_files": result["scanned_files"],
            "searched_files": result["searched_files"],
            "skipped_files": result["skipped_files"],
            "truncated": result["truncated"],
        }
        if "samples" in count:
            response["samples"] = count["samples"]
            response["samples_truncated"] = count["samples_truncated"]
        _record_context_operation("count_code")
        if int(sample_limit) > 0:
            _record_context_operation("count_code_sampled")
        return response

    def count_code_many(
        self,
        queries,
        path=".",
        case_sensitive=False,
        regex=False,
        group_by=None,
        sample_limit=0,
    ):
        result = self._count_code_many(
            queries,
            path=path,
            case_sensitive=case_sensitive,
            regex=regex,
            group_by=group_by,
            sample_limit=sample_limit,
        )
        _record_context_operation("count_code_many")
        if int(sample_limit) > 0:
            _record_context_operation("count_code_many_sampled")
        return result

    def _count_code_many(
        self,
        queries,
        path=".",
        case_sensitive=False,
        regex=False,
        group_by=None,
        sample_limit=0,
    ):
        """Count several patterns over one consistent bounded file scan."""
        if isinstance(queries, (str, bytes)):
            raise TypeError("queries must be a sequence of strings")
        try:
            normalized = [str(query) for query in queries]
        except TypeError as exc:
            raise TypeError("queries must be a sequence of strings") from exc
        if not normalized:
            raise ValueError("queries must not be empty")
        if len(normalized) > MAX_COUNT_QUERIES:
            raise ValueError("queries exceed count limit")
        sample_limit = max(0, min(int(sample_limit), MAX_COUNT_SAMPLES))
        for query in normalized:
            if not query:
                raise ValueError("query must not be empty")
            if len(query) > MAX_SEARCH_QUERY_CHARS:
                raise ValueError("query exceeds search limit")

        grouping = None if group_by in (None, "") else str(group_by)
        if grouping not in (None, "file", "top_dir", "extension"):
            raise ValueError("group_by must be file, top_dir, extension, or None")
        if regex:
            matchers = []
            for query in normalized:
                try:
                    matchers.append(
                        re.compile(query, 0 if case_sensitive else re.IGNORECASE).search
                    )
                except re.error as exc:
                    raise ValueError(
                        "invalid regular expression: " + str(exc)
                    ) from exc
            needles = None
        else:
            matchers = None
            needles = (
                normalized if case_sensitive else [q.casefold() for q in normalized]
            )

        root = self._resolve(path)
        counts = []
        for query in normalized:
            count = {
                "query": query,
                "matching_lines": 0,
                "matching_files": 0,
                "groups": {},
            }
            if sample_limit:
                count["samples"] = []
            counts.append(count)
        scanned = 0
        searched = 0
        skipped = {"binary": 0, "oversized": 0, "unreadable": 0}
        entries, scan_truncated = self._file_entries(root)
        root_is_dir = grouping == "top_dir" and root.is_dir()
        for candidate, size in entries:
            if scanned >= MAX_SEARCH_FILES:
                scan_truncated = True
                break
            scanned += 1
            file_matches = [0] * len(normalized)
            sample_path = None
            sample_starts = (
                [len(count["samples"]) for count in counts] if sample_limit else None
            )
            try:
                if size > MAX_FILE_BYTES:
                    skipped["oversized"] += 1
                    scan_truncated = True
                    continue
                handle = self._open_searchable_text(candidate)
                if handle is None:
                    skipped["binary"] += 1
                    continue
                searched += 1
                with handle:
                    for number, line in enumerate(handle, 1):
                        if matchers is not None:
                            for index, matcher in enumerate(matchers):
                                if matcher(line) is not None:
                                    file_matches[index] += 1
                                    samples = counts[index].get("samples")
                                    if samples is not None and len(samples) < sample_limit:
                                        if sample_path is None:
                                            sample_path = str(candidate.relative_to(self._root))
                                        samples.append(
                                            {
                                                "path": sample_path,
                                                "line": number,
                                                "text": line.rstrip("\n")[
                                                    :MAX_COUNT_SAMPLE_TEXT_CHARS
                                                ],
                                            }
                                        )
                        else:
                            haystack = line if case_sensitive else line.casefold()
                            for index, needle in enumerate(needles):
                                if needle in haystack:
                                    file_matches[index] += 1
                                    samples = counts[index].get("samples")
                                    if samples is not None and len(samples) < sample_limit:
                                        if sample_path is None:
                                            sample_path = str(candidate.relative_to(self._root))
                                        samples.append(
                                            {
                                                "path": sample_path,
                                                "line": number,
                                                "text": line.rstrip("\n")[
                                                    :MAX_COUNT_SAMPLE_TEXT_CHARS
                                                ],
                                            }
                                        )
            except (OSError, UnicodeError):
                if sample_starts is not None:
                    for index, start in enumerate(sample_starts):
                        del counts[index]["samples"][start:]
                skipped["unreadable"] += 1
                scan_truncated = True
                continue
            if not any(file_matches):
                continue
            if grouping == "file":
                key = str(candidate.relative_to(self._root))
            elif grouping == "top_dir":
                relative = candidate.relative_to(root) if root_is_dir else pathlib.Path(candidate.name)
                key = relative.parts[0] if len(relative.parts) > 1 else "."
            elif grouping == "extension":
                key = candidate.suffix or "(none)"
            else:
                key = None
            for index, matches in enumerate(file_matches):
                if matches == 0:
                    continue
                count = counts[index]
                count["matching_lines"] += matches
                count["matching_files"] += 1
                if key is not None:
                    groups = count["groups"]
                    groups[key] = groups.get(key, 0) + matches

        if sample_limit:
            for count in counts:
                count["samples_truncated"] = (
                    count["matching_lines"] > len(count["samples"])
                )

        return {
            "counts": counts,
            "scanned_files": scanned,
            "searched_files": searched,
            "skipped_files": skipped,
            "truncated": scan_truncated,
        }

    def list_files(self, path=".", pattern=None):
        # Inventory questions — how many files of a kind, which are largest,
        # how work is distributed across directories — have no answer in
        # search_code or read_slice, so without this the only route is a raw
        # a raw directory walk, which does not use the parent-published index
        # and therefore sees ignored trees. Sizes come along because they are the one attribute
        # the search path cannot produce at all.
        #
        # There is deliberately no result limit: a capped list would make
        # len(...) a plausible but wrong count, which is the failure this whole
        # surface is meant to avoid. The walk is bounded by the same file
        # ceiling as search_code and says so through truncated.
        root = self._resolve(path)
        matcher = None if pattern in (None, "") else str(pattern)
        files = []
        scanned = 0
        entries, scan_truncated = self._file_entries(root)
        for candidate, size in entries:
            if scanned >= MAX_SEARCH_FILES:
                scan_truncated = True
                break
            scanned += 1
            relative = str(candidate.relative_to(self._root))
            if matcher is not None and not fnmatch.fnmatch(relative, matcher):
                continue
            files.append({"path": relative, "size": size})
        response = {
            "files": files,
            "scanned_files": scanned,
            "truncated": scan_truncated,
        }
        _record_context_operation("list_files")
        return response

    def file_stats(
        self, path=".", pattern=None, exclude_pattern=None, group_by=None
    ):
        """Aggregate inventory counts/bytes without materialising file paths."""
        grouping = None if group_by in (None, "") else str(group_by)
        if grouping not in (None, "extension", "top_dir"):
            raise ValueError("group_by must be extension, top_dir, or None")
        include = None if pattern in (None, "") else str(pattern)
        exclude = None if exclude_pattern in (None, "") else str(exclude_pattern)
        root = self._resolve(path)
        root_is_dir = grouping == "top_dir" and root.is_dir()
        matched = 0
        total_bytes = 0
        scanned = 0
        groups = {}
        entries, scan_truncated = self._file_entries(root, retain_snapshot=False)
        for candidate, size in entries:
            if scanned >= MAX_SEARCH_FILES:
                scan_truncated = True
                break
            scanned += 1
            relative = str(candidate.relative_to(self._root))
            if include is not None and not fnmatch.fnmatch(relative, include):
                continue
            if exclude is not None and fnmatch.fnmatch(relative, exclude):
                continue
            matched += 1
            total_bytes += size
            if grouping == "extension":
                key = candidate.suffix or "(none)"
            elif grouping == "top_dir":
                scoped = (
                    candidate.relative_to(root)
                    if root_is_dir
                    else pathlib.Path(candidate.name)
                )
                key = scoped.parts[0] if len(scoped.parts) > 1 else "."
            else:
                key = None
            if key is not None:
                group = groups.get(key)
                if group is None:
                    if len(groups) >= MAX_FILE_STAT_GROUPS:
                        raise ValueError(
                            "file_stats groups exceed "
                            f"{MAX_FILE_STAT_GROUPS}-entry limit"
                        )
                    group = {"files": 0, "bytes": 0}
                    groups[key] = group
                group["files"] += 1
                group["bytes"] += size
        response = {
            "matching_files": matched,
            "total_bytes": total_bytes,
            "groups": groups,
            "scanned_files": scanned,
            "truncated": scan_truncated,
        }
        _record_context_operation("file_stats")
        return response

    def _file_entries(self, root, retain_snapshot=True):
        cache_key = str(root.relative_to(self._root)) or "."
        if _cell_entry_cache_enabled:
            cached = _cell_entry_cache.get(cache_key)
            if cached is not None:
                _record_context_operation("file_inventory")
                return cached.iterate(), cached.truncated
        root_info = root.stat()
        if stat.S_ISREG(root_info.st_mode):
            _record_context_operation("file_inventory")
            return iter(((root, root_info.st_size),)), False
        if not stat.S_ISDIR(root_info.st_mode):
            _record_context_operation("file_inventory")
            return iter(()), False
        entries, truncated = self._indexed_file_entries(root)
        # Public operation markers explain which primitive was used; this
        # lowest-layer worker marker remains authoritative for snapshot replay.
        _record_context_operation("file_inventory")
        if not _cell_entry_cache_enabled or not retain_snapshot:
            return entries, truncated
        snapshot = _CellEntrySnapshot(entries, truncated)
        _cell_entry_cache[cache_key] = snapshot
        return snapshot.iterate(), snapshot.truncated

    def _indexed_file_entries(self, root):
        relative_root = root.relative_to(self._root)
        response = _protocol_call(
            "context.file_index",
            {"path": str(relative_root) if relative_root.parts else "."},
        )
        if not isinstance(response, dict):
            raise RuntimeError("invalid file index response")
        raw_path = response.get("path")
        if not isinstance(raw_path, str) or not raw_path:
            raise RuntimeError("file index response has no path")
        index_path = pathlib.Path(raw_path).resolve(strict=True)
        runtime_root = pathlib.Path(__file__).resolve(strict=True).parent
        if index_path.parent != runtime_root or index_path.name != "visible-files.index":
            raise PermissionError("file index is outside the worker runtime")
        truncated = bool(response.get("truncated"))
        # Read immediately after the parent response. The parent may republish
        # this fixed runtime path for another cached scope later in the same
        # cell; a lazily-opened generator must retain the snapshot selected by
        # this callback rather than observe that later replacement.
        with index_path.open("rb") as handle:
            data = handle.read(MAX_FILE_INDEX_BYTES + 1)
        if len(data) > MAX_FILE_INDEX_BYTES:
            raise RuntimeError("file index exceeds runtime limit")

        def entries():
            # Do not bytes.split the complete bounded index: that creates a list
            # and one new bytes object per path while the original buffer and the
            # cell snapshot are still live. Parse NUL offsets sequentially so peak
            # memory stays close to the index plus retained Path entries.
            start = 0
            while start < len(data):
                end = data.find(b"\x00", start)
                if end < 0:
                    raise RuntimeError("file index is not NUL-terminated")
                if end == start:
                    start = end + 1
                    continue
                encoded = data[start:end]
                start = end + 1
                relative = pathlib.Path(os.fsdecode(encoded))
                if relative.is_absolute() or ".." in relative.parts:
                    raise PermissionError("invalid path in file index")
                candidate = self._root / relative
                try:
                    resolved = candidate.resolve(strict=True)
                    scan_relative = resolved.relative_to(root)
                    info = resolved.stat()
                except (OSError, ValueError):
                    continue
                # Apply the built-in metadata/vendor exclusions relative to
                # the requested root. Thus context.count_code(path=".gokin")
                # remains an explicit, readable request while a scan of "."
                # cannot ingest agent journals and repository internals.
                if any(part in self._SKIP_DIRS for part in scan_relative.parts[:-1]):
                    continue
                if stat.S_ISREG(info.st_mode):
                    yield resolved, info.st_size

        return entries(), truncated

    def _open_searchable_text(self, candidate):
        if candidate.suffix.lower() in self._BINARY_SUFFIXES:
            return None
        raw = candidate.open("rb")
        try:
            # BufferedReader.peek does not advance the logical position, so the
            # text wrapper consumes these bytes once rather than re-reading a
            # detection prefix from disk.
            sample = raw.peek(8192)[:8192]
            if b"\x00" in sample:
                raw.close()
                return None
            return io.TextIOWrapper(raw, encoding="utf-8", errors="replace")
        except BaseException:
            raw.close()
            raise

    def artifact_get(self, artifact_id, offset=0, limit=MAX_ARTIFACT_CHUNK_BYTES):
        result = _artifacts.get(artifact_id, offset, limit)
        _record_context_operation("artifact_get")
        return result

    def runtime_limits(self):
        result = dict(self._runtime_limits)
        _record_context_operation("runtime_limits")
        return result


_context_operation_callers.update(
    {
        Context.read_slice.__code__: frozenset({"read_slice"}),
        Context.search_code.__code__: frozenset({"search_code"}),
        Context.count_code.__code__: frozenset({"count_code", "count_code_sampled"}),
        Context.count_code_many.__code__: frozenset(
            {"count_code_many", "count_code_many_sampled"}
        ),
        Context.list_files.__code__: frozenset({"list_files"}),
        Context.file_stats.__code__: frozenset({"file_stats"}),
        Context._file_entries.__code__: frozenset({"file_inventory"}),
        Context.artifact_get.__code__: frozenset({"artifact_get"}),
        Context.runtime_limits.__code__: frozenset({"runtime_limits"}),
    }
)


def _install_audit_guard():
    exact_type = type
    string_type = str
    integer_type = int
    value_count = len
    truth_value = bool
    filesystem_path = os.fspath
    path_is_absolute = os.path.isabs
    path_real = os.path.realpath
    workspace_root = path_real(os.getcwd())
    workspace_prefix = workspace_root + os.sep
    write_flag_mask = (
        os.O_WRONLY | os.O_RDWR | os.O_CREAT | os.O_TRUNC | os.O_APPEND
    )

    def guard(event, args):
        if event == "import" and args and args[0] in {"signal", "_signal"}:
            raise PermissionError("signal controls are reserved for runtime limits")
        if event == "subprocess.Popen":
            raise PermissionError(
                "direct subprocess execution is disabled; use rlm or structured tools"
            )
        if event in {
            "os.system",
            "os.exec",
            "os.fork",
            "os.forkpty",
            "os.kill",
            "os.killpg",
            "os.posix_spawn",
            "os.posix_spawnp",
            "os.spawn",
        }:
            raise PermissionError("direct process creation is disabled")
        if event.startswith("socket."):
            raise PermissionError("network access is disabled in the REPL")
        if event in {"ctypes.dlopen", "ctypes.dlsym", "ctypes.dlsym/handle"}:
            raise PermissionError("native library access is disabled in the REPL")
        if event in {"os.listdir", "os.scandir"} and args:
            raw_path = args[0]
            deny_enumeration = exact_type(raw_path) is integer_type
            if not deny_enumeration:
                try:
                    raw_path = filesystem_path(raw_path)
                    deny_enumeration = not path_is_absolute(raw_path)
                    if not deny_enumeration:
                        resolved_path = path_real(raw_path)
                        deny_enumeration = (
                            resolved_path == workspace_root
                            or resolved_path.startswith(workspace_prefix)
                        )
                except (TypeError, ValueError, OSError):
                    deny_enumeration = True
            if deny_enumeration:
                raise PermissionError(
                    "direct workspace directory enumeration is disabled; use "
                    "context.list_files, context.file_stats, or context search/count APIs"
                )
        if event in {
            "os.remove",
            "os.rename",
            "os.rmdir",
            "os.mkdir",
            "os.link",
            "os.symlink",
            "os.chmod",
            "os.chown",
            "os.truncate",
            "os.utime",
            "os.setxattr",
            "os.removexattr",
        }:
            raise PermissionError("filesystem mutations are disabled in the REPL")
        if event == "open" and value_count(args) >= 2:
            path = args[0]
            mode = args[1]
            flags = args[2] if value_count(args) >= 3 else 0
            writes = exact_type(mode) is string_type and (
                "w" in mode or "a" in mode or "x" in mode or "+" in mode
            )
            if exact_type(flags) is integer_type:
                writes = writes or truth_value(flags & write_flag_mask)
            if writes:
                raise PermissionError("filesystem writes are disabled in the REPL")

    sys.addaudithook(guard)


_BLOCKED_CELL_CALLS = {
    "__import__",
    "breakpoint",
    "compile",
    "delattr",
    "dir",
    "eval",
    "exec",
    "getattr",
    "globals",
    "input",
    "locals",
    "open",
    "setattr",
    "vars",
}
_BLOCKED_CELL_ATTRIBUTES = _BLOCKED_CELL_CALLS | {
    "__closure__",
    "__class__",
    "__code__",
    "__dict__",
    "__getattribute__",
    "__globals__",
    "__func__",
    "__reduce__",
    "__reduce_ex__",
    "__self__",
    "__setattr__",
    "__subclasses__",
    "__traceback__",
    "__builtins__",
    "_getframe",
    "attrgetter",
    "cr_frame",
    "current_frames",
    "f_globals",
    "f_locals",
    "getclosurevars",
    "gi_frame",
    "methodcaller",
    "modules",
    "setprofile",
    "settrace",
}
_ALLOWED_CELL_IMPORTS = {
    "base64",
    "binascii",
    "bisect",
    "collections",
    "csv",
    "datetime",
    "decimal",
    "fractions",
    "functools",
    "hashlib",
    "heapq",
    "html",
    "itertools",
    "json",
    "math",
    "operator",
    "pprint",
    "random",
    "re",
    "statistics",
    "textwrap",
    "time",
    "unicodedata",
    "urllib.parse",
}

_RUNTIME_IMPORT = builtins.__import__
_ANALYTICAL_MODULES = {}


class _AnalyticalModule:
    """Read-only module facade whose mutable constants cannot alter runtime state."""

    __slots__ = ("_module",)

    def __init__(self, module):
        object.__setattr__(self, "_module", module)

    def __getattribute__(self, name):
        if name == "__name__":
            module = object.__getattribute__(self, "_module")
            return module.__name__
        if str(name).startswith("_"):
            raise PermissionError("private module attributes are disabled in the REPL")
        module = object.__getattribute__(self, "_module")
        return _copy_analytical_value(getattr(module, name))

    def __setattr__(self, _name, _value):
        raise PermissionError("analytical modules are read-only")

    def __repr__(self):
        module = object.__getattribute__(self, "_module")
        return "<analytical module %r>" % module.__name__


def _analytical_module(module):
    name = module.__name__
    if name not in _ALLOWED_CELL_IMPORTS and not any(
        name.startswith(allowed + ".") or allowed.startswith(name + ".")
        for allowed in _ALLOWED_CELL_IMPORTS
    ):
        raise PermissionError("runtime module references are disabled in the REPL")
    proxy = _ANALYTICAL_MODULES.get(name)
    if proxy is None:
        proxy = _AnalyticalModule(module)
        _ANALYTICAL_MODULES[name] = proxy
    return proxy


def _copy_analytical_value(value, memo=None):
    if isinstance(value, types.ModuleType):
        return _analytical_module(value)
    if memo is None:
        memo = {}
    identity = id(value)
    if identity in memo:
        return memo[identity]
    if isinstance(value, dict):
        result = {}
        memo[identity] = result
        for key, item in value.items():
            result[_copy_analytical_value(key, memo)] = _copy_analytical_value(item, memo)
        return result
    if isinstance(value, list):
        result = []
        memo[identity] = result
        result.extend(_copy_analytical_value(item, memo) for item in value)
        return result
    if isinstance(value, set):
        result = set()
        memo[identity] = result
        result.update(_copy_analytical_value(item, memo) for item in value)
        return result
    if isinstance(value, bytearray):
        return bytearray(value)
    if isinstance(value, tuple):
        return tuple(_copy_analytical_value(item, memo) for item in value)
    if isinstance(value, frozenset):
        return frozenset(_copy_analytical_value(item, memo) for item in value)
    return value


def _analytical_import(name, globals=None, locals=None, fromlist=(), level=0):
    if level != 0 or name not in _ALLOWED_CELL_IMPORTS:
        raise PermissionError("this module is outside the analytical REPL import allowlist")
    module = _RUNTIME_IMPORT(name, globals, locals, fromlist, 0)
    return _analytical_module(module)


def _cell_eprint(*args, **kwargs):
    if "file" in kwargs:
        raise PermissionError("eprint file redirection is disabled")
    print(*args, file=sys.stderr, **kwargs)


_CELL_BUILTINS = dict(vars(builtins))
for _blocked_builtin in _BLOCKED_CELL_CALLS | {
    "copyright",
    "credits",
    "exit",
    "help",
    "license",
    "quit",
}:
    _CELL_BUILTINS.pop(_blocked_builtin, None)
_CELL_BUILTINS["__import__"] = _analytical_import
_CELL_BUILTINS["eprint"] = _cell_eprint


def _validate_cell_ast(tree):
    """Keep model code out of worker telemetry/runtime internals.

    This is an integrity boundary for operational evidence, not the filesystem
    security boundary (the OS sandbox owns that). Public Python computation and
    ordinary imports remain available; private/reflection/dynamic-code routes
    that can reach function globals or protocol state fail before execution.
    """
    for node in ast.walk(tree):
        if isinstance(node, ast.Attribute):
            if isinstance(node.ctx, (ast.Store, ast.Del)):
                raise PermissionError("object and module attribute mutation is disabled in the REPL")
            if node.attr.startswith("_") and node.attr != "__name__":
                raise PermissionError("private attribute access is disabled in the REPL")
            private_runtime_attribute = (
                node.attr.startswith("_")
                and isinstance(node.value, ast.Name)
                and node.value.id in {"context", "rlm"}
            )
            if private_runtime_attribute or node.attr in _BLOCKED_CELL_ATTRIBUTES:
                raise PermissionError(
                    "private or reflective attribute access is disabled in the REPL"
                )
        elif isinstance(node, ast.Name):
            if isinstance(node.ctx, ast.Load) and (
                node.id in _BLOCKED_CELL_CALLS or node.id == "__builtins__"
            ):
                raise PermissionError(
                    "dynamic code and runtime reflection are disabled in the REPL"
                )
        elif isinstance(node, ast.Call):
            if isinstance(node.func, ast.Name) and node.func.id in _BLOCKED_CELL_CALLS:
                raise PermissionError(
                    "dynamic code and runtime reflection are disabled in the REPL"
                )
        elif isinstance(node, ast.Import):
            if any(
                alias.name not in _ALLOWED_CELL_IMPORTS
                for alias in node.names
            ):
                raise PermissionError("this module is outside the analytical REPL import allowlist")
        elif isinstance(node, ast.ImportFrom):
            module = node.module or ""
            if module not in _ALLOWED_CELL_IMPORTS or any(
                alias.name == "*"
                or alias.name in _BLOCKED_CELL_ATTRIBUTES
                or alias.name.startswith("_")
                for alias in node.names
            ):
                raise PermissionError("this import is outside the analytical REPL allowlist")


def _evaluate(code, namespace):
    tree = ast.parse(code, mode="exec")
    _validate_cell_ast(tree)
    if tree.body and isinstance(tree.body[-1], ast.Expr):
        prefix = ast.Module(body=tree.body[:-1], type_ignores=[])
        if prefix.body:
            exec(compile(prefix, "<gokin-repl>", "exec"), namespace, namespace)
        expression = ast.Expression(tree.body[-1].value)
        return eval(compile(expression, "<gokin-repl>", "eval"), namespace, namespace)
    exec(compile(tree, "<gokin-repl>", "exec"), namespace, namespace)
    return None


def _inline_or_artifact(value):
    if value is None:
        return "", None, False
    source_truncated = False
    if isinstance(value, str) and len(value) > MAX_ARTIFACT_BYTES:
        rendered = repr(value[:MAX_ARTIFACT_BYTES])
        source_truncated = True
    elif isinstance(value, (bytes, bytearray)) and len(value) > MAX_ARTIFACT_BYTES:
        rendered = repr(value[:MAX_ARTIFACT_BYTES])
        source_truncated = True
    else:
        rendered = repr(value)
    inline, overflow = _utf8_prefix_bytes(rendered, MAX_INLINE_BYTES)
    if not overflow and not source_truncated:
        return inline.decode("utf-8"), None, False
    artifact = _artifacts.put(rendered, source_truncated=source_truncated)
    return inline.decode("utf-8"), artifact, True


def _capture_payload(stdout, stderr):
    inline = {}
    artifacts = {}
    for name, stream in (("stdout", stdout), ("stderr", stderr)):
        value = stream.getvalue()
        encoded, bounded = _utf8_prefix_bytes(value, MAX_INLINE_BYTES)
        overflow = stream.truncated or bounded
        inline[name] = encoded.decode("utf-8")
        if overflow:
            artifacts[name] = _artifacts.put(
                value, source_truncated=stream.truncated
            )
    return inline, artifacts


def _attach_artifacts(response, artifacts):
    if not artifacts:
        return response
    response["artifacts"] = artifacts
    for name in ("value", "stdout", "stderr"):
        if name in artifacts:
            # Keep the original singular field for protocol compatibility.
            response["artifact"] = artifacts[name]
            break
    if "artifact" not in response:
        response["artifact"] = next(iter(artifacts.values()))
    return response


def _error_payload(exc):
    message = str(exc)
    trace = traceback.format_exc(limit=20)
    artifacts = {}
    inline_message, message_bounded = _utf8_prefix_bytes(message, MAX_INLINE_BYTES)
    inline_trace, trace_bounded = _utf8_tail_bytes(trace, MAX_INLINE_BYTES)
    if message_bounded:
        artifacts["error_message"] = _artifacts.put(message)
    if trace_bounded:
        artifacts["traceback"] = _artifacts.put(trace)
    return {
        "type": type(exc).__name__,
        "message": inline_message.decode("utf-8"),
        "traceback": inline_trace.decode("utf-8"),
    }, artifacts


def _execute(request, namespace, generation):
    code = request.get("code")
    if not isinstance(code, str) or not code.strip():
        raise ValueError("code must be a non-empty string")
    if len(code.encode("utf-8")) > MAX_REQUEST_BYTES:
        raise ValueError("code exceeds worker request limit")
    stdout = _BoundedText()
    stderr = _BoundedText()
    _begin_cell_entry_cache()
    _begin_operation_capture()
    try:
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            value = _evaluate(code, namespace)
        rendered, artifact, value_truncated = _inline_or_artifact(value)
        inline, artifacts = _capture_payload(stdout, stderr)
        if artifact is not None:
            artifacts["value"] = artifact
        response = {
            "type": "response",
            "ok": True,
            "generation": generation,
            "stdout": inline["stdout"],
            "stderr": inline["stderr"],
            "value": rendered,
            "truncated": bool(value_truncated or artifacts),
        }
        response = _attach_artifacts(response, artifacts)
        response["operations"] = _operation_snapshot()
        return response
    except _MemoryLimitExceeded:
        raise
    except MemoryError as exc:
        raise _MemoryLimitExceeded("worker memory allocation failed") from exc
    except BaseException as exc:
        inline, artifacts = _capture_payload(stdout, stderr)
        error, error_artifacts = _error_payload(exc)
        artifacts.update(error_artifacts)
        response = {
            "type": "response",
            "ok": False,
            "generation": generation,
            "stdout": inline["stdout"],
            "stderr": inline["stderr"],
            "truncated": bool(artifacts),
            "error": error,
        }
        response = _attach_artifacts(response, artifacts)
        response["operations"] = _operation_snapshot()
        return response


def _write(response):
    encoded = json.dumps(response, ensure_ascii=False, separators=(",", ":"))
    sys.__stdout__.write(encoded + "\n")
    sys.__stdout__.flush()


def main():
    if len(sys.argv) != 5:
        raise SystemExit(
            "usage: worker.py WORKDIR GENERATION BACKEND MAX_MEMORY_BYTES"
        )
    generation = int(sys.argv[2])
    memory_limit = max(0, int(sys.argv[4]))
    memory_watchdog, set_watchdog_request, check_memory = _install_memory_watchdog(
        memory_limit, generation
    )
    # Do not leave the timer controls cached for cell imports. The installed C
    # signal handler and timer remain active without either module object.
    sys.modules.pop("signal", None)
    sys.modules.pop("_signal", None)
    globals().pop("signal", None)
    runtime_limits = _apply_resource_limits(sys.argv[3])
    runtime_limits.update(
        {
            "max_memory_bytes": memory_limit,
            "memory_watchdog": memory_watchdog,
        }
    )
    _install_audit_guard()
    runtime_rlm = RLM()
    runtime_rlm.harness = Harness()
    runtime_context = Context(sys.argv[1], runtime_limits)
    namespace = {
        "__name__": "__gokin_repl__",
        "__builtins__": _CELL_BUILTINS,
        "context": runtime_context,
        "rlm": runtime_rlm,
    }
    for raw in sys.stdin.buffer:
        if len(raw) > MAX_REQUEST_BYTES * 2:
            _write(
                {
                    "type": "response",
                    "id": "",
                    "ok": False,
                    "generation": generation,
                    "error": {"type": "ProtocolError", "message": "request too large"},
                }
            )
            continue
        try:
            request = json.loads(raw)
            request_id = request.get("id", "")
            method = request.get("method")
            if method == "ping":
                response = {
                    "type": "response",
                    "ok": True,
                    "generation": generation,
                    "value": "pong",
                }
            elif method == "exec":
                set_watchdog_request(request_id)
                try:
                    response = _execute(request, namespace, generation)
                    # Catch fast C-level allocations that can begin and finish
                    # between timer ticks before publishing the cell result.
                    check_memory()
                except _MemoryLimitExceeded as exc:
                    response = {
                        "type": "response",
                        "ok": False,
                        "generation": generation,
                        "kernel_reset": True,
                        "error": {
                            "type": "MemoryLimitExceeded",
                            "message": _truncate_utf8(str(exc), MAX_INLINE_BYTES),
                        },
                    }
            else:
                response = {
                    "type": "response",
                    "ok": False,
                    "generation": generation,
                    "error": {"type": "ProtocolError", "message": "unknown method"},
                }
            response["id"] = request_id
            _write(response)
            set_watchdog_request("")
        except BaseException as exc:
            _write(
                {
                    "type": "response",
                    "id": "",
                    "ok": False,
                    "generation": generation,
                    "error": {
                        "type": type(exc).__name__,
                        "message": _truncate_utf8(str(exc), MAX_INLINE_BYTES),
                    },
                }
            )
            set_watchdog_request("")


if __name__ == "__main__":
    main()
