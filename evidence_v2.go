package main

// evidence_v2.go — Semantic evidence bundle (version 2).
//
// v2 extends v1 with four additional sections derived from static analysis:
//
//	package  — package name and sorted import list
//	symbols  — all top-level declarations (functions, types, vars, consts)
//	calls    — deduplicated, sorted outbound call graph for the file
//	signals  — deterministic boolean heuristics (fs, db, net, concurrency)
//
// Implementation separation (see INVARIANT.md INV-20..22):
//
//	createEvidenceBundleV2   — pure analysis, no side effects
//	writeEvidenceBundleV2    — marshals + writes companion .evidence.yaml
//	validateEvidenceBundleV2 — re-hashes file, returns error if stale

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Data types
// ---------------------------------------------------------------------------

// EvidenceBundleV2 is the top-level container for a v2 evidence bundle.
// Field order matches the desired YAML output order; yaml.v3 respects struct
// field order, so no additional sorting is needed at the top level.
type EvidenceBundleV2 struct {
	Version int         `yaml:"version"`
	File    FileMeta    `yaml:"file"`    // reuses FileMeta from main.go
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
	Functions []Function `yaml:"functions,omitempty"`
	Types     []TypeDecl `yaml:"types,omitempty"`
	Variables []VarDecl  `yaml:"variables,omitempty"`
	Constants []VarDecl  `yaml:"constants,omitempty"`
}

// Function describes a top-level function or method declaration.
type Function struct {
	Name     string   `yaml:"name"`
	Exported bool     `yaml:"exported"`
	Receiver string   `yaml:"receiver,omitempty"` // non-empty for methods
	Params   []string `yaml:"params,omitempty"`
	Returns  []string `yaml:"returns,omitempty"`
}

// TypeDecl describes a top-level type declaration.
type TypeDecl struct {
	Name     string `yaml:"name"`
	Kind     string `yaml:"kind"` // "struct" | "interface" | "alias"
	Exported bool   `yaml:"exported"`
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
}

// ---------------------------------------------------------------------------
// Public API — Generation / Serialization / Validation
// ---------------------------------------------------------------------------

// createEvidenceBundleV2 performs pure static analysis on a Go source file
// and returns a v2 evidence bundle. It does not write any files (INV-20).
//
// It first attempts to load the package with full type information via
// golang.org/x/tools/go/packages. On failure it falls back to AST-only
// analysis — call targets and type strings are then best-effort.
func createEvidenceBundleV2(filePath string) (*EvidenceBundleV2, error) {
	// Step 1 — integrity (same as v1): read raw bytes, compute hash.
	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	sum := sha256.Sum256(fileBytes)
	hash := hex.EncodeToString(sum[:])
	normalizedPath := filepath.ToSlash(filePath)

	// Step 2 — parse + type-load.
	// Try the richer path (go/packages) first; fall back to go/parser.
	file, typesInfo, typesPkg, err := loadTypeInfoForFile(filePath)
	if err != nil {
		// Fall back: parse with go/parser, no type info.
		fset := token.NewFileSet()
		file, err = parser.ParseFile(fset, filePath, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}
		typesInfo = nil
		typesPkg = nil
	}

	return buildBundle(normalizedPath, hash, file, typesInfo, typesPkg), nil
}

// buildBundle assembles an EvidenceBundleV2 from pre-loaded AST and type data.
// normalizedPath is already slash-normalized; hash is the hex-encoded SHA256.
// typesInfo and typesPkg may be nil (AST-only fallback).
func buildBundle(normalizedPath, hash string, file *ast.File, typesInfo *types.Info, typesPkg *types.Package) *EvidenceBundleV2 {
	qualifier := makeQualifier(typesPkg)
	pkgMeta := extractPackageMeta(file)
	syms := extractSymbols(file, typesInfo, typesPkg, qualifier)
	calls := extractCalls(file, typesInfo, typesPkg, qualifier)
	sigs := extractSignals(pkgMeta, calls, file)

	return &EvidenceBundleV2{
		Version: 2,
		File: FileMeta{
			Path:   normalizedPath,
			SHA256: hash,
		},
		Package: pkgMeta,
		Symbols: syms,
		Calls:   calls,
		Signals: sigs,
	}
}

