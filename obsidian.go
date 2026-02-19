package main

// obsidian.go — Converts a SystemModel into an Obsidian vault.
//
// The vault is a directory of inter-linked markdown files using Obsidian's
// [[wiki link]] syntax, making the system model explorable via graph view.
//
// Three categories of relationships are surfaced as direct graph edges:
//
//  1. Import graph    — package → packages it imports (dependency / blast-radius)
//  2. Effect edges    — package → state domains it writes or reads (causality)
//  3. Concurrency risk — concurrent files → state domains they touch (race risk)
//
// See INVARIANT.md INV-42..46.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Relationship context
// ---------------------------------------------------------------------------

// vaultCtx holds pre-computed relationship maps so each note writer can
// produce direct wiki-link edges without re-scanning the full model.
type vaultCtx struct {
	importedBy            map[string][]string // pkg → packages that import it
	pkgToDomains          map[string][]string // pkg → state domain IDs (LLM ownership)
	pkgToZones            map[string][]string // pkg → trust zone IDs
	pkgEffects            map[string][]Effect // pkg → effects it produces
	domainWriters         map[string][]string // domain → packages that write to it
	domainReaders         map[string][]string // domain → packages that read from it
	domainConcurrentFiles map[string][]string // domain → concurrent files touching it
	concurrencyToDomains  map[string][]string // concurrency domain ID → state domain IDs
}

func buildVaultCtx(model *SystemModel) vaultCtx {
	fileToPkg := buildFileToPackage(model.Inventory.Packages)
	writers, readers := buildDomainPackageEffects(model.Effects, fileToPkg)
	return vaultCtx{
		importedBy:            buildImportedBy(model.Inventory.Packages),
		pkgToDomains:          buildPkgToDomains(model.StateDomains),
		pkgToZones:            buildPkgToZones(model.TrustZones),
		pkgEffects:            buildPkgEffects(model.Effects, fileToPkg),
		domainWriters:         writers,
		domainReaders:         readers,
		domainConcurrentFiles: buildDomainToConcurrentFiles(model.ConcurrencyDomains, model.Effects),
		concurrencyToDomains:  buildConcurrencyToDomains(model.ConcurrencyDomains, model.Effects),
	}
}

// buildFileToPackage returns a map from file path to the package that owns it.
func buildFileToPackage(packages []PackageEntry) map[string]string {
	m := make(map[string]string)
	for _, pkg := range packages {
		for _, f := range pkg.Files {
			m[f] = pkg.Name
		}
	}
	return m
}

// buildImportedBy returns the reverse of PackageEntry.Imports:
// for each package, which packages import it.
func buildImportedBy(packages []PackageEntry) map[string][]string {
	m := make(map[string][]string)
	for _, pkg := range packages {
		for _, dep := range pkg.Imports {
			m[dep] = append(m[dep], pkg.Name)
		}
	}
	for k := range m {
		sort.Strings(m[k])
	}
	return m
}

// buildPkgToDomains maps package name → state domain IDs that list it as an owner.
func buildPkgToDomains(domains []StateDomain) map[string][]string {
	m := make(map[string][]string)
	for _, d := range domains {
		for _, pkg := range d.Owners {
			m[pkg] = append(m[pkg], d.ID)
		}
	}
	return m
}

// buildPkgToZones maps package name → trust zone IDs that include it.
func buildPkgToZones(zones []TrustZone) map[string][]string {
	m := make(map[string][]string)
	for _, z := range zones {
		for _, pkg := range z.Packages {
			m[pkg] = append(m[pkg], z.ID)
		}
	}
	return m
}

// buildPkgEffects maps package name → the effects its files produce.
func buildPkgEffects(effects []Effect, fileToPkg map[string]string) map[string][]Effect {
	m := make(map[string][]Effect)
	for _, e := range effects {
		pkg := fileToPkg[e.Via]
		if pkg == "" {
			continue
		}
		m[pkg] = append(m[pkg], e)
	}
	return m
}

