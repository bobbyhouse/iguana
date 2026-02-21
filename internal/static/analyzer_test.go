package static_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"iguana/internal/static"
)

func TestGoAnalyzerName(t *testing.T) {
	a := &static.GoAnalyzer{}
	if a.Name() != "static" {
		t.Errorf("expected name 'static', got %q", a.Name())
	}
}

func TestGoAnalyzerConfigure(t *testing.T) {
	a := &static.GoAnalyzer{}
	qs, err := a.Configure()
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if len(qs) == 0 {
		t.Fatal("expected at least one ConfigQuestion")
	}
	if qs[0].Key != "repository" {
		t.Errorf("expected key 'repository', got %q", qs[0].Key)
	}
}

func TestGoAnalyzerAnalyzeMissingRepo(t *testing.T) {
	a := &static.GoAnalyzer{}
	err := a.Analyze(map[string]string{}, t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing repository config")
	}
}

// TestAnalyzeDir exercises the low-level directory analysis without network.
func TestAnalyzeDir(t *testing.T) {
	// Create a minimal Go source tree.
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(`package main

func Hello() string { return "hello" }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	if err := static.AnalyzeDir(src, "https://github.com/test/repo", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", outDir); err != nil {
		t.Fatalf("AnalyzeDir: %v", err)
	}

	bundlePath := filepath.Join(outDir, "main.go.md")
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("bundle not found at %s: %v", bundlePath, err)
	}
	if len(data) == 0 {
		t.Fatal("empty bundle")
	}
	if !containsStr(data, "plugin: static") {
		t.Errorf("bundle missing 'plugin: static':\n%s", data)
	}
	if !containsStr(data, "Hello") {
		t.Errorf("bundle missing function 'Hello':\n%s", data)
	}
}

func containsStr(data []byte, sub string) bool {
	s := string(data)
	return len(s) >= len(sub) && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}

func runGitCmd(args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %w\n%s", args, err, out)
	}
	return nil
}

// ensure runGitCmd is used by at least one test if needed.
var _ = runGitCmd