// writeEvidenceBundleV2 marshals the bundle to YAML and writes it to the
// companion file `<bundle.File.Path>.evidence.yaml` (INV-14, INV-21).
// The file is overwritten entirely on each call.
func writeEvidenceBundleV2(bundle *EvidenceBundleV2) error {
	data, err := yaml.Marshal(bundle)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	outputPath := filepath.FromSlash(bundle.File.Path + ".evidence.yaml")
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outputPath, err)
	}
	return nil
}

// validateEvidenceBundleV2 re-hashes the source file and returns an error if
// the current hash differs from the stored hash (INV-2, INV-22).
// It does not modify any files.
func validateEvidenceBundleV2(bundle *EvidenceBundleV2) error {
	filePath := filepath.FromSlash(bundle.File.Path)
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	sum := sha256.Sum256(raw)
	current := hex.EncodeToString(sum[:])
	if current != bundle.File.SHA256 {
		return fmt.Errorf("evidence bundle is stale: file hash changed (stored %s, current %s)",
			bundle.File.SHA256, current)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Type loading
// ---------------------------------------------------------------------------

// loadTypeInfoForFile loads the Go package containing filePath using
// golang.org/x/tools/go/packages to obtain full type information.
// Returns the *ast.File for filePath, *types.Info, and *types.Package.
// Returns an error if loading fails or the file is not found in the package.
func loadTypeInfoForFile(filePath string) (*ast.File, *types.Info, *types.Package, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("abs path: %w", err)
	}

	fset := token.NewFileSet()
	cfg := &packages.Config{
		Mode: packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedImports,
		Dir:  filepath.Dir(absPath),
		Fset: fset,
	}

	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("packages.Load: %w", err)
	}
	if len(pkgs) == 0 {
		return nil, nil, nil, fmt.Errorf("no packages found")
	}

	pkg := pkgs[0]
	if pkg.TypesInfo == nil || pkg.Types == nil {
		return nil, nil, nil, fmt.Errorf("no type info (package may have errors)")
	}

	// Find our specific file among the parsed syntax files.
	for _, f := range pkg.Syntax {
		pos := fset.Position(f.Pos())
		if pos.Filename == absPath {
			return f, pkg.TypesInfo, pkg.Types, nil
		}
	}
	return nil, nil, nil, fmt.Errorf("file %s not found in package syntax", absPath)
}

// loadPackageForDir loads the Go package in dir using golang.org/x/tools/go/packages.
// Returns the *packages.Package and *token.FileSet so all files in the package
// can be found in pkg.Syntax without re-loading (INV-26).
// Returns an error if loading fails or no type info is available.
func loadPackageForDir(dir string) (*packages.Package, *token.FileSet, error) {
	fset := token.NewFileSet()
	cfg := &packages.Config{
		Mode: packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedImports,
		Dir:  dir,
		Fset: fset,
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("packages.Load: %w", err)
	}
	if len(pkgs) == 0 {
		return nil, nil, fmt.Errorf("no packages found")
	}
	pkg := pkgs[0]
	if pkg.TypesInfo == nil || pkg.Types == nil {
		return nil, nil, fmt.Errorf("no type info (package may have errors)")
	}
	return pkg, fset, nil
}

// makeQualifier returns a types.Qualifier that prints external package names
// and the empty string for the current package (so its symbols are unqualified).
// If pkg is nil (AST-only fallback), all packages are printed by name.
func makeQualifier(pkg *types.Package) types.Qualifier {
	return func(p *types.Package) string {
		if pkg != nil && p == pkg {
			return "" // same package — unqualified
		}
		return p.Name()
	}
}

// ---------------------------------------------------------------------------
// Extraction — package metadata
// ---------------------------------------------------------------------------

// extractPackageMeta extracts the package name and sorted import list from
// the AST. Does not require type information.
func extractPackageMeta(file *ast.File) PackageMeta {
	meta := PackageMeta{Name: file.Name.Name}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		entry := Import{Path: path}
		if imp.Name != nil {
			entry.Alias = imp.Name.Name
		}
		meta.Imports = append(meta.Imports, entry)
	}
	// INV-7: sort alphabetically by path.
	sort.Slice(meta.Imports, func(i, j int) bool {
		return meta.Imports[i].Path < meta.Imports[j].Path
	})
	return meta
}

