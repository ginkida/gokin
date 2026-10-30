package repl

import (
	"strings"
	"testing"
)

func TestManagerCellValidatorAllowsPlainData(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	for _, tc := range []struct {
		name string
		code string
		want string
	}{
		{"dict get", `d = {}; d.get("k", 0)`, "0"},
		{"list extend", `xs = []; xs.extend([1, 2]); xs`, "[1, 2]"},
		{"list append", `xs = []; xs.append(3); xs`, "[3]"},
		{"bound method", `xs = []; append = xs.append; append(4); xs`, "[4]"},
		{"string split", `s = "a b"; s.split()`, "['a', 'b']"},
		{"string join", `", ".join(["a", "b"])`, "'a, b'"},
		{"dict items", `d = {"k": 1}; list(d.items())`, "[('k', 1)]"},
		{"dict keys", `d = {"k": 1}; list(d.keys())`, "['k']"},
		{"dict values", `d = {"k": 1}; list(d.values())`, "[1]"},
		{"unbound methods", `d = {"k": 1}; [list(dict.items(d)), list(dict.keys(d)), list(dict.values(d))]`, "[[('k', 1)], ['k'], [1]]"},
		{"sorted", `sorted([3, 1, 2])`, "[1, 2, 3]"},
		{"list comprehension", `[d.get("k", 0) for d in [{"k": 2}, {}]]`, "[2, 0]"},
		{"dict comprehension", `{k: v + 1 for k, v in {"k": 1}.items()}`, "{'k': 2}"},
		{"chained methods", `" ".join("a,b".split(",")).upper()`, "'A B'"},
		{"private data keys", `d = {"__globals__": 1}; d.get("__globals__")`, "1"},
		{"context data", `context.runtime_limits().get("missing", 42)`, "42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := manager.Execute(t.Context(), tc.code)
			if err != nil || !result.OK() || result.Value != tc.want {
				t.Fatalf("plain data cell %q: result=%+v err=%v, want %s", tc.code, result, err, tc.want)
			}
		})
	}
}

func TestManagerCellValidatorExplainsRejectedConstructs(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	for _, tc := range []struct {
		name      string
		code      string
		construct string
	}{
		{"class traversal", `().__class__.__bases__`, "__bases__"},
		{"function globals", `context.search_code.__globals__`, "__globals__"},
		{"private context", `context._root`, "_root"},
		{"private alias", `c = context; c._root`, "_root"},
		{"runtime name", `rlm.__name__`, "__name__"},
		{"getattr", `getattr(context.search_code, "__globals__")`, "getattr"},
		{"reflection alias", `reflect = getattr`, "getattr"},
		{"import call", `__import__("os")`, "__import__"},
		{"builtins", `__builtins__["open"]("anything")`, "__builtins__"},
		{"open", `open("anything")`, "open"},
		{"os", `import os`, "os"},
		{"subprocess", `import subprocess`, "subprocess"},
		{"builtins module", `import builtins`, "builtins"},
		{"relative import", `from .math import sqrt`, "math"},
		{"runtime module", `import json; json.codecs`, "codecs"},
		{"reflective import", `from operator import attrgetter`, "attrgetter"},
		{"reflective method", `import operator; operator.methodcaller("__getattribute__")`, "methodcaller"},
		{"wildcard import", `from math import *`, "*"},
		{"attribute store", `context.search_code = print`, "search_code"},
		{"attribute delete", `del context.search_code`, "search_code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := manager.Execute(t.Context(), tc.code)
			if err != nil || result.Error == nil || result.Error.Type != "PermissionError" || result.KernelReset {
				t.Fatalf("unsafe cell %q: result=%+v err=%v", tc.code, result, err)
			}
			for _, want := range []string{tc.construct, "Use ", "Do not retry unchanged code"} {
				if !strings.Contains(result.Error.Message, want) {
					t.Errorf("rejection for %q omitted %q: %s", tc.code, want, result.Error.Message)
				}
			}
		})
	}
}

func TestManagerCellValidatorRejectsBeforeExecutingPrefix(t *testing.T) {
	manager := testManager(t, t.TempDir(), nil)
	result, err := manager.Execute(t.Context(), `xs = []; xs`)
	if err != nil || !result.OK() {
		t.Fatalf("setup result=%+v err=%v", result, err)
	}
	for range 2 {
		rejected, err := manager.Execute(t.Context(), "xs.append(1)\ncontext._root")
		if err != nil || rejected.Error == nil || rejected.Error.Type != "PermissionError" ||
			rejected.Generation != result.Generation || len(rejected.Operations) != 0 {
			t.Fatalf("rejected cell=%+v err=%v", rejected, err)
		}
		if !strings.Contains(rejected.Error.Message, "attribute '_root' at line 2, column 1") {
			t.Fatalf("rejection omitted the exact construct/location: %s", rejected.Error.Message)
		}
	}
	after, err := manager.Execute(t.Context(), `xs`)
	if err != nil || !after.OK() || after.Value != "[]" || after.Generation != result.Generation {
		t.Fatalf("rejection executed a prefix or discarded state: result=%+v err=%v", after, err)
	}
}
