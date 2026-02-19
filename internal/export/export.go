package export

// export.go — knowledge_export domain: converts a SystemModel into a vault.
//
// The vault contains state domain pages, a dependency graph, boundary map,
// risk report, and open-questions index. No symbols/ bipartite graph.
//
// Vault layout:
//   index.md                 — lists all state domains
//   domains/<id>.md          — one per state domain
//   boundaries.md            — persistence + network
//   risk.md                  — in-degree, write domains, import cycles
//   open-questions.md        — grouped by domain
//   graphs/dependencies.md   — Mermaid LR import graph
//
// See INVARIANT.md INV-42..46, INV-53..55.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"iguana/internal/model"
)

// KnowledgeBundle holds pre-generated page content (path → markdown).
// Paths are relative to the output directory, using forward slashes.
type KnowledgeBundle struct {
	pages map[string]string
}

// GenerateKnowledgeBundle builds all vault pages from sys.
// No files are written (pure function for testability, INV-44).
func GenerateKnowledgeBundle(sys *model.SystemModel) (*KnowledgeBundle, error) {
	pages := make(map[string]string)

	pages["index.md"] = buildOverviewPage(sys)

	for _, d := range sys.StateDomains {
		id := sanitizeFilename(d.ID)
		pages["domains/"+id+".md"] = buildDomainPage(d, sys.Effects)
	}

	pages["boundaries.md"] = buildBoundaryMap(sys)
	pages["risk.md"] = buildRiskReport(sys)
	pages["open-questions.md"] = buildOpenQuestionsIndex(sys)
	pages["graphs/dependencies.md"] = buildDependencyGraph(sys)

	return &KnowledgeBundle{pages: pages}, nil
}