// ---------------------------------------------------------------------------
// Extraction — symbols
// ---------------------------------------------------------------------------

// extractSymbols collects all top-level declarations from the file.
// When typesInfo and typesPkg are non-nil, type strings are resolved via
// go/types; otherwise they are derived from the AST.
// All result slices are sorted alphabetically by name (INV-8..11).
func extractSymbols(file *ast.File, typesInfo *types.Info, pkg *types.Package, qualifier types.Qualifier) Symbols {
	var syms Symbols
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			syms.Functions = append(syms.Functions, extractFunction(d, typesInfo, qualifier))

		case *ast.GenDecl:
			switch d.Tok.String() {
			case "type":
				for _, spec := range d.Specs {
					ts := spec.(*ast.TypeSpec)
					syms.Types = append(syms.Types, TypeDecl{
						Name:     ts.Name.Name,
						Kind:     typeKind(ts.Type),
						Exported: ast.IsExported(ts.Name.Name),
					})
				}
			case "var":
				for _, spec := range d.Specs {
					vs := spec.(*ast.ValueSpec)
					for _, name := range vs.Names {
						syms.Variables = append(syms.Variables, VarDecl{
							Name:     name.Name,
							Exported: ast.IsExported(name.Name),
						})
					}
				}
			case "const":
				for _, spec := range d.Specs {
					vs := spec.(*ast.ValueSpec)
					for _, name := range vs.Names {
						syms.Constants = append(syms.Constants, VarDecl{
							Name:     name.Name,
							Exported: ast.IsExported(name.Name),
						})
					}
				}
			}
		}
	}

	sort.Slice(syms.Functions, func(i, j int) bool { return syms.Functions[i].Name < syms.Functions[j].Name })
	sort.Slice(syms.Types, func(i, j int) bool { return syms.Types[i].Name < syms.Types[j].Name })
	sort.Slice(syms.Variables, func(i, j int) bool { return syms.Variables[i].Name < syms.Variables[j].Name })
	sort.Slice(syms.Constants, func(i, j int) bool { return syms.Constants[i].Name < syms.Constants[j].Name })
	return syms
}

// extractFunction builds a Function from an ast.FuncDecl.
// Uses type info when available for accurate receiver and parameter types.
func extractFunction(decl *ast.FuncDecl, typesInfo *types.Info, qualifier types.Qualifier) Function {
	fn := Function{
		Name:     decl.Name.Name,
		Exported: ast.IsExported(decl.Name.Name),
	}

	if typesInfo != nil {
		if obj := typesInfo.Defs[decl.Name]; obj != nil {
			if sig, ok := obj.Type().(*types.Signature); ok {
				if recv := sig.Recv(); recv != nil {
					fn.Receiver = types.TypeString(recv.Type(), qualifier)
				}
				params := sig.Params()
				for i := 0; i < params.Len(); i++ {
					typeStr := types.TypeString(params.At(i).Type(), qualifier)
					// Mark variadic last parameter.
					if sig.Variadic() && i == params.Len()-1 {
						typeStr = "..." + typeStr
					}
					fn.Params = append(fn.Params, typeStr)
				}
				results := sig.Results()
				for i := 0; i < results.Len(); i++ {
					fn.Returns = append(fn.Returns, types.TypeString(results.At(i).Type(), qualifier))
				}
				return fn
			}
		}
	}

	// Fallback: AST-based extraction.
	if decl.Recv != nil && len(decl.Recv.List) > 0 {
		fn.Receiver = exprToString(decl.Recv.List[0].Type)
	}
	if decl.Type.Params != nil {
		for _, field := range decl.Type.Params.List {
			typeStr := exprToString(field.Type)
			if len(field.Names) == 0 {
				fn.Params = append(fn.Params, typeStr)
			} else {
				for range field.Names {
					fn.Params = append(fn.Params, typeStr)
				}
			}
		}
	}
	if decl.Type.Results != nil {
		for _, field := range decl.Type.Results.List {
			typeStr := exprToString(field.Type)
			if len(field.Names) == 0 {
				fn.Returns = append(fn.Returns, typeStr)
			} else {
				for range field.Names {
					fn.Returns = append(fn.Returns, typeStr)
				}
			}
		}
	}
	return fn
}

