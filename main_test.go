package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCleanEvidenceBundles verifies that cleanEvidenceBundles removes all
// *.evidence.yaml files and returns the correct count, leaving other files alone.
func TestCleanEvidenceBundles(t *testing.T) {
	dir := t.TempDir()

	// Create a mix of evidence files and unrelated files.
	evidence := []string{
		"main.go.evidence.yaml",
		"sub/store.go.evidence.yaml",
	}
	other := []string{
		"main.go",
		"system_model.yaml",
	}
	for _, name := range append(evidence, other...) {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := cleanEvidenceBundles(dir)
	if err != nil {
		t.Fatalf("cleanEvidenceBundles: %v", err)
	}
	if removed != len(evidence) {
		t.Errorf("removed %d files, want %d", removed, len(evidence))
	}

	// Evidence files must be gone.
	for _, name := range evidence {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed", name)
		}
	}
	// Other files must still exist.
	for _, name := range other {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s to still exist: %v", name, err)
		}
	}
}
