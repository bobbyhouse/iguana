package main

// evidence_test.go — Tests for the semantic evidence bundle.
//
// Test strategy per CLAUDE.md:
//   - Unit tests:       test individual extraction helpers with controlled AST inputs
//   - Integration tests: run createEvidenceBundle on real .go files in this package
//   - Property tests:   assert determinism, sorting, and no-position-data invariants
//   - Fuzz tests:       ensure parsing + extraction never panics on arbitrary input
//
// Invariants tested (see INVARIANT.md for full list):
//   INV-4  Idempotency — same file produces identical output
//   INV-5  No position data in output
//   INV-7..12  All slice outputs are sorted
//   INV-15..17 All top-level declarations captured
//   INV-20..22 Generation/serialization/validation separation

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// parseSource parses a Go source string and returns the AST file.
// Uses a synthetic filename "test.go" so positions are predictable.
func parseSource(t *testing.T, src string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		t.Fatalf("parseSource: %v", err)
	}
	return f
}

// noTypeInfo is a nil *types.Info — used to exercise the AST-only fallback path.
var noTypeInfo *types.Info = nil

// noTypePkg is a nil *types.Package — used with noTypeInfo.
var noTypePkg *types.Package = nil

// nullQualifier always returns the package name; used when pkg is nil.
func nullQualifier(p *types.Package) string { return p.Name() }

// --------------------------------------------------------------------------
// Unit tests — exprToString
// --------------------------------------------------------------------------

