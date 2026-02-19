package main

// obsidian_test.go — Tests for the Obsidian vault generator.
//
// All tests build *SystemModel directly (no file I/O for model construction),
// call GenerateObsidianVault, then assert on file contents.
//
// Invariants tested:
//   INV-42: subdirectory structure always created
//   INV-43: wiki links use [[path|display]] with no .md extension
//   INV-44: idempotent — byte-identical output on second run
//   INV-45: filename sanitization (/ and . → -, collapse runs, trim)

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// minimalModel returns a small but complete SystemModel for use in tests.
func minimalModel() *SystemModel {
	return &SystemModel{
		Version:     1,
		GeneratedAt: "2024-01-01T00:00:00Z",
		Inputs:      ModelInputs{BundleSetSHA256: "abc123"},
		Inventory: Inventory{
			Packages: []PackageEntry{
				// main imports store — creates a directed import graph edge.
				{Name: "main", Files: []string{"main.go"}, Imports: []string{"store"}},
				{Name: "store", Files: []string{"store/db.go", "store/query.go"}},
			},
		},
		StateDomains: []StateDomain{
			{
				ID:              "evidence_store",
				Description:     "Stores evidence bundles",
				Owners:          []string{"store"},
				Aggregate:       "EvidenceBundle",
				Representations: []string{"EvidenceRecord"},
				PrimaryMutators: []string{"SaveBundle"},
				PrimaryReaders:  []string{"LoadBundle"},
				Confidence:      0.9,
			},
		},
		TrustZones: []TrustZone{
			{
				ID:          "internal",
				Packages:    []string{"main", "store"},
				ExternalVia: []string{"cli_args"},
			},
		},
		ConcurrencyDomains: []ConcurrencyDomain{
			{
				ID:    "store/db.go",
				Files: []string{"store/db.go"},
			},
		},
		Effects: []Effect{
			{Kind: "fs_read", Via: "main.go", Domain: "evidence_store"},
			{Kind: "fs_write", Via: "store/db.go", Domain: "evidence_store"},
		},
		OpenQuestions: []OpenQuestion{
			{
				Question:        "Is the store thread-safe?",
				RelatedDomain:   "evidence_store",
				MissingEvidence: []string{"mutex usage", "goroutine analysis"},
			},
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

// ---------------------------------------------------------------------------
// INV-45: sanitizeFilename
// ---------------------------------------------------------------------------

// TestSanitizeFilename verifies INV-45: / and . are replaced with -, consecutive
// dashes are collapsed, and leading/trailing dashes are trimmed.
func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Slashes become dashes.
		{"store/db.go", "store-db-go"},
		// Dots become dashes.
		{"foo.bar", "foo-bar"},
		// Consecutive separators collapse.
		{"a//b", "a-b"},
		{"a..b", "a-b"},
		// Mixed slash and dot.
		{"path/to/file.go", "path-to-file-go"},
		// No transformation needed.
		{"simple", "simple"},
		// Leading/trailing trimmed.
		{"/leading", "leading"},
		{"trailing/", "trailing"},
		// Already clean.
		{"foo-bar", "foo-bar"},
	}

	for _, tc := range tests {
		got := sanitizeFilename(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// INV-42: vault directory structure
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_DirectoryStructure verifies INV-42: the four
// required subdirectories are always created, even with a minimal model.
func TestGenerateObsidianVault_DirectoryStructure(t *testing.T) {
	dir := t.TempDir()
	model := minimalModel()

	if err := GenerateObsidianVault(model, dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	requiredDirs := []string{
		"packages",
		"state-domains",
		"trust-zones",
		"concurrency-domains",
	}
	for _, sub := range requiredDirs {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory, got file", sub)
		}
	}
}

// ---------------------------------------------------------------------------
// INV-43: wiki link format in index
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_IndexContainsLinks verifies INV-43: index.md uses
// [[path|display]] wiki links with no .md extension in the path component.
func TestGenerateObsidianVault_IndexContainsLinks(t *testing.T) {
	dir := t.TempDir()
	model := minimalModel()

	if err := GenerateObsidianVault(model, dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	content := readFile(t, filepath.Join(dir, "index.md"))

	// Package link: [[packages/main|main]]
	if !strings.Contains(content, "[[packages/main|main]]") {
		t.Errorf("index.md missing package wiki link for 'main';\ngot:\n%s", content)
	}
	// State domain link: [[state-domains/evidence_store|evidence_store]]
	if !strings.Contains(content, "[[state-domains/evidence_store|evidence_store]]") {
		t.Errorf("index.md missing state-domain wiki link;\ngot:\n%s", content)
	}
	// Trust zone link.
	if !strings.Contains(content, "[[trust-zones/internal|internal]]") {
		t.Errorf("index.md missing trust-zone wiki link;\ngot:\n%s", content)
	}
	// No .md extension anywhere in wiki link paths (INV-43).
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if idx := strings.Index(line, "[["); idx >= 0 {
			// Extract path portion up to | or ]].
			inner := line[idx+2:]
			end := strings.IndexAny(inner, "|]")
			if end >= 0 {
				path := inner[:end]
				if strings.HasSuffix(path, ".md") {
					t.Errorf("wiki link path must not end with .md: %q in line %q", path, line)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Package note back-links
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_PackageNoteLinks verifies that a package note
// contains wiki links back to the state domains that own it.
func TestGenerateObsidianVault_PackageNoteLinks(t *testing.T) {
	dir := t.TempDir()
	model := minimalModel()

	if err := GenerateObsidianVault(model, dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	// 'store' package is owned by 'evidence_store' domain.
	content := readFile(t, filepath.Join(dir, "packages", "store.md"))

	if !strings.Contains(content, "[[state-domains/evidence_store|evidence_store]]") {
		t.Errorf("packages/store.md missing back-link to evidence_store;\ngot:\n%s", content)
	}
	// Trust zone back-link.
	if !strings.Contains(content, "[[trust-zones/internal|internal]]") {
		t.Errorf("packages/store.md missing back-link to internal trust zone;\ngot:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// State domain note
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_StateDomainNote verifies the state domain note
// contains owner wiki links and confidence.
func TestGenerateObsidianVault_StateDomainNote(t *testing.T) {
	dir := t.TempDir()
	model := minimalModel()

	if err := GenerateObsidianVault(model, dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	content := readFile(t, filepath.Join(dir, "state-domains", "evidence_store.md"))

	// Owner back-link.
	if !strings.Contains(content, "[[packages/store|store]]") {
		t.Errorf("state-domains/evidence_store.md missing owner link;\ngot:\n%s", content)
	}
	// Confidence present.
	if !strings.Contains(content, "**Confidence**: 0.90") {
		t.Errorf("state-domains/evidence_store.md missing confidence;\ngot:\n%s", content)
	}
	// Description present.
	if !strings.Contains(content, "Stores evidence bundles") {
		t.Errorf("state-domains/evidence_store.md missing description;\ngot:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// Effects note
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_Frontmatter verifies that each note type carries a
// type-specific tag in its YAML frontmatter so Obsidian graph Groups can color
// nodes differently.
func TestGenerateObsidianVault_Frontmatter(t *testing.T) {
	dir := t.TempDir()
	model := minimalModel()

	if err := GenerateObsidianVault(model, dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	cases := []struct {
		file string
		tag  string
	}{
		{"index.md", "iguana/index"},
		{"packages/main.md", "iguana/package"},
		{"state-domains/evidence_store.md", "iguana/state-domain"},
		{"trust-zones/internal.md", "iguana/trust-zone"},
		{"concurrency-domains/store-db-go.md", "iguana/concurrency-domain"},
		{"effects.md", "iguana/effects"},
		{"open-questions.md", "iguana/open-questions"},
	}

	for _, tc := range cases {
		content := readFile(t, filepath.Join(dir, tc.file))
		if !strings.HasPrefix(content, "---\n") {
			t.Errorf("%s: missing YAML frontmatter (no leading ---)", tc.file)
		}
		if !strings.Contains(content, "  - "+tc.tag) {
			t.Errorf("%s: missing tag %q in frontmatter;\ngot:\n%s", tc.file, tc.tag, content)
		}
	}
}

// TestGenerateObsidianVault_EffectsNote verifies the effects table is present
// and contains wiki links to state domains.
func TestGenerateObsidianVault_EffectsNote(t *testing.T) {
	dir := t.TempDir()
	model := minimalModel()

	if err := GenerateObsidianVault(model, dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	content := readFile(t, filepath.Join(dir, "effects.md"))

	// Table header.
	if !strings.Contains(content, "| Kind | Via | Domain |") {
		t.Errorf("effects.md missing table header;\ngot:\n%s", content)
	}
	// Effect row for fs_read.
	if !strings.Contains(content, "fs_read") {
		t.Errorf("effects.md missing fs_read effect;\ngot:\n%s", content)
	}
	// Domain wiki link in effects table.
	if !strings.Contains(content, "[[state-domains/evidence_store|evidence_store]]") {
		t.Errorf("effects.md missing domain wiki link;\ngot:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// INV-44: idempotency
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_Idempotent verifies INV-44: calling
// GenerateObsidianVault twice on the same model produces byte-identical files.
func TestGenerateObsidianVault_Idempotent(t *testing.T) {
	dir := t.TempDir()
	model := minimalModel()

	if err := GenerateObsidianVault(model, dir); err != nil {
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

	if err := GenerateObsidianVault(model, dir); err != nil {
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

// ---------------------------------------------------------------------------
// Import graph edges
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_ImportGraph verifies that package notes contain
// directed import edges: main → store (Imports) and store ← main (Imported By).
func TestGenerateObsidianVault_ImportGraph(t *testing.T) {
	dir := t.TempDir()
	model := minimalModel() // main.Imports = ["store"]

	if err := GenerateObsidianVault(model, dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	mainNote := readFile(t, filepath.Join(dir, "packages", "main.md"))
	storeNote := readFile(t, filepath.Join(dir, "packages", "store.md"))

	// main's Imports section should link to store.
	if !strings.Contains(mainNote, "[[packages/store|store]]") {
		t.Errorf("packages/main.md Imports section missing link to store;\ngot:\n%s", mainNote)
	}
	// store's Imported By section should link back to main.
	if !strings.Contains(storeNote, "[[packages/main|main]]") {
		t.Errorf("packages/store.md Imported By section missing link to main;\ngot:\n%s", storeNote)
	}
	// store has no imports in the model, so its Imports section should say _none_.
	if !strings.Contains(storeNote, "_none_") {
		t.Errorf("packages/store.md Imports section should be _none_;\ngot:\n%s", storeNote)
	}
}

// ---------------------------------------------------------------------------
// Domain writers and readers
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_DomainWritersReaders verifies that state domain
// notes show which packages write to and read from the domain, derived from
// effects — not just LLM ownership.
//
// Model:
//   fs_read  via main.go     → evidence_store  (main reads)
//   fs_write via store/db.go → evidence_store  (store writes)
func TestGenerateObsidianVault_DomainWritersReaders(t *testing.T) {
	dir := t.TempDir()
	model := minimalModel()

	if err := GenerateObsidianVault(model, dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	content := readFile(t, filepath.Join(dir, "state-domains", "evidence_store.md"))

	// Writers section: store produces fs_write to this domain.
	if !strings.Contains(content, "## Writers") {
		t.Errorf("evidence_store.md missing ## Writers section;\ngot:\n%s", content)
	}
	if !strings.Contains(content, "[[packages/store|store]]") {
		t.Errorf("evidence_store.md Writers missing store link;\ngot:\n%s", content)
	}

	// Readers section: main produces fs_read to this domain.
	if !strings.Contains(content, "## Readers") {
		t.Errorf("evidence_store.md missing ## Readers section;\ngot:\n%s", content)
	}
	if !strings.Contains(content, "[[packages/main|main]]") {
		t.Errorf("evidence_store.md Readers missing main link;\ngot:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// Concurrency × domain intersection
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_ConcurrencyDomainIntersection verifies that:
//   - concurrency domain notes link to state domains they touch
//   - state domain notes flag concurrent access with a warning section
//
// Model:
//   ConcurrencyDomain {ID: "store/db.go", Files: ["store/db.go"]}
//   Effect {Kind: "fs_write", Via: "store/db.go", Domain: "evidence_store"}
//   → store/db.go is both concurrent and writes to evidence_store
func TestGenerateObsidianVault_ConcurrencyDomainIntersection(t *testing.T) {
	dir := t.TempDir()
	model := minimalModel()

	if err := GenerateObsidianVault(model, dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	// Concurrency domain note should link to the state domain it touches.
	cdNote := readFile(t, filepath.Join(dir, "concurrency-domains", "store-db-go.md"))
	if !strings.Contains(cdNote, "## Touches State Domains") {
		t.Errorf("concurrency-domains/store-db-go.md missing ## Touches State Domains;\ngot:\n%s", cdNote)
	}
	if !strings.Contains(cdNote, "[[state-domains/evidence_store|evidence_store]]") {
		t.Errorf("concurrency-domains/store-db-go.md missing link to evidence_store;\ngot:\n%s", cdNote)
	}

	// State domain note should flag concurrent access.
	domNote := readFile(t, filepath.Join(dir, "state-domains", "evidence_store.md"))
	if !strings.Contains(domNote, "Concurrent Access") {
		t.Errorf("evidence_store.md missing Concurrent Access section;\ngot:\n%s", domNote)
	}
	if !strings.Contains(domNote, "store-db-go") {
		t.Errorf("evidence_store.md Concurrent Access missing store/db.go link;\ngot:\n%s", domNote)
	}
}

// ---------------------------------------------------------------------------
// Package effects table
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_PackageEffectsTable verifies that a package note
// contains a table of effects it produces with kind, file, and domain links.
func TestGenerateObsidianVault_PackageEffectsTable(t *testing.T) {
	dir := t.TempDir()
	model := minimalModel()

	if err := GenerateObsidianVault(model, dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	// store produces fs_write to evidence_store via store/db.go.
	storeNote := readFile(t, filepath.Join(dir, "packages", "store.md"))
	if !strings.Contains(storeNote, "## Effects") {
		t.Errorf("packages/store.md missing ## Effects section;\ngot:\n%s", storeNote)
	}
	if !strings.Contains(storeNote, "fs_write") {
		t.Errorf("packages/store.md Effects missing fs_write row;\ngot:\n%s", storeNote)
	}
	if !strings.Contains(storeNote, "[[state-domains/evidence_store|evidence_store]]") {
		t.Errorf("packages/store.md Effects missing domain link;\ngot:\n%s", storeNote)
	}
}

// ---------------------------------------------------------------------------
// Empty model
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_EmptyModel verifies graceful handling of a model
// with no domains, effects, trust zones, or open questions.
func TestGenerateObsidianVault_EmptyModel(t *testing.T) {
	dir := t.TempDir()
	model := &SystemModel{
		Version:     1,
		GeneratedAt: "2024-01-01T00:00:00Z",
		Inputs:      ModelInputs{BundleSetSHA256: "empty"},
	}

	if err := GenerateObsidianVault(model, dir); err != nil {
		t.Fatalf("GenerateObsidianVault on empty model: %v", err)
	}

	// index.md must still be created.
	if _, err := os.Stat(filepath.Join(dir, "index.md")); err != nil {
		t.Errorf("index.md not created for empty model: %v", err)
	}

	// effects.md must still be created (with header but no rows).
	content := readFile(t, filepath.Join(dir, "effects.md"))
	if !strings.Contains(content, "# Effects") {
		t.Errorf("effects.md missing header;\ngot:\n%s", content)
	}
}