// typeKind classifies an AST type expression as "struct", "interface", or "alias".
func typeKind(expr ast.Expr) string {
	switch expr.(type) {
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	default:
		return "alias"
	}
}

// exprToString converts an AST type expression to its canonical string
// representation without requiring type information. Used as a fallback when
// go/packages loading fails.
func exprToString(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprToString(e.X) + "." + e.Sel.Name
	case *ast.StarExpr:
		return "*" + exprToString(e.X)
	case *ast.ArrayType:
		if e.Len == nil {
			return "[]" + exprToString(e.Elt)
		}
		return "[...]" + exprToString(e.Elt) // fixed-size; length unknown without eval
	case *ast.MapType:
		return "map[" + exprToString(e.Key) + "]" + exprToString(e.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.StructType:
		return "struct{}"
	case *ast.FuncType:
		return "func(...)"
	case *ast.ChanType:
		switch e.Dir {
		case ast.SEND:
			return "chan<- " + exprToString(e.Value)
		case ast.RECV:
			return "<-chan " + exprToString(e.Value)
		default:
			return "chan " + exprToString(e.Value)
		}
	case *ast.Ellipsis:
		if e.Elt != nil {
			return "..." + exprToString(e.Elt)
		}
		return "..."
	case *ast.ParenExpr:
		return "(" + exprToString(e.X) + ")"
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Extraction — calls
// ---------------------------------------------------------------------------

// extractCalls walks the AST and collects deduplicated outbound function calls.
//
// The algorithm uses ast.Inspect with a paired "pushed" stack to track which
// function currently encloses each call site. When a *ast.FuncDecl or
// *ast.FuncLit is entered, its name is pushed; the nil post-visit call pops it.
//
// Deduplication: (from, to) pairs are unique in the output.
// Sorting: by from, then to (INV-12).
func extractCalls(file *ast.File, typesInfo *types.Info, pkg *types.Package, qualifier types.Qualifier) []Call {
	var calls []Call
	seen := make(map[[2]string]bool)

	// funcStack tracks nested function names at each traversal depth.
	// pushedStack mirrors the traversal stack: true at position i means we
	// pushed a name onto funcStack when visiting the node at depth i.
	var funcStack []string
	var pushedStack []bool

	addCall := func(from, to string) {
		if to == "" {
			return
		}
		key := [2]string{from, to}
		if !seen[key] {
			seen[key] = true
			calls = append(calls, Call{From: from, To: to})
		}
	}

	currentFunc := func() string {
		if len(funcStack) == 0 {
			return "<global>"
		}
		return funcStack[len(funcStack)-1]
	}

	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			// Post-visit: pop if we pushed at this level.
			if len(pushedStack) > 0 {
				if pushedStack[len(pushedStack)-1] {
					funcStack = funcStack[:len(funcStack)-1]
				}
				pushedStack = pushedStack[:len(pushedStack)-1]
			}
			return true
		}

		pushed := false
		switch node := n.(type) {
		case *ast.FuncDecl:
			name := funcDeclName(node, typesInfo, qualifier)
			funcStack = append(funcStack, name)
			pushed = true

		case *ast.FuncLit:
			// Anonymous function: use enclosing name as prefix.
			parent := "<global>"
			if len(funcStack) > 0 {
				parent = funcStack[len(funcStack)-1]
			}
			funcStack = append(funcStack, parent+".<anonymous>")
			pushed = true

		case *ast.CallExpr:
			to := resolveCallTarget(node.Fun, typesInfo, pkg, qualifier)
			addCall(currentFunc(), to)
		}

		pushedStack = append(pushedStack, pushed)
		return true
	})

	// INV-12: sort by from, then to.
	sort.Slice(calls, func(i, j int) bool {
		if calls[i].From != calls[j].From {
			return calls[i].From < calls[j].From
		}
		return calls[i].To < calls[j].To
	})
	return calls
}

