package main

// system_model.go — System model generator (v1).
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
	b "iguana/baml_client"
	"iguana/baml_client/types"
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

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Top-level output types
// ---------------------------------------------------------------------------

// SystemModel is the root output artifact written to system_model.yaml.
// Field order matches desired YAML output order (INV-28: arrays sorted).
type SystemModel struct {
	Version            int                 `yaml:"version"`
	GeneratedAt        string              `yaml:"generated_at"`
	Inputs             ModelInputs         `yaml:"inputs"`
	Inventory          Inventory           `yaml:"inventory"`
	StateDomains       []StateDomain       `yaml:"state_domains,omitempty"`
	Boundaries         Boundaries          `yaml:"boundaries"`
	Effects            []Effect            `yaml:"effects,omitempty"`
	Transitions        []Transition        `yaml:"transitions,omitempty"` // empty in v1
	TrustZones         []TrustZone         `yaml:"trust_zones,omitempty"`
	ConcurrencyDomains []ConcurrencyDomain `yaml:"concurrency_domains,omitempty"`
	OpenQuestions      []OpenQuestion      `yaml:"open_questions,omitempty"`
}

// ModelInputs records provenance of the model (INV-31).
type ModelInputs struct {
	BundleSetSHA256 string `yaml:"bundle_set_sha256"`
}

// ---------------------------------------------------------------------------
// Inventory
// ---------------------------------------------------------------------------

// Inventory groups all packages found in the analyzed root.
type Inventory struct {
	Packages    []PackageEntry `yaml:"packages,omitempty"`
	Entrypoints []Entrypoint   `yaml:"entrypoints,omitempty"`
}

// PackageEntry represents one Go package in the inventory.
type PackageEntry struct {
	Name         string   `yaml:"name"`
	Files        []string `yaml:"files,omitempty"`
	EvidenceRefs []string `yaml:"evidence_refs,omitempty"`
}

// Entrypoint identifies a package+symbol that is a program entry point
// (package main with a main function).
type Entrypoint struct {
	Package      string   `yaml:"package"`
	Symbol       string   `yaml:"symbol"`
	EvidenceRefs []string `yaml:"evidence_refs,omitempty"`
}

// ---------------------------------------------------------------------------
// State domains (inferred)
// ---------------------------------------------------------------------------

// StateDomain is a logical cluster of related state (inferred by LLM).
type StateDomain struct {
	ID              string       `yaml:"id"`
	Description     string       `yaml:"description"`
	Owners          []string     `yaml:"owners,omitempty"`
	Aggregate       string       `yaml:"aggregate"`                   // primary concept name
	Representations []string     `yaml:"representations,omitempty"`   // 1-3 related type names
	PrimaryMutators []string     `yaml:"primary_mutators,omitempty"`  // deduped write functions
	PrimaryReaders  []string     `yaml:"primary_readers,omitempty"`   // deduped read functions
	Persistence     *Persistence `yaml:"persistence,omitempty"`
	EvidenceRefs    []string     `yaml:"evidence_refs,omitempty"`
	Confidence      float64      `yaml:"confidence"`
}

// Persistence describes how a state domain is persisted (derived from signals).
type Persistence struct {
	Kind         string   `yaml:"kind"` // "db" | "fs" | "memory"
	EvidenceRefs []string `yaml:"evidence_refs,omitempty"`
}

// ---------------------------------------------------------------------------
// Boundaries
// ---------------------------------------------------------------------------

// Boundaries groups process, persistence, and network boundary information.
type Boundaries struct {
	Process     []ProcessBoundary     `yaml:"process,omitempty"`
	Persistence []PersistenceBoundary `yaml:"persistence,omitempty"`
	Network     *NetworkBoundary      `yaml:"network,omitempty"`
}

// ProcessBoundary describes a subprocess or command boundary.
type ProcessBoundary struct {
	Kind         string   `yaml:"kind"`
	EvidenceRefs []string `yaml:"evidence_refs,omitempty"`
}

// PersistenceBoundary describes a storage system used by the codebase.
type PersistenceBoundary struct {
	Kind         string      `yaml:"kind"` // "db" | "fs"
	Writers      []SymbolRef `yaml:"writers,omitempty"`
	EvidenceRefs []string    `yaml:"evidence_refs,omitempty"`
}

// NetworkBoundary describes outbound network usage.
type NetworkBoundary struct {
	Outbound     []SymbolRef `yaml:"outbound,omitempty"`
	EvidenceRefs []string    `yaml:"evidence_refs,omitempty"`
}

// SymbolRef points to a source file (with optional symbol fragment).
type SymbolRef struct {
	File         string   `yaml:"file"`
	EvidenceRefs []string `yaml:"evidence_refs,omitempty"`
}

// ---------------------------------------------------------------------------
// Effects
// ---------------------------------------------------------------------------

// Effect represents a side-effect kind observed at a symbol site.
type Effect struct {
	Kind         string   `yaml:"kind"`             // "db_write" | "fs_read" | "fs_write" | "net_call"
	Domain       string   `yaml:"domain,omitempty"` // state domain this effect belongs to (linked post-LLM)
	Via          string   `yaml:"via"`              // file path where the effect originates
	EvidenceRefs []string `yaml:"evidence_refs,omitempty"`
}