// buildDomainPackageEffects returns two maps:
//   - writers: domain → packages that produce write effects (db_write, fs_write)
//   - readers: domain → packages that produce read effects (fs_read)
func buildDomainPackageEffects(effects []Effect, fileToPkg map[string]string) (writers, readers map[string][]string) {
	wSet := make(map[string]map[string]bool)
	rSet := make(map[string]map[string]bool)
	for _, e := range effects {
		if e.Domain == "" {
			continue
		}
		pkg := fileToPkg[e.Via]
		if pkg == "" {
			continue
		}
		switch e.Kind {
		case "db_write", "fs_write":
			if wSet[e.Domain] == nil {
				wSet[e.Domain] = make(map[string]bool)
			}
			wSet[e.Domain][pkg] = true
		case "fs_read":
			if rSet[e.Domain] == nil {
				rSet[e.Domain] = make(map[string]bool)
			}
			rSet[e.Domain][pkg] = true
		}
	}
	toSliceMap := func(m map[string]map[string]bool) map[string][]string {
		out := make(map[string][]string, len(m))
		for k, set := range m {
			sl := make([]string, 0, len(set))
			for v := range set {
				sl = append(sl, v)
			}
			sort.Strings(sl)
			out[k] = sl
		}
		return out
	}
	return toSliceMap(wSet), toSliceMap(rSet)
}

// buildDomainToConcurrentFiles returns a map from state domain ID → files that
// both appear in a ConcurrencyDomain and produce effects in that domain.
// These represent concurrent access to shared state — the highest-risk sites.
func buildDomainToConcurrentFiles(concDomains []ConcurrencyDomain, effects []Effect) map[string][]string {
	concurrentFiles := make(map[string]bool)
	for _, cd := range concDomains {
		for _, f := range cd.Files {
			concurrentFiles[f] = true
		}
	}
	domainSet := make(map[string]map[string]bool)
	for _, e := range effects {
		if e.Domain == "" || !concurrentFiles[e.Via] {
			continue
		}
		if domainSet[e.Domain] == nil {
			domainSet[e.Domain] = make(map[string]bool)
		}
		domainSet[e.Domain][e.Via] = true
	}
	out := make(map[string][]string, len(domainSet))
	for domain, fileSet := range domainSet {
		files := make([]string, 0, len(fileSet))
		for f := range fileSet {
			files = append(files, f)
		}
		sort.Strings(files)
		out[domain] = files
	}
	return out
}

