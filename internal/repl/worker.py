"""Gokin stateful read-only Python worker.

The transport is newline-delimited JSON on stdin/stdout. User stdout/stderr is
captured and returned as data, so it can never forge protocol events.
Filesystem and network isolation are owned by the parent OS sandbox; this file
only provides bounded, workspace-contained convenience APIs.
"""

import ast
import contextlib
import io
import json
import os
import pathlib
import subprocess
import sys
import traceback
import uuid
from collections import OrderedDict

try:
    import resource
except ImportError:  # Windows
    resource = None

MAX_REQUEST_BYTES = 64 * 1024
MAX_INLINE_CHARS = 64 * 1024
MAX_CAPTURE_CHARS = 4 * 1024 * 1024
MAX_ARTIFACT_BYTES = 4 * 1024 * 1024
MAX_ARTIFACT_TOTAL = 16 * 1024 * 1024
MAX_FILE_BYTES = 2 * 1024 * 1024
MAX_SEARCH_FILES = 20_000
MAX_SEARCH_MATCHES = 500
MAX_READ_LINES = 2_000


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
    def __init__(self, limit=MAX_CAPTURE_CHARS):
        self._limit = limit
        self._parts = []
        self._size = 0
        self.truncated = False

    def writable(self):
        return True

    def write(self, value):
        value = str(value)
        remaining = self._limit - self._size
        if remaining > 0:
            chunk = value[:remaining]
            self._parts.append(chunk)
            self._size += len(chunk)
        if len(value) > max(remaining, 0):
            self.truncated = True
        return len(value)

    def getvalue(self):
        return "".join(self._parts)


class _Artifacts:
    def __init__(self):
        self._items = OrderedDict()
        self._total = 0

    def put(self, value):
        raw = str(value).encode("utf-8", "replace")
        truncated = len(raw) > MAX_ARTIFACT_BYTES
        raw = raw[:MAX_ARTIFACT_BYTES]
        while self._items and self._total + len(raw) > MAX_ARTIFACT_TOTAL:
            _, old = self._items.popitem(last=False)
            self._total -= len(old)
        artifact_id = "art_" + uuid.uuid4().hex
        self._items[artifact_id] = raw
        self._total += len(raw)
        return {"id": artifact_id, "size": len(raw), "truncated": truncated}

    def get(self, artifact_id, offset=0, limit=MAX_INLINE_CHARS):
        if artifact_id not in self._items:
            raise KeyError("unknown or expired artifact: " + str(artifact_id))
        raw = self._items[artifact_id]
        self._items.move_to_end(artifact_id)
        offset = max(0, int(offset))
        limit = max(1, min(int(limit), MAX_INLINE_CHARS))
        chunk = raw[offset : offset + limit]
        return {
            "id": artifact_id,
            "offset": offset,
            "size": len(raw),
            "content": chunk.decode("utf-8", "replace"),
            "has_more": offset + len(chunk) < len(raw),
        }


_artifacts = _Artifacts()


def _protocol_call(method, params):
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


