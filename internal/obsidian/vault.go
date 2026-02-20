package obsidian

// vault.go — Thin backward-compat wrapper around internal/export.
// All vault logic now lives in the knowledge_export domain (internal/export).
// See INVARIANT.md INV-42..46, INV-53..55.

import (
	"iguana/internal/export"
	"iguana/internal/model"
)

// GenerateObsidianVault writes a knowledge bundle rooted at outputDir from sys.
// Delegates entirely to export.GenerateKnowledgeBundle + export.WriteKnowledgeBundle.
func GenerateObsidianVault(sys *model.SystemModel, outputDir string) error {
	bundle, err := export.GenerateKnowledgeBundle(sys)
	if err != nil {
		return err
	}
	return export.WriteKnowledgeBundle(bundle, outputDir)
}
