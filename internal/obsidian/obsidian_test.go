package obsidian

// obsidian_test.go — Tests for the Obsidian vault generator.
//
// All tests build *model.SystemModel directly (no file I/O for model construction),
// call GenerateObsidianVault, then assert on file contents.
//
// Invariants tested:
//   INV-42: subdirectory structure (state-domains/, symbols/)
//   INV-43: wiki links use [[path|display]] with no .md extension
//   INV-44: idempotent — byte-identical output on second run
//   INV-45: filename sanitization (/ and . → -, collapse runs, trim)
//   INV-47: symbol and domain note correspondence
//   INV-48: tag requirements per note type

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"iguana/internal/model"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// minimalModel returns a small but complete SystemModel for use in tests.
func minimalModel() *model.SystemModel {
	return &model.SystemModel{
		Version:     1,
		GeneratedAt: "2024-01-01T00:00:00Z",
		Inputs:      model.ModelInputs{BundleSetSHA256: "abc123"},
		Inventory: model.Inventory{
			Packages: []model.PackageEntry{
				// main imports store — not rendered in vault but present in model.
				{Name: "main", Files: []string{"main.go"}, Imports: []string{"store"}},
				{Name: "store", Files: []string{"store/db.go", "store/query.go"}},
			},
		},
		StateDomains: []model.StateDomain{
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
		TrustZones: []model.TrustZone{
			{
				ID:          "internal",
				Packages:    []string{"main", "store"},
				ExternalVia: []string{"cli_args"},
			},
		},
		ConcurrencyDomains: []model.ConcurrencyDomain{
			{
				ID:    "store/db.go",
				Files: []string{"store/db.go"},
			},
		},
		Effects: []model.Effect{
			{Kind: "fs_read", Via: "main.go", Domain: "evidence_store"},
			{Kind: "fs_write", Via: "store/db.go", Domain: "evidence_store"},
		},
		OpenQuestions: []model.OpenQuestion{
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

// TestGenerateObsidianVault_DirectoryStructure verifies INV-42: state-domains/ and
// symbols/ are always created; old directories (packages/, trust-zones/, etc.) are not.
func TestGenerateObsidianVault_DirectoryStructure(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateObsidianVault(minimalModel(), dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	for _, sub := range []string{"state-domains", "symbols"} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory, got file", sub)
		}
	}

	// Old directories must not be created.
	for _, absent := range []string{"packages", "trust-zones", "concurrency-domains"} {
		if _, err := os.Stat(filepath.Join(dir, absent)); err == nil {
			t.Errorf("unexpected directory %s should not exist", absent)
		}
	}
}

// ---------------------------------------------------------------------------
// INV-43: wiki link format in index
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_IndexContainsLinks verifies index.md lists only
// state domains using [[path|display]] wiki links with no .md extension (INV-43).
func TestGenerateObsidianVault_IndexContainsLinks(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateObsidianVault(minimalModel(), dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	content := readFile(t, filepath.Join(dir, "index.md"))

	// State domain link present.
	if !strings.Contains(content, "[[state-domains/evidence_store|evidence_store]]") {
		t.Errorf("index.md missing state-domain wiki link;\ngot:\n%s", content)
	}

	// No package or trust-zone links in index.
	if strings.Contains(content, "[[packages/") {
		t.Errorf("index.md should not contain package links;\ngot:\n%s", content)
	}
	if strings.Contains(content, "[[trust-zones/") {
		t.Errorf("index.md should not contain trust-zone links;\ngot:\n%s", content)
	}

	// No .md extension in any wiki link path (INV-43).
	for _, line := range strings.Split(content, "\n") {
		if idx := strings.Index(line, "[["); idx >= 0 {
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
// State domain note
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_StateDomainNote verifies wiki links to symbols,
// confidence tag, description, and effects table.
func TestGenerateObsidianVault_StateDomainNote(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateObsidianVault(minimalModel(), dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	content := readFile(t, filepath.Join(dir, "state-domains", "evidence_store.md"))

	// Aggregate wiki link (not inline code).
	if !strings.Contains(content, "[[symbols/EvidenceBundle|EvidenceBundle]]") {
		t.Errorf("missing aggregate wiki link;\ngot:\n%s", content)
	}
	// Representation wiki link.
	if !strings.Contains(content, "[[symbols/EvidenceRecord|EvidenceRecord]]") {
		t.Errorf("missing representation wiki link;\ngot:\n%s", content)
	}
	// Mutator wiki link.
	if !strings.Contains(content, "[[symbols/SaveBundle|SaveBundle]]") {
		t.Errorf("missing mutator wiki link;\ngot:\n%s", content)
	}
	// Reader wiki link.
	if !strings.Contains(content, "[[symbols/LoadBundle|LoadBundle]]") {
		t.Errorf("missing reader wiki link;\ngot:\n%s", content)
	}
	// Confidence present.
	if !strings.Contains(content, "**Confidence**: 0.90") {
		t.Errorf("missing confidence;\ngot:\n%s", content)
	}
	// Description present.
	if !strings.Contains(content, "Stores evidence bundles") {
		t.Errorf("missing description;\ngot:\n%s", content)
	}
	// Effects table.
	if !strings.Contains(content, "## Effects") {
		t.Errorf("missing ## Effects section;\ngot:\n%s", content)
	}
	if !strings.Contains(content, "fs_read") {
		t.Errorf("missing fs_read effect;\ngot:\n%s", content)
	}
	// confidence-high tag from frontmatter.
	if !strings.Contains(content, "  - confidence-high") {
		t.Errorf("missing confidence-high tag;\ngot:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// Frontmatter (INV-48)
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_Frontmatter verifies that each note type carries
// appropriate YAML frontmatter tags for Obsidian graph coloring (INV-48).
func TestGenerateObsidianVault_Frontmatter(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateObsidianVault(minimalModel(), dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	cases := []struct {
		file string
		tag  string
	}{
		{"index.md", "iguana/index"},
		{"state-domains/evidence_store.md", "state-domain"},
		{"state-domains/evidence_store.md", "confidence-high"},
		{"symbols/EvidenceBundle.md", "symbol"},
		{"symbols/EvidenceBundle.md", "domain/evidence_store"},
		{"symbols/EvidenceBundle.md", "role/aggregate"},
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

// ---------------------------------------------------------------------------
// INV-44: idempotency
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// INV-47: symbol note creation
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_SymbolNotes_Created verifies INV-47: a symbols/ file
// is created for each unique aggregate, representation, mutator, and reader.
func TestGenerateObsidianVault_SymbolNotes_Created(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateObsidianVault(minimalModel(), dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	for _, name := range []string{"EvidenceBundle", "EvidenceRecord", "SaveBundle", "LoadBundle"} {
		path := filepath.Join(dir, "symbols", name+".md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("symbol note %s not created: %v", path, err)
		}
	}
}

// TestGenerateObsidianVault_SymbolNote_BackLink verifies that a symbol note
// contains a back-link to its owning state domain (INV-47).
func TestGenerateObsidianVault_SymbolNote_BackLink(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateObsidianVault(minimalModel(), dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	// EvidenceBundle is the aggregate of evidence_store.
	content := readFile(t, filepath.Join(dir, "symbols", "EvidenceBundle.md"))
	if !strings.Contains(content, "[[state-domains/evidence_store|evidence_store]]") {
		t.Errorf("EvidenceBundle.md missing back-link to evidence_store;\ngot:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// INV-48: symbol note tags
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_SymbolNote_Tags verifies INV-48: symbol notes carry
// role/ and domain/ tags in their frontmatter.
func TestGenerateObsidianVault_SymbolNote_Tags(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateObsidianVault(minimalModel(), dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	content := readFile(t, filepath.Join(dir, "symbols", "EvidenceBundle.md"))
	for _, want := range []string{"  - symbol", "  - domain/evidence_store", "  - role/aggregate"} {
		if !strings.Contains(content, want) {
			t.Errorf("EvidenceBundle.md missing tag %q;\ngot:\n%s", want, content)
		}
	}
}

// ---------------------------------------------------------------------------
// Confidence tag mapping (INV-54)
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_StateDomainNote_ConfidenceTag verifies that the
// confidenceTag helper maps scores to the correct tag strings.
func TestGenerateObsidianVault_StateDomainNote_ConfidenceTag(t *testing.T) {
	cases := []struct {
		confidence float64
		wantTag    string
	}{
		{0.9, "confidence-high"},
		{0.8, "confidence-high"},
		{0.75, "confidence-medium"},
		{0.7, "confidence-medium"},
		{0.6, "confidence-low"},
		{0.0, "confidence-low"},
	}

	for _, tc := range cases {
		got := confidenceTag(tc.confidence)
		if got != tc.wantTag {
			t.Errorf("confidenceTag(%.2f) = %q, want %q", tc.confidence, got, tc.wantTag)
		}
	}
}

// ---------------------------------------------------------------------------
// INV-43: wiki link format in state domain notes
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_StateDomainNote_WikiLinks verifies INV-43 within
// state domain notes: all wiki links use [[path|display]] with no .md extension.
func TestGenerateObsidianVault_StateDomainNote_WikiLinks(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateObsidianVault(minimalModel(), dir); err != nil {
		t.Fatalf("GenerateObsidianVault: %v", err)
	}

	content := readFile(t, filepath.Join(dir, "state-domains", "evidence_store.md"))

	// All wiki links in the note must have no .md extension in the path (INV-43).
	for _, line := range strings.Split(content, "\n") {
		if idx := strings.Index(line, "[["); idx >= 0 {
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
// Empty model
// ---------------------------------------------------------------------------

// TestGenerateObsidianVault_EmptyModel verifies graceful handling of a model
// with no domains, effects, or open questions.
func TestGenerateObsidianVault_EmptyModel(t *testing.T) {
	dir := t.TempDir()
	m := &model.SystemModel{
		Version:     1,
		GeneratedAt: "2024-01-01T00:00:00Z",
		Inputs:      model.ModelInputs{BundleSetSHA256: "empty"},
	}

	if err := GenerateObsidianVault(m, dir); err != nil {
		t.Fatalf("GenerateObsidianVault on empty model: %v", err)
	}

	// index.md must be created.
	if _, err := os.Stat(filepath.Join(dir, "index.md")); err != nil {
		t.Errorf("index.md not created for empty model: %v", err)
	}

	// effects.md must NOT be created (feature removed).
	if _, err := os.Stat(filepath.Join(dir, "effects.md")); err == nil {
		t.Errorf("effects.md should not be created")
	}

	// state-domains/ and symbols/ must exist even when empty.
	for _, sub := range []string{"state-domains", "symbols"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); err != nil {
			t.Errorf("expected directory %s to exist: %v", sub, err)
		}
	}
}
