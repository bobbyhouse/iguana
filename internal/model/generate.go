package model

// generate.go — System model generator (v1).
//
// Aggregates evidence bundles from a directory tree into a single YAML artifact
// (system_model.yaml) that answers "what kind of system is this?"
//
// Two halves:
//   - Deterministic: inventory, boundaries, effects, concurrency (no LLM)
//   - Inferred:      state domains, trust zones, open questions (LLM via BAML)
//
// See INVARIANT.md INV-27..31.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	b "iguana/baml_client"
	"iguana/baml_client/types"
	"iguana/internal/evidence"
	"iguana/internal/settings"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Evidence ref helper (INV-30)
// ---------------------------------------------------------------------------

// evidenceRef formats a reference per spec (INV-30):
//
//	bundle:<path>@v<version>[#symbol:<name>|#signal:<name>]
func evidenceRef(path string, version int, fragment string) string {
	base := fmt.Sprintf("bundle:%s@v%d", path, version)
	if fragment != "" {
		return base + "#" + fragment
	}
	return base
}

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------

// loadEvidenceBundles walks root for *.evidence.yaml files, unmarshals each,
// and returns them sorted by File.Path (INV-31 requires deterministic hash).
func loadEvidenceBundles(root string) ([]*evidence.EvidenceBundle, error) {
	settings, err := settings.LoadSettings(root)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	var bundles []*evidence.EvidenceBundle

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "vendor" || name == "testdata" || name == "examples" || name == "docs" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			// Skip directories denied by settings (INV-39).
			if path != root {
				rel, _ := filepath.Rel(root, path)
				if settings.IsDenied(filepath.ToSlash(rel)) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".evidence.yaml") {
			return nil
		}
		// Skip test evidence bundles (INV-24: test files are not analyzed).
		if strings.HasSuffix(d.Name(), "_test.go.evidence.yaml") {
			return nil
		}
		// Skip evidence bundles whose source file is denied by settings (INV-39).
		// Bundle File.Path is relative with forward slashes (INV-23).
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if settings.IsDenied(rel) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		var bundle evidence.EvidenceBundle
		if err := yaml.Unmarshal(data, &bundle); err != nil {
			return fmt.Errorf("unmarshal %s: %w", path, err)
		}
		bundles = append(bundles, &bundle)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}

	// Sort by File.Path for determinism (INV-31).
	sort.Slice(bundles, func(i, j int) bool {
		return bundles[i].File.Path < bundles[j].File.Path
	})
	return bundles, nil
}

// ---------------------------------------------------------------------------
// Bundle set hash
// ---------------------------------------------------------------------------

