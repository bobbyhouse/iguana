package main

// main_test.go — Deterministic tests for categorizeFile (ig-r74m).
//
// All tests here are deterministic — no LLM calls are made.
// The typeOfState package variable is replaced with a mock that returns a
// fixed response, satisfying INV-53 and INV-56.
//
// Invariants tested:
//   INV-53  categorizeFile delegates to typeOfState (mock injection)
//   INV-54  file read errors propagate as errors
//   INV-55  successful classification returns a valid State
//   INV-56  no subtest calls the real LLM

import (
	"context"
	"errors"
	"os"
	"testing"

	"iguana/baml_client/types"
)

// TestCategorizeFile verifies that categorizeFile correctly classifies file
// content by forwarding it to the classifier and returning its result.
//
// Each subtest injects a deterministic mock for typeOfState so the LLM is
// never called (INV-56). The mock returns the exact state listed in the test
// table, which categorizeFile must pass through unchanged (INV-53, INV-55).
func TestCategorizeFile(t *testing.T) {
	tests := []struct {
		name          string
		content       string      // Go source written to a temp file
		classifyState types.State // state the mock returns
		wantState     types.State // state categorizeFile must return
	}{
		{
			name:          "system configuration file",
			content:       "package commands\nfunc init() { registry.Register(\"catalog\", CatalogCmd) }",
			classifyState: types.StateSYSTEM_STATE,
			wantState:     types.StateSYSTEM_STATE,
		},
		{
			name:          "client-side code or configuration direct",
			content:       "package client\nfunc Parse(r io.Reader) (*Config, error) { return nil, nil }",
			classifyState: types.StateCLIENT_STATE,
			wantState:     types.StateCLIENT_STATE,
		},
		{
			name:          "client-side code or configuration ambiguous",
			content:       "package client\nfunc connect(addr string) error { return dial(addr) }",
			classifyState: types.StateCLIENT_STATE,
			wantState:     types.StateCLIENT_STATE,
		},
		{
			name:          "runtime state or process information",
			content:       "package gateway\nvar sessionCache sync.Map",
			classifyState: types.StateRUNTIME_STATE,
			wantState:     types.StateRUNTIME_STATE,
		},
		{
			// This test was flaky when it called the real LLM: the model
			// sometimes returned CLIENT_STATE or SYSTEM_STATE for ambiguous
			// content instead of UNKNOWN_STATE.  The mock makes it deterministic
			// (INV-56).
			name:          "ambiguous or unclassifiable content",
			content:       "package embeddings\nfunc encode(data []byte) []byte { return data }",
			classifyState: types.StateUNKNOWN_STATE,
			wantState:     types.StateUNKNOWN_STATE,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write test content to a temp file so categorizeFile can read it.
			f, err := os.CreateTemp(t.TempDir(), "*.go")
			if err != nil {
				t.Fatalf("create temp file: %v", err)
			}
			if _, err := f.WriteString(tt.content); err != nil {
				t.Fatalf("write temp file: %v", err)
			}
			f.Close()

			// Inject deterministic mock — no LLM call (INV-56).
			orig := typeOfState
			t.Cleanup(func() { typeOfState = orig })
			typeOfState = func(_ context.Context, _ string) (types.State, error) {
				return tt.classifyState, nil
			}

			got, err := categorizeFile(f.Name())
			if err != nil {
				t.Fatalf("categorizeFile() error: %v", err)
			}

			// INV-55: result must be a valid State enum value.
			if !got.IsValid() {
				t.Errorf("categorizeFile() returned invalid state: %v", got)
			}

			// INV-53: categorizeFile must pass through the classifier's result.
			if got != tt.wantState {
				t.Errorf("categorizeFile() = %v, want %v", got, tt.wantState)
			}
		})
	}
}

// TestCategorizeFile_FileNotFound verifies that categorizeFile propagates the
// OS error when the file does not exist (INV-54).
func TestCategorizeFile_FileNotFound(t *testing.T) {
	_, err := categorizeFile("/nonexistent/path/file.go")
	if err == nil {
		t.Error("expected error for non-existent file, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got: %v", err)
	}
}
