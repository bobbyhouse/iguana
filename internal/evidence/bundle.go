package evidence

// bundle.go — EvidenceBundle type definitions.
//
// An evidence bundle captures four sections derived from static analysis:
//
//	package  — package name and sorted import list
//	symbols  — all top-level declarations (functions, types, vars, consts)
//	calls    — deduplicated, sorted outbound call graph for the file
//	signals  — deterministic boolean heuristics (fs, db, net, concurrency)
//
// Implementation separation (see INVARIANT.md INV-20..22):
//
//	createEvidenceBundle   — pure analysis, no side effects
//	writeEvidenceBundle    — marshals + writes companion .evidence.yaml
//	validateEvidenceBundle — re-hashes file, returns error if stale

// FileMeta holds the path and integrity hash of the analyzed source file.
type FileMeta struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
}

// EvidenceBundle is the top-level container for an evidence bundle.
// Field order matches the desired YAML output order; yaml.v3 respects struct
// field order, so no additional sorting is needed at the top level.
type EvidenceBundle struct {
	Version int         `yaml:"version"`
	File    FileMeta    `yaml:"file"`
	Package PackageMeta `yaml:"package"`
	Symbols Symbols     `yaml:"symbols"`
	Calls   []Call      `yaml:"calls,omitempty"`
	Signals Signals     `yaml:"signals"`
}

// PackageMeta holds the package name and sorted import list.
type PackageMeta struct {
	Name    string   `yaml:"name"`
	Imports []Import `yaml:"imports,omitempty"`
}

// Import represents a single import statement.
// Alias is omitted from YAML when empty (no alias).
type Import struct {
	Path  string `yaml:"path"`
	Alias string `yaml:"alias,omitempty"`
}

// Symbols groups all top-level declarations in the file.
type Symbols struct {
	Functions    []Function `yaml:"functions,omitempty"`
	Types        []TypeDecl `yaml:"types,omitempty"`
	Variables    []VarDecl  `yaml:"variables,omitempty"`
	Constants    []VarDecl  `yaml:"constants,omitempty"`
	Constructors []string   `yaml:"constructors,omitempty"` // INV-49: functions returning package-local types
}

// Function describes a top-level function or method declaration.
type Function struct {
	Name     string   `yaml:"name"`
	Exported bool     `yaml:"exported"`
	Receiver string   `yaml:"receiver,omitempty"` // non-empty for methods
	Params   []string `yaml:"params,omitempty"`
	Returns  []string `yaml:"returns,omitempty"`
}

// FieldDecl describes a single exported field of a struct type.
type FieldDecl struct {
	Name    string `yaml:"name"`
	TypeStr string `yaml:"type"`
}

// TypeDecl describes a top-level type declaration.
type TypeDecl struct {
	Name     string      `yaml:"name"`
	Kind     string      `yaml:"kind"` // "struct" | "interface" | "alias"
	Exported bool        `yaml:"exported"`
	Fields   []FieldDecl `yaml:"fields,omitempty"` // INV-48: struct only, declaration order
}

// VarDecl describes a top-level variable or constant declaration.
type VarDecl struct {
	Name     string `yaml:"name"`
	Exported bool   `yaml:"exported"`
}

// Call represents a single deduplicated outbound function call.
type Call struct {
	From string `yaml:"from"` // enclosing function name
	To   string `yaml:"to"`   // qualified call target
}

// Signals are deterministic boolean heuristics derived from static analysis.
// They are purely syntactic — no runtime inspection is performed.
type Signals struct {
	FSReads     bool `yaml:"fs_reads"`
	FSWrites    bool `yaml:"fs_writes"`
	DBCalls     bool `yaml:"db_calls"`
	NetCalls    bool `yaml:"net_calls"`
	Concurrency bool `yaml:"concurrency"`
	YAMLio      bool `yaml:"yaml_io"` // INV-49: imports yaml library or calls yaml.*
	JSONio      bool `yaml:"json_io"` // INV-49: imports encoding/json or calls json.*
}