// computeBundleSetHash computes a deterministic SHA256 over the set of bundles
// by hashing the sorted "path@sha256" lines (INV-31).
func computeBundleSetHash(bundles []*evidence.EvidenceBundle) string {
	lines := make([]string, len(bundles))
	for i, b := range bundles {
		lines[i] = b.File.Path + "@" + b.File.SHA256
	}
	sort.Strings(lines)
	combined := strings.Join(lines, "\n")
	sum := sha256.Sum256([]byte(combined))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// hasSymbol checks if a bundle contains a symbol with the given name.
// ---------------------------------------------------------------------------

func hasSymbol(bundle *evidence.EvidenceBundle, name string) bool {
	for _, fn := range bundle.Symbols.Functions {
		if fn.Name == name {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Deterministic builders
// ---------------------------------------------------------------------------

// buildInventory groups bundles by package name, assembles PackageEntry slices,
// and identifies entrypoints (package main + main function).
func buildInventory(bundles []*evidence.EvidenceBundle) Inventory {
	// Group bundles by package name.
	pkgFiles := make(map[string][]string)
	pkgRefs := make(map[string][]string)

	for _, bnd := range bundles {
		pkg := bnd.Package.Name
		pkgFiles[pkg] = append(pkgFiles[pkg], bnd.File.Path)
		pkgRefs[pkg] = append(pkgRefs[pkg], evidenceRef(bnd.File.Path, bnd.Version, ""))
	}

	// Sort package names (INV-28).
	pkgNames := make([]string, 0, len(pkgFiles))
	for name := range pkgFiles {
		pkgNames = append(pkgNames, name)
	}
	sort.Strings(pkgNames)

	// Build set of known package names for import matching.
	pkgNameSet := make(map[string]bool, len(pkgNames))
	for _, name := range pkgNames {
		pkgNameSet[name] = true
	}

	// Collect internal imports per package: for each import path, the last
	// path segment is matched against known package names. This identifies
	// intra-codebase dependencies (e.g. "iguana/store" → "store").
	pkgImports := make(map[string]map[string]bool)
	for _, bnd := range bundles {
		name := bnd.Package.Name
		for _, imp := range bnd.Package.Imports {
			parts := strings.Split(imp.Path, "/")
			dep := parts[len(parts)-1]
			if pkgNameSet[dep] && dep != name {
				if pkgImports[name] == nil {
					pkgImports[name] = make(map[string]bool)
				}
				pkgImports[name][dep] = true
			}
		}
	}

	var entries []PackageEntry
	var entrypoints []Entrypoint

	for _, name := range pkgNames {
		files := pkgFiles[name]
		refs := pkgRefs[name]
		sort.Strings(files)
		sort.Strings(refs)

		var imports []string
		for dep := range pkgImports[name] {
			imports = append(imports, dep)
		}
		sort.Strings(imports)

		entries = append(entries, PackageEntry{
			Name:         name,
			Files:        files,
			Imports:      imports,
			EvidenceRefs: refs,
		})

		// Entrypoints: package main with a main function.
		if name == "main" {
			for _, bnd := range bundles {
				if bnd.Package.Name == "main" && hasSymbol(bnd, "main") {
					entrypoints = append(entrypoints, Entrypoint{
						Package: bnd.Package.Name,
						Symbol:  "main",
						EvidenceRefs: []string{
							evidenceRef(bnd.File.Path, bnd.Version, "symbol:main"),
						},
					})
				}
			}
		}
	}

	return Inventory{
		Packages:    entries,
		Entrypoints: entrypoints,
	}
}

// buildBoundaries derives persistence and network boundaries from signals.
func buildBoundaries(bundles []*evidence.EvidenceBundle) Boundaries {
	var dbWriters []SymbolRef
	var fsWriters []SymbolRef
	var outbound []SymbolRef

	for _, bnd := range bundles {
		if bnd.Signals.DBCalls {
			dbWriters = append(dbWriters, SymbolRef{
				File: bnd.File.Path,
				EvidenceRefs: []string{
					evidenceRef(bnd.File.Path, bnd.Version, "signal:db_calls"),
				},
			})
		}
		if bnd.Signals.FSWrites {
			fsWriters = append(fsWriters, SymbolRef{
				File: bnd.File.Path,
				EvidenceRefs: []string{
					evidenceRef(bnd.File.Path, bnd.Version, "signal:fs_writes"),
				},
			})
		}
		if bnd.Signals.NetCalls {
			outbound = append(outbound, SymbolRef{
				File: bnd.File.Path,
				EvidenceRefs: []string{
					evidenceRef(bnd.File.Path, bnd.Version, "signal:net_calls"),
				},
			})
		}
	}

	var bnd Boundaries

	if len(dbWriters) > 0 {
		bnd.Persistence = append(bnd.Persistence, PersistenceBoundary{
			Kind:    "db",
			Writers: dbWriters,
		})
	}
	if len(fsWriters) > 0 {
		bnd.Persistence = append(bnd.Persistence, PersistenceBoundary{
			Kind:    "fs",
			Writers: fsWriters,
		})
	}
	if len(outbound) > 0 {
		bnd.Network = &NetworkBoundary{Outbound: outbound}
	}

	return bnd
}

// buildEffects produces one Effect per signal kind per file.
// Effects are sorted by kind then from_file (INV-28).
func buildEffects(bundles []*evidence.EvidenceBundle) []Effect {
	var effects []Effect

	for _, bnd := range bundles {
		if bnd.Signals.DBCalls {
			effects = append(effects, Effect{
				Kind: "db_write",
				Via:  bnd.File.Path,
				EvidenceRefs: []string{
					evidenceRef(bnd.File.Path, bnd.Version, "signal:db_calls"),
				},
			})
		}
		if bnd.Signals.FSReads {
			effects = append(effects, Effect{
				Kind: "fs_read",
				Via:  bnd.File.Path,
				EvidenceRefs: []string{
					evidenceRef(bnd.File.Path, bnd.Version, "signal:fs_reads"),
				},
			})
		}
		if bnd.Signals.FSWrites {
			effects = append(effects, Effect{
				Kind: "fs_write",
				Via:  bnd.File.Path,
				EvidenceRefs: []string{
					evidenceRef(bnd.File.Path, bnd.Version, "signal:fs_writes"),
				},
			})
		}
		if bnd.Signals.NetCalls {
			effects = append(effects, Effect{
				Kind: "net_call",
				Via:  bnd.File.Path,
				EvidenceRefs: []string{
					evidenceRef(bnd.File.Path, bnd.Version, "signal:net_calls"),
				},
			})
		}
	}

	// Sort by kind then via (INV-28).
	sort.Slice(effects, func(i, j int) bool {
		if effects[i].Kind != effects[j].Kind {
			return effects[i].Kind < effects[j].Kind
		}
		return effects[i].Via < effects[j].Via
	})
	return effects
}

// buildConcurrencyDomains collects one domain per file with concurrency signals.
func buildConcurrencyDomains(bundles []*evidence.EvidenceBundle) []ConcurrencyDomain {
	var domains []ConcurrencyDomain

	for _, bnd := range bundles {
		if !bnd.Signals.Concurrency {
			continue
		}
		id := bnd.File.Path
		domains = append(domains, ConcurrencyDomain{
			ID:    id,
			Files: []string{bnd.File.Path},
			EvidenceRefs: []string{
				evidenceRef(bnd.File.Path, bnd.Version, "signal:concurrency"),
			},
		})
	}

	// Sort by id (INV-28).
	sort.Slice(domains, func(i, j int) bool {
		return domains[i].ID < domains[j].ID
	})
	return domains
}

// ---------------------------------------------------------------------------
// Package summaries for LLM
// ---------------------------------------------------------------------------

// readModuleName reads the module name from go.mod in root.
// Returns "" if go.mod is absent or unparseable.
func readModuleName(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.SplitN(string(data), "\n", 10) {
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// formatStructDesc returns a compact description of a struct type for the LLM:
// "TypeName{Field1:Type1, Field2:Type2}"
func formatStructDesc(td evidence.TypeDecl) string {
	if td.Kind != "struct" || len(td.Fields) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(td.Name)
	sb.WriteString("{")
	for i, f := range td.Fields {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(f.Name)
		sb.WriteString(":")
		sb.WriteString(f.TypeStr)
	}
	sb.WriteString("}")
	return sb.String()
}

// formatFuncDesc returns a compact description of a function for the LLM:
// "FuncName(Type1, Type2) ReturnType" or "(Type1, Type2)" for multi-return.
func formatFuncDesc(fn evidence.Function) string {
	if !fn.Exported || fn.Receiver != "" {
		return "" // skip unexported and methods; focus on top-level functions
	}
	var sb strings.Builder
	sb.WriteString(fn.Name)
	sb.WriteString("(")
	for i, p := range fn.Params {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(p)
	}
	sb.WriteString(")")
	if len(fn.Returns) == 1 {
		sb.WriteString(" ")
		sb.WriteString(fn.Returns[0])
	} else if len(fn.Returns) > 1 {
		sb.WriteString(" (")
		for i, r := range fn.Returns {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(r)
		}
		sb.WriteString(")")
	}
	return sb.String()
}

// buildPackageSummaries groups bundles by package, ORs signals, collects
// types/funcs/imports (capped at 10), and filters to packages with ≥1 signal.
// At most 60 packages are sent to the LLM.
func buildPackageSummaries(bundles []*evidence.EvidenceBundle, s *settings.Settings, moduleName string) []types.PackageSummary {
	type pkgAccum struct {
		files     []string
		types     map[string]bool
		typeDescs map[string]bool // formatted struct descriptions
		functions map[string]bool
		funcDescs map[string]bool // formatted function signatures
		imports   map[string]bool
		signals   types.PackageSignals
	}

	accum := make(map[string]*pkgAccum)
	for _, bnd := range bundles {
		name := bnd.Package.Name
		a, ok := accum[name]
		if !ok {
			a = &pkgAccum{
				types:     make(map[string]bool),
				typeDescs: make(map[string]bool),
				functions: make(map[string]bool),
				funcDescs: make(map[string]bool),
				imports:   make(map[string]bool),
			}
			accum[name] = a
		}
		a.files = append(a.files, bnd.File.Path)

		// OR signals.
		if bnd.Signals.FSReads {
			a.signals.Fs_reads = true
		}
		if bnd.Signals.FSWrites {
			a.signals.Fs_writes = true
		}
		if bnd.Signals.DBCalls {
			a.signals.Db_calls = true
		}
		if bnd.Signals.NetCalls {
			a.signals.Net_calls = true
		}
		if bnd.Signals.Concurrency {
			a.signals.Concurrency = true
		}

		// Collect exported types and their struct field descriptions.
		for _, td := range bnd.Symbols.Types {
			if td.Exported {
				a.types[td.Name] = true
				if desc := formatStructDesc(td); desc != "" {
					a.typeDescs[desc] = true
				}
			}
		}
		// Collect exported top-level functions and their signatures.
		for _, fn := range bnd.Symbols.Functions {
			if fn.Exported {
				a.functions[fn.Name] = true
			}
			if desc := formatFuncDesc(fn); desc != "" {
				a.funcDescs[desc] = true
			}
		}
		// Collect imports, skipping any that resolve to a denied local path.
		// Import paths like "iguana/baml_client" are stripped of the module
		// prefix to get "baml_client", then checked against the deny list.
		for _, imp := range bnd.Package.Imports {
			rel := imp.Path
			if moduleName != "" {
				rel = strings.TrimPrefix(imp.Path, moduleName+"/")
			}
			if s.IsDenied(rel) {
				continue
			}
			a.imports[imp.Path] = true
		}
	}

	// Build sorted package names.
	pkgNames := make([]string, 0, len(accum))
	for name := range accum {
		pkgNames = append(pkgNames, name)
	}
	sort.Strings(pkgNames)

	// topN returns a sorted, capped slice from a set.
	topN := func(set map[string]bool, n int) []string {
		items := make([]string, 0, len(set))
		for k := range set {
			items = append(items, k)
		}
		sort.Strings(items)
		if len(items) > n {
			items = items[:n]
		}
		return items
	}

	hasAnySignal := func(s types.PackageSignals) bool {
		return s.Fs_reads || s.Fs_writes || s.Db_calls || s.Net_calls || s.Concurrency
	}

	var summaries []types.PackageSummary
	for _, name := range pkgNames {
		a := accum[name]
		if !hasAnySignal(a.signals) {
			continue
		}
		files := append([]string(nil), a.files...)
		sort.Strings(files)

		// Merge struct descriptions and function signatures into one sorted slice.
		allDescs := append(topN(a.typeDescs, 30), topN(a.funcDescs, 20)...)
		sort.Strings(allDescs)

		summaries = append(summaries, types.PackageSummary{
			Name:              name,
			Files:             files,
			Types:             topN(a.types, 30),
			Type_descriptions: allDescs,
			Functions:         topN(a.functions, 10),
			Signals:           a.signals,
			Imports:           topN(a.imports, 10),
		})
	}

	// Cap at 60 (INV: keep LLM prompt manageable).
	if len(summaries) > 60 {
		summaries = summaries[:60]
	}
	return summaries
}

// ---------------------------------------------------------------------------
// LLM output mapping
// ---------------------------------------------------------------------------

// pkgBundleRefs returns evidence refs for all bundles belonging to the given
// package names.
func pkgBundleRefs(bundles []*evidence.EvidenceBundle, pkgNames []string) []string {
	pkgSet := make(map[string]bool, len(pkgNames))
	for _, p := range pkgNames {
		pkgSet[p] = true
	}
	var refs []string
	for _, bnd := range bundles {
		if pkgSet[bnd.Package.Name] {
			refs = append(refs, evidenceRef(bnd.File.Path, bnd.Version, ""))
		}
	}
	sort.Strings(refs)
	return refs
}

// mapStateDomains converts LLM StateDomainSpec slices to Go StateDomain slices.
func mapStateDomains(specs []types.StateDomainSpec, bundles []*evidence.EvidenceBundle) []StateDomain {
	var domains []StateDomain
	for _, spec := range specs {
		refs := pkgBundleRefs(bundles, spec.Owners)
		domains = append(domains, StateDomain{
			ID:              spec.Id,
			Description:     spec.Description,
			Owners:          sortedCopy(spec.Owners),
			Aggregate:       spec.Aggregate,
			Representations: sortedCopy(spec.Representations),
			PrimaryMutators: sortedCopy(spec.Primary_mutators),
			PrimaryReaders:  sortedCopy(spec.Primary_readers),
			EvidenceRefs:    refs,
			Confidence:      spec.Confidence,
		})
	}
	// Sort by ID (INV-28).
	sort.Slice(domains, func(i, j int) bool {
		return domains[i].ID < domains[j].ID
	})
	return domains
}

// linkEffectsToDomains annotates each effect's Domain field by resolving
// file → package → domain owner. Effects with no matching domain are left
// with an empty Domain field.
func linkEffectsToDomains(effects []Effect, domains []StateDomain, bundles []*evidence.EvidenceBundle) {
	// Build file path → package name.
	fileToPkg := make(map[string]string, len(bundles))
	for _, b := range bundles {
		fileToPkg[b.File.Path] = b.Package.Name
	}
	// Build package name → domain ID (first owner wins).
	pkgToDomain := make(map[string]string)
	for _, d := range domains {
		for _, pkg := range d.Owners {
			if _, exists := pkgToDomain[pkg]; !exists {
				pkgToDomain[pkg] = d.ID
			}
		}
	}
	for i := range effects {
		pkg := fileToPkg[effects[i].Via]
		effects[i].Domain = pkgToDomain[pkg]
	}
}

// mapTrustZones converts LLM TrustZoneSpec slices to Go TrustZone slices.
func mapTrustZones(specs []types.TrustZoneSpec, bundles []*evidence.EvidenceBundle) []TrustZone {
	var zones []TrustZone
	for _, spec := range specs {
		refs := pkgBundleRefs(bundles, spec.Packages)
		zones = append(zones, TrustZone{
			ID:           spec.Id,
			Packages:     sortedCopy(spec.Packages),
			ExternalVia:  sortedCopy(spec.External_via),
			EvidenceRefs: refs,
		})
	}
	// Sort by ID (INV-28).
	sort.Slice(zones, func(i, j int) bool {
		return zones[i].ID < zones[j].ID
	})
	return zones
}

// mapOpenQuestions converts LLM OpenQuestionSpec slices to Go OpenQuestion slices.
func mapOpenQuestions(specs []types.OpenQuestionSpec) []OpenQuestion {
	var questions []OpenQuestion
	for _, spec := range specs {
		questions = append(questions, OpenQuestion{
			Question:        spec.Question,
			RelatedDomain:   spec.Related_domain,
			MissingEvidence: sortedCopy(spec.Missing_evidence),
		})
	}
	// Sort by question text (INV-28).
	sort.Slice(questions, func(i, j int) bool {
		return questions[i].Question < questions[j].Question
	})
	return questions
}

// sortedCopy returns a sorted copy of a string slice (nil-safe).
func sortedCopy(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Main orchestration
// ---------------------------------------------------------------------------

// GenerateSystemModel orchestrates: load → compute → build deterministic →
// build summaries → LLM → assemble. Returns the assembled *SystemModel.
func GenerateSystemModel(ctx context.Context, root string) (*SystemModel, error) {
	// Step 1: load all evidence bundles.
	bundles, err := loadEvidenceBundles(root)
	if err != nil {
		return nil, fmt.Errorf("load bundles: %w", err)
	}
	if len(bundles) == 0 {
		return nil, fmt.Errorf("no evidence bundles found in %s (run iguana on the directory first)", root)
	}

	// Step 2: compute bundle set hash.
	bundleSetHash := computeBundleSetHash(bundles)

	// Step 3: build deterministic sections.
	inventory := buildInventory(bundles)
	boundaries := buildBoundaries(bundles)
	effects := buildEffects(bundles)
	concurrencyDomains := buildConcurrencyDomains(bundles)

	// Step 4: build package summaries for LLM, filtering denied imports so
	// the LLM does not wonder about packages it has no evidence for.
	s, _ := settings.LoadSettings(root) // nil settings = no filtering
	mod := readModuleName(root)
	summaries := buildPackageSummaries(bundles, s, mod)

	// Step 5: call LLM (skip if no summaries — nothing with signals).
	var stateDomains []StateDomain
	var trustZones []TrustZone
	var openQuestions []OpenQuestion

	if len(summaries) > 0 {
		inference, err := b.InferSystemModel(ctx, summaries)
		if err != nil {
			return nil, fmt.Errorf("infer system model: %w", err)
		}
		stateDomains = mapStateDomains(inference.State_domains, bundles)
		trustZones = mapTrustZones(inference.Trust_zones, bundles)
		openQuestions = mapOpenQuestions(inference.Open_questions)
		// Annotate effects with their owning domain (requires LLM output).
		linkEffectsToDomains(effects, stateDomains, bundles)
	}

	return &SystemModel{
		Version:     1,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Inputs: ModelInputs{
			BundleSetSHA256: bundleSetHash,
		},
		Inventory:          inventory,
		StateDomains:       stateDomains,
		Boundaries:         boundaries,
		Effects:            effects,
		ConcurrencyDomains: concurrencyDomains,
		TrustZones:         trustZones,
		OpenQuestions:      openQuestions,
	}, nil
}
