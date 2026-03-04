package evidence

// write.go — Evidence bundle serialization and validation.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// WriteEvidenceBundle marshals the bundle to YAML and writes it to the
// companion file `<bundle.File.Path>.evidence.yaml` (INV-14, INV-21).
// If force is false and an existing bundle has the same file.sha256, the file
// is not overwritten and skipped=true is returned (INV-50).
func WriteEvidenceBundle(bundle *EvidenceBundle, force bool) (skipped bool, err error) {
	outputPath := filepath.FromSlash(bundle.File.Path + ".evidence.yaml")
	if !force && bundleUpToDate(outputPath, bundle.File.SHA256) {
		return true, nil
	}
	data, err := yaml.Marshal(bundle)
	if err != nil {
		return false, fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", outputPath, err)
	}
	return false, nil
}

// bundleUpToDate returns true if the existing evidence bundle at outputPath
// was generated from a source file with the same SHA256 as newSHA256.
// Returns false if the file does not exist, cannot be read, or has a
// different hash (INV-50).
func bundleUpToDate(outputPath, newSHA256 string) bool {
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return false
	}
	var existing EvidenceBundle
	if err := yaml.Unmarshal(data, &existing); err != nil {
		return false
	}
	return existing.File.SHA256 == newSHA256
}

// validateEvidenceBundle re-hashes the source file and returns an error if
// the current hash differs from the stored hash (INV-2, INV-22).
// It does not modify any files.
func validateEvidenceBundle(bundle *EvidenceBundle) error {
	filePath := filepath.FromSlash(bundle.File.Path)
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	sum := sha256.Sum256(raw)
	current := hex.EncodeToString(sum[:])
	if current != bundle.File.SHA256 {
		return fmt.Errorf("evidence bundle is stale: file hash changed (stored %s, current %s)",
			bundle.File.SHA256, current)
	}
	return nil
}

// writeBundleAt marshals bundle to YAML and writes it to absFilePath+".evidence.yaml".
// The companion file is written using the absolute path so it lands next to the
// source regardless of the caller's working directory (INV-14).
// If force is false and the existing bundle has the same SHA256, writing is
// skipped and skipped=true is returned (INV-50).
func writeBundleAt(bundle *EvidenceBundle, absFilePath string, force bool) (skipped bool, err error) {
	outputPath := absFilePath + ".evidence.yaml"
	if !force && bundleUpToDate(outputPath, bundle.File.SHA256) {
		return true, nil
	}
	data, err := yaml.Marshal(bundle)
	if err != nil {
		return false, fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", outputPath, err)
	}
	return false, nil
}

// CleanEvidenceBundles removes all *.evidence.yaml files under root.
// Returns the number of files removed.
func CleanEvidenceBundles(root string) (int, error) {
	var removed int
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".evidence.yaml") {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove %s: %w", path, err)
			}
			removed++
		}
		return nil
	})
	return removed, err
}