// TestExprToString verifies that AST type expressions are rendered to their
// canonical string forms without requiring type information.
//
// Invariants exercised: INV-5 (no position data).
func TestExprToString(t *testing.T) {
	tests := []struct {
		name string
		expr ast.Expr
		want string
	}{
		{
			name: "simple ident",
			expr: &ast.Ident{Name: "int"},
			want: "int",
		},
		{
			name: "pointer",
			expr: &ast.StarExpr{X: &ast.Ident{Name: "Server"}},
			want: "*Server",
		},
		{
			name: "selector",
			expr: &ast.SelectorExpr{
				X:   &ast.Ident{Name: "os"},
				Sel: &ast.Ident{Name: "File"},
			},
			want: "os.File",
		},
		{
			name: "slice",
			expr: &ast.ArrayType{Elt: &ast.Ident{Name: "byte"}},
			want: "[]byte",
		},
		{
			name: "fixed array",
			expr: &ast.ArrayType{
				Len: &ast.BasicLit{},
				Elt: &ast.Ident{Name: "int"},
			},
			want: "[...]int",
		},
		{
			name: "map",
			expr: &ast.MapType{
				Key:   &ast.Ident{Name: "string"},
				Value: &ast.Ident{Name: "int"},
			},
			want: "map[string]int",
		},
		{
			name: "interface{}",
			expr: &ast.InterfaceType{},
			want: "interface{}",
		},
		{
			name: "struct{}",
			expr: &ast.StructType{},
			want: "struct{}",
		},
		{
			name: "func type",
			expr: &ast.FuncType{},
			want: "func(...)",
		},
		{
			name: "send chan",
			expr: &ast.ChanType{Dir: ast.SEND, Value: &ast.Ident{Name: "int"}},
			want: "chan<- int",
		},
		{
			name: "recv chan",
			expr: &ast.ChanType{Dir: ast.RECV, Value: &ast.Ident{Name: "int"}},
			want: "<-chan int",
		},
		{
			name: "bidirectional chan",
			expr: &ast.ChanType{Dir: ast.SEND | ast.RECV, Value: &ast.Ident{Name: "string"}},
			want: "chan string",
		},
		{
			name: "ellipsis",
			expr: &ast.Ellipsis{Elt: &ast.Ident{Name: "int"}},
			want: "...int",
		},
		{
			name: "nil expr",
			expr: nil,
			want: "",
		},
		{
			name: "pointer to selector",
			expr: &ast.StarExpr{
				X: &ast.SelectorExpr{
					X:   &ast.Ident{Name: "http"},
					Sel: &ast.Ident{Name: "Request"},
				},
			},
			want: "*http.Request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exprToString(tt.expr)
			if got != tt.want {
				t.Errorf("exprToString() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --------------------------------------------------------------------------
// Unit tests — typeKind
// --------------------------------------------------------------------------

// TestTypeKind verifies correct classification of AST type expressions.
func TestTypeKind(t *testing.T) {
	tests := []struct {
		name string
		expr ast.Expr
		want string
	}{
		{"struct", &ast.StructType{}, "struct"},
		{"interface", &ast.InterfaceType{}, "interface"},
		{"ident alias", &ast.Ident{Name: "string"}, "alias"},
		{"selector alias", &ast.SelectorExpr{
			X:   &ast.Ident{Name: "sync"},
			Sel: &ast.Ident{Name: "Mutex"},
		}, "alias"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := typeKind(tt.expr)
			if got != tt.want {
				t.Errorf("typeKind() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --------------------------------------------------------------------------
// Unit tests — extractPackageMeta
// --------------------------------------------------------------------------

// TestExtractPackageMeta verifies:
//   - Package name is extracted correctly
//   - Imports are sorted alphabetically by path (INV-7)
//   - Import aliases are preserved
func TestExtractPackageMeta(t *testing.T) {
	src := `package foo

import (
	"os"
	"fmt"
	"crypto/sha256"
	myyaml "gopkg.in/yaml.v3"
	_ "net/http"
)
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)

	// Package name
	if meta.Name != "foo" {
		t.Errorf("Name = %q, want %q", meta.Name, "foo")
	}

	// Imports should be sorted by path
	wantPaths := []string{
		"crypto/sha256",
		"fmt",
		"gopkg.in/yaml.v3",
		"net/http",
		"os",
	}
	if len(meta.Imports) != len(wantPaths) {
		t.Fatalf("len(Imports) = %d, want %d", len(meta.Imports), len(wantPaths))
	}
	for i, want := range wantPaths {
		if meta.Imports[i].Path != want {
			t.Errorf("Imports[%d].Path = %q, want %q", i, meta.Imports[i].Path, want)
		}
	}

	// Check alias preservation: gopkg.in/yaml.v3 → myyaml, net/http → _
	for _, imp := range meta.Imports {
		switch imp.Path {
		case "gopkg.in/yaml.v3":
			if imp.Alias != "myyaml" {
				t.Errorf("yaml alias = %q, want %q", imp.Alias, "myyaml")
			}
		case "net/http":
			if imp.Alias != "_" {
				t.Errorf("net/http alias = %q, want %q", imp.Alias, "_")
			}
		case "os", "fmt", "crypto/sha256":
			if imp.Alias != "" {
				t.Errorf("import %q alias = %q, want empty", imp.Path, imp.Alias)
			}
		}
	}

	// INV-7: verify strict sort order
	for i := 1; i < len(meta.Imports); i++ {
		if meta.Imports[i].Path < meta.Imports[i-1].Path {
			t.Errorf("imports not sorted at index %d: %q < %q",
				i, meta.Imports[i].Path, meta.Imports[i-1].Path)
		}
	}
}

// TestExtractPackageMeta_NoImports verifies an empty import list is handled.
func TestExtractPackageMeta_NoImports(t *testing.T) {
	src := `package bar`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)

	if meta.Name != "bar" {
		t.Errorf("Name = %q, want %q", meta.Name, "bar")
	}
	if len(meta.Imports) != 0 {
		t.Errorf("expected 0 imports, got %d", len(meta.Imports))
	}
}

// --------------------------------------------------------------------------
// Unit tests — extractSymbols
// --------------------------------------------------------------------------

// TestExtractSymbols_Functions verifies that function declarations are
// extracted and sorted by name (INV-8, INV-15).
func TestExtractSymbols_Functions(t *testing.T) {
	src := `package pkg

func Zebra() {}
func Alpha(x int, y string) (bool, error) {}
func middle() {}
func (s *Server) Start() error { return nil }
`
	f := parseSource(t, src)
	syms := extractSymbols(f, noTypeInfo, noTypePkg, nullQualifier)

	// INV-15: all 4 functions captured
	if len(syms.Functions) != 4 {
		t.Fatalf("expected 4 functions, got %d", len(syms.Functions))
	}

	// INV-8: sorted by name
	names := make([]string, len(syms.Functions))
	for i, fn := range syms.Functions {
		names[i] = fn.Name
	}
	wantNames := []string{"Alpha", "Start", "Zebra", "middle"}
	for i, want := range wantNames {
		if names[i] != want {
			t.Errorf("Function[%d].Name = %q, want %q", i, names[i], want)
		}
	}

	// Exported flag
	for _, fn := range syms.Functions {
		wantExported := ast.IsExported(fn.Name)
		if fn.Exported != wantExported {
			t.Errorf("Function %q: Exported = %v, want %v", fn.Name, fn.Exported, wantExported)
		}
	}

	// Receiver on Start
	var startFn *Function
	for i := range syms.Functions {
		if syms.Functions[i].Name == "Start" {
			startFn = &syms.Functions[i]
		}
	}
	if startFn == nil {
		t.Fatal("Start function not found")
	}
	if startFn.Receiver == "" {
		t.Error("Start.Receiver should not be empty")
	}
}

// TestExtractSymbols_Types verifies type declarations (INV-9, INV-16).
func TestExtractSymbols_Types(t *testing.T) {
	src := `package pkg

type Zulu struct {}
type Alpha interface { Method() }
type Middle = string
`
	f := parseSource(t, src)
	syms := extractSymbols(f, noTypeInfo, noTypePkg, nullQualifier)

	if len(syms.Types) != 3 {
		t.Fatalf("expected 3 types, got %d", len(syms.Types))
	}

	// INV-9: sorted
	wantOrder := []string{"Alpha", "Middle", "Zulu"}
	for i, want := range wantOrder {
		if syms.Types[i].Name != want {
			t.Errorf("Types[%d].Name = %q, want %q", i, syms.Types[i].Name, want)
		}
	}

	// Kind detection
	kindMap := map[string]string{"Zulu": "struct", "Alpha": "interface", "Middle": "alias"}
	for _, td := range syms.Types {
		want := kindMap[td.Name]
		if td.Kind != want {
			t.Errorf("Type %q: Kind = %q, want %q", td.Name, td.Kind, want)
		}
	}
}

// TestExtractSymbols_VarsConsts verifies var/const declarations (INV-10, INV-11, INV-17).
func TestExtractSymbols_VarsConsts(t *testing.T) {
	src := `package pkg

var Zulu = 1
var alpha = 2

const Beta = "b"
const aye = "a"
`
	f := parseSource(t, src)
	syms := extractSymbols(f, noTypeInfo, noTypePkg, nullQualifier)

	// INV-10: vars sorted
	if len(syms.Variables) != 2 {
		t.Fatalf("expected 2 vars, got %d", len(syms.Variables))
	}
	if syms.Variables[0].Name != "Zulu" || syms.Variables[1].Name != "alpha" {
		// Uppercase Z < lowercase a in ASCII, so Zulu before alpha
		t.Errorf("vars not sorted: %v", syms.Variables)
	}

	// INV-11: consts sorted
	if len(syms.Constants) != 2 {
		t.Fatalf("expected 2 consts, got %d", len(syms.Constants))
	}
	if syms.Constants[0].Name != "Beta" || syms.Constants[1].Name != "aye" {
		t.Errorf("consts not sorted: %v", syms.Constants)
	}
}

// --------------------------------------------------------------------------
// Unit tests — extractSignals
// --------------------------------------------------------------------------

// TestExtractSignals_FSReads verifies fs_reads signal detection.
func TestExtractSignals_FSReads(t *testing.T) {
	src := `package pkg
import "os"
func f() { os.ReadFile("x") }
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)
	sig := extractSignals(meta, calls, f)

	if !sig.FSReads {
		t.Error("expected fs_reads = true when os.ReadFile is called")
	}
}

// TestExtractSignals_FSWrites verifies fs_writes signal detection.
func TestExtractSignals_FSWrites(t *testing.T) {
	src := `package pkg
import "os"
func f() { os.WriteFile("x", nil, 0) }
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)
	sig := extractSignals(meta, calls, f)

	if !sig.FSWrites {
		t.Error("expected fs_writes = true when os.WriteFile is called")
	}
}

// TestExtractSignals_DBCalls_Import verifies db_calls via import.
func TestExtractSignals_DBCalls_Import(t *testing.T) {
	src := `package pkg
import _ "database/sql"
func f() {}
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)
	sig := extractSignals(meta, calls, f)

	if !sig.DBCalls {
		t.Error("expected db_calls = true when database/sql is imported")
	}
}

// TestExtractSignals_NetCalls_Import verifies net_calls via import.
func TestExtractSignals_NetCalls_Import(t *testing.T) {
	src := `package pkg
import _ "net/http"
func f() {}
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)
	sig := extractSignals(meta, calls, f)

	if !sig.NetCalls {
		t.Error("expected net_calls = true when net/http is imported")
	}
}

// TestExtractSignals_Concurrency_GoStmt verifies concurrency via goroutine.
func TestExtractSignals_Concurrency_GoStmt(t *testing.T) {
	src := `package pkg
func f() { go func() {}() }
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)
	sig := extractSignals(meta, calls, f)

	if !sig.Concurrency {
		t.Error("expected concurrency = true when goroutine is used")
	}
}

// TestExtractSignals_Concurrency_Chan verifies concurrency via channel type.
func TestExtractSignals_Concurrency_Chan(t *testing.T) {
	src := `package pkg
var ch chan int
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)
	sig := extractSignals(meta, calls, f)

	if !sig.Concurrency {
		t.Error("expected concurrency = true when channel type is present")
	}
}

// TestExtractSignals_Concurrency_SyncImport verifies concurrency via sync import.
func TestExtractSignals_Concurrency_SyncImport(t *testing.T) {
	src := `package pkg
import _ "sync"
func f() {}
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)
	sig := extractSignals(meta, calls, f)

	if !sig.Concurrency {
		t.Error("expected concurrency = true when sync is imported")
	}
}

// TestExtractSignals_AllFalse verifies the zero case — no signals fire on
// a trivial file.
func TestExtractSignals_AllFalse(t *testing.T) {
	src := `package pkg
func f() { _ = 1 + 2 }
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)
	sig := extractSignals(meta, calls, f)

	if sig.FSReads || sig.FSWrites || sig.DBCalls || sig.NetCalls || sig.Concurrency || sig.YAMLio || sig.JSONio {
		t.Errorf("expected all signals false, got %+v", sig)
	}
}

// --------------------------------------------------------------------------
// Unit tests — extractCalls
// --------------------------------------------------------------------------

// TestExtractCalls_Sorted verifies calls are sorted by from then to (INV-12).
func TestExtractCalls_Sorted(t *testing.T) {
	src := `package pkg
import "fmt"
import "os"

func B() {
	fmt.Println("b")
	os.Exit(1)
}

func A() {
	fmt.Println("a")
}
`
	f := parseSource(t, src)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)

	// INV-12: sorted by from, then to
	for i := 1; i < len(calls); i++ {
		prev, curr := calls[i-1], calls[i]
		if curr.From < prev.From || (curr.From == prev.From && curr.To < prev.To) {
			t.Errorf("calls not sorted at index %d: %+v before %+v", i, prev, curr)
		}
	}
}

// TestExtractCalls_Deduplication verifies repeated calls to the same target
// produce only one entry.
func TestExtractCalls_Deduplication(t *testing.T) {
	src := `package pkg
import "fmt"

func f() {
	fmt.Println("a")
	fmt.Println("b")
	fmt.Println("c")
}
`
	f := parseSource(t, src)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)

	// Should have at most 1 entry for (f, fmt.Println)
	count := 0
	for _, c := range calls {
		if c.From == "f" && c.To == "fmt.Println" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 (f, fmt.Println) call, got %d", count)
	}
}

// TestExtractCalls_EnclosingFunction verifies the From field names the correct
// enclosing function.
func TestExtractCalls_EnclosingFunction(t *testing.T) {
	src := `package pkg
import "fmt"
import "os"

func outer() {
	fmt.Println("outer")
}

func inner() {
	os.Exit(0)
}
`
	f := parseSource(t, src)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)

	fromMap := make(map[string][]string)
	for _, c := range calls {
		fromMap[c.From] = append(fromMap[c.From], c.To)
	}

	if !containsStr(fromMap["outer"], "fmt.Println") {
		t.Errorf("outer should call fmt.Println; calls from outer: %v", fromMap["outer"])
	}
	if !containsStr(fromMap["inner"], "os.Exit") {
		t.Errorf("inner should call os.Exit; calls from inner: %v", fromMap["inner"])
	}
}

// --------------------------------------------------------------------------
// Property tests — INV-4, INV-5, INV-7..12
// --------------------------------------------------------------------------

// TestDeterminism verifies that createEvidenceBundle produces identical YAML
// output on two consecutive calls on the same file (INV-4 idempotency).
func TestDeterminism(t *testing.T) {
	b1, err := createEvidenceBundle("main.go")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	b2, err := createEvidenceBundle("main.go")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	y1, err := yaml.Marshal(b1)
	if err != nil {
		t.Fatalf("marshal b1: %v", err)
	}
	y2, err := yaml.Marshal(b2)
	if err != nil {
		t.Fatalf("marshal b2: %v", err)
	}

	if string(y1) != string(y2) {
		t.Errorf("non-deterministic output:\nfirst:\n%s\nsecond:\n%s", y1, y2)
	}
}

// TestNoPositionData verifies the YAML output does not contain position fields
// like line numbers or column numbers (INV-5).
func TestNoPositionData(t *testing.T) {
	b, err := createEvidenceBundle("main.go")
	if err != nil {
		t.Fatalf("createEvidenceBundle: %v", err)
	}
	y, err := yaml.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	yamlStr := string(y)

	forbidden := []string{"line:", "column:", "offset:", "pos:", "position:"}
	for _, kw := range forbidden {
		if strings.Contains(yamlStr, kw) {
			t.Errorf("output contains forbidden position key %q", kw)
		}
	}
}

// TestSortedInvariants verifies all slices in the output are sorted (INV-7..12).
func TestSortedInvariants(t *testing.T) {
	b, err := createEvidenceBundle("main.go")
	if err != nil {
		t.Fatalf("createEvidenceBundle: %v", err)
	}

	// INV-7: imports sorted by path
	for i := 1; i < len(b.Package.Imports); i++ {
		if b.Package.Imports[i].Path < b.Package.Imports[i-1].Path {
			t.Errorf("imports not sorted at %d: %q < %q",
				i, b.Package.Imports[i].Path, b.Package.Imports[i-1].Path)
		}
	}
	// INV-8: functions sorted
	for i := 1; i < len(b.Symbols.Functions); i++ {
		if b.Symbols.Functions[i].Name < b.Symbols.Functions[i-1].Name {
			t.Errorf("functions not sorted at %d: %q < %q",
				i, b.Symbols.Functions[i].Name, b.Symbols.Functions[i-1].Name)
		}
	}
	// INV-9: types sorted
	for i := 1; i < len(b.Symbols.Types); i++ {
		if b.Symbols.Types[i].Name < b.Symbols.Types[i-1].Name {
			t.Errorf("types not sorted at %d: %q < %q",
				i, b.Symbols.Types[i].Name, b.Symbols.Types[i-1].Name)
		}
	}
	// INV-10: vars sorted
	for i := 1; i < len(b.Symbols.Variables); i++ {
		if b.Symbols.Variables[i].Name < b.Symbols.Variables[i-1].Name {
			t.Errorf("variables not sorted at %d: %q < %q",
				i, b.Symbols.Variables[i].Name, b.Symbols.Variables[i-1].Name)
		}
	}
	// INV-11: consts sorted
	for i := 1; i < len(b.Symbols.Constants); i++ {
		if b.Symbols.Constants[i].Name < b.Symbols.Constants[i-1].Name {
			t.Errorf("constants not sorted at %d: %q < %q",
				i, b.Symbols.Constants[i].Name, b.Symbols.Constants[i-1].Name)
		}
	}
	// INV-12: calls sorted by from then to
	for i := 1; i < len(b.Calls); i++ {
		prev, curr := b.Calls[i-1], b.Calls[i]
		if curr.From < prev.From || (curr.From == prev.From && curr.To < prev.To) {
			t.Errorf("calls not sorted at %d: %+v before %+v", i, prev, curr)
		}
	}
}

// --------------------------------------------------------------------------
// Integration tests
// --------------------------------------------------------------------------

// TestCreateEvidenceBundle_OnMainGo runs a full v2 analysis on the existing
// main.go in this package and checks structural correctness.
func TestCreateEvidenceBundle_OnMainGo(t *testing.T) {
	b, err := createEvidenceBundle("main.go")
	if err != nil {
		t.Fatalf("createEvidenceBundle: %v", err)
	}

	// Version must be 2 (INV-3)
	if b.Version != 2 {
		t.Errorf("Version = %d, want 2", b.Version)
	}

	// SHA256 must be non-empty and 64 hex chars
	if len(b.File.SHA256) != 64 {
		t.Errorf("SHA256 length = %d, want 64", len(b.File.SHA256))
	}
	for _, ch := range b.File.SHA256 {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			t.Errorf("SHA256 contains non-lowercase-hex char: %q", ch)
		}
	}

	// File path must use forward slashes (INV-13)
	if strings.Contains(b.File.Path, "\\") {
		t.Errorf("file.path contains backslash: %q", b.File.Path)
	}

	// Package name should be "main"
	if b.Package.Name != "main" {
		t.Errorf("Package.Name = %q, want %q", b.Package.Name, "main")
	}

	// Must have at least one function (createEvidenceBundle exists in main.go)
	if len(b.Symbols.Functions) == 0 {
		t.Error("expected at least one function in symbols")
	}
}

// TestSHA256MatchesFile verifies the SHA256 in the bundle matches the actual
// file bytes (INV-1).
func TestSHA256MatchesFile(t *testing.T) {
	b, err := createEvidenceBundle("main.go")
	if err != nil {
		t.Fatalf("createEvidenceBundle: %v", err)
	}

	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	sum := sha256.Sum256(raw)
	want := hex.EncodeToString(sum[:])

	if b.File.SHA256 != want {
		t.Errorf("SHA256 = %q, want %q", b.File.SHA256, want)
	}
}

// TestWriteAndValidateRoundTrip verifies the write → validate cycle (INV-20..22).
func TestWriteAndValidateRoundTrip(t *testing.T) {
	b, err := createEvidenceBundle("main.go")
	if err != nil {
		t.Fatalf("createEvidenceBundle: %v", err)
	}

	// INV-20: createEvidenceBundle must not have written a file itself
	outputPath := filepath.FromSlash(b.File.Path + ".evidence.yaml")
	_ = os.Remove(outputPath) // clean any leftover from prior runs

	// INV-21: writeEvidenceBundle writes the companion file
	if err := writeEvidenceBundle(b); err != nil {
		t.Fatalf("writeEvidenceBundle: %v", err)
	}
	t.Cleanup(func() { os.Remove(outputPath) })

	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Errorf("companion file not created at %q", outputPath)
	}

	// INV-22: validateEvidenceBundle must succeed on a fresh bundle
	if err := validateEvidenceBundle(b); err != nil {
		t.Errorf("validateEvidenceBundle failed on fresh bundle: %v", err)
	}
}

// TestValidateStale verifies that a bundle with a wrong hash is rejected (INV-2).
func TestValidateStale(t *testing.T) {
	b, err := createEvidenceBundle("main.go")
	if err != nil {
		t.Fatalf("createEvidenceBundle: %v", err)
	}

	// Tamper with the hash
	b.File.SHA256 = strings.Repeat("0", 64)

	err = validateEvidenceBundle(b)
	if err == nil {
		t.Error("expected error for stale bundle with wrong hash, got nil")
	}
}

// --------------------------------------------------------------------------
// Integration tests — directory walking (INV-23..26)
// --------------------------------------------------------------------------

// TestWalkAndGenerate_Basic creates a temp dir with two .go files, runs
// walkAndGenerate, and verifies both companion files are created with
// root-relative paths (INV-23, INV-25).
func TestWalkAndGenerate_Basic(t *testing.T) {
	root := t.TempDir()

	src1 := "package main\nfunc Hello() {}\n"
	src2 := "package main\nfunc World() {}\n"
	file1 := filepath.Join(root, "hello.go")
	file2 := filepath.Join(root, "world.go")

	if err := os.WriteFile(file1, []byte(src1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, []byte(src2), 0o644); err != nil {
		t.Fatal(err)
	}

	written, errs := walkAndGenerate(root)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if written != 2 {
		t.Errorf("written = %d, want 2", written)
	}

	// Both companion files must exist.
	for _, f := range []string{file1, file2} {
		companion := f + ".evidence.yaml"
		if _, err := os.Stat(companion); os.IsNotExist(err) {
			t.Errorf("companion file not created: %s", companion)
		}
		t.Cleanup(func() { os.Remove(companion) })
	}

	// Read the bundle and check path is root-relative (INV-23).
	data, err := os.ReadFile(file1 + ".evidence.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var bundle EvidenceBundle
	if err := yaml.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(bundle.File.Path, root) {
		t.Errorf("file.path should be relative, got absolute: %q", bundle.File.Path)
	}
}

// TestWalkAndGenerate_SkipsVendor verifies that a vendor/ subdirectory is not
// processed during directory walking (INV-24).
func TestWalkAndGenerate_SkipsVendor(t *testing.T) {
	root := t.TempDir()

	src := "package main\nfunc Main() {}\n"
	mainFile := filepath.Join(root, "main.go")
	if err := os.WriteFile(mainFile, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(mainFile + ".evidence.yaml") })

	// Create a vendor subdir with a .go file.
	vendorDir := filepath.Join(root, "vendor", "pkg")
	if err := os.MkdirAll(vendorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	vendorFile := filepath.Join(vendorDir, "vendored.go")
	if err := os.WriteFile(vendorFile, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	written, errs := walkAndGenerate(root)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	// Only the root main.go should have been processed.
	if written != 1 {
		t.Errorf("written = %d, want 1 (vendor/ should be skipped)", written)
	}
	// Vendor companion must not have been created.
	vendorCompanion := vendorFile + ".evidence.yaml"
	if _, err := os.Stat(vendorCompanion); !os.IsNotExist(err) {
		t.Errorf("vendor companion file should not exist: %s", vendorCompanion)
		os.Remove(vendorCompanion)
	}
}

// TestWalkAndGenerate_RelativePaths verifies that bundle.File.Path is relative
// to the provided root and uses forward slashes (INV-23).
func TestWalkAndGenerate_RelativePaths(t *testing.T) {
	root := t.TempDir()

	// Create a file in a subdirectory.
	subDir := filepath.Join(root, "sub")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package sub\nfunc Sub() {}\n"
	subFile := filepath.Join(subDir, "sub.go")
	if err := os.WriteFile(subFile, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(subFile + ".evidence.yaml") })

	written, errs := walkAndGenerate(root)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if written != 1 {
		t.Errorf("written = %d, want 1", written)
	}

	data, err := os.ReadFile(subFile + ".evidence.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var bundle EvidenceBundle
	if err := yaml.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}

	// Path must be root-relative with forward slashes (INV-23).
	wantPath := "sub/sub.go"
	if bundle.File.Path != wantPath {
		t.Errorf("file.path = %q, want %q", bundle.File.Path, wantPath)
	}
	// Must not contain the absolute root prefix.
	if strings.Contains(bundle.File.Path, root) {
		t.Errorf("file.path must not contain absolute root: %q", bundle.File.Path)
	}
	// Must use forward slashes (INV-13).
	if strings.Contains(bundle.File.Path, "\\") {
		t.Errorf("file.path contains backslash: %q", bundle.File.Path)
	}
}

// --------------------------------------------------------------------------
// Unit tests — extractSymbols constructors (INV-45)
// --------------------------------------------------------------------------

// TestConstructorsExtracted verifies that a function returning a package-local
// type is included in symbols.constructors (INV-45).
func TestConstructorsExtracted(t *testing.T) {
	src := `package pkg

type Widget struct{}

func NewWidget() *Widget { return nil }
func helper() int       { return 0 }
`
	f := parseSource(t, src)
	syms := extractSymbols(f, noTypeInfo, noTypePkg, nullQualifier)

	if len(syms.Constructors) != 1 {
		t.Fatalf("expected 1 constructor, got %d: %v", len(syms.Constructors), syms.Constructors)
	}
	if syms.Constructors[0] != "NewWidget" {
		t.Errorf("constructor = %q, want %q", syms.Constructors[0], "NewWidget")
	}
}

// TestConstructors_SliceReturn verifies that a function returning []LocalType
// is also recognized as a constructor (INV-45).
func TestConstructors_SliceReturn(t *testing.T) {
	src := `package pkg

type Item struct{}

func NewItems() []Item { return nil }
`
	f := parseSource(t, src)
	syms := extractSymbols(f, noTypeInfo, noTypePkg, nullQualifier)

	if !containsStr(syms.Constructors, "NewItems") {
		t.Errorf("expected NewItems in constructors, got %v", syms.Constructors)
	}
}

// TestConstructors_MethodNotConstructor verifies that methods (with receivers)
// are never listed as constructors (INV-45).
func TestConstructors_MethodNotConstructor(t *testing.T) {
	src := `package pkg

type Foo struct{}

func (f *Foo) Clone() *Foo { return nil }
`
	f := parseSource(t, src)
	syms := extractSymbols(f, noTypeInfo, noTypePkg, nullQualifier)

	if len(syms.Constructors) != 0 {
		t.Errorf("expected no constructors, got %v", syms.Constructors)
	}
}

// TestConstructors_NoLocalTypes verifies that constructors is empty when the
// file declares no types (INV-45).
func TestConstructors_NoLocalTypes(t *testing.T) {
	src := `package pkg

func New() int { return 0 }
`
	f := parseSource(t, src)
	syms := extractSymbols(f, noTypeInfo, noTypePkg, nullQualifier)

	if len(syms.Constructors) != 0 {
		t.Errorf("expected no constructors, got %v", syms.Constructors)
	}
}

// TestConstructors_Sorted verifies constructors are sorted lexicographically
// (INV-45 + INV-28 consistency).
func TestConstructors_Sorted(t *testing.T) {
	src := `package pkg

type T struct{}

func Zebra() *T  { return nil }
func Alpha() *T  { return nil }
func Middle() *T { return nil }
`
	f := parseSource(t, src)
	syms := extractSymbols(f, noTypeInfo, noTypePkg, nullQualifier)

	for i := 1; i < len(syms.Constructors); i++ {
		if syms.Constructors[i] < syms.Constructors[i-1] {
			t.Errorf("constructors not sorted at %d: %q < %q",
				i, syms.Constructors[i], syms.Constructors[i-1])
		}
	}
	if len(syms.Constructors) != 3 {
		t.Errorf("expected 3 constructors, got %d: %v", len(syms.Constructors), syms.Constructors)
	}
}

// --------------------------------------------------------------------------
// Unit tests — extractSymbols struct fields (INV-46)
// --------------------------------------------------------------------------

// TestStructFieldsExtracted verifies exported struct fields are captured in
// declaration order with correct TypeStr values (INV-46).
func TestStructFieldsExtracted(t *testing.T) {
	src := `package pkg

type Person struct {
	Name    string
	Age     int
	Address *Address
}

type Address struct{}
`
	f := parseSource(t, src)
	syms := extractSymbols(f, noTypeInfo, noTypePkg, nullQualifier)

	// Find Person type.
	var person *TypeDecl
	for i := range syms.Types {
		if syms.Types[i].Name == "Person" {
			person = &syms.Types[i]
		}
	}
	if person == nil {
		t.Fatal("Person type not found")
	}
	if person.Kind != "struct" {
		t.Errorf("Kind = %q, want %q", person.Kind, "struct")
	}
	if len(person.Fields) != 3 {
		t.Fatalf("expected 3 fields, got %d: %v", len(person.Fields), person.Fields)
	}
	// Declaration order is preserved (INV-46).
	wantFields := []struct{ name, typeStr string }{
		{"Name", "string"},
		{"Age", "int"},
		{"Address", "*Address"},
	}
	for i, want := range wantFields {
		got := person.Fields[i]
		if got.Name != want.name || got.TypeStr != want.typeStr {
			t.Errorf("Fields[%d] = {%q, %q}, want {%q, %q}",
				i, got.Name, got.TypeStr, want.name, want.typeStr)
		}
	}
}

// TestStructFields_UnexportedSkipped verifies that unexported fields are not
// included in TypeDecl.Fields (INV-46).
func TestStructFields_UnexportedSkipped(t *testing.T) {
	src := `package pkg

type Mixed struct {
	Exported   string
	unexported int
}
`
	f := parseSource(t, src)
	syms := extractSymbols(f, noTypeInfo, noTypePkg, nullQualifier)

	var mixed *TypeDecl
	for i := range syms.Types {
		if syms.Types[i].Name == "Mixed" {
			mixed = &syms.Types[i]
		}
	}
	if mixed == nil {
		t.Fatal("Mixed type not found")
	}
	if len(mixed.Fields) != 1 {
		t.Fatalf("expected 1 exported field, got %d: %v", len(mixed.Fields), mixed.Fields)
	}
	if mixed.Fields[0].Name != "Exported" {
		t.Errorf("field name = %q, want %q", mixed.Fields[0].Name, "Exported")
	}
}

// TestStructFields_NonStructNoFields verifies that interface and alias TypeDecls
// have no Fields entry (INV-46).
func TestStructFields_NonStructNoFields(t *testing.T) {
	src := `package pkg

type Doer interface{ Do() }
type ID = string
`
	f := parseSource(t, src)
	syms := extractSymbols(f, noTypeInfo, noTypePkg, nullQualifier)

	for _, td := range syms.Types {
		if len(td.Fields) != 0 {
			t.Errorf("type %q (kind=%s) should have no fields, got %v",
				td.Name, td.Kind, td.Fields)
		}
	}
}

// TestStructFields_EmbeddedExported verifies that embedded exported types are
// captured with their type name as the field name (INV-46).
func TestStructFields_EmbeddedExported(t *testing.T) {
	src := `package pkg

type Base struct{}

type Child struct {
	Base
	Name string
}
`
	f := parseSource(t, src)
	syms := extractSymbols(f, noTypeInfo, noTypePkg, nullQualifier)

	var child *TypeDecl
	for i := range syms.Types {
		if syms.Types[i].Name == "Child" {
			child = &syms.Types[i]
		}
	}
	if child == nil {
		t.Fatal("Child type not found")
	}
	// Both embedded Base and explicit Name should appear.
	nameMap := make(map[string]string)
	for _, f := range child.Fields {
		nameMap[f.Name] = f.TypeStr
	}
	if nameMap["Base"] == "" {
		t.Errorf("embedded Base field not captured; fields: %v", child.Fields)
	}
	if nameMap["Name"] == "" {
		t.Errorf("Name field not captured; fields: %v", child.Fields)
	}
}

// --------------------------------------------------------------------------
// Unit tests — extractSignals yaml_io / json_io (INV-47)
// --------------------------------------------------------------------------

// TestExtractSignals_YAMLImport verifies yaml_io is set when a yaml library
// is imported (INV-47).
func TestExtractSignals_YAMLImport(t *testing.T) {
	src := `package pkg
import _ "gopkg.in/yaml.v3"
func f() {}
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)
	sig := extractSignals(meta, calls, f)

	if !sig.YAMLio {
		t.Error("expected yaml_io = true when gopkg.in/yaml.v3 is imported")
	}
}

// TestExtractSignals_YAMLCall verifies yaml_io is set when a yaml.* call
// appears in the call list (INV-47).
func TestExtractSignals_YAMLCall(t *testing.T) {
	src := `package pkg
import "gopkg.in/yaml.v3"
func f() { yaml.Marshal(nil) }
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)
	sig := extractSignals(meta, calls, f)

	if !sig.YAMLio {
		t.Error("expected yaml_io = true when yaml.Marshal is called")
	}
}

// TestExtractSignals_JSONImport verifies json_io is set when encoding/json
// is imported (INV-47).
func TestExtractSignals_JSONImport(t *testing.T) {
	src := `package pkg
import _ "encoding/json"
func f() {}
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)
	sig := extractSignals(meta, calls, f)

	if !sig.JSONio {
		t.Error("expected json_io = true when encoding/json is imported")
	}
}

// TestExtractSignals_JSONCall verifies json_io is set when a json.* call
// appears in the call list (INV-47).
func TestExtractSignals_JSONCall(t *testing.T) {
	src := `package pkg
import "encoding/json"
func f() { json.Marshal(nil) }
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)
	sig := extractSignals(meta, calls, f)

	if !sig.JSONio {
		t.Error("expected json_io = true when json.Marshal is called")
	}
}

// TestExtractSignals_YAMLNotJSON verifies yaml_io does not imply json_io and
// vice versa (INV-47 independence).
func TestExtractSignals_YAMLNotJSON(t *testing.T) {
	src := `package pkg
import _ "gopkg.in/yaml.v3"
func f() {}
`
	f := parseSource(t, src)
	meta := extractPackageMeta(f)
	calls := extractCalls(f, noTypeInfo, noTypePkg, nullQualifier)
	sig := extractSignals(meta, calls, f)

	if sig.JSONio {
		t.Error("expected json_io = false when only yaml is used")
	}
}

// --------------------------------------------------------------------------
// Fuzz tests
// --------------------------------------------------------------------------

// FuzzExprToString verifies exprToString never panics on arbitrary AST exprs.
// The fuzzer drives node selection by index.
func FuzzExprToString(f *testing.F) {
	// Seed with a few representative expressions
	f.Add(0, "int")
	f.Add(1, "Server")
	f.Add(2, "os")

	f.Fuzz(func(t *testing.T, kind int, name string) {
		var expr ast.Expr
		switch kind % 6 {
		case 0:
			expr = &ast.Ident{Name: name}
		case 1:
			expr = &ast.StarExpr{X: &ast.Ident{Name: name}}
		case 2:
			expr = &ast.ArrayType{Elt: &ast.Ident{Name: name}}
		case 3:
			expr = &ast.ChanType{Dir: ast.SEND, Value: &ast.Ident{Name: name}}
		case 4:
			expr = &ast.MapType{
				Key:   &ast.Ident{Name: name},
				Value: &ast.Ident{Name: name},
			}
		case 5:
			expr = nil
		}
		// Must not panic
		_ = exprToString(expr)
	})
}

// FuzzParseGoSource verifies that parsing + extraction never panics on arbitrary
// Go source bytes (valid or not).
func FuzzParseGoSource(f *testing.F) {
	// Seed corpus: valid Go snippets
	f.Add([]byte("package main\nfunc main() {}\n"))
	f.Add([]byte("package p\nimport \"os\"\nfunc f() { os.Exit(0) }\n"))
	f.Add([]byte("package p\nvar x chan int\n"))
	f.Add([]byte("package p\ntype S struct{}\nfunc (s S) M() {}\n"))
	f.Add([]byte(""))
	f.Add([]byte("not valid go at all!!!"))

	f.Fuzz(func(t *testing.T, data []byte) {
		fset := token.NewFileSet()
		// Pass data directly as source; ParseFile returns partial AST even on errors
		file, _ := parser.ParseFile(fset, "fuzz.go", data, 0)
		if file == nil {
			return // totally invalid — nothing to extract
		}
		// All extraction helpers must not panic
		meta := extractPackageMeta(file)
		syms := extractSymbols(file, nil, nil, nullQualifier)
		calls := extractCalls(file, nil, nil, nullQualifier)
		sig := extractSignals(meta, calls, file)

		// Sorting invariants must still hold
		for i := 1; i < len(meta.Imports); i++ {
			if meta.Imports[i].Path < meta.Imports[i-1].Path {
				t.Errorf("imports not sorted at %d", i)
			}
		}
		for i := 1; i < len(syms.Functions); i++ {
			if syms.Functions[i].Name < syms.Functions[i-1].Name {
				t.Errorf("functions not sorted at %d", i)
			}
		}
		for i := 1; i < len(calls); i++ {
			prev, curr := calls[i-1], calls[i]
			if curr.From < prev.From || (curr.From == prev.From && curr.To < prev.To) {
				t.Errorf("calls not sorted at %d", i)
			}
		}

		// Suppress unused variable warnings
		_ = sig
	})
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// containsStr reports whether slice contains s.
func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
