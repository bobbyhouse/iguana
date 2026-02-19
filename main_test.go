package main

import (
	"iguana/baml_client/types"
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

// TestCategorizeFile tests the classification of file content into different state types.
//
// Test Invariants:
// - Each file path must be readable
// - categorizeFile must return one of the four valid states: SYSTEM_STATE, CLIENT_STATE, RUNTIME_STATE, or UNKNOWN_STATE
// - The same file content should always return the same state (deterministic behavior)
// - Empty files should not cause panics or errors (should return a valid state)
// - Nonexistent files should return an error
func TestCategorizeFile(t *testing.T) {
	tests := []struct {
		name         string      // Test case description
		filePath     string      // Path to the test fixture file
		expectedType types.State // Expected state classification
	}{
		{
			name:     "system configuration file",
			filePath: "/Users/bobby/git/gateway/mcp-gateway/cmd/docker-mcp/commands/catalog_next.go",

			expectedType: types.StateSYSTEM_STATE,
		},
		{
			name:         "client-side code or configuration direct",
			filePath:     "/Users/bobby/git/gateway/mcp-gateway/pkg/client/parse.go",
			expectedType: types.StateCLIENT_STATE,
		},

		{
			name:         "client-side code or configuration ambigous",
			filePath:     "/Users/bobby/git/gateway/mcp-gateway/cmd/docker-mcp/client/connect.go",
			expectedType: types.StateCLIENT_STATE,
		},
		{
			name:         "runtime state or process information",
			filePath:     "/Users/bobby/git/gateway/mcp-gateway/pkg/gateway/mcpadd.go",
			expectedType: types.StateRUNTIME_STATE,
		},
		{
			name:         "ambiguous or unclassifiable content",
			filePath:     "/Users/bobby/git/gateway/mcp-gateway/pkg/gateway/embeddings/oci.go",
			expectedType: types.StateUNKNOWN_STATE,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act: Categorize the file
			actualState, err := categorizeFile(tt.filePath)

			// Assert: Check for errors
			if err != nil {
				t.Fatalf("categorizeFile() returned error: %v", err)
			}

			// Verify the state is one of the valid enum values
			if !actualState.IsValid() {
				t.Errorf("categorizeFile() returned invalid state: %v", actualState)
			}

			// Check if the classification matches expectation
			if actualState != tt.expectedType {
				t.Errorf("categorizeFile() = %v, want %v", actualState, tt.expectedType)
			}
		})
	}
}
