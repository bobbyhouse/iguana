package model

// io.go — System model serialization: read, write, and up-to-date check.

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ReadSystemModel reads and unmarshals a system_model.yaml file.
func ReadSystemModel(path string) (*SystemModel, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var model SystemModel
	if err := yaml.Unmarshal(data, &model); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &model, nil
}

// SystemModelUpToDate returns true if the system model at outputPath was
// generated from the same set of evidence bundles currently in root (INV-51).
// Returns false (without error) if the file does not exist or cannot be read.
func SystemModelUpToDate(root, outputPath string) (bool, error) {
	bundles, err := loadEvidenceBundles(root)
	if err != nil {
		return false, fmt.Errorf("load bundles: %w", err)
	}
	if len(bundles) == 0 {
		return false, nil
	}
	existing, err := ReadSystemModel(outputPath)
	if err != nil {
		return false, nil // doesn't exist or unreadable — not up to date
	}
	return existing.Inputs.BundleSetSHA256 == computeBundleSetHash(bundles), nil
}

// WriteSystemModel marshals model to YAML and writes it to outputPath.
func WriteSystemModel(model *SystemModel, outputPath string) error {
	data, err := yaml.Marshal(model)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outputPath, err)
	}
	return nil
}
