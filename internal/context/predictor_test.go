package context

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// helper: create a temp file and return its absolute path
func tmpFile(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// --- ContextMetrics ---

func TestContextMetrics_RecordPrepare(t *testing.T) {
	m := NewContextMetrics()
	m.RecordPrepare(10*time.Millisecond, 500)
	m.RecordPrepare(20*time.Millisecond, 300)

	s := m.GetSummary()
	if s.Requests != 2 {
		t.Errorf("Requests = %d, want 2", s.Requests)
	}
	if s.TokensProcessed != 800 {
		t.Errorf("TokensProcessed = %d, want 800", s.TokensProcessed)
	}
	if s.AveragePrepareTime != 15*time.Millisecond {
		t.Errorf("AveragePrepareTime = %v, want 15ms", s.AveragePrepareTime)
	}
}

func TestContextMetrics_RecordOptimize(t *testing.T) {
	m := NewContextMetrics()
	m.RecordOptimize(5*time.Millisecond, 1000, 400)

	s := m.GetSummary()
	if s.Optimizations != 1 {
		t.Errorf("Optimizations = %d, want 1", s.Optimizations)
	}
	if s.TokensSaved != 600 {
		t.Errorf("TokensSaved = %d, want 600", s.TokensSaved)
	}
	if s.AverageOptimizeTime != 5*time.Millisecond {
		t.Errorf("AverageOptimizeTime = %v, want 5ms", s.AverageOptimizeTime)
	}
}

func TestContextMetrics_RecordSummary(t *testing.T) {
	m := NewContextMetrics()
	m.RecordSummary(100*time.Millisecond, 200, 1000, true) // cache hit
	m.RecordSummary(50*time.Millisecond, 150, 800, false)  // cache miss

	s := m.GetSummary()
	if s.Summaries != 2 {
		t.Errorf("Summaries = %d, want 2", s.Summaries)
	}
	if s.CacheHitRate != 0.5 {
		t.Errorf("CacheHitRate = %f, want 0.5", s.CacheHitRate)
	}
	// saved per summary = (800 + 650) / 2 = 725
	if s.TokensSavedPerSummary != 725 {
		t.Errorf("TokensSavedPerSummary = %d, want 725", s.TokensSavedPerSummary)
	}
}

func TestContextMetrics_RecordEstimationAndAPICount(t *testing.T) {
	m := NewContextMetrics()
	m.RecordEstimation() // miss
	m.RecordEstimation() // miss
	m.RecordAPICount()   // hit
	m.RecordAPICount()   // hit
	m.RecordAPICount()   // hit

	s := m.GetSummary()
	// 3 hits / 5 total = 0.6
	if s.EstimationAccuracy != 0.6 {
		t.Errorf("EstimationAccuracy = %f, want 0.6", s.EstimationAccuracy)
	}
}

func TestContextMetrics_GetCacheStats(t *testing.T) {
	m := NewContextMetrics()
	hits, misses, rate := m.GetCacheStats()
	if hits != 0 || misses != 0 || rate != 0 {
		t.Errorf("empty GetCacheStats = (%d,%d,%f), want zeros", hits, misses, rate)
	}

	m.RecordSummary(1*time.Millisecond, 10, 100, true)
	m.RecordSummary(1*time.Millisecond, 10, 100, false)
	m.RecordSummary(1*time.Millisecond, 10, 100, true)

	hits, misses, rate = m.GetCacheStats()
	if hits != 2 || misses != 1 {
		t.Errorf("GetCacheStats = (%d,%d), want (2,1)", hits, misses)
	}
	if rate != 2.0/3.0 {
		t.Errorf("rate = %f, want %f", rate, 2.0/3.0)
	}
}

func TestContextMetrics_GetTokenStats(t *testing.T) {
	m := NewContextMetrics()
	processed, saved, ratio := m.GetTokenStats()
	if processed != 0 || saved != 0 || ratio != 0 {
		t.Errorf("empty GetTokenStats = (%d,%d,%f), want zeros", processed, saved, ratio)
	}

	m.RecordPrepare(1*time.Millisecond, 1000)
	m.RecordOptimize(1*time.Millisecond, 1000, 400)

	processed, saved, ratio = m.GetTokenStats()
	if processed != 1000 {
		t.Errorf("processed = %d, want 1000", processed)
	}
	if saved != 600 {
		t.Errorf("saved = %d, want 600", saved)
	}
	if ratio != 0.6 {
		t.Errorf("ratio = %f, want 0.6", ratio)
	}
}

func TestContextMetrics_Reset(t *testing.T) {
	m := NewContextMetrics()
	m.RecordPrepare(1*time.Millisecond, 100)
	m.RecordOptimize(1*time.Millisecond, 100, 50)
	m.RecordSummary(1*time.Millisecond, 10, 100, true)
	m.RecordEstimation()

	m.Reset()

	s := m.GetSummary()
	if s.Requests != 0 || s.Optimizations != 0 || s.Summaries != 0 {
		t.Errorf("after Reset, summary = %+v, want all zeros", s)
	}
	hits, misses, _ := m.GetCacheStats()
	if hits != 0 || misses != 0 {
		t.Errorf("after Reset, cache = (%d,%d), want zeros", hits, misses)
	}
}

func TestContextMetrics_GetUptime(t *testing.T) {
	m := NewContextMetrics()
	// uptime should be non-negative and small for a fresh instance
	u := m.GetUptime()
	if u < 0 {
		t.Errorf("uptime = %v, want >= 0", u)
	}
	if u > time.Second {
		t.Errorf("uptime = %v, want < 1s for fresh instance", u)
	}
}

// --- ContextPredictor ---

func TestNewContextPredictor(t *testing.T) {
	cp := NewContextPredictor("/work")
	if cp == nil {
		t.Fatal("NewContextPredictor returned nil")
	}
	if cp.workDir != "/work" {
		t.Errorf("workDir = %q, want /work", cp.workDir)
	}
	stats := cp.GetStats()
	if stats["access_history_size"].(int) != 0 {
		t.Errorf("fresh predictor should have empty access history")
	}
}

func TestPredictor_RecordAccessAndCoAccess(t *testing.T) {
	cp := NewContextPredictor("")

	fileA := tmpFile(t, "a.go")
	fileB := tmpFile(t, "b.go")

	// Record several co-accesses from A → B
	for i := 0; i < 5; i++ {
		cp.RecordAccess(fileB, "read", fileA)
	}

	stats := cp.GetStats()
	if stats["access_history_size"].(int) != 5 {
		t.Errorf("access_history_size = %v, want 5", stats["access_history_size"])
	}
	if stats["co_access_entries"].(int) != 1 {
		t.Errorf("co_access_entries = %v, want 1", stats["co_access_entries"])
	}
}

func TestPredictor_RecordAccessTrimsHistory(t *testing.T) {
	cp := NewContextPredictor("")

	for i := 0; i < 1100; i++ {
		cp.RecordAccess(filepath.Join("file", string(rune('a'+i%26))), "read", "")
	}

	stats := cp.GetStats()
	if stats["access_history_size"].(int) > 1000 {
		t.Errorf("access_history_size = %v, want <= 1000 (trimmed)", stats["access_history_size"])
	}
}

func TestPredictor_RecordAccessTypeRelations(t *testing.T) {
	cp := NewContextPredictor("")

	cp.RecordAccess("target.ts", "read", "source.go")

	stats := cp.GetStats()
	if stats["type_relations"].(int) != 1 {
		t.Errorf("type_relations = %v, want 1", stats["type_relations"])
	}
}

func TestPredictor_PredictFiles_CoAccess(t *testing.T) {
	cp := NewContextPredictor("")

	fileA := tmpFile(t, "main.go")
	fileB := tmpFile(t, "helper.go")

	// Build strong co-access (count > 2 → "frequently accessed together" reason)
	for i := 0; i < 5; i++ {
		cp.RecordAccess(fileB, "read", fileA)
	}

	preds := cp.PredictFiles(fileA, 10)
	if len(preds) == 0 {
		t.Fatal("expected predictions, got none")
	}

	// fileB should be predicted
	found := false
	for _, p := range preds {
		if p.Path == fileB {
			found = true
			if p.Confidence <= 0 || p.Confidence > 1 {
				t.Errorf("Confidence = %f, want in (0,1]", p.Confidence)
			}
			if p.Reason != "frequently accessed together" {
				t.Errorf("Reason = %q, want %q", p.Reason, "frequently accessed together")
			}
		}
	}
	if !found {
		t.Errorf("fileB %q not in predictions: %+v", fileB, preds)
	}
}

func TestPredictor_PredictFiles_Limit(t *testing.T) {
	cp := NewContextPredictor("")

	dir := t.TempDir()
	current := filepath.Join(dir, "main.go")
	if err := os.WriteFile(current, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create 5 co-accessed files
	for i := 0; i < 5; i++ {
		other := filepath.Join(dir, "file"+string(rune('0'+i))+".go")
		if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		cp.RecordAccess(other, "read", current)
	}

	preds := cp.PredictFiles(current, 2)
	if len(preds) > 2 {
		t.Errorf("got %d predictions, want <= 2 (limit)", len(preds))
	}
}

func TestPredictor_PredictFiles_SameDirectory(t *testing.T) {
	cp := NewContextPredictor("")

	dir := t.TempDir()
	current := filepath.Join(dir, "main.go")
	neighbor := filepath.Join(dir, "neighbor.go")
	if err := os.WriteFile(current, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(neighbor, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Single access — not strong co-access (count=1, reason falls to "same directory")
	cp.RecordAccess(neighbor, "read", current)

	preds := cp.PredictFiles(current, 10)
	found := false
	for _, p := range preds {
		if p.Path == neighbor {
			found = true
		}
	}
	if !found {
		t.Errorf("neighbor %q not predicted via same-directory signal", neighbor)
	}
}

func TestPredictor_PredictFiles_NonExistentFiltered(t *testing.T) {
	cp := NewContextPredictor("")

	// Co-access to a file that doesn't exist on disk
	cp.RecordAccess("/nonexistent/file.go", "read", "/also/nonexistent.go")

	preds := cp.PredictFiles("/also/nonexistent.go", 10)
	// os.Stat fails → filtered out
	for _, p := range preds {
		if p.Path == "/nonexistent/file.go" {
			t.Errorf("non-existent file should be filtered, got %q", p.Path)
		}
	}
}

func TestPredictor_LearnImports_Go(t *testing.T) {
	dir := t.TempDir()
	// Create imported files so they resolve on disk
	fooPath := filepath.Join(dir, "foo.go")
	barPath := filepath.Join(dir, "bar.go")
	if err := os.WriteFile(fooPath, []byte("package foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(barPath, []byte("package bar"), 0o644); err != nil {
		t.Fatal(err)
	}

	mainPath := filepath.Join(dir, "main.go")
	// Note: parseGoImports returns the raw import PATH strings, not resolved files.
	mainContent := `package main

import (
	"fmt"
	"os"
)

import "strings"
`
	if err := os.WriteFile(mainPath, []byte(mainContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cp := NewContextPredictor(dir)
	cp.LearnImports(mainPath)

	stats := cp.GetStats()
	if stats["import_graph_files"].(int) != 1 {
		t.Errorf("import_graph_files = %v, want 1", stats["import_graph_files"])
	}
}

func TestPredictor_LearnImports_NonexistentFile(t *testing.T) {
	cp := NewContextPredictor("")
	// Should not panic / should silently return
	cp.LearnImports("/does/not/exist.go")

	stats := cp.GetStats()
	if stats["import_graph_files"].(int) != 0 {
		t.Errorf("import_graph_files = %v, want 0 for unreadable file", stats["import_graph_files"])
	}
}

func TestPredictor_Clear(t *testing.T) {
	cp := NewContextPredictor("")

	cp.RecordAccess("a.go", "read", "b.go")
	cp.RecordAccess("c.go", "read", "b.go")

	cp.Clear()

	stats := cp.GetStats()
	if stats["access_history_size"].(int) != 0 {
		t.Errorf("after Clear, access_history_size = %v, want 0", stats["access_history_size"])
	}
	if stats["co_access_entries"].(int) != 0 {
		t.Errorf("after Clear, co_access_entries = %v, want 0", stats["co_access_entries"])
	}
	if stats["type_relations"].(int) != 0 {
		t.Errorf("after Clear, type_relations = %v, want 0", stats["type_relations"])
	}
	if stats["import_graph_files"].(int) != 0 {
		t.Errorf("after Clear, import_graph_files = %v, want 0", stats["import_graph_files"])
	}
}

func TestCalculateStrength(t *testing.T) {
	// count=0 → base = 0.3 + 0.7*(1-1/1) = 0.3
	s := calculateStrength(0, time.Now())
	if s < 0.29 || s > 0.31 {
		t.Errorf("calculateStrength(0, now) = %f, want ~0.3", s)
	}

	// Higher count → higher base strength (but capped by decay)
	s2 := calculateStrength(10, time.Now())
	if s2 <= s {
		t.Errorf("calculateStrength(10, now) = %f should be > count=0 (%f)", s2, s)
	}

	// Old lastSeen → decayed
	old := calculateStrength(10, time.Now().AddDate(-1, 0, 0))
	if old >= s2 {
		t.Errorf("decayed strength %f should be < fresh %f", old, s2)
	}
}

func TestNormalizeScore(t *testing.T) {
	if normalizeScore(0) != 0 {
		t.Errorf("normalizeScore(0) = %f, want 0", normalizeScore(0))
	}
	if normalizeScore(-1) != 0 {
		t.Errorf("normalizeScore(-1) = %f, want 0", normalizeScore(-1))
	}
	// score/(score+1): 1 → 0.5
	if r := normalizeScore(1); r < 0.49 || r > 0.51 {
		t.Errorf("normalizeScore(1) = %f, want ~0.5", r)
	}
	// Large score → approaches 1
	if r := normalizeScore(1000); r > 1 {
		t.Errorf("normalizeScore(1000) = %f, want <= 1", r)
	}
}

func TestParseGoImports(t *testing.T) {
	content := `package main

import (
	"fmt"
	"os"
)

import "strings"
`
	imports := parseGoImports(content, "/dir")
	if len(imports) != 3 {
		t.Fatalf("got %d imports, want 3: %v", len(imports), imports)
	}
	// Should contain fmt, os, strings
	want := map[string]bool{"fmt": false, "os": false, "strings": false}
	for _, imp := range imports {
		if _, ok := want[imp]; ok {
			want[imp] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("import %q not found in %v", k, imports)
		}
	}
}

func TestParseGoImports_SingleLine(t *testing.T) {
	content := `package main
import "fmt"
`
	imports := parseGoImports(content, "/dir")
	if len(imports) != 1 || imports[0] != "fmt" {
		t.Errorf("parseGoImports single = %v, want [fmt]", imports)
	}
}

func TestParseJSImports_Relative(t *testing.T) {
	dir := t.TempDir()
	// Create the target file so it resolves
	target := filepath.Join(dir, "helper.ts")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	content := `import { foo } from "./helper";
import bar from "/abs/path";
`
	imports := parseJSImports(content, dir)
	// Should resolve ./helper → helper.ts
	found := false
	for _, imp := range imports {
		if imp == target {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q in imports %v", target, imports)
	}
}

func TestParseJSImports_NonRelative(t *testing.T) {
	content := `import React from "react";
import lodash from "lodash";
`
	imports := parseJSImports(content, "/dir")
	// Non-relative imports are not resolved to files
	if len(imports) != 0 {
		t.Errorf("non-relative imports should be skipped, got %v", imports)
	}
}

func TestParsePythonImports_Relative(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "utils.py")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	content := `from .utils import helper
import os
`
	imports := parsePythonImports(content, dir)
	found := false
	for _, imp := range imports {
		if imp == target {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q in python imports %v", target, imports)
	}
}

func TestParsePythonImports_Absolute(t *testing.T) {
	content := `import os
from sys import path
`
	imports := parsePythonImports(content, "/dir")
	// Absolute (non-relative) imports don't resolve to files
	if len(imports) != 0 {
		t.Errorf("absolute python imports should be skipped, got %v", imports)
	}
}

func TestGetPredictionReason_PatternMatch(t *testing.T) {
	cp := NewContextPredictor("")
	// Two files in different dirs, no co-access, no imports, same ext
	a := "/dir1/file.go"
	b := "/dir2/other.go"
	r := getPredictionReason(b, a, cp)
	// different dir, same ext (.go) → falls past "same directory" to "related file type" or "pattern match"
	if r == "" {
		t.Errorf("expected non-empty reason")
	}
}