// WriteKnowledgeBundle writes all pages in bundle to outputDir.
// Pages are written in sorted path order for idempotency (INV-44).
// Always creates domains/ and graphs/ subdirectories (INV-42).
func WriteKnowledgeBundle(bundle *KnowledgeBundle, outputDir string) error {
	// INV-42: always create these subdirectories.
	for _, sub := range []string{"domains", "graphs"} {
		if err := os.MkdirAll(filepath.Join(outputDir, sub), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}

	paths := make([]string, 0, len(bundle.pages))
	for p := range bundle.pages {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		abs := filepath.Join(outputDir, filepath.FromSlash(p))
		if err := writeNote(abs, bundle.pages[p]); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Page builders
// ---------------------------------------------------------------------------

// buildOverviewPage builds index.md — entry point listing all state domains.
func buildOverviewPage(sys *model.SystemModel) string {
	var b strings.Builder
	b.WriteString(frontmatter([]string{"iguana/index"}))
	b.WriteString("# System Model\n\n")
	b.WriteString(fmt.Sprintf("- **Generated**: %s\n", sys.GeneratedAt))
	b.WriteString(fmt.Sprintf("- **Bundle hash**: `%s`\n\n", sys.Inputs.BundleSetSHA256))
	b.WriteString("## State Domains\n\n")
	for _, d := range sys.StateDomains {
		id := sanitizeFilename(d.ID)
		b.WriteString(fmt.Sprintf("- [[domains/%s|%s]] — %s\n", id, d.ID, d.Description))
	}
	return b.String()
}

// buildDomainPage builds domains/<id>.md for one state domain.
// Symbols are plain text (no wiki links). Evidence section included when
// EvidenceRefs is non-empty (INV-55).
func buildDomainPage(d model.StateDomain, effects []model.Effect) string {
	var b strings.Builder

	tags := []string{"state-domain", confidenceTag(d.Confidence)}
	b.WriteString(frontmatter(tags))
	b.WriteString(fmt.Sprintf("# %s\n\n", d.ID))
	b.WriteString(d.Description + "\n\n")
	b.WriteString(fmt.Sprintf("**Confidence**: %.2f\n", d.Confidence))
	if len(d.Owners) > 0 {
		b.WriteString(fmt.Sprintf("**Owners**: %s\n", strings.Join(d.Owners, ", ")))
	}

	if d.Aggregate != "" {
		b.WriteString("\n## Aggregate\n\n")
		b.WriteString(d.Aggregate + "\n")
	}

	if len(d.Representations) > 0 {
		b.WriteString("\n## Representations\n\n")
		for _, r := range d.Representations {
			b.WriteString("- " + r + "\n")
		}
	}

	if len(d.PrimaryMutators) > 0 {
		b.WriteString("\n## Primary Mutators\n\n")
		for _, mut := range d.PrimaryMutators {
			b.WriteString("- " + mut + "\n")
		}
	}

	if len(d.PrimaryReaders) > 0 {
		b.WriteString("\n## Primary Readers\n\n")
		for _, rdr := range d.PrimaryReaders {
			b.WriteString("- " + rdr + "\n")
		}
	}

	fx := domainEffects(d.ID, effects)
	if len(fx) > 0 {
		b.WriteString("\n## Effects\n\n")
		b.WriteString("| Kind | Via |\n")
		b.WriteString("|------|-----|\n")
		for _, e := range fx {
			b.WriteString(fmt.Sprintf("| %s | `%s` |\n", e.Kind, e.Via))
		}
	}

	// INV-55: Evidence section when EvidenceRefs non-empty.
	if len(d.EvidenceRefs) > 0 {
		b.WriteString("\n## Evidence\n\n")
		for _, ref := range d.EvidenceRefs {
			b.WriteString("- " + ref + "\n")
		}
	}

	return b.String()
}

// buildBoundaryMap builds boundaries.md — persistence and network boundaries.
func buildBoundaryMap(sys *model.SystemModel) string {
	var b strings.Builder
	b.WriteString(frontmatter([]string{"iguana/boundaries"}))
	b.WriteString("# Boundaries\n\n")

	if len(sys.Boundaries.Persistence) > 0 {
		b.WriteString("## Persistence\n\n")
		b.WriteString("| Kind | File |\n")
		b.WriteString("|------|------|\n")
		for _, pb := range sys.Boundaries.Persistence {
			for _, w := range pb.Writers {
				b.WriteString(fmt.Sprintf("| %s | `%s` |\n", pb.Kind, w.File))
			}
		}
		b.WriteString("\n")
	}

	if sys.Boundaries.Network != nil && len(sys.Boundaries.Network.Outbound) > 0 {
		b.WriteString("## Network\n\n")
		b.WriteString("| File |\n")
		b.WriteString("|------|\n")
		for _, ob := range sys.Boundaries.Network.Outbound {
			b.WriteString(fmt.Sprintf("| `%s` |\n", ob.File))
		}
	}

	return b.String()
}

// buildRiskReport builds risk.md — in-degree, write domains, import cycles.
func buildRiskReport(sys *model.SystemModel) string {
	var b strings.Builder
	b.WriteString(frontmatter([]string{"iguana/risk"}))
	b.WriteString("# Risk Report\n\n")

	// --- Top packages by in-degree ---
	inDegree := make(map[string]int)
	for _, pkg := range sys.Inventory.Packages {
		for _, imp := range pkg.Imports {
			inDegree[imp]++
		}
	}

	type pkgCount struct {
		name  string
		count int
	}
	counts := make([]pkgCount, 0, len(inDegree))
	for name, count := range inDegree {
		counts = append(counts, pkgCount{name, count})
	}
	// Sort descending by count, then ascending by name for determinism.
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].count != counts[j].count {
			return counts[i].count > counts[j].count
		}
		return counts[i].name < counts[j].name
	})
	if len(counts) > 10 {
		counts = counts[:10]
	}

	b.WriteString("## Top Packages by In-Degree\n\n")
	if len(counts) > 0 {
		b.WriteString("| Package | Dependents |\n")
		b.WriteString("|---------|------------|\n")
		for _, pc := range counts {
			b.WriteString(fmt.Sprintf("| %s | %d |\n", pc.name, pc.count))
		}
	}
	b.WriteString("\n")

	// --- Domains with write effects ---
	writeDomains := make(map[string][]string) // domainID → []Via
	for _, e := range sys.Effects {
		if (e.Kind == "fs_write" || e.Kind == "db_write") && e.Domain != "" {
			writeDomains[e.Domain] = append(writeDomains[e.Domain], e.Via)
		}
	}

	b.WriteString("## Domains with Write Effects\n\n")
	if len(writeDomains) > 0 {
		domainIDs := make([]string, 0, len(writeDomains))
		for id := range writeDomains {
			domainIDs = append(domainIDs, id)
		}
		sort.Strings(domainIDs)

		b.WriteString("| Domain | Writers |\n")
		b.WriteString("|--------|----------|\n")
		for _, id := range domainIDs {
			san := sanitizeFilename(id)
			writers := strings.Join(writeDomains[id], ", ")
			b.WriteString(fmt.Sprintf("| [[domains/%s|%s]] | %s |\n", san, id, writers))
		}
	}
	b.WriteString("\n")

	// --- Import cycles ---
	b.WriteString("## Import Cycles\n\n")
	cycles := findCycles(sys.Inventory.Packages)
	if len(cycles) == 0 {
		b.WriteString("_None found._\n")
	} else {
		for _, cycle := range cycles {
			b.WriteString("- " + cycle + "\n")
		}
	}

	return b.String()
}