// funcDeclName returns the qualified name used as the "from" identifier for
// calls originating in this function. Methods include their receiver type.
func funcDeclName(decl *ast.FuncDecl, typesInfo *types.Info, qualifier types.Qualifier) string {
	name := decl.Name.Name
	if decl.Recv == nil || len(decl.Recv.List) == 0 {
		return name
	}
	// Method: prepend receiver type.
	recvStr := ""
	if typesInfo != nil {
		if obj := typesInfo.Defs[decl.Name]; obj != nil {
			if sig, ok := obj.Type().(*types.Signature); ok && sig.Recv() != nil {
				recvStr = types.TypeString(sig.Recv().Type(), qualifier)
			}
		}
	}
	if recvStr == "" {
		recvStr = exprToString(decl.Recv.List[0].Type)
	}
	return recvStr + "." + name
}

// resolveCallTarget returns the qualified call target string for an AST call
// expression function node. Returns "" for unresolvable or anonymous targets.
func resolveCallTarget(expr ast.Expr, typesInfo *types.Info, pkg *types.Package, qualifier types.Qualifier) string {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		if typesInfo != nil {
			// Check if the left side is a package name.
			if ident, ok := e.X.(*ast.Ident); ok {
				if obj := typesInfo.Uses[ident]; obj != nil {
					if pkgName, ok := obj.(*types.PkgName); ok {
						return pkgName.Imported().Name() + "." + e.Sel.Name
					}
				}
			}
			// Otherwise it's a method call on a value.
			if obj := typesInfo.Uses[e.Sel]; obj != nil {
				if obj.Pkg() != nil {
					if pkg != nil && obj.Pkg() == pkg {
						return obj.Name()
					}
					return obj.Pkg().Name() + "." + obj.Name()
				}
			}
		}
		// AST fallback: <X>.<Sel>
		if ident, ok := e.X.(*ast.Ident); ok {
			return ident.Name + "." + e.Sel.Name
		}
		return e.Sel.Name

	case *ast.Ident:
		if typesInfo != nil {
			if obj := typesInfo.Uses[e]; obj != nil {
				// Skip type conversions and built-in identifiers without packages.
				if _, isType := obj.(*types.TypeName); isType {
					return "" // type conversion, not a call
				}
				if obj.Pkg() == nil {
					return e.Name // built-in (make, len, append, …)
				}
				if pkg != nil && obj.Pkg() == pkg {
					return obj.Name()
				}
				return obj.Pkg().Name() + "." + obj.Name()
			}
		}
		return e.Name

	case *ast.FuncLit:
		// Calling an anonymous function inline; not a named target.
		return ""

	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Extraction — signals
// ---------------------------------------------------------------------------

// extractSignals derives boolean behavioral heuristics from imports, the call
// list, and AST node types. All detection is purely static (INV-18).
func extractSignals(meta PackageMeta, calls []Call, file *ast.File) Signals {
	importSet := make(map[string]bool, len(meta.Imports))
	for _, imp := range meta.Imports {
		importSet[imp.Path] = true
	}

	callSet := make(map[string]bool, len(calls))
	for _, c := range calls {
		callSet[c.To] = true
	}

	var sig Signals

	// fs_reads: calls to well-known file-read functions.
	for _, fn := range []string{"os.Open", "os.ReadFile", "ioutil.ReadFile", "filepath.Walk"} {
		if callSet[fn] {
			sig.FSReads = true
			break
		}
	}

	// fs_writes: calls to well-known file-write/delete functions.
	for _, fn := range []string{"os.Create", "os.WriteFile", "os.Remove"} {
		if callSet[fn] {
			sig.FSWrites = true
			break
		}
	}

	// db_calls: database/sql import or call target containing Query/Exec/Scan.
	if importSet["database/sql"] {
		sig.DBCalls = true
	}
	if !sig.DBCalls {
		for target := range callSet {
			if strings.Contains(target, "Query") ||
				strings.Contains(target, "Exec") ||
				strings.Contains(target, "Scan") {
				sig.DBCalls = true
				break
			}
		}
	}

	// net_calls: net or net/http import, or call referencing http.Client.
	if importSet["net"] || importSet["net/http"] {
		sig.NetCalls = true
	}
	if !sig.NetCalls {
		for target := range callSet {
			if strings.Contains(target, "http.Client") {
				sig.NetCalls = true
				break
			}
		}
	}

	// concurrency: sync import, goroutine statement, or channel type.
	for path := range importSet {
		if path == "sync" || strings.HasPrefix(path, "sync/") {
			sig.Concurrency = true
			break
		}
	}
	if !sig.Concurrency {
		ast.Inspect(file, func(n ast.Node) bool {
			if sig.Concurrency {
				return false // short-circuit once found
			}
			switch n.(type) {
			case *ast.GoStmt, *ast.ChanType:
				sig.Concurrency = true
				return false
			}
			return true
		})
	}

	return sig
}

// ---------------------------------------------------------------------------
// Directory Walking
// ---------------------------------------------------------------------------

// walkAndGenerate walks root recursively, generating a v2 evidence bundle for
// every .go file found. Directories named vendor, testdata, or starting with
// "." are skipped entirely (INV-24). Directories and files are processed in
// sorted order (INV-25). Each directory's package is loaded once (INV-26).
//
// Returns the number of bundles written and any errors encountered.
// Errors are accumulated — processing continues even if individual files fail.
func walkAndGenerate(root string) (written int, errs []error) {
	// Collect .go files grouped by directory.
	filesByDir := make(map[string][]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			// Always descend into the root itself.
			if path == root {
				return nil
			}
			// Skip vendor, testdata, examples, docs, and hidden directories (INV-24).
			if name == "vendor" || name == "testdata" || name == "examples" || name == "docs" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(name) != ".go" {
			return nil
		}
		// Skip test files (INV-24).
		if strings.HasSuffix(name, "_test.go") {
			return nil
		}
		dir := filepath.Dir(path)
		filesByDir[dir] = append(filesByDir[dir], path)
		return nil
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("walk %s: %w", root, err))
		return
	}

	// Sort directories for deterministic processing (INV-25).
	dirs := make([]string, 0, len(filesByDir))
	for dir := range filesByDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		files := filesByDir[dir]
		sort.Strings(files) // sort files within each dir (INV-25)

		// Load the package once per directory (INV-26).
		// pkg may be nil if loading fails; buildBundleForFile falls back to go/parser.
		pkg, fset, _ := loadPackageForDir(dir)

		for _, absPath := range files {
			relPath, err := filepath.Rel(root, absPath)
			if err != nil {
				errs = append(errs, fmt.Errorf("rel path %s: %w", absPath, err))
				continue
			}
			relPath = filepath.ToSlash(relPath)

			bundle, err := buildBundleForFile(absPath, relPath, pkg, fset)
			if err != nil {
				errs = append(errs, fmt.Errorf("build bundle %s: %w", relPath, err))
				continue
			}

			if err := writeBundleAt(bundle, absPath); err != nil {
				errs = append(errs, fmt.Errorf("write bundle %s: %w", relPath, err))
				continue
			}
			written++
		}
	}
	return
}