class Context:
    _SKIP_DIRS = {".git", ".hg", ".svn", "node_modules", "vendor", ".idea", ".vscode"}

    @staticmethod
    def _ignored_roots():
        # Top-level directories .gitignore excludes, resolved by the Go side
        # with the repository's own matcher. Traversing them produces answers
        # that are confidently wrong rather than merely noisy: a vendored
        # toolchain cache once made a "most TODOs" ranking return the Go
        # standard library, and nothing in the result said so.
        raw = os.environ.get("GOKIN_REPL_IGNORE_DIRS", "")
        return {part for part in raw.split(os.pathsep) if part}

    def __init__(self, workdir, git_path, runtime_limits):
        self._root = pathlib.Path(workdir).resolve(strict=True)
        self._git_path = git_path
        self._runtime_limits = dict(runtime_limits)

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
        return {
            "type": "response",
            "path": str(resolved.relative_to(self._root)),
            "start_line": start,
            "end_line": lines[-1]["line"] if lines else start - 1,
            "lines": lines,
        }

    def search_code(self, query, path=".", limit=50, case_sensitive=False):
        query = str(query)
        if not query:
            raise ValueError("query must not be empty")
        root = self._resolve(path)
        limit = max(1, min(int(limit), MAX_SEARCH_MATCHES))
        needle = query if case_sensitive else query.casefold()
        matches = []
        scanned = 0
        paths = [root] if root.is_file() else None
        iterator = paths if paths is not None else self._walk_files(root)
        for candidate in iterator:
            scanned += 1
            if scanned > MAX_SEARCH_FILES:
                break
            try:
                if candidate.stat().st_size > MAX_FILE_BYTES:
                    continue
                with candidate.open("r", encoding="utf-8", errors="replace") as handle:
                    for number, line in enumerate(handle, 1):
                        haystack = line if case_sensitive else line.casefold()
                        if needle in haystack:
                            matches.append(
                                {
                                    "path": str(candidate.relative_to(self._root)),
                                    "line": number,
                                    "text": line.rstrip("\n")[:1000],
                                }
                            )
                            if len(matches) >= limit:
                                return {
                                    "matches": matches,
                                    "scanned_files": scanned,
                                    "truncated": True,
                                }
            except (OSError, UnicodeError):
                continue
        return {
            "matches": matches,
            "scanned_files": scanned,
            "truncated": scanned > MAX_SEARCH_FILES,
        }

    def _walk_files(self, root):
        ignored_roots = self._ignored_roots()
        for current, dirs, files in os.walk(str(root), followlinks=False):
            keep = []
            for name in sorted(dirs):
                if name in self._SKIP_DIRS:
                    continue
                try:
                    relative = (pathlib.Path(current) / name).relative_to(self._root)
                except ValueError:
                    relative = None
                if relative is not None and str(relative) in ignored_roots:
                    continue
                keep.append(name)
            dirs[:] = keep
            for name in sorted(files):
                candidate = pathlib.Path(current) / name
                try:
                    resolved = candidate.resolve(strict=True)
                    resolved.relative_to(self._root)
                except (OSError, ValueError):
                    continue
                if resolved.is_file():
                    yield resolved

    # The audit guard permits a subprocess only from this frame, so this frame
    # must not be a general-purpose git runner. `context` lives in the cell
    # namespace and Python has no real privacy, so cell code can call
    # `context._git(...)` directly — and git turns arbitrary configuration into
    # arbitrary execution (`-c alias.x='!cmd' x`, core.pager, diff.external,
    # uploadpack.packObjectsHook). Only the exact argument vectors the two
    # public read-only helpers need may run; everything else is refused here,
    # which is what makes "one fixed read-only subprocess path" true.
    _ALLOWED_GIT_ARGS = frozenset(
        {
            ("status", "--short", "--branch"),
            ("diff", "--no-ext-diff"),
            ("diff", "--no-ext-diff", "--cached"),
        }
    )

    def _git(self, *args):
        if not self._git_path:
            raise RuntimeError("git executable is unavailable")
        if tuple(args) not in self._ALLOWED_GIT_ARGS:
            raise PermissionError(
                "only context.git_status() and context.git_diff() may run git"
            )
        completed = subprocess.run(
            [self._git_path, "-c", "color.ui=false", *args],
            cwd=str(self._root),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=10,
            check=False,
        )
        stdout = completed.stdout[:MAX_ARTIFACT_BYTES].decode("utf-8", "replace")
        stderr = completed.stderr[:MAX_INLINE_CHARS].decode("utf-8", "replace")
        if completed.returncode != 0:
            raise RuntimeError(stderr.strip() or "git command failed")
        return stdout

    def git_status(self):
        return self._git("status", "--short", "--branch")

    def git_diff(self, staged=False):
        args = ["diff", "--no-ext-diff"]
        if staged:
            args.append("--cached")
        return self._git(*args)

    def artifact_get(self, artifact_id, offset=0, limit=MAX_INLINE_CHARS):
        return _artifacts.get(artifact_id, offset, limit)

    def runtime_limits(self):
        return dict(self._runtime_limits)


