package obsidian

// obsidian_test.go — Smoke test for the obsidian wrapper.
// Full test coverage lives in internal/export/export_test.go.
// This file only verifies the wrapper delegates correctly (INV-44).

import (
	"os"
	"path/filepath"
	"testing"

	"iguana/internal/model"
)

// minimalModel returns a small but complete SystemModel for the smoke test.
func minimalModel() *model.SystemModel {
	return &model.SystemModel{
		Version:     1,
		GeneratedAt: "2024-01-01T00:00:00Z",
		Inputs:      model.ModelInputs{BundleSetSHA256: "abc123"},
		Inventory: model.Inventory{
			Packages: []model.PackageEntry{
				{Name: "main", Files: []string{"main.go"}, Imports: []string{"store"}},
				{Name: "store", Files: []string{"store/db.go"}},
			},
		},
		StateDomains: []model.StateDomain{
			{
				ID:          "evidence_store",
				Description: "Stores evidence bundles",
				Owners:      []string{"store"},
				Aggregate:   "EvidenceBundle",
				Confidence:  0.9,
			},
		},
		Effects: []model.Effect{
			{Kind: "fs_write", Via: "store/db.go", Domain: "evidence_store"},
		},
	}
}

// readFile is a test helper that reads a file and fails the test on error.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readFile %s: %v", path, err)
	}
	return string(data)
}

// TestGenerateObsidianVault_Idempotent verifies INV-44: calling
// GenerateObsidianVault twice on the same model produces byte-identical files.
func TestGenerateObsidianVault_Idempotent(t *testing.T) {
	dir := t.TempDir()
	m := minimalModel()

	if err := GenerateObsidianVault(m, dir); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Collect first-run file contents.
	firstRun := make(map[string]string)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			firstRun[path] = readFile(t, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk after first run: %v", err)
	}

	if err := GenerateObsidianVault(m, dir); err != nil {
		t.Fatalf("second run: %v", err)
	}

	// Compare second-run contents to first run.
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			second := readFile(t, path)
			first, ok := firstRun[path]
			if !ok {
				t.Errorf("new file appeared on second run: %s", path)
				return nil
			}
			if first != second {
				t.Errorf("file %s differs between runs:\nfirst:\n%s\nsecond:\n%s", path, first, second)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk after second run: %v", err)
	}
}