// ---------------------------------------------------------------------------
// Transitions (empty in v1)
// ---------------------------------------------------------------------------

// Transition is reserved for v2 (call-graph-based state transitions).
type Transition struct {
	From         string   `yaml:"from"`
	To           string   `yaml:"to"`
	EvidenceRefs []string `yaml:"evidence_refs,omitempty"`
}

// ---------------------------------------------------------------------------
// Trust zones (inferred)
// ---------------------------------------------------------------------------

// TrustZone is a group of packages at the same security boundary.
type TrustZone struct {
	ID           string   `yaml:"id"`
	Packages     []string `yaml:"packages,omitempty"`
	ExternalVia  []string `yaml:"external_via,omitempty"`
	EvidenceRefs []string `yaml:"evidence_refs,omitempty"`
}

// ---------------------------------------------------------------------------
// Concurrency domains
// ---------------------------------------------------------------------------

// ConcurrencyDomain identifies a file with concurrent code.
type ConcurrencyDomain struct {
	ID           string   `yaml:"id"`
	Files        []string `yaml:"files,omitempty"`
	EvidenceRefs []string `yaml:"evidence_refs,omitempty"`
}

// ---------------------------------------------------------------------------
// Open questions (inferred)
// ---------------------------------------------------------------------------

// OpenQuestion captures something static analysis could not determine.
type OpenQuestion struct {
	Question        string   `yaml:"question"`
	RelatedDomain   string   `yaml:"related_domain,omitempty"`
	MissingEvidence []string `yaml:"missing_evidence,omitempty"`
}

// ---------------------------------------------------------------------------
// Evidence ref helper (INV-30)
// ---------------------------------------------------------------------------

// evidenceRef formats a reference per spec:
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
func loadEvidenceBundles(root string) ([]*EvidenceBundleV2, error) {
	var bundles []*EvidenceBundleV2

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "vendor" || name == "testdata" || name == "examples" || name == "docs" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".evidence.yaml") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		var bundle EvidenceBundleV2
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
func computeBundleSetHash(bundles []*EvidenceBundleV2) string {
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

func hasSymbol(bundle *EvidenceBundleV2, name string) bool {
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
func buildInventory(bundles []*EvidenceBundleV2) Inventory {
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

	var entries []PackageEntry
	var entrypoints []Entrypoint

	for _, name := range pkgNames {
		files := pkgFiles[name]
		refs := pkgRefs[name]
		sort.Strings(files)
		sort.Strings(refs)

		entries = append(entries, PackageEntry{
			Name:         name,
			Files:        files,
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
func buildBoundaries(bundles []*EvidenceBundleV2) Boundaries {
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
func buildEffects(bundles []*EvidenceBundleV2) []Effect {
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
func buildConcurrencyDomains(bundles []*EvidenceBundleV2) []ConcurrencyDomain {
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

// buildPackageSummaries groups bundles by package, ORs signals, collects
// types/funcs/imports (capped at 10), and filters to packages with ≥1 signal.
// At most 60 packages are sent to the LLM.
func buildPackageSummaries(bundles []*EvidenceBundleV2) []types.PackageSummary {
	type pkgAccum struct {
		files     []string
		types     map[string]bool
		functions map[string]bool
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
				functions: make(map[string]bool),
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

		// Collect exported types.
		for _, td := range bnd.Symbols.Types {
			if td.Exported {
				a.types[td.Name] = true
			}
		}
		// Collect exported functions.
		for _, fn := range bnd.Symbols.Functions {
			if fn.Exported {
				a.functions[fn.Name] = true
			}
		}
		// Collect imports.
		for _, imp := range bnd.Package.Imports {
			a.imports[imp.Path] = true
		}
	}

	// Build sorted package names.
	pkgNames := make([]string, 0, len(accum))
	for name := range accum {
		pkgNames = append(pkgNames, name)
	}
	sort.Strings(pkgNames)

	// top10 returns a sorted, capped slice from a set.
	top10 := func(set map[string]bool) []string {
		items := make([]string, 0, len(set))
		for k := range set {
			items = append(items, k)
		}
		sort.Strings(items)
		if len(items) > 10 {
			items = items[:10]
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

		summaries = append(summaries, types.PackageSummary{
			Name:      name,
			Files:     files,
			Types:     top10(a.types),
			Functions: top10(a.functions),
			Signals:   a.signals,
			Imports:   top10(a.imports),
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
func pkgBundleRefs(bundles []*EvidenceBundleV2, pkgNames []string) []string {
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
func mapStateDomains(specs []types.StateDomainSpec, bundles []*EvidenceBundleV2) []StateDomain {
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
func linkEffectsToDomains(effects []Effect, domains []StateDomain, bundles []*EvidenceBundleV2) {
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
func mapTrustZones(specs []types.TrustZoneSpec, bundles []*EvidenceBundleV2) []TrustZone {
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

	// Step 4: build package summaries for LLM.
	summaries := buildPackageSummaries(bundles)

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