// buildOpenQuestionsIndex builds open-questions.md — questions grouped by domain.
// Questions with no RelatedDomain appear under ## General.
func buildOpenQuestionsIndex(sys *model.SystemModel) string {
	var b strings.Builder
	b.WriteString(frontmatter([]string{"iguana/open-questions"}))
	b.WriteString("# Open Questions\n\n")

	// Group by RelatedDomain; use sentinel for empty domain.
	const general = "__general__"
	domainQuestions := make(map[string][]string)
	for _, q := range sys.OpenQuestions {
		key := q.RelatedDomain
		if key == "" {
			key = general
		}
		domainQuestions[key] = append(domainQuestions[key], q.Question)
	}

	// Collect non-general domain IDs, sorted.
	domainIDs := make([]string, 0)
	for id := range domainQuestions {
		if id != general {
			domainIDs = append(domainIDs, id)
		}
	}
	sort.Strings(domainIDs)

	for _, id := range domainIDs {
		san := sanitizeFilename(id)
		b.WriteString(fmt.Sprintf("## [[domains/%s|%s]]\n\n", san, id))
		for _, q := range domainQuestions[id] {
			b.WriteString("- " + q + "\n")
		}
		b.WriteString("\n")
	}

	if questions, ok := domainQuestions[general]; ok {
		b.WriteString("## General\n\n")
		for _, q := range questions {
			b.WriteString("- " + q + "\n")
		}
	}

	return b.String()
}

// buildDependencyGraph builds graphs/dependencies.md — Mermaid LR import graph.
func buildDependencyGraph(sys *model.SystemModel) string {
	var b strings.Builder
	b.WriteString(frontmatter([]string{"iguana/graph"}))
	b.WriteString("# Dependency Graph\n\n")

	// Collect all import edges.
	type edge struct {
		from, to string
	}
	var edges []edge
	for _, pkg := range sys.Inventory.Packages {
		for _, imp := range pkg.Imports {
			edges = append(edges, edge{pkg.Name, imp})
		}
	}

	if len(edges) == 0 {
		b.WriteString("_No packages._\n")
		return b.String()
	}

	// Sort edges alphabetically for determinism (INV-44).
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].from != edges[j].from {
			return edges[i].from < edges[j].from
		}
		return edges[i].to < edges[j].to
	})

	b.WriteString("```mermaid\ngraph LR\n")
	for _, e := range edges {
		b.WriteString(fmt.Sprintf("  %s --> %s\n", e.from, e.to))
	}
	b.WriteString("```\n")

	return b.String()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// domainEffects returns all effects whose Domain field matches domainID.
func domainEffects(domainID string, effects []model.Effect) []model.Effect {
	var out []model.Effect
	for _, e := range effects {
		if e.Domain == domainID {
			out = append(out, e)
		}
	}
	return out
}

// confidenceTag maps a confidence score to a tag string (INV-54).
// ≥0.8 → "confidence-high", ≥0.7 → "confidence-medium", <0.7 → "confidence-low".
func confidenceTag(c float64) string {
	switch {
	case c >= 0.8:
		return "confidence-high"
	case c >= 0.7:
		return "confidence-medium"
	default:
		return "confidence-low"
	}
}

// frontmatter returns a YAML frontmatter block. Tags are sorted alphabetically (INV-54).
func frontmatter(tags []string) string {
	sorted := make([]string, len(tags))
	copy(sorted, tags)
	sort.Strings(sorted)
	var b strings.Builder
	b.WriteString("---\ntags:\n")
	for _, t := range sorted {
		b.WriteString("  - " + t + "\n")
	}
	b.WriteString("---\n\n")
	return b.String()
}

// sanitizeFilename replaces / and . with -, collapses consecutive - to one,
// and trims leading/trailing - (INV-45).
func sanitizeFilename(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, ".", "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	return s
}

// writeNote writes content to path, creating parent directories as needed.
func writeNote(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// findCycles performs DFS cycle detection on the package import graph.
// Returns one string per cycle in "pkgA → pkgB → pkgA" format.
// Results are deterministic because nodes and neighbors are sorted.
func findCycles(packages []model.PackageEntry) []string {
	// Build adjacency list and set of known package names.
	graph := make(map[string][]string)
	allPkgs := make(map[string]bool)
	for _, p := range packages {
		allPkgs[p.Name] = true
		if len(p.Imports) > 0 {
			graph[p.Name] = p.Imports
		}
	}

	// Process nodes in sorted order for determinism.
	nodes := make([]string, 0, len(allPkgs))
	for n := range allPkgs {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	// DFS coloring: 0=white (unvisited), 1=gray (in stack), 2=black (done).
	color := make(map[string]int)
	var cycles []string
	var path []string

	var dfs func(node string)
	dfs = func(node string) {
		if color[node] == 2 {
			return
		}
		if color[node] == 1 {
			// Cycle: find start of cycle in path.
			for i, n := range path {
				if n == node {
					cycleNodes := make([]string, len(path)-i+1)
					copy(cycleNodes, path[i:])
					cycleNodes[len(cycleNodes)-1] = node // close
					cycles = append(cycles, strings.Join(cycleNodes, " → "))
					return
				}
			}
			return
		}
		color[node] = 1
		path = append(path, node)
		// Sort neighbors for deterministic output.
		neighbors := make([]string, len(graph[node]))
		copy(neighbors, graph[node])
		sort.Strings(neighbors)
		for _, neighbor := range neighbors {
			if allPkgs[neighbor] { // only traverse known packages
				dfs(neighbor)
			}
		}
		path = path[:len(path)-1]
		color[node] = 2
	}

	for _, node := range nodes {
		if color[node] == 0 {
			dfs(node)
		}
	}

	return cycles
}
