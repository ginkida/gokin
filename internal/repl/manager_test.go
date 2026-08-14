package repl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func testManager(t *testing.T, workDir string, mutate func(*Options)) *Manager {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	opts := Options{
		WorkDir: workDir, PythonPath: python,
		CellTimeout: 3 * time.Second,
	}
	if mutate != nil {
		mutate(&opts)
	}
	manager, err := newTestManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return manager
}

func TestManagerPreservesStateAndEvaluatesLastExpression(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	first, err := manager.Execute(t.Context(), "counter = 40\ncounter + 2")
	if err != nil {
		t.Fatal(err)
	}
	if !first.OK() || first.Value != "42" || first.Generation != 1 {
		t.Fatalf("first result = %+v", first)
	}
	second, err := manager.Execute(t.Context(), "counter += 1\ncounter")
	if err != nil {
		t.Fatal(err)
	}
	if second.Value != "41" || second.Generation != first.Generation {
		t.Fatalf("second result = %+v, want preserved generation/state", second)
	}
}

func TestManagerContextIsWorkspaceContained(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "sample.go"), []byte("package sample\n\nfunc Target() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, workDir, nil)
	result, err := manager.Execute(t.Context(), `context.search_code("Target", limit=5)`)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK() || !strings.Contains(result.Value, "sample.go") || !strings.Contains(result.Value, "Target") {
		t.Fatalf("search result = %+v", result)
	}

	escape, err := manager.Execute(t.Context(), `context.read_slice("../outside", 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	if escape.Error == nil || escape.Error.Type != "FileNotFoundError" && escape.Error.Type != "PermissionError" {
		t.Fatalf("escape result = %+v, want contained failure", escape)
	}
}

func TestManagerReportsRuntimeOperationsPerCell(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "sample.go"), []byte("package sample\n// TODO one\n// FIXME two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, workDir, nil)

	first, err := manager.Execute(t.Context(), `
context.count_code("TODO", sample_limit=1)
context.search_code("FIXME")
`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"count_code": 1, "count_code_sampled": 1, "file_inventory": 2, "search_code": 1}
	if fmt.Sprint(first.Operations) != fmt.Sprint(want) {
		t.Fatalf("first operations = %#v, want %#v", first.Operations, want)
	}

	second, err := manager.Execute(t.Context(), "40 + 2")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Operations) != 0 {
		t.Fatalf("operations leaked across cells: %#v", second.Operations)
	}

	third, err := manager.Execute(t.Context(), `context.count_code_many(["TODO", "FIXME"], sample_limit=2)`)
	if err != nil {
		t.Fatal(err)
	}
	if third.Operations["count_code_many"] != 1 || third.Operations["count_code_many_sampled"] != 1 {
		t.Fatalf("third operations = %#v", third.Operations)
	}
	if third.FileIndexRefreshes != 1 {
		t.Fatalf("file index refreshes = %d, want one parent-observed directory scan", third.FileIndexRefreshes)
	}
	if third.Operations["count_code"] != 0 {
		t.Fatalf("count_code_many was double-counted as count_code: %#v", third.Operations)
	}
	if third.Operations["file_inventory"] != 1 {
		t.Fatalf("lowest-layer inventory operations = %#v, want one", third.Operations)
	}

	failed, err := manager.Execute(t.Context(), `context.count_code_many([])`)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Error == nil || len(failed.Operations) != 0 {
		t.Fatalf("failed primitive was reported as completed: %+v", failed)
	}

	for _, code := range []string{
		`context._record_operation("count_code_many")`,
		`context.count_code.__func__.__globals__["_record_context_operation"]("count_code_many")`,
		`context.file_stats.__func__.__globals__["_record_context_operation"]("file_stats")`,
		`context.count_code.__func__.__globals__["_operation_counts"].clear()`,
		`from __main__ import _operation_counts`,
		`import sys
sys.modules["__main__"]._operation_counts.clear()`,
		`reflection = getattr
reflection(context.count_code.__func__, "__globals__")`,
		`globals()["context"]`,
		`c = context
c._file_entries`,
		`c = context
c.count_code = lambda *args: {"matching_lines": 999}`,
		`c = context
type(c).count_code = lambda *args: {"matching_lines": 999}`,
		`c = context
type(c)._SKIP_DIRS`,
		`import json
json.dumps = lambda *args, **kwargs: "forged"`,
		`import pathlib
pathlib.Path.open = lambda *args, **kwargs: None`,
	} {
		tamper, err := manager.Execute(t.Context(), code)
		if err != nil {
			t.Fatal(err)
		}
		if tamper.Error == nil || len(tamper.Operations) != 0 {
			t.Fatalf("cell forged runtime operation evidence with %q: %+v", code, tamper)
		}
	}
}

func TestManagerDefenseInDepthBlocksAmbientActionsButAllowsContextGit(t *testing.T) {
	workDir := t.TempDir()
	protected := filepath.Join(workDir, "protected.txt")
	if err := os.WriteFile(protected, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	initCmd := exec.Command("git", "init", workDir)
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v (%s)", err, output)
	}
	manager := testManager(t, workDir, nil)
	limits, err := manager.Execute(t.Context(), `context.runtime_limits()`)
	if err != nil || !limits.OK() || !strings.Contains(limits.Value, "RLIMIT_NOFILE") {
		t.Fatalf("runtime limits=%+v err=%v", limits, err)
	}
	for name, code := range map[string]string{
		"write": `open("marker.txt", "w").write("no")`,
		"mutable devnull": `
import os
original_devnull = os.devnull
os.devnull = "marker.txt"
try:
    open("marker.txt", "w").write("no")
finally:
    os.devnull = original_devnull`,
		"mutable write flags": `
import os
original_flags = (os.O_WRONLY, os.O_RDWR, os.O_CREAT, os.O_TRUNC, os.O_APPEND)
os.O_WRONLY = os.O_RDWR = os.O_CREAT = os.O_TRUNC = os.O_APPEND = 0
try:
    fd = os.open("marker.txt", original_flags[0] | original_flags[2])
    os.close(fd)
finally:
    os.O_WRONLY, os.O_RDWR, os.O_CREAT, os.O_TRUNC, os.O_APPEND = original_flags`,
		"subprocess":         `__import__("subprocess").run(["echo", "no"])`,
		"exec":               `__import__("os").execl("/bin/echo", "echo", "no")`,
		"signal parent":      `__import__("os").kill(__import__("os").getppid(), 0)`,
		"network":            `__import__("socket").socket()`,
		"native":             `__import__("ctypes").CDLL(None)`,
		"mutation":           `__import__("os").remove("protected.txt")`,
		"watchdog":           `__import__("signal")`,
		"watchdog_low_level": `__import__("_signal")`,
	} {
		t.Run(name, func(t *testing.T) {
			result, execErr := manager.Execute(t.Context(), code)
			if execErr != nil {
				t.Fatal(execErr)
			}
			if result.Error == nil || result.Error.Type != "PermissionError" {
				t.Fatalf("ambient action result = %+v", result)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(workDir, "marker.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked write created marker: %v", err)
	}
	if data, err := os.ReadFile(protected); err != nil || string(data) != "keep" {
		t.Fatalf("blocked mutation changed protected file: data=%q err=%v", data, err)
	}
	for name, code := range map[string]string{
		"listdir": `import os
os.listdir(".")`,
		"scandir": `import os
list(os.scandir("."))`,
		"absolute listdir": `import os
os.listdir(context.workspace)`,
		"absolute scandir": `import os
list(os.scandir(context.workspace))`,
		"walk": `import os
list(os.walk(".", onerror=lambda error: (_ for _ in ()).throw(error)))`,
		"absolute walk": `import os
list(os.walk(context.workspace, onerror=lambda error: (_ for _ in ()).throw(error)))`,
		"path iterdir": `import pathlib
list(pathlib.Path(".").iterdir())`,
		"absolute path iterdir": `import pathlib
list(pathlib.Path(context.workspace).iterdir())`,
	} {
		t.Run("directory enumeration "+name, func(t *testing.T) {
			result, execErr := manager.Execute(t.Context(), code)
			if execErr != nil {
				t.Fatal(execErr)
			}
			if result.Error == nil || result.Error.Type != "PermissionError" ||
				len(result.Operations) != 0 || result.FileIndexRefreshes != 0 {
				t.Fatalf("ambient directory traversal result = %+v", result)
			}
		})
	}
	for name, code := range map[string]string{
		"walk": `import os
list(os.walk("."))`,
		"path glob": `import pathlib
list(pathlib.Path(".").glob("**/*"))`,
	} {
		t.Run("early-blocked directory enumeration "+name, func(t *testing.T) {
			result, execErr := manager.Execute(t.Context(), code)
			if execErr != nil || result.Error == nil || result.Error.Type != "PermissionError" ||
				len(result.Operations) != 0 || result.FileIndexRefreshes != 0 {
				t.Fatalf("ambient directory traversal leaked paths: result=%+v err=%v", result, execErr)
			}
		})
	}
	privateWalk, err := manager.Execute(t.Context(), `list(context._walk_file_entries(context._root))`)
	if err != nil || privateWalk.Error == nil || privateWalk.Error.Type != "PermissionError" ||
		len(privateWalk.Operations) != 0 || privateWalk.FileIndexRefreshes != 0 {
		t.Fatalf("removed private walk remained callable: result=%+v err=%v", privateWalk, err)
	}
	listed, err := manager.Execute(t.Context(), `context.list_files(pattern="*.txt")`)
	if err != nil || listed.Error != nil || listed.Operations["list_files"] != 1 ||
		listed.FileIndexRefreshes != 1 || !strings.Contains(listed.Value, "protected.txt") {
		t.Fatalf("bounded context inventory was blocked: result=%+v err=%v", listed, err)
	}
	gitSurface, err := manager.Execute(t.Context(), `
[hasattr(context, "git_status"), hasattr(context, "git_diff")]`)
	if err != nil || gitSurface.Error != nil || gitSurface.Value != "[False, False]" {
		t.Fatalf("REPL retained Git execution surface=%+v err=%v", gitSurface, err)
	}
}

func TestManagerCellImportAllowlistPreservesAnalyticsAndBlocksRuntimeModules(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	allowed, err := manager.Execute(t.Context(), `import base64, binascii, bisect, collections, csv, datetime
import decimal, fractions, functools, hashlib, heapq, html, itertools, json
import math, operator, pprint, random, re, statistics, textwrap, time
import unicodedata, urllib.parse
[json.loads('{"x": 2}')["x"], statistics.mean([2, 4]), urllib.parse.urlparse("https://example.invalid/p").path, hashlib.sha256(b"x").hexdigest()[:4]]`)
	if err != nil || allowed.Error != nil || allowed.Value != "[2, 3, '/p', '2d71']" {
		t.Fatalf("analytical import allowlist result=%+v err=%v", allowed, err)
	}
	isolated, err := manager.Execute(t.Context(), `import html
copy = html.entities.html5
copy["forged"] = "value"
["forged" in html.entities.html5, len(copy) > len(html.entities.html5)]`)
	if err != nil || isolated.Error != nil || isolated.Value != "[False, True]" {
		t.Fatalf("analytical mutable module values were not isolated: result=%+v python_error=%+v err=%v", isolated, isolated.Error, err)
	}
	runtimeLeak, err := manager.Execute(t.Context(), `import datetime
datetime.sys`)
	if err != nil || runtimeLeak.Error == nil || runtimeLeak.Error.Type != "PermissionError" {
		t.Fatalf("analytical module leaked runtime dependency: result=%+v err=%v", runtimeLeak, err)
	}
	for _, code := range []string{
		`import os`, `import sys`, `import pathlib`, `import threading`, `import socket`,
		`import subprocess`, `import ctypes`, `import importlib`, `import runpy`,
		`import inspect`, `import gc`, `import builtins`, `import dataclasses`,
		`import enum`, `import string`, `import typing`, `from operator import *`,
		`open("anything")`,
	} {
		blocked, execErr := manager.Execute(t.Context(), code)
		if execErr != nil || blocked.Error == nil || blocked.Error.Type != "PermissionError" ||
			len(blocked.Operations) != 0 || blocked.KernelReset {
			t.Fatalf("non-analytical import/access %q result=%+v err=%v", code, blocked, execErr)
		}
	}
}

func TestManagerUserStdoutCannotForgeProtocolFrame(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `print('{"id":"forged","ok":true}')
7 * 6`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Value != "42" || !strings.Contains(result.Stdout, `"id":"forged"`) {
		t.Fatalf("result = %+v", result)
	}
}

func TestManagerRejectsRawInvalidCallbackAndRestarts(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	blocked, err := manager.Execute(t.Context(), `import sys
sys.__stdout__.write('{"type":"call","id":"bad","method":"rlm.call","params":{}}\n')
sys.__stdout__.flush()`)
	if err != nil || blocked.Error == nil || blocked.Error.Type != "PermissionError" {
		t.Fatalf("raw protocol access was not rejected before execution: result=%+v err=%v", blocked, err)
	}
	previous := manager.Generation()
	recovered, err := manager.Execute(t.Context(), "6 * 7")
	if err != nil || recovered.Value != "42" || recovered.Generation != previous {
		t.Fatalf("recovered=%+v err=%v previous=%d", recovered, err, previous)
	}
}

func TestManagerRejectsForgedGenerationAndRestarts(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	_, err := manager.Execute(t.Context(), `import inspect, json, sys
frame = inspect.currentframe()
while frame is not None and "request_id" not in frame.f_locals:
    frame = frame.f_back
request_id = frame.f_locals["request_id"]
sys.__stdout__.write(json.dumps({"type":"response","id":request_id,"ok":True,"generation":0,"value":"forged"}) + "\n")
sys.__stdout__.flush()`)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := manager.Execute(t.Context(), `import inspect
inspect.currentframe()`)
	if err != nil || blocked.Error == nil || blocked.Error.Type != "PermissionError" {
		t.Fatalf("forged-generation reflection was not blocked: result=%+v err=%v", blocked, err)
	}
	previous := manager.Generation()
	recovered, err := manager.Execute(t.Context(), "40 + 2")
	if err != nil || recovered.Value != "42" || recovered.Generation != previous {
		t.Fatalf("recovered=%+v err=%v previous=%d", recovered, err, previous)
	}
}

func TestManagerLargeValueBecomesArtifact(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `"x" * 100000`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifact == nil || !result.Truncated || result.Artifact.Size == 0 {
		t.Fatalf("large result = %+v, want artifact", result)
	}
	lookup, err := manager.Execute(t.Context(), `context.artifact_get("`+result.Artifact.ID+`", 0, 16)`)
	if err != nil {
		t.Fatal(err)
	}
	if !lookup.OK() || !strings.Contains(lookup.Value, "xxxxxxxx") {
		t.Fatalf("artifact lookup = %+v", lookup)
	}
}

func TestManagerCapsHugeScalarWithoutResettingKernel(t *testing.T) {
	manager := testManager(t, t.TempDir(), func(opts *Options) {
		opts.MaxMemoryBytes = 80 * 1024 * 1024
	})
	result, err := manager.Execute(t.Context(), `"x" * (32 * 1024 * 1024)`)
	if err != nil {
		t.Fatal(err)
	}
	artifact := result.Artifacts["value"]
	if result.Error != nil || result.KernelReset || artifact == nil || !artifact.Truncated ||
		artifact.Size > 4*1024*1024 || len(result.Value) > 8*1024 {
		t.Fatalf("huge scalar: error=%+v reset=%t inline_bytes=%d artifact=%+v",
			result.Error, result.KernelReset, len(result.Value), artifact)
	}
	if stats := manager.Stats(); !stats.Running || stats.ResourceLimitFailures != 0 {
		t.Fatalf("huge scalar should preserve the bounded kernel: %+v", stats)
	}
}

func TestManagerLargeStdoutBecomesArtifactWithoutSilentLoss(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `print("stdout-prefix:" + "s" * 100000)
42`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifact == nil || !result.Truncated {
		t.Fatalf("large stdout: inline_bytes=%d artifact=%+v truncated=%t",
			len(result.Stdout), result.Artifact, result.Truncated)
	}
	lookup, err := manager.Execute(t.Context(), `context.artifact_get("`+result.Artifact.ID+`", 0, 32)`)
	if err != nil {
		t.Fatal(err)
	}
	if !lookup.OK() || !strings.Contains(lookup.Value, "stdout-prefix") {
		t.Fatalf("stdout artifact lookup = %+v", lookup)
	}
}

func TestManagerPreservesSimultaneousValueStdoutAndStderrArtifacts(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `print("stdout-prefix:" + "s" * 100000)
eprint("stderr-prefix:" + "e" * 100000)
"value-prefix:" + "v" * 100000`)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Artifacts) != 3 {
		t.Fatalf("multi-channel result: inline value/stdout/stderr bytes=%d/%d/%d artifacts=%+v truncated=%t",
			len(result.Value), len(result.Stdout), len(result.Stderr), result.Artifacts, result.Truncated)
	}
	if result.Artifact == nil || result.Artifacts["value"] == nil ||
		result.Artifact.ID != result.Artifacts["value"].ID {
		t.Fatalf("primary artifact = %+v, named = %+v", result.Artifact, result.Artifacts)
	}
	for name, prefix := range map[string]string{
		"value": "value-prefix", "stdout": "stdout-prefix", "stderr": "stderr-prefix",
	} {
		artifact := result.Artifacts[name]
		if artifact == nil {
			t.Fatalf("missing %s artifact: %+v", name, result.Artifacts)
		}
		lookup, lookupErr := manager.Execute(t.Context(), `context.artifact_get("`+artifact.ID+`", 0, 32)`)
		if lookupErr != nil {
			t.Fatal(lookupErr)
		}
		if !lookup.OK() || !strings.Contains(lookup.Value, prefix) {
			t.Fatalf("%s artifact lookup = %+v", name, lookup)
		}
	}
}

func TestManagerPreservesLargeCaptureArtifactOnPythonError(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `print("before-error:" + "x" * 100000)
raise ValueError("boom")`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == nil || result.Error.Type != "ValueError" || !result.Truncated ||
		result.Artifacts["stdout"] == nil {
		t.Fatalf("large error capture: error=%+v inline_stdout_bytes=%d artifacts=%+v truncated=%t",
			result.Error, len(result.Stdout), result.Artifacts, result.Truncated)
	}
	lookup, err := manager.Execute(t.Context(), `context.artifact_get("`+result.Artifacts["stdout"].ID+`", 0, 32)`)
	if err != nil || !lookup.OK() || !strings.Contains(lookup.Value, "before-error") {
		t.Fatalf("error stdout artifact = %+v err=%v", lookup, err)
	}
}

func TestManagerMarksCaptureArtifactCappedAtHardLimit(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `print("capture-prefix:" + "x" * (5 * 1024 * 1024))`)
	if err != nil {
		t.Fatal(err)
	}
	artifact := result.Artifacts["stdout"]
	if artifact == nil || !artifact.Truncated || artifact.Size > 4*1024*1024 ||
		len(result.Stdout) > 8*1024 || !result.Truncated {
		t.Fatalf("capture cap: inline_bytes=%d artifact=%+v result_truncated=%t",
			len(result.Stdout), artifact, result.Truncated)
	}
}

func TestManagerPreservesOversizedPythonErrorDetailsAsArtifacts(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `raise ValueError("error-prefix:" + "x" * 10000)`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == nil || len(result.Error.Message) > 8*1024 || !result.Truncated {
		t.Fatalf("bounded error: error=%+v artifacts=%+v truncated=%t",
			result.Error, result.Artifacts, result.Truncated)
	}
	for _, name := range []string{"error_message", "traceback"} {
		if result.Artifacts[name] == nil {
			t.Fatalf("missing %s artifact: %+v", name, result.Artifacts)
		}
	}
}

func TestManagerUnicodeStdoutIsByteBoundedAndValid(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `print("😀" * 3000)`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifacts["stdout"] == nil || len(result.Stdout) > 8*1024 ||
		!utf8.ValidString(result.Stdout) || strings.ContainsRune(result.Stdout, '�') {
		t.Fatalf("unicode stdout: inline_bytes=%d valid=%t artifacts=%+v",
			len(result.Stdout), utf8.ValidString(result.Stdout), result.Artifacts)
	}
}

func TestManagerArtifactChunksPreserveUTF8AndReturnNextOffset(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `"😀" * 20000`)
	if err != nil {
		t.Fatal(err)
	}
	artifact := result.Artifacts["value"]
	if artifact == nil || !utf8.ValidString(result.Value) {
		t.Fatalf("unicode artifact: inline_bytes=%d valid_utf8=%t artifacts=%+v",
			len(result.Value), utf8.ValidString(result.Value), result.Artifacts)
	}
	lookup, err := manager.Execute(t.Context(), `
c = context.artifact_get("`+artifact.ID+`", 2, 5)
[c["offset"], c["next_offset"], "�" in c["content"], len(c["content"])]`)
	if err != nil || !lookup.OK() || lookup.Value != "[5, 9, False, 1]" {
		t.Fatalf("unicode artifact chunk = %+v err=%v", lookup, err)
	}
}

func TestManagerTimeoutRestartsCleanGeneration(t *testing.T) {
	manager := testManager(t, t.TempDir(), func(opts *Options) {
		opts.CellTimeout = 100 * time.Millisecond
	})
	before, err := manager.Execute(t.Context(), "survives = 42\nsurvives")
	if err != nil || before.Value != "42" {
		t.Fatalf("initial cell = %+v, %v", before, err)
	}
	_, err = manager.Execute(t.Context(), "while True:\n    pass")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("infinite cell error = %v, want deadline", err)
	}
	after, err := manager.Execute(t.Context(), `try:
    survives
    clean = False
except NameError:
    clean = True
clean`)
	if err != nil || after.Error != nil || after.Generation <= before.Generation || after.Value != "True" {
		t.Fatalf("post-timeout result = %+v, before=%+v", after, before)
	}
	stats := manager.Stats()
	if stats.Timeouts != 1 || stats.TransportFailures != 1 || stats.Restarts < 1 || stats.Executions != 2 {
		t.Fatalf("post-recovery stats = %+v", stats)
	}
}

func TestManagerMemoryLimitDiscardsGenerationAndRecovers(t *testing.T) {
	manager := testManager(t, t.TempDir(), func(opts *Options) {
		opts.MaxMemoryBytes = 48 * 1024 * 1024
		opts.CellTimeout = 3 * time.Second
	})
	limited, err := manager.Execute(t.Context(), memoryLimitProbeCode())
	if err != nil {
		t.Fatalf("memory breach should return a fatal cell result: %v", err)
	}
	if limited.Error == nil || limited.Error.Type != "MemoryLimitExceeded" || !limited.KernelReset {
		t.Fatalf("memory-limited result = %+v", limited)
	}
	stats := manager.Stats()
	if stats.Running || stats.ResourceLimitFailures != 1 || stats.TransportFailures != 0 || stats.Executions != 1 {
		t.Fatalf("post-limit stats = %+v", stats)
	}
	recovered, err := manager.Execute(t.Context(), `try:
    payload
    clean = False
except NameError:
    clean = True
clean`)
	if err != nil || recovered.Value != "True" || recovered.Generation <= limited.Generation {
		t.Fatalf("post-limit recovery=%+v err=%v limited_generation=%d", recovered, err, limited.Generation)
	}
}

func TestParentMemoryMonitorSurvivesWorkerTimerTampering(t *testing.T) {
	if !residentMemorySupported() {
		t.Skip("parent resident-memory monitor is unavailable in this build")
	}
	const limit = 48 * 1024 * 1024
	manager := testManager(t, t.TempDir(), func(opts *Options) {
		opts.MaxMemoryBytes = limit
		opts.CellTimeout = 3 * time.Second
	})
	tamper, err := manager.Execute(t.Context(), memoryWatchdogTamperCode(limit))
	if err != nil || tamper.Error == nil || tamper.Error.Type != "PermissionError" || tamper.KernelReset {
		t.Fatalf("watchdog reflection was not rejected before execution: result=%+v err=%v", tamper, err)
	}
	limited, err := manager.Execute(t.Context(), memoryLimitProbeCode())
	if limited.Error == nil || limited.Error.Type != "MemoryLimitExceeded" || !limited.KernelReset {
		t.Fatalf("parent memory monitor result = %+v", limited)
	}
	if stats := manager.Stats(); stats.ResourceLimitFailures != 1 || stats.TransportFailures != 0 {
		t.Fatalf("parent monitor stats = %+v", stats)
	}
}

func memoryWatchdogTamperCode(limit int64) string {
	return fmt.Sprintf(`import inspect, time
frame = inspect.currentframe()
watchdog = None
while frame is not None:
    candidate = frame.f_locals.get("check_memory")
    if callable(candidate):
        watchdog = candidate
        break
    frame = frame.f_back
if watchdog is not None:
    for cell in watchdog.__closure__ or ():
        if cell.cell_contents == %d:
            cell.cell_contents = 2**63 - 1
payload = "x" * (64 * 1024 * 1024)
deadline = time.monotonic() + 1
while time.monotonic() < deadline:
    pass
"cell survived"`, limit)
}

func memoryLimitProbeCode() string {
	return `import time
try:
    payload = "x" * (64 * 1024 * 1024)
    deadline = time.monotonic() + 1
    while time.monotonic() < deadline:
        pass
except BaseException:
    attempted_to_suppress_limit = True
"cell survived"`
}

func TestManagerManualResetDiscardsStateAndUpdatesStats(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	before, err := manager.Execute(t.Context(), "state = 42\nstate")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	stopped := manager.Stats()
	if stopped.Running || stopped.ManualResets != 1 || stopped.Generation != before.Generation {
		t.Fatalf("stats after reset = %+v", stopped)
	}
	after, err := manager.Execute(t.Context(), `try:
    state
    clean = False
except NameError:
    clean = True
clean`)
	if err != nil || after.Value != "True" || after.Generation <= before.Generation {
		t.Fatalf("after reset=%+v err=%v before=%+v", after, err, before)
	}
}

func TestNewManagerRejectsUnrestrictedBackend(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, err = NewManager(Options{WorkDir: t.TempDir(), PythonPath: python, Backend: BackendTest})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("NewManager error = %v, want ErrUnavailable", err)
	}
}

func TestManagerLimitsCodeBeforeStartingWorker(t *testing.T) {
	manager := testManager(t, t.TempDir(), func(opts *Options) { opts.MaxCodeBytes = 8 })
	_, err := manager.Execute(t.Context(), "123456789")
	if err == nil || !strings.Contains(err.Error(), "8-byte") {
		t.Fatalf("oversized code error = %v", err)
	}
	if manager.Generation() != 0 {
		t.Fatalf("oversized code started generation %d", manager.Generation())
	}
}

func TestReadFrameRejectsOversizedResponse(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("123456789\nnext\n"))
	_, err := readFrame(reader, 8)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("readFrame error = %v", err)
	}
}

func TestProtocolIDsAndCallbackEnvelopeValidation(t *testing.T) {
	first, err := newProtocolID("req-")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newProtocolID("req-")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != len("req-")+32 || !strings.HasPrefix(first, "req-") {
		t.Fatalf("protocol ids first=%q second=%q", first, second)
	}
	for _, tc := range []struct {
		id, method string
		wantOK     bool
	}{
		{"call_0123456789abcdef0123456789abcdef", "harness.memory_put", true},
		{"call_bad", "rlm.call", false},
		{"call_0123456789abcdef0123456789abcg", "rlm.call", false},
		{"call_0123456789abcdef0123456789abcdef", "RLM.call", false},
		{"call_0123456789abcdef0123456789abcdef", strings.Repeat("a", 129), false},
	} {
		err := validateCallbackEnvelope(tc.id, tc.method)
		if (err == nil) != tc.wantOK {
			t.Errorf("validateCallbackEnvelope(%q,%q) error=%v wantOK=%v", tc.id, tc.method, err, tc.wantOK)
		}
	}
}

func TestManagerCloseIsIdempotent(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	if _, err := manager.Execute(t.Context(), "1"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Execute(t.Context(), "2"); err == nil {
		t.Fatal("Execute after Close unexpectedly succeeded")
	}
}

func TestManagerRLMCallbacksAreTypedAndStateful(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	var calls []Call
	manager.SetCallHandler(func(_ context.Context, call Call) (any, error) {
		calls = append(calls, call)
		switch call.Method {
		case "rlm.call":
			if async, _ := call.Params["async"].(bool); async {
				return map[string]any{"success": true, "data": map[string]any{"agent_id": "agent-1"}}, nil
			}
			return map[string]any{"success": true, "content": "sync result"}, nil
		case "rlm.result":
			return map[string]any{"success": true, "content": "async result"}, nil
		default:
			return nil, errors.New("unexpected method")
		}
	})

	syncResult, err := manager.Execute(t.Context(), `rlm("inspect", {"paths": ["a.go"]})`)
	if err != nil || !syncResult.OK() || !strings.Contains(syncResult.Value, "sync result") {
		t.Fatalf("sync rlm = %+v, err=%v", syncResult, err)
	}
	asyncResult, err := manager.Execute(t.Context(), `future = rlm.async_call("inspect")
future.result(timeout=1)`)
	if err != nil || !asyncResult.OK() || !strings.Contains(asyncResult.Value, "async result") {
		t.Fatalf("async rlm = %+v, err=%v", asyncResult, err)
	}
	if len(calls) != 3 || calls[0].Method != "rlm.call" || calls[2].Method != "rlm.result" {
		t.Fatalf("callback sequence = %+v", calls)
	}
}

func TestManagerCallbackWaitDoesNotConsumePythonInactivityBudget(t *testing.T) {
	manager := testManager(t, t.TempDir(), func(opts *Options) {
		opts.CellTimeout = 40 * time.Millisecond
	})
	manager.SetCallHandler(func(context.Context, Call) (any, error) {
		time.Sleep(120 * time.Millisecond)
		return map[string]any{"success": true, "content": "patient"}, nil
	})
	result, err := manager.Execute(t.Context(), `rlm("slow delegation")`)
	if err != nil || !result.OK() || !strings.Contains(result.Value, "patient") {
		t.Fatalf("callback-paused inactivity result = %+v, err=%v", result, err)
	}
}

func TestManagerMissingCallbackBecomesPythonError(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `rlm("not wired")`)
	if err != nil {
		t.Fatalf("missing handler should be a cell error: %v", err)
	}
	if result.Error == nil || result.Error.Type != "RuntimeError" ||
		!strings.Contains(result.Error.Message, "unavailable") {
		t.Fatalf("missing callback result = %+v", result)
	}
}

func TestManagerEnforcesCallbackBudgetAndRestarts(t *testing.T) {
	manager := testManager(t, t.TempDir(), func(opts *Options) { opts.MaxCallbacks = 1 })
	manager.SetCallHandler(func(_ context.Context, call Call) (any, error) {
		if call.Method == "rlm.call" {
			return map[string]any{"success": true, "data": map[string]any{"agent_id": "agent-1"}}, nil
		}
		return map[string]any{"success": true}, nil
	})
	_, err := manager.Execute(t.Context(), `future = rlm.async_call("one")
future.poll()`)
	if err == nil || !strings.Contains(err.Error(), "exceeded 1") {
		t.Fatalf("callback budget error = %v", err)
	}
	previous := manager.Generation()
	recovered, err := manager.Execute(t.Context(), "1 + 1")
	if err != nil || recovered.Generation <= previous || recovered.Value != "2" {
		t.Fatalf("recovered result = %+v, err=%v, previous generation=%d", recovered, err, previous)
	}
}

func TestManagerFileIndexRefreshDoesNotConsumeOrchestratorCallbackBudget(t *testing.T) {
	manager := testManager(t, t.TempDir(), func(opts *Options) { opts.MaxCallbacks = 1 })
	manager.SetCallHandler(func(_ context.Context, call Call) (any, error) {
		if call.Method != "rlm.call" {
			t.Fatalf("internal callback leaked to orchestrator handler: %s", call.Method)
		}
		return map[string]any{"success": true, "content": "done"}, nil
	})
	result, err := manager.Execute(t.Context(), `
inventory = context.list_files()
rlm("one allowed callback")`)
	if err != nil || !result.OK() || !strings.Contains(result.Value, "done") {
		t.Fatalf("index plus orchestrator callback result=%+v err=%v", result, err)
	}
}

func TestManagerReusesFileIndexWithinCellButRefreshesBetweenCells(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	if err := os.WriteFile(filepath.Join(workDir, "first.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, workDir, nil)

	first, err := manager.Execute(t.Context(), `
listed = context.list_files(pattern="*.txt")
counted = context.count_code("needle")
[len(listed["files"]), counted["matching_files"]]`)
	if err != nil || first.Error != nil || first.Value != "[1, 1]" {
		t.Fatalf("compound scan=%+v err=%v", first, err)
	}
	if first.FileIndexRefreshes != 1 {
		t.Fatalf("compound scan refreshed index %d times, want one per-cell scope snapshot", first.FileIndexRefreshes)
	}
	if first.Operations["file_inventory"] != 2 {
		t.Fatalf("compound scan logical inventories=%v, want two", first.Operations)
	}

	if err := os.WriteFile(filepath.Join(workDir, "second.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := manager.Execute(t.Context(), `context.count_code("needle")["matching_files"]`)
	if err != nil || second.Error != nil || second.Value != "2" {
		t.Fatalf("next-cell refresh=%+v err=%v", second, err)
	}
	if second.FileIndexRefreshes != 1 {
		t.Fatalf("next cell refreshed index %d times, want one", second.FileIndexRefreshes)
	}
}

func TestManagerReusesSeveralFileIndexScopesWithinCell(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	if err := os.MkdirAll(filepath.Join(workDir, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"root.txt":        "needle root\n",
		"src/nested.txt":  "needle nested\n",
		"src/another.txt": "other\n",
	} {
		if err := os.WriteFile(filepath.Join(workDir, filepath.FromSlash(path)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := testManager(t, workDir, nil)
	result, err := manager.Execute(t.Context(), `
root_before = context.list_files(path=".", pattern="*.txt")
nested = context.count_code("needle", path="src")
root_after = context.count_code("needle", path=".")
[len(root_before["files"]), nested["matching_files"], root_after["matching_files"]]`)
	if err != nil || result.Error != nil || result.Value != "[3, 1, 2]" {
		t.Fatalf("multi-scope reuse=%+v err=%v", result, err)
	}
	if result.FileIndexRefreshes != 2 {
		t.Fatalf("root/src/root refreshed index %d times, want two unique scopes", result.FileIndexRefreshes)
	}
}

func TestManagerInvalidatesFileIndexAfterOrchestratorCallback(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	if err := os.WriteFile(filepath.Join(workDir, "before.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, workDir, func(opts *Options) { opts.MaxCallbacks = 4 })
	manager.SetCallHandler(func(_ context.Context, call Call) (any, error) {
		if call.Method != "rlm.call" {
			t.Fatalf("unexpected callback: %s", call.Method)
		}
		if err := os.WriteFile(filepath.Join(workDir, "after.txt"), []byte("needle\n"), 0o600); err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "content": "mutated"}, nil
	})

	result, err := manager.Execute(t.Context(), `
before = context.count_code("needle")["matching_files"]
rlm("create a file")
after = context.count_code("needle")["matching_files"]
[before, after]`)
	if err != nil || result.Error != nil || result.Value != "[1, 2]" {
		t.Fatalf("callback invalidation=%+v err=%v", result, err)
	}
	if result.FileIndexRefreshes != 2 {
		t.Fatalf("callback invalidation refreshed index %d times, want before+after", result.FileIndexRefreshes)
	}
}

func TestManagerClearsFileIndexAfterFailedCell(t *testing.T) {
	workDir := resolvedReplTempDir(t)
	initFileIndexGitRepo(t, workDir)
	if err := os.WriteFile(filepath.Join(workDir, "before.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := testManager(t, workDir, nil)

	failed, err := manager.Execute(t.Context(), `
context.count_code("needle")
raise RuntimeError("stop after inventory")`)
	if err != nil || failed.Error == nil || failed.FileIndexRefreshes != 1 {
		t.Fatalf("failed inventory cell=%+v err=%v", failed, err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "after.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	refreshed, err := manager.Execute(t.Context(), `context.count_code("needle")["matching_files"]`)
	if err != nil || refreshed.Error != nil || refreshed.Value != "2" || refreshed.FileIndexRefreshes != 1 {
		t.Fatalf("post-error refresh=%+v err=%v", refreshed, err)
	}
}

func TestManagerContextExposesNoGitProcessSurface(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `
[hasattr(context, "git_status"), hasattr(context, "git_diff")]`)
	if err != nil || result.Error != nil || result.Value != "[False, False]" {
		t.Fatalf("REPL retained Git/process capability: result=%+v err=%v", result, err)
	}
	private, err := manager.Execute(t.Context(), `hasattr(context, "_git")`)
	if err != nil || private.Error == nil || private.Error.Type != "PermissionError" {
		t.Fatalf("REPL allowed private context probing: result=%+v err=%v", private, err)
	}
	reflection, err := manager.Execute(t.Context(), `context.__class__.__init__.__globals__`)
	if err != nil || reflection.Error == nil || reflection.Error.Type != "PermissionError" {
		t.Fatalf("REPL exposed worker globals through reflection: result=%+v err=%v", reflection, err)
	}
}

func TestCloseCommandExtraFilesClosesAndClearsDescriptors(t *testing.T) {
	first, err := os.CreateTemp(t.TempDir(), "extra-first-*")
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.CreateTemp(t.TempDir(), "extra-second-*")
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	cmd := &exec.Cmd{ExtraFiles: []*os.File{first, nil, second}}
	closeCommandExtraFiles(cmd)
	if cmd.ExtraFiles != nil {
		t.Fatalf("extra descriptors retained: %v", cmd.ExtraFiles)
	}
	for _, file := range []*os.File{first, second} {
		if _, err := file.Write([]byte("x")); err == nil {
			t.Fatalf("descriptor %q remained writable after cleanup", file.Name())
		}
	}
	// Repeated cleanup is safe on partially initialized command paths.
	closeCommandExtraFiles(cmd)
	closeCommandExtraFiles(nil)
}
