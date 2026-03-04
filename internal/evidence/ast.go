package evidence

// ast.go — Static analysis helpers: type loading, symbol extraction, call
// graph extraction, and signal detection.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

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
					td := TypeDecl{
						Name:     ts.Name.Name,
						Kind:     typeKind(ts.Type),
						Exported: ast.IsExported(ts.Name.Name),
					}
					// INV-48: extract exported fields for struct types.
					if st, ok := ts.Type.(*ast.StructType); ok {
						td.Fields = extractStructFields(st)
					}
					syms.Types = append(syms.Types, td)
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

	// INV-49: collect constructors — top-level functions whose return types
	// include at least one type declared in this file.
	typeNames := make(map[string]bool)
	for _, decl := range file.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok.String() == "type" {
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				typeNames[ts.Name.Name] = true
			}
		}
	}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Type.Results == nil {
			continue // skip methods and functions with no return values
		}
		for _, field := range fd.Type.Results.List {
			if name := extractBaseTypeName(field.Type); name != "" && typeNames[name] {
				syms.Constructors = append(syms.Constructors, fd.Name.Name)
				break // count each function only once
			}
		}
	}
	sort.Strings(syms.Constructors)

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

// extractStructFields collects exported fields from an ast.StructType in
// declaration order (INV-48). Embedded types use their base type name as the
// field name. Unexported fields are skipped.
func extractStructFields(st *ast.StructType) []FieldDecl {
	var fields []FieldDecl
	for _, field := range st.Fields.List {
		typeStr := exprToString(field.Type)
		if len(field.Names) == 0 {
			// Embedded field: use base type name as field name.
			name := extractBaseTypeName(field.Type)
			if name == "" || !ast.IsExported(name) {
				continue
			}
			fields = append(fields, FieldDecl{Name: name, TypeStr: typeStr})
		} else {
			for _, n := range field.Names {
				if !ast.IsExported(n.Name) {
					continue
				}
				fields = append(fields, FieldDecl{Name: n.Name, TypeStr: typeStr})
			}
		}
	}
	return fields
}

// extractBaseTypeName unwraps pointer (*T) and slice ([]T) wrappers to find
// the innermost named identifier. Returns "" for maps, channels, and other
// complex composite types.
func extractBaseTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return extractBaseTypeName(e.X)
	case *ast.ArrayType:
		return extractBaseTypeName(e.Elt)
	case *ast.Ident:
		return e.Name
	default:
		return ""
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

	// yaml_io: imports a yaml library (path contains "yaml") or calls yaml.* (INV-49).
	for path := range importSet {
		if strings.Contains(path, "yaml") {
			sig.YAMLio = true
			break
		}
	}
	if !sig.YAMLio {
		for target := range callSet {
			if strings.HasPrefix(target, "yaml.") {
				sig.YAMLio = true
				break
			}
		}
	}

	// json_io: encoding/json import or calls json.* (INV-49).
	if importSet["encoding/json"] {
		sig.JSONio = true
	}
	if !sig.JSONio {
		for target := range callSet {
			if strings.HasPrefix(target, "json.") {
				sig.JSONio = true
				break
			}
		}
	}

	return sig
}