// buildConcurrencyToDomains returns a map from concurrency domain ID → state
// domain IDs that the domain's files touch via effects.
func buildConcurrencyToDomains(concDomains []ConcurrencyDomain, effects []Effect) map[string][]string {
	// file → set of domain IDs it touches via effects.
	fileDomains := make(map[string]map[string]bool)
	for _, e := range effects {
		if e.Domain == "" {
			continue
		}
		if fileDomains[e.Via] == nil {
			fileDomains[e.Via] = make(map[string]bool)
		}
		fileDomains[e.Via][e.Domain] = true
	}
	out := make(map[string][]string)
	for _, cd := range concDomains {
		domainSet := make(map[string]bool)
		for _, f := range cd.Files {
			for d := range fileDomains[f] {
				domainSet[d] = true
			}
		}
		if len(domainSet) > 0 {
			domains := make([]string, 0, len(domainSet))
			for d := range domainSet {
				domains = append(domains, d)
			}
			sort.Strings(domains)
			out[cd.ID] = domains
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// GenerateObsidianVault writes an Obsidian-compatible markdown vault rooted at
// outputDir from the given SystemModel. Subdirectories are created as needed.
// Existing files are overwritten (INV-46). Produces identical output when called
// twice with the same inputs (INV-44).
func GenerateObsidianVault(model *SystemModel, outputDir string) error {
	// INV-42: always create these subdirectories.
	for _, sub := range []string{"packages", "state-domains", "trust-zones", "concurrency-domains"} {
		if err := os.MkdirAll(filepath.Join(outputDir, sub), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}

	ctx := buildVaultCtx(model)

	if err := writeVaultIndex(model, outputDir); err != nil {
		return err
	}

	for _, pkg := range model.Inventory.Packages {
		if err := writePackageNote(pkg, ctx, outputDir); err != nil {
			return err
		}
	}

	for _, d := range model.StateDomains {
		if err := writeStateDomainNote(d, ctx, outputDir); err != nil {
			return err
		}
	}

	for _, z := range model.TrustZones {
		if err := writeTrustZoneNote(z, outputDir); err != nil {
			return err
		}
	}

	for _, cd := range model.ConcurrencyDomains {
		if err := writeConcurrencyDomainNote(cd, ctx, outputDir); err != nil {
			return err
		}
	}

	if err := writeEffectsNote(model.Effects, outputDir); err != nil {
		return err
	}

	if err := writeOpenQuestionsNote(model.OpenQuestions, outputDir); err != nil {
		return err
	}

	return nil
}

// ---------------------------------------------------------------------------
// Note writers
// ---------------------------------------------------------------------------

// writeVaultIndex writes index.md — the entry point for the vault.
func writeVaultIndex(model *SystemModel, outputDir string) error {
	var b strings.Builder

	b.WriteString(frontmatter("iguana/index"))
	b.WriteString("# System Model\n\n")
	b.WriteString(fmt.Sprintf("- **Generated**: %s\n", model.GeneratedAt))
	b.WriteString(fmt.Sprintf("- **Bundle hash**: `%s`\n\n", model.Inputs.BundleSetSHA256))

	b.WriteString("## Packages\n\n")
	for _, pkg := range model.Inventory.Packages {
		name := sanitizeFilename(pkg.Name)
		b.WriteString(fmt.Sprintf("- [[packages/%s|%s]]\n", name, pkg.Name))
	}

	b.WriteString("\n## State Domains\n\n")
	for _, d := range model.StateDomains {
		id := sanitizeFilename(d.ID)
		b.WriteString(fmt.Sprintf("- [[state-domains/%s|%s]] — %s\n", id, d.ID, d.Description))
	}

	b.WriteString("\n## Trust Zones\n\n")
	for _, z := range model.TrustZones {
		id := sanitizeFilename(z.ID)
		b.WriteString(fmt.Sprintf("- [[trust-zones/%s|%s]]\n", id, z.ID))
	}

	b.WriteString("\n## Concurrency Domains\n\n")
	for _, cd := range model.ConcurrencyDomains {
		id := sanitizeFilename(cd.ID)
		b.WriteString(fmt.Sprintf("- [[concurrency-domains/%s|%s]]\n", id, cd.ID))
	}

	b.WriteString("\n## Other\n\n")
	b.WriteString("- [[effects|Effects]]\n")
	b.WriteString("- [[open-questions|Open Questions]]\n")

	return writeNote(filepath.Join(outputDir, "index.md"), b.String())
}

// writePackageNote writes packages/<name>.md.
//
// Sections:
//   - Files: source files in this package
//   - Imports: packages this one depends on (import graph edges)
//   - Imported by: packages that depend on this one (reverse import edges)
//   - Owned state domains: LLM-inferred ownership
//   - Effects: what this package actually does to which domains (write/read edges)
//   - Trust zones: security boundary membership
func writePackageNote(pkg PackageEntry, ctx vaultCtx, outputDir string) error {
	var b strings.Builder

	b.WriteString(frontmatter("iguana/package"))
	b.WriteString(fmt.Sprintf("# Package: %s\n\n", pkg.Name))

	b.WriteString("## Files\n\n")
	for _, f := range pkg.Files {
		b.WriteString(fmt.Sprintf("- `%s`\n", f))
	}

	// Import graph: outbound dependencies.
	b.WriteString("\n## Imports\n\n")
	if len(pkg.Imports) > 0 {
		for _, dep := range pkg.Imports {
			san := sanitizeFilename(dep)
			b.WriteString(fmt.Sprintf("- [[packages/%s|%s]]\n", san, dep))
		}
	} else {
		b.WriteString("_none_\n")
	}

	// Import graph: inbound dependencies (who breaks if this changes).
	b.WriteString("\n## Imported By\n\n")
	importedBy := ctx.importedBy[pkg.Name]
	if len(importedBy) > 0 {
		for _, dep := range importedBy {
			san := sanitizeFilename(dep)
			b.WriteString(fmt.Sprintf("- [[packages/%s|%s]]\n", san, dep))
		}
	} else {
		b.WriteString("_none_\n")
	}

	// What this package actually does (effect edges to state domains).
	effects := ctx.pkgEffects[pkg.Name]
	if len(effects) > 0 {
		b.WriteString("\n## Effects\n\n")
		b.WriteString("| Kind | File | Domain |\n")
		b.WriteString("|------|------|--------|\n")
		for _, e := range effects {
			domainCell := e.Domain
			if e.Domain != "" {
				san := sanitizeFilename(e.Domain)
				domainCell = fmt.Sprintf("[[state-domains/%s|%s]]", san, e.Domain)
			}
			b.WriteString(fmt.Sprintf("| %s | `%s` | %s |\n", e.Kind, e.Via, domainCell))
		}
	}

	// LLM-inferred ownership (semantic clustering).
	domainIDs := ctx.pkgToDomains[pkg.Name]
	b.WriteString("\n## Owned State Domains\n\n")
	if len(domainIDs) > 0 {
		for _, id := range domainIDs {
			san := sanitizeFilename(id)
			b.WriteString(fmt.Sprintf("- [[state-domains/%s|%s]]\n", san, id))
		}
	} else {
		b.WriteString("_none_\n")
	}

	b.WriteString("\n## Trust Zones\n\n")
	zoneIDs := ctx.pkgToZones[pkg.Name]
	if len(zoneIDs) > 0 {
		for _, id := range zoneIDs {
			san := sanitizeFilename(id)
			b.WriteString(fmt.Sprintf("- [[trust-zones/%s|%s]]\n", san, id))
		}
	} else {
		b.WriteString("_none_\n")
	}

	name := sanitizeFilename(pkg.Name)
	return writeNote(filepath.Join(outputDir, "packages", name+".md"), b.String())
}

// writeStateDomainNote writes state-domains/<id>.md.
//
// Sections:
//   - Owners: LLM-inferred owning packages
//   - Writers: packages that produce write effects in this domain (causal edges)
//   - Readers: packages that produce read effects in this domain (causal edges)
//   - Concurrent access: files in concurrency domains that touch this domain (risk signal)
//   - Aggregate / Representations / Primary Mutators / Primary Readers
func writeStateDomainNote(d StateDomain, ctx vaultCtx, outputDir string) error {
	var b strings.Builder

	b.WriteString(frontmatter("iguana/state-domain"))
	b.WriteString(fmt.Sprintf("# State Domain: %s\n\n", d.ID))
	b.WriteString(d.Description + "\n\n")
	b.WriteString(fmt.Sprintf("**Confidence**: %.2f\n\n", d.Confidence))

	b.WriteString("## Owners\n\n")
	for _, pkg := range d.Owners {
		san := sanitizeFilename(pkg)
		b.WriteString(fmt.Sprintf("- [[packages/%s|%s]]\n", san, pkg))
	}
	if len(d.Owners) == 0 {
		b.WriteString("_none_\n")
	}

	// Causal edges: packages that write to this domain.
	b.WriteString("\n## Writers\n\n")
	writers := ctx.domainWriters[d.ID]
	if len(writers) > 0 {
		for _, pkg := range writers {
			san := sanitizeFilename(pkg)
			b.WriteString(fmt.Sprintf("- [[packages/%s|%s]]\n", san, pkg))
		}
	} else {
		b.WriteString("_none_\n")
	}

	// Causal edges: packages that read from this domain.
	b.WriteString("\n## Readers\n\n")
	readers := ctx.domainReaders[d.ID]
	if len(readers) > 0 {
		for _, pkg := range readers {
			san := sanitizeFilename(pkg)
			b.WriteString(fmt.Sprintf("- [[packages/%s|%s]]\n", san, pkg))
		}
	} else {
		b.WriteString("_none_\n")
	}

	// Risk signal: concurrent code touching this domain.
	concFiles := ctx.domainConcurrentFiles[d.ID]
	if len(concFiles) > 0 {
		b.WriteString("\n## ⚠ Concurrent Access\n\n")
		b.WriteString("These files contain concurrency primitives **and** produce effects in this domain.\n\n")
		for _, f := range concFiles {
			san := sanitizeFilename(f)
			b.WriteString(fmt.Sprintf("- [[concurrency-domains/%s|%s]]\n", san, f))
		}
	}

	b.WriteString("\n## Aggregate\n\n")
	b.WriteString(d.Aggregate + "\n")

	if len(d.Representations) > 0 {
		b.WriteString("\n## Representations\n\n")
		for _, r := range d.Representations {
			b.WriteString(fmt.Sprintf("- `%s`\n", r))
		}
	}

	if len(d.PrimaryMutators) > 0 {
		b.WriteString("\n## Primary Mutators\n\n")
		for _, m := range d.PrimaryMutators {
			b.WriteString(fmt.Sprintf("- `%s`\n", m))
		}
	}

	if len(d.PrimaryReaders) > 0 {
		b.WriteString("\n## Primary Readers\n\n")
		for _, r := range d.PrimaryReaders {
			b.WriteString(fmt.Sprintf("- `%s`\n", r))
		}
	}

	id := sanitizeFilename(d.ID)
	return writeNote(filepath.Join(outputDir, "state-domains", id+".md"), b.String())
}

// writeTrustZoneNote writes trust-zones/<id>.md.
func writeTrustZoneNote(z TrustZone, outputDir string) error {
	var b strings.Builder

	b.WriteString(frontmatter("iguana/trust-zone"))
	b.WriteString(fmt.Sprintf("# Trust Zone: %s\n\n", z.ID))

	b.WriteString("## Packages\n\n")
	for _, pkg := range z.Packages {
		san := sanitizeFilename(pkg)
		b.WriteString(fmt.Sprintf("- [[packages/%s|%s]]\n", san, pkg))
	}
	if len(z.Packages) == 0 {
		b.WriteString("_none_\n")
	}

	if len(z.ExternalVia) > 0 {
		b.WriteString("\n## External Via\n\n")
		for _, via := range z.ExternalVia {
			b.WriteString(fmt.Sprintf("- `%s`\n", via))
		}
	}

	id := sanitizeFilename(z.ID)
	return writeNote(filepath.Join(outputDir, "trust-zones", id+".md"), b.String())
}

// writeConcurrencyDomainNote writes concurrency-domains/<id>.md.
//
// The "Touches State Domains" section creates direct edges to any state domain
// that this concurrent code accesses — the intersection that matters for races.
func writeConcurrencyDomainNote(cd ConcurrencyDomain, ctx vaultCtx, outputDir string) error {
	var b strings.Builder

	b.WriteString(frontmatter("iguana/concurrency-domain"))
	b.WriteString(fmt.Sprintf("# Concurrency Domain: %s\n\n", cd.ID))

	b.WriteString("## Files\n\n")
	for _, f := range cd.Files {
		b.WriteString(fmt.Sprintf("- `%s`\n", f))
	}

	domains := ctx.concurrencyToDomains[cd.ID]
	if len(domains) > 0 {
		b.WriteString("\n## Touches State Domains\n\n")
		for _, domID := range domains {
			san := sanitizeFilename(domID)
			b.WriteString(fmt.Sprintf("- [[state-domains/%s|%s]]\n", san, domID))
		}
	}

	id := sanitizeFilename(cd.ID)
	return writeNote(filepath.Join(outputDir, "concurrency-domains", id+".md"), b.String())
}

// writeEffectsNote writes effects.md — a flat reference table of all effects.
func writeEffectsNote(effects []Effect, outputDir string) error {
	var b strings.Builder

	b.WriteString(frontmatter("iguana/effects"))
	b.WriteString("# Effects\n\n")
	b.WriteString("| Kind | Via | Domain |\n")
	b.WriteString("|------|-----|--------|\n")
	for _, e := range effects {
		domainLink := e.Domain
		if e.Domain != "" {
			san := sanitizeFilename(e.Domain)
			domainLink = fmt.Sprintf("[[state-domains/%s|%s]]", san, e.Domain)
		}
		b.WriteString(fmt.Sprintf("| %s | `%s` | %s |\n", e.Kind, e.Via, domainLink))
	}

	return writeNote(filepath.Join(outputDir, "effects.md"), b.String())
}

// writeOpenQuestionsNote writes open-questions.md.
func writeOpenQuestionsNote(questions []OpenQuestion, outputDir string) error {
	var b strings.Builder

	b.WriteString(frontmatter("iguana/open-questions"))
	b.WriteString("# Open Questions\n\n")
	for _, q := range questions {
		b.WriteString(fmt.Sprintf("## %s\n\n", q.Question))
		if q.RelatedDomain != "" {
			san := sanitizeFilename(q.RelatedDomain)
			b.WriteString(fmt.Sprintf("**Related domain**: [[state-domains/%s|%s]]\n\n", san, q.RelatedDomain))
		}
		if len(q.MissingEvidence) > 0 {
			b.WriteString("**Missing evidence**:\n\n")
			for _, m := range q.MissingEvidence {
				b.WriteString(fmt.Sprintf("- %s\n", m))
			}
			b.WriteString("\n")
		}
	}

	return writeNote(filepath.Join(outputDir, "open-questions.md"), b.String())
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// frontmatter returns a YAML frontmatter block with a single tag.
// Obsidian's graph view "Groups" feature can then color nodes by tag
// (e.g. tag:iguana/state-domain gets one color, tag:iguana/package another).
func frontmatter(tag string) string {
	return "---\ntags:\n  - " + tag + "\n---\n\n"
}

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