def _install_audit_guard():
    git_code = Context._git.__code__

    def called_from_context_git():
        frame = sys._getframe(2)
        while frame is not None:
            if frame.f_code is git_code:
                return True
            frame = frame.f_back
        return False

    def guard(event, args):
        if event == "subprocess.Popen":
            if not called_from_context_git():
                raise PermissionError(
                    "direct subprocess execution is disabled; use rlm or structured tools"
                )
            return
        if event in {
            "os.system",
            "os.fork",
            "os.forkpty",
            "os.posix_spawn",
            "os.posix_spawnp",
        }:
            raise PermissionError("direct process creation is disabled")
        if event.startswith("socket."):
            raise PermissionError("network access is disabled in the REPL")
        if event in {"ctypes.dlopen", "ctypes.dlsym", "ctypes.dlsym/handle"}:
            raise PermissionError("native library access is disabled in the REPL")
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
        if event == "open" and len(args) >= 2:
            path = args[0]
            mode = args[1]
            flags = args[2] if len(args) >= 3 else 0
            writes = isinstance(mode, str) and any(char in mode for char in "wax+")
            if isinstance(flags, int):
                writes = writes or bool(
                    flags
                    & (os.O_WRONLY | os.O_RDWR | os.O_CREAT | os.O_TRUNC | os.O_APPEND)
                )
            if writes:
                if path == os.devnull and called_from_context_git():
                    return
                raise PermissionError("filesystem writes are disabled in the REPL")

    sys.addaudithook(guard)


def _evaluate(code, namespace):
    tree = ast.parse(code, mode="exec")
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
    rendered = repr(value)
    if len(rendered) <= MAX_INLINE_CHARS:
        return rendered, None, False
    artifact = _artifacts.put(rendered)
    return rendered[:MAX_INLINE_CHARS], artifact, True


def _execute(request, namespace, generation):
    code = request.get("code")
    if not isinstance(code, str) or not code.strip():
        raise ValueError("code must be a non-empty string")
    if len(code.encode("utf-8")) > MAX_REQUEST_BYTES:
        raise ValueError("code exceeds worker request limit")
    stdout = _BoundedText()
    stderr = _BoundedText()
    try:
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            value = _evaluate(code, namespace)
        rendered, artifact, value_truncated = _inline_or_artifact(value)
        combined = stdout.getvalue() + stderr.getvalue()
        capture_artifact = None
        if stdout.truncated or stderr.truncated:
            capture_artifact = _artifacts.put(combined)
        return {
            "type": "response",
            "ok": True,
            "generation": generation,
            "stdout": stdout.getvalue()[:MAX_INLINE_CHARS],
            "stderr": stderr.getvalue()[:MAX_INLINE_CHARS],
            "value": rendered,
            "artifact": artifact or capture_artifact,
            "truncated": bool(value_truncated or stdout.truncated or stderr.truncated),
        }
    except BaseException as exc:
        return {
            "type": "response",
            "ok": False,
            "generation": generation,
            "stdout": stdout.getvalue()[:MAX_INLINE_CHARS],
            "stderr": stderr.getvalue()[:MAX_INLINE_CHARS],
            "error": {
                "type": type(exc).__name__,
                "message": str(exc)[:MAX_INLINE_CHARS],
                "traceback": traceback.format_exc(limit=20)[-MAX_INLINE_CHARS:],
            },
        }


def _write(response):
    encoded = json.dumps(response, ensure_ascii=False, separators=(",", ":"))
    sys.__stdout__.write(encoded + "\n")
    sys.__stdout__.flush()


def main():
    if len(sys.argv) != 5:
        raise SystemExit("usage: worker.py WORKDIR GENERATION GIT_PATH BACKEND")
    generation = int(sys.argv[2])
    runtime_limits = _apply_resource_limits(sys.argv[4])
    _install_audit_guard()
    runtime_rlm = RLM()
    runtime_rlm.harness = Harness()
    namespace = {
        "__name__": "__gokin_repl__",
        "context": Context(sys.argv[1], sys.argv[3], runtime_limits),
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
                response = _execute(request, namespace, generation)
            else:
                response = {
                    "type": "response",
                    "ok": False,
                    "generation": generation,
                    "error": {"type": "ProtocolError", "message": "unknown method"},
                }
            response["id"] = request_id
            _write(response)
        except BaseException as exc:
            _write(
                {
                    "type": "response",
                    "id": "",
                    "ok": False,
                    "generation": generation,
                    "error": {
                        "type": type(exc).__name__,
                        "message": str(exc)[:MAX_INLINE_CHARS],
                    },
                }
            )


if __name__ == "__main__":
    main()