// buildBundleForFile creates an EvidenceBundleV2 for a single file.
// It uses the pre-loaded pkg/fset when the file can be found in pkg.Syntax;
// otherwise it falls back to go/parser with no type information.
// absPath is the absolute filesystem path; relPath is the root-relative
// forward-slash path stored as file.path in the bundle (INV-23).
func buildBundleForFile(absPath, relPath string, pkg *packages.Package, fset *token.FileSet) (*EvidenceBundleV2, error) {
	fileBytes, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	sum := sha256.Sum256(fileBytes)
	hash := hex.EncodeToString(sum[:])

	// Try to find the file in the pre-loaded package syntax.
	if pkg != nil && fset != nil && pkg.TypesInfo != nil && pkg.Types != nil {
		for _, f := range pkg.Syntax {
			pos := fset.Position(f.Pos())
			if pos.Filename == absPath {
				return buildBundle(relPath, hash, f, pkg.TypesInfo, pkg.Types), nil
			}
		}
	}

	// Fall back to go/parser (no type info).
	fileFset := token.NewFileSet()
	file, err := parser.ParseFile(fileFset, absPath, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return buildBundle(relPath, hash, file, nil, nil), nil
}

// writeBundleAt marshals bundle to YAML and writes it to absFilePath+".evidence.yaml".
// The companion file is written using the absolute path so it lands next to the
// source regardless of the caller's working directory (INV-14).
func writeBundleAt(bundle *EvidenceBundleV2, absFilePath string) error {
	data, err := yaml.Marshal(bundle)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	outputPath := absFilePath + ".evidence.yaml"
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outputPath, err)
	}
	return nil
}
