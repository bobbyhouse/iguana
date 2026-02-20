package export

// export_test.go — Tests for the knowledge_export domain.
//
// All tests build *model.SystemModel directly (no file I/O for model construction),
// call GenerateKnowledgeBundle + WriteKnowledgeBundle, then assert on file contents.
//
// Invariants tested:
//   INV-42: domains/ and graphs/ created; state-domains/ and symbols/ absent
//   INV-43: wiki links use [[path|display]] with no .md extension
//   INV-44: idempotent — byte-identical output on second run
//   INV-45: filename sanitization (/ and . → -, collapse runs, trim)
//   INV-53: one domains/<id>.md per domain; no symbols/
//   INV-54: tag requirements per note type
//   INV-55: DomainPage ## Evidence section when EvidenceRefs non-empty

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

// minimalModel returns a small but complete SystemModel with effects,
// open questions, boundaries, and evidence refs.
func minimalModel() *model.SystemModel {
	return &model.SystemModel{
		Version:     1,
		GeneratedAt: "2024-01-01T00:00:00Z",
		Inputs:      model.ModelInputs{BundleSetSHA256: "abc123"},
		Inventory: model.Inventory{
			Packages: []model.PackageEntry{
				// main imports store — edges appear in dependency graph.
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
				EvidenceRefs:    []string{"bundle:store/db.go#signal:db_calls", "bundle:store/query.go"},
			},
		},
		Boundaries: model.Boundaries{
			Persistence: []model.PersistenceBoundary{
				{
					Kind:    "fs",
					Writers: []model.SymbolRef{{File: "store/db.go"}},
				},
			},
			Network: &model.NetworkBoundary{
				Outbound: []model.SymbolRef{{File: "api/client.go"}},
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

// multiDomainModel returns a SystemModel with multiple domains, packages with
// imports (for in-degree), write effects, and a general open question.
func multiDomainModel() *model.SystemModel {
	return &model.SystemModel{
		Version:     1,
		GeneratedAt: "2024-01-01T00:00:00Z",
		Inputs:      model.ModelInputs{BundleSetSHA256: "multi123"},
		Inventory: model.Inventory{
			Packages: []model.PackageEntry{
				// api and worker both import store → store in-degree 2
				{Name: "api", Imports: []string{"auth", "store"}},
				{Name: "worker", Imports: []string{"queue", "store"}},
				{Name: "auth", Imports: nil},
				{Name: "queue", Imports: nil},
				{Name: "store", Imports: nil},
			},
		},
		StateDomains: []model.StateDomain{
			{
				ID:          "job_queue",
				Description: "Async job processing",
				Confidence:  0.75,
			},
			{
				ID:          "user_state",
				Description: "User session state",
				Confidence:  0.85,
			},
		},
		Effects: []model.Effect{
			{Kind: "db_write", Via: "store/db.go", Domain: "user_state"},
			{Kind: "fs_read", Via: "worker/processor.go", Domain: "job_queue"},
			{Kind: "fs_write", Via: "api/handler.go", Domain: "user_state"},
		},
		OpenQuestions: []model.OpenQuestion{
			{
				Question:      "Is there a rate limiter?",
				RelatedDomain: "",
			},
			{
				Question:      "What is the retry policy?",
				RelatedDomain: "job_queue",
			},
			{
				Question:      "How does session expiry work?",
				RelatedDomain: "user_state",
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

// writeBundle is a test helper that generates and writes a bundle, failing on error.
func writeBundle(t *testing.T, m *model.SystemModel, dir string) {
	t.Helper()
	bundle, err := GenerateKnowledgeBundle(m)
	if err != nil {
		t.Fatalf("GenerateKnowledgeBundle: %v", err)
	}
	if err := WriteKnowledgeBundle(bundle, dir); err != nil {
		t.Fatalf("WriteKnowledgeBundle: %v", err)
	}
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

// TestGenerateKnowledgeBundle_DirectoryStructure verifies INV-42: domains/ and
// graphs/ are always created; state-domains/ and symbols/ are absent.
func TestGenerateKnowledgeBundle_DirectoryStructure(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, minimalModel(), dir)

	// Required directories.
	for _, sub := range []string{"domains", "graphs"} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory, got file", sub)
		}
	}

	// Old directories must not be created (INV-42 updated).
	for _, absent := range []string{"state-domains", "symbols"} {
		if _, err := os.Stat(filepath.Join(dir, absent)); err == nil {
			t.Errorf("unexpected directory %s should not exist", absent)
		}
	}
}

// ---------------------------------------------------------------------------
// INV-43: wiki link format in index
// ---------------------------------------------------------------------------

// TestGenerateKnowledgeBundle_IndexLinks verifies index.md lists state domains
// using [[domains/<id>|<id>]] wiki links with no .md extension (INV-43).
// Packages and trust-zones must not appear.
func TestGenerateKnowledgeBundle_IndexLinks(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, minimalModel(), dir)

	content := readFile(t, filepath.Join(dir, "index.md"))

	// State domain link uses domains/ path (not state-domains/).
	if !strings.Contains(content, "[[domains/evidence_store|evidence_store]]") {
		t.Errorf("index.md missing domains/ wiki link;\ngot:\n%s", content)
	}

	// No package or trust-zone links.
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
// Domain page
// ---------------------------------------------------------------------------

// TestGenerateKnowledgeBundle_DomainPage verifies domains/<id>.md has plain-text
// symbols (no wiki links), effects table, and evidence section (INV-55).
func TestGenerateKnowledgeBundle_DomainPage(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, minimalModel(), dir)

	content := readFile(t, filepath.Join(dir, "domains", "evidence_store.md"))

	// Aggregate appears as plain text (no wiki link syntax).
	if !strings.Contains(content, "EvidenceBundle") {
		t.Errorf("missing aggregate EvidenceBundle;\ngot:\n%s", content)
	}
	if strings.Contains(content, "[[symbols/EvidenceBundle") {
		t.Errorf("aggregate must not use wiki link to symbols/;\ngot:\n%s", content)
	}

	// Representations, mutators, readers as plain text bullets.
	for _, sym := range []string{"EvidenceRecord", "SaveBundle", "LoadBundle"} {
		if !strings.Contains(content, "- "+sym) {
			t.Errorf("missing plain-text bullet for %s;\ngot:\n%s", sym, content)
		}
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
	if !strings.Contains(content, "fs_write") {
		t.Errorf("missing fs_write effect;\ngot:\n%s", content)
	}

	// Evidence section (INV-55).
	if !strings.Contains(content, "## Evidence") {
		t.Errorf("missing ## Evidence section;\ngot:\n%s", content)
	}
	if !strings.Contains(content, "bundle:store/db.go#signal:db_calls") {
		t.Errorf("missing evidence ref;\ngot:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// INV-54: confidence tag mapping
// ---------------------------------------------------------------------------

// TestGenerateKnowledgeBundle_DomainPage_ConfidenceTag verifies that the
// confidenceTag helper maps scores to the correct tag strings (INV-54).
func TestGenerateKnowledgeBundle_DomainPage_ConfidenceTag(t *testing.T) {
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
// INV-43: wiki link format in domain notes
// ---------------------------------------------------------------------------

// TestGenerateKnowledgeBundle_DomainPage_WikiLinks verifies INV-43 within domain
// notes: any wiki links present use [[path|display]] with no .md extension.
func TestGenerateKnowledgeBundle_DomainPage_WikiLinks(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, minimalModel(), dir)

	content := readFile(t, filepath.Join(dir, "domains", "evidence_store.md"))

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
// Boundary map
// ---------------------------------------------------------------------------

// TestGenerateKnowledgeBundle_BoundaryMap verifies boundaries.md has a
// persistence table with the expected rows.
func TestGenerateKnowledgeBundle_BoundaryMap(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, minimalModel(), dir)

	content := readFile(t, filepath.Join(dir, "boundaries.md"))

	// Frontmatter tag.
	if !strings.Contains(content, "iguana/boundaries") {
		t.Errorf("missing iguana/boundaries tag;\ngot:\n%s", content)
	}

	// Persistence section.
	if !strings.Contains(content, "## Persistence") {
		t.Errorf("missing ## Persistence section;\ngot:\n%s", content)
	}
	if !strings.Contains(content, "| Kind | File |") {
		t.Errorf("missing persistence table header;\ngot:\n%s", content)
	}
	// Row for the fs writer.
	if !strings.Contains(content, "| fs |") {
		t.Errorf("missing fs row in persistence table;\ngot:\n%s", content)
	}
	if !strings.Contains(content, "store/db.go") {
		t.Errorf("missing store/db.go in persistence table;\ngot:\n%s", content)
	}

	// Network section.
	if !strings.Contains(content, "## Network") {
		t.Errorf("missing ## Network section;\ngot:\n%s", content)
	}
	if !strings.Contains(content, "api/client.go") {
		t.Errorf("missing api/client.go in network table;\ngot:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// Risk report
// ---------------------------------------------------------------------------

// TestGenerateKnowledgeBundle_RiskReport_InDegree verifies risk.md contains
// a top-packages-by-in-degree table with expected entries.
func TestGenerateKnowledgeBundle_RiskReport_InDegree(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, multiDomainModel(), dir)

	content := readFile(t, filepath.Join(dir, "risk.md"))

	if !strings.Contains(content, "## Top Packages by In-Degree") {
		t.Errorf("missing ## Top Packages by In-Degree;\ngot:\n%s", content)
	}
	if !strings.Contains(content, "| Package | Dependents |") {
		t.Errorf("missing table header;\ngot:\n%s", content)
	}
	// store is imported by api and worker → in-degree 2 (highest).
	if !strings.Contains(content, "| store | 2 |") {
		t.Errorf("expected store with in-degree 2;\ngot:\n%s", content)
	}
}

// TestGenerateKnowledgeBundle_RiskReport_WriteDomains verifies risk.md contains
// a write-domains table with wiki-linked domains.
func TestGenerateKnowledgeBundle_RiskReport_WriteDomains(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, multiDomainModel(), dir)

	content := readFile(t, filepath.Join(dir, "risk.md"))

	if !strings.Contains(content, "## Domains with Write Effects") {
		t.Errorf("missing ## Domains with Write Effects;\ngot:\n%s", content)
	}
	// user_state has fs_write and db_write effects.
	if !strings.Contains(content, "[[domains/user_state|user_state]]") {
		t.Errorf("expected wiki link for user_state;\ngot:\n%s", content)
	}
}

// TestGenerateKnowledgeBundle_RiskReport_Cycles verifies risk.md reports
// "None found" on an acyclic import graph.
func TestGenerateKnowledgeBundle_RiskReport_Cycles(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, minimalModel(), dir)

	content := readFile(t, filepath.Join(dir, "risk.md"))

	if !strings.Contains(content, "## Import Cycles") {
		t.Errorf("missing ## Import Cycles;\ngot:\n%s", content)
	}
	if !strings.Contains(content, "_None found._") {
		t.Errorf("expected _None found._ on acyclic model;\ngot:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// Open questions
// ---------------------------------------------------------------------------

// TestGenerateKnowledgeBundle_OpenQuestions verifies open-questions.md groups
// questions by domain and puts orphans under ## General.
func TestGenerateKnowledgeBundle_OpenQuestions(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, multiDomainModel(), dir)

	content := readFile(t, filepath.Join(dir, "open-questions.md"))

	// Domain-specific sections use wiki links.
	if !strings.Contains(content, "## [[domains/user_state|user_state]]") {
		t.Errorf("missing user_state domain section;\ngot:\n%s", content)
	}
	if !strings.Contains(content, "## [[domains/job_queue|job_queue]]") {
		t.Errorf("missing job_queue domain section;\ngot:\n%s", content)
	}

	// Orphan question goes under ## General.
	if !strings.Contains(content, "## General") {
		t.Errorf("missing ## General section;\ngot:\n%s", content)
	}
	if !strings.Contains(content, "Is there a rate limiter?") {
		t.Errorf("missing general question;\ngot:\n%s", content)
	}

	// Domain questions appear under their domain.
	if !strings.Contains(content, "How does session expiry work?") {
		t.Errorf("missing user_state question;\ngot:\n%s", content)
	}
	if !strings.Contains(content, "What is the retry policy?") {
		t.Errorf("missing job_queue question;\ngot:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// Dependency graph
// ---------------------------------------------------------------------------

// TestGenerateKnowledgeBundle_DependencyGraph verifies graphs/dependencies.md
// contains a Mermaid LR block with import edges.
func TestGenerateKnowledgeBundle_DependencyGraph(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, minimalModel(), dir)

	content := readFile(t, filepath.Join(dir, "graphs", "dependencies.md"))

	// Mermaid code fence.
	if !strings.Contains(content, "```mermaid") {
		t.Errorf("missing mermaid code fence;\ngot:\n%s", content)
	}
	if !strings.Contains(content, "graph LR") {
		t.Errorf("missing graph LR directive;\ngot:\n%s", content)
	}

	// main → store edge from minimalModel.
	if !strings.Contains(content, "main --> store") {
		t.Errorf("missing main --> store edge;\ngot:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// INV-44: idempotency
// ---------------------------------------------------------------------------

// TestGenerateKnowledgeBundle_Idempotent verifies INV-44: calling
// GenerateKnowledgeBundle + WriteKnowledgeBundle twice on the same model
// produces byte-identical files.
func TestGenerateKnowledgeBundle_Idempotent(t *testing.T) {
	dir := t.TempDir()
	m := minimalModel()

	writeBundle(t, m, dir)

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

	writeBundle(t, m, dir)

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
// Empty model
// ---------------------------------------------------------------------------

// TestGenerateKnowledgeBundle_EmptyModel verifies that an empty model does not
// panic and all top-level pages are created (INV-42).
func TestGenerateKnowledgeBundle_EmptyModel(t *testing.T) {
	dir := t.TempDir()
	m := &model.SystemModel{
		Version:     1,
		GeneratedAt: "2024-01-01T00:00:00Z",
		Inputs:      model.ModelInputs{BundleSetSHA256: "empty"},
	}

	writeBundle(t, m, dir)

	// All top-level pages must be created.
	for _, name := range []string{"index.md", "boundaries.md", "risk.md", "open-questions.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not created for empty model: %v", name, err)
		}
	}
	// graphs/dependencies.md always written.
	if _, err := os.Stat(filepath.Join(dir, "graphs", "dependencies.md")); err != nil {
		t.Errorf("graphs/dependencies.md not created for empty model: %v", err)
	}

	// domains/ directory must exist (INV-42).
	if info, err := os.Stat(filepath.Join(dir, "domains")); err != nil || !info.IsDir() {
		t.Errorf("domains/ directory not created for empty model")
	}

	// Empty dependency graph says no packages.
	graphContent := readFile(t, filepath.Join(dir, "graphs", "dependencies.md"))
	if !strings.Contains(graphContent, "_No packages._") {
		t.Errorf("expected _No packages._ in empty dependency graph;\ngot:\n%s", graphContent)
	}

	// state-domains/ and symbols/ must not exist.
	for _, absent := range []string{"state-domains", "symbols"} {
		if _, err := os.Stat(filepath.Join(dir, absent)); err == nil {
			t.Errorf("unexpected directory %s should not exist", absent)
		}
	}
}

// ---------------------------------------------------------------------------
// INV-53: one domain file per domain
// ---------------------------------------------------------------------------

// TestGenerateKnowledgeBundle_DomainPage_CorrespondenceWithINV53 verifies that
// each state domain produces exactly one domains/<id>.md (INV-53).
func TestGenerateKnowledgeBundle_DomainPage_CorrespondenceWithINV53(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, multiDomainModel(), dir)

	for _, id := range []string{"user_state", "job_queue"} {
		path := filepath.Join(dir, "domains", id+".md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected domains/%s.md to exist: %v", id, err)
		}
	}
}
