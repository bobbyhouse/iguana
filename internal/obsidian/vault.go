package obsidian

// vault.go — Converts a SystemModel into an Obsidian vault.
//
// The vault contains only state domains and their associated symbols.
// Domains and symbols form a bipartite graph: each state domain note
// wiki-links to its aggregate, representations, mutators, and readers;
// each symbol note wiki-links back to every domain that references it.
//
// Vault layout:
//   index.md                      — entry point listing all state domains
//   state-domains/<id>.md         — one note per state domain
//   symbols/<name>.md             — one note per unique symbol name
//
// See INVARIANT.md INV-42..46, INV-53..54.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"iguana/internal/model"
)

// symbolRole records one domain's claim on a symbol name.
type symbolRole struct {
	domainID string
	role     string // "aggregate" | "representation" | "mutator" | "reader"
}

// collectSymbols gathers all symbol names from every state domain,
// returning a map of symbol name → []symbolRole (one entry per domain reference).
// Roles are appended in domain slice order, giving deterministic output.
func collectSymbols(domains []model.StateDomain) map[string][]symbolRole {
	m := make(map[string][]symbolRole)
	for _, d := range domains {
		if d.Aggregate != "" {
			m[d.Aggregate] = append(m[d.Aggregate], symbolRole{domainID: d.ID, role: "aggregate"})
		}
		for _, r := range d.Representations {
			if r != "" {
				m[r] = append(m[r], symbolRole{domainID: d.ID, role: "representation"})
			}
		}
		for _, mut := range d.PrimaryMutators {
			if mut != "" {
				m[mut] = append(m[mut], symbolRole{domainID: d.ID, role: "mutator"})
			}
		}
		for _, rdr := range d.PrimaryReaders {
			if rdr != "" {
				m[rdr] = append(m[rdr], symbolRole{domainID: d.ID, role: "reader"})
			}
		}
	}
	return m
}

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

// confidenceTag maps a confidence score to an Obsidian tag string (INV-54).
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

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// GenerateObsidianVault writes an Obsidian-compatible markdown vault rooted at
// outputDir from the given SystemModel. Only state-domains/ and symbols/
// subdirectories are created. Existing files are overwritten (INV-46).
// Produces identical output when called twice with the same inputs (INV-44).
func GenerateObsidianVault(sys *model.SystemModel, outputDir string) error {
	// INV-42: always create these subdirectories.
	for _, sub := range []string{"state-domains", "symbols"} {
		if err := os.MkdirAll(filepath.Join(outputDir, sub), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}

	if err := writeVaultIndex(sys, outputDir); err != nil {
		return err
	}

	symbols := collectSymbols(sys.StateDomains)

	for _, d := range sys.StateDomains {
		if err := writeStateDomainNote(d, sys.Effects, outputDir); err != nil {
			return err
		}
	}

	// Sort symbol names for deterministic output (INV-44).
	names := make([]string, 0, len(symbols))
	for name := range symbols {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := writeSymbolNote(name, symbols[name], outputDir); err != nil {
			return err
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Note writers
// ---------------------------------------------------------------------------

// writeVaultIndex writes index.md — the entry point listing all state domains.
func writeVaultIndex(sys *model.SystemModel, outputDir string) error {
	var b strings.Builder

	b.WriteString(frontmatter([]string{"iguana/index"}))
	b.WriteString("# System Model\n\n")
	b.WriteString(fmt.Sprintf("- **Generated**: %s\n", sys.GeneratedAt))
	b.WriteString(fmt.Sprintf("- **Bundle hash**: `%s`\n\n", sys.Inputs.BundleSetSHA256))

	b.WriteString("## State Domains\n\n")
	for _, d := range sys.StateDomains {
		id := sanitizeFilename(d.ID)
		b.WriteString(fmt.Sprintf("- [[state-domains/%s|%s]] — %s\n", id, d.ID, d.Description))
	}

	return writeNote(filepath.Join(outputDir, "index.md"), b.String())
}

// writeStateDomainNote writes state-domains/<id>.md.
//
// Sections:
//   - Header with confidence and owners
//   - Aggregate: wiki link to symbols/<name>
//   - Representations: wiki links to symbols/<name>
//   - Primary Mutators: wiki links to symbols/<name>
//   - Primary Readers: wiki links to symbols/<name>
//   - Effects: table of kind + via for this domain
func writeStateDomainNote(d model.StateDomain, effects []model.Effect, outputDir string) error {
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
		san := sanitizeFilename(d.Aggregate)
		b.WriteString(fmt.Sprintf("[[symbols/%s|%s]]\n", san, d.Aggregate))
	}

	if len(d.Representations) > 0 {
		b.WriteString("\n## Representations\n\n")
		for _, r := range d.Representations {
			san := sanitizeFilename(r)
			b.WriteString(fmt.Sprintf("- [[symbols/%s|%s]]\n", san, r))
		}
	}

	if len(d.PrimaryMutators) > 0 {
		b.WriteString("\n## Primary Mutators\n\n")
		for _, mut := range d.PrimaryMutators {
			san := sanitizeFilename(mut)
			b.WriteString(fmt.Sprintf("- [[symbols/%s|%s]]\n", san, mut))
		}
	}

	if len(d.PrimaryReaders) > 0 {
		b.WriteString("\n## Primary Readers\n\n")
		for _, rdr := range d.PrimaryReaders {
			san := sanitizeFilename(rdr)
			b.WriteString(fmt.Sprintf("- [[symbols/%s|%s]]\n", san, rdr))
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

	id := sanitizeFilename(d.ID)
	return writeNote(filepath.Join(outputDir, "state-domains", id+".md"), b.String())
}

// writeSymbolNote writes symbols/<name>.md with back-links to all owning domains.
// Single-domain symbols use a simple key/value layout; multi-domain symbols use a table.
func writeSymbolNote(name string, roles []symbolRole, outputDir string) error {
	var b strings.Builder

	// Build tag set: symbol + domain/<id> + role/<role> for each entry (INV-54).
	tagSet := make(map[string]bool)
	tagSet["symbol"] = true
	for _, sr := range roles {
		tagSet["domain/"+sr.domainID] = true
		tagSet["role/"+sr.role] = true
	}
	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
	}

	b.WriteString(frontmatter(tags)) // frontmatter sorts tags

	b.WriteString(fmt.Sprintf("# %s\n\n", name))

	if len(roles) == 1 {
		sr := roles[0]
		san := sanitizeFilename(sr.domainID)
		b.WriteString(fmt.Sprintf("**Role**: %s\n", sr.role))
		b.WriteString(fmt.Sprintf("**Domain**: [[state-domains/%s|%s]]\n", san, sr.domainID))
	} else {
		b.WriteString("| Role | Domain |\n")
		b.WriteString("|------|--------|\n")
		for _, sr := range roles {
			san := sanitizeFilename(sr.domainID)
			b.WriteString(fmt.Sprintf("| %s | [[state-domains/%s|%s]] |\n", sr.role, san, sr.domainID))
		}
	}

	san := sanitizeFilename(name)
	return writeNote(filepath.Join(outputDir, "symbols", san+".md"), b.String())
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// sanitizeFilename replaces / and . with -, collapses consecutive - to one,
// and trims leading/trailing - (INV-45).
func sanitizeFilename(s string) string {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, ".", "-")
	// Collapse consecutive dashes.
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
