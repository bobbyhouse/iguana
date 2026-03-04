package model

// types.go — System model type definitions.
//
// All struct types that constitute the system_model.yaml output artifact.
// See INVARIANT.md INV-27..31.

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
	Imports      []string `yaml:"imports,omitempty"` // internal package dependencies (by name)
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
