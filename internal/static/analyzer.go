// Package static implements the iguana "static" plugin: a Go AST-based
// evidence producer that clones a git repository and writes one markdown
// bundle per .go source file.
package static

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	_ "embed"

	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"

	"iguana/internal/frontmatter"
	"iguana/internal/plugin"
)

//go:embed schema.md
var schemaDoc []byte

// GoAnalyzer implements plugin.EvidenceProducer for Go source repositories.
type GoAnalyzer struct{}

func (g *GoAnalyzer) Name() string { return "static" }

func (g *GoAnalyzer) Configure() ([]plugin.ConfigQuestion, error) {
	return []plugin.ConfigQuestion{
		{Key: "repository", Prompt: "Git repository URL", Type: "text"},
	}, nil
}

// Analyze clones (or pulls) the repository, walks all non-test .go files, and
// writes one markdown+frontmatter bundle per file into outputDir.
func (g *GoAnalyzer) Analyze(config map[string]string, outputDir string) error {
	repoURL := config["repository"]
	if repoURL == "" {
		return fmt.Errorf("static: missing required config key 'repository'")
	}

	// Clone/update the repo into a temp directory.
	tmpDir, err := cloneOrPull(repoURL)
	if err != nil {
		return fmt.Errorf("static: fetch repo: %w", err)
	}

	// Get the commit hash.
	commitHash, err := gitRevParse(tmpDir)
	if err != nil {
		return fmt.Errorf("static: rev-parse: %w", err)
	}

	// Copy schema.md to outputDir.
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("static: create output dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "schema.md"), schemaDoc, 0o644); err != nil {
		return fmt.Errorf("static: write schema: %w", err)
	}

	// Walk all .go files grouped by directory.
	filesByDir, err := collectGoFiles(tmpDir)
	if err != nil {
		return fmt.Errorf("static: walk: %w", err)
	}

	dirs := sortedKeys(filesByDir)
	var errs []error
	for _, dir := range dirs {
		files := filesByDir[dir]
		sort.Strings(files)

		pkg, fset, _ := loadPackageForDir(dir)
		for _, absPath := range files {
			relPath, err := filepath.Rel(tmpDir, absPath)
			if err != nil {
				errs = append(errs, fmt.Errorf("rel path %s: %w", absPath, err))
				continue
			}
			relPath = filepath.ToSlash(relPath)

			if err := writeBundle(absPath, relPath, repoURL, commitHash, pkg, fset, outputDir); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", relPath, err))
			}
		}
	}
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return fmt.Errorf("static: %d error(s):\n%s", len(errs), strings.Join(msgs, "\n"))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Git helpers
// ---------------------------------------------------------------------------

func cloneOrPull(repoURL string) (string, error) {
	// Derive a stable temp-dir name from the URL.
	sum := sha256.Sum256([]byte(repoURL))
	name := "iguana-static-" + hex.EncodeToString(sum[:8])
	tmpDir := filepath.Join(os.TempDir(), name)

	if _, err := os.Stat(filepath.Join(tmpDir, ".git")); err == nil {
		// Already cloned: pull.
		cmd := exec.Command("git", "-C", tmpDir, "pull", "--depth", "1", "--ff-only")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git pull: %w\n%s", err, out)
		}
		return tmpDir, nil
	}

	// Fresh clone.
	cmd := exec.Command("git", "clone", "--depth", "1", repoURL, tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git clone: %w\n%s", err, out)
	}
	return tmpDir, nil
}

func gitRevParse(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ---------------------------------------------------------------------------
// File collection
// ---------------------------------------------------------------------------

func collectGoFiles(root string) (map[string][]string, error) {
	filesByDir := make(map[string][]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path == root {
				return nil
			}
			if name == "vendor" || name == "testdata" || name == "examples" ||
				name == "docs" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		dir := filepath.Dir(path)
		filesByDir[dir] = append(filesByDir[dir], path)
		return nil
	})
	return filesByDir, err
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// Bundle writing
// ---------------------------------------------------------------------------

func writeBundle(absPath, relPath, repoURL, commitHash string, pkg *packages.Package, fset *token.FileSet, outputDir string) error {
	fileBytes, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	sum := sha256.Sum256(fileBytes)
	hash := hex.EncodeToString(sum[:])

	// Determine output path: <outputDir>/<package>/<file>.md
	outFile := filepath.Join(outputDir, filepath.Dir(relPath), filepath.Base(relPath)+".md")
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return err
	}

	// Check staleness: if existing bundle has same hash, skip.
	if existing, err := os.ReadFile(outFile); err == nil {
		if existingHash, ok := extractHashFromBundle(existing); ok && existingHash == hash {
			return nil // up to date
		}
	}

	// Build the bundle frontmatter.
	fm, err := buildFrontmatter(absPath, relPath, repoURL, commitHash, hash, pkg, fset)
	if err != nil {
		return fmt.Errorf("build frontmatter: %w", err)
	}

	data, err := frontmatter.Write(fm, "")
	if err != nil {
		return fmt.Errorf("write frontmatter: %w", err)
	}

	return os.WriteFile(outFile, data, 0o644)
}

// extractHashFromBundle reads the hash field from an existing bundle file.
func extractHashFromBundle(data []byte) (string, bool) {
	fmBytes, _, err := frontmatter.Parse(data)
	if err != nil {
		return "", false
	}
	var m map[string]any
	if err := yaml.Unmarshal(fmBytes, &m); err != nil {
		return "", false
	}
	h, ok := m["hash"].(string)
	return h, ok
}

// ---------------------------------------------------------------------------
// Frontmatter construction
// ---------------------------------------------------------------------------

// bundleFrontmatter is the YAML structure written for each file.
type bundleFrontmatter struct {
	Plugin    string      `yaml:"plugin"`
	Schema    string      `yaml:"schema"`
	Hash      string      `yaml:"hash"`
	File      fileMeta    `yaml:"file"`
	Package   packageMeta `yaml:"package"`
	Functions []function  `yaml:"functions,omitempty"`
	Types     []typeDecl  `yaml:"types,omitempty"`
	Signals   signals     `yaml:"signals"`
}

type fileMeta struct {
	Path string `yaml:"path"`
	Ref  string `yaml:"ref"`
}

type packageMeta struct {
	Name    string   `yaml:"name"`
	Imports []importEntry `yaml:"imports,omitempty"`
}

type importEntry struct {
	Path  string `yaml:"path"`
	Alias string `yaml:"alias,omitempty"`
}

type function struct {
	Name     string   `yaml:"name"`
	Exported bool     `yaml:"exported"`
	Receiver string   `yaml:"receiver,omitempty"`
	Params   []string `yaml:"params,omitempty"`
	Returns  []string `yaml:"returns,omitempty"`
	Ref      string   `yaml:"ref"`
}

type fieldDecl struct {
	Name    string `yaml:"name"`
	TypeStr string `yaml:"type"`
}

type typeDecl struct {
	Name     string      `yaml:"name"`
	Kind     string      `yaml:"kind"`
	Exported bool        `yaml:"exported"`
	Fields   []fieldDecl `yaml:"fields,omitempty"`
}

type signals struct {
	FSReads     bool `yaml:"fs_reads"`
	FSWrites    bool `yaml:"fs_writes"`
	DBCalls     bool `yaml:"db_calls"`
	NetCalls    bool `yaml:"net_calls"`
	Concurrency bool `yaml:"concurrency"`
	YAMLio      bool `yaml:"yaml_io"`
	JSONio      bool `yaml:"json_io"`
}

func buildFrontmatter(absPath, relPath, repoURL, commitHash, hash string, pkg *packages.Package, fset *token.FileSet) (*bundleFrontmatter, error) {
	fileRef := buildRef(repoURL, commitHash, relPath, 0)

	var file *ast.File
	var typesInfo *types.Info
	var typesPkg *types.Package
	var fileFset *token.FileSet

	if pkg != nil && fset != nil && pkg.TypesInfo != nil && pkg.Types != nil {
		for _, f := range pkg.Syntax {
			pos := fset.Position(f.Pos())
			if pos.Filename == absPath {
				file = f
				typesInfo = pkg.TypesInfo
				typesPkg = pkg.Types
				fileFset = fset
				break
			}
		}
	}

	if file == nil {
		// Fallback: parse with go/parser.
		fileFset = token.NewFileSet()
		var err error
		file, err = parser.ParseFile(fileFset, absPath, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}
	}

	qualifier := makeQualifier(typesPkg)
	pkgMeta := extractPackageMeta(file)
	fns := extractFunctions(file, typesInfo, typesPkg, qualifier, fileFset, repoURL, commitHash, relPath)
	tds := extractTypes(file)
	sigs := extractSignals(pkgMeta, file, fns)

	return &bundleFrontmatter{
		Plugin: "static",
		Schema: "schema.md",
		Hash:   hash,
		File: fileMeta{
			Path: relPath,
			Ref:  fileRef,
		},
		Package:   pkgMeta,
		Functions: fns,
		Types:     tds,
		Signals:   sigs,
	}, nil
}

// buildRef constructs a git:// URL. line=0 means no line anchor.
func buildRef(repoURL, commitHash, relPath string, line int) string {
	// Normalize: strip trailing .git if present.
	base := strings.TrimSuffix(repoURL, ".git")
	// Convert https:// to git:// host notation.
	base = strings.TrimPrefix(base, "https://")
	base = strings.TrimPrefix(base, "http://")
	ref := fmt.Sprintf("git://%s@%s/%s", base, commitHash, relPath)
	if line > 0 {
		ref += fmt.Sprintf("#L%d", line)
	}
	return ref
}

// ---------------------------------------------------------------------------
// Type loading (reused from evidence package)
// ---------------------------------------------------------------------------

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
		return nil, nil, err
	}
	if len(pkgs) == 0 {
		return nil, nil, fmt.Errorf("no packages found")
	}
	pkg := pkgs[0]
	if pkg.TypesInfo == nil || pkg.Types == nil {
		return nil, nil, fmt.Errorf("no type info")
	}
	return pkg, fset, nil
}

func makeQualifier(pkg *types.Package) types.Qualifier {
	return func(p *types.Package) string {
		if pkg != nil && p == pkg {
			return ""
		}
		return p.Name()
	}
}

// ---------------------------------------------------------------------------
// Extraction: package metadata
// ---------------------------------------------------------------------------

func extractPackageMeta(file *ast.File) packageMeta {
	meta := packageMeta{Name: file.Name.Name}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		entry := importEntry{Path: path}
		if imp.Name != nil {
			entry.Alias = imp.Name.Name
		}
		meta.Imports = append(meta.Imports, entry)
	}
	sort.Slice(meta.Imports, func(i, j int) bool {
		return meta.Imports[i].Path < meta.Imports[j].Path
	})
	return meta
}

// ---------------------------------------------------------------------------
// Extraction: functions
// ---------------------------------------------------------------------------

func extractFunctions(file *ast.File, typesInfo *types.Info, typesPkg *types.Package, qualifier types.Qualifier, fset *token.FileSet, repoURL, commitHash, relPath string) []function {
	var fns []function
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		fn := extractFunction(fd, typesInfo, qualifier)

		// Compute line number for ref.
		line := 0
		if fset != nil {
			line = fset.Position(fd.Pos()).Line
		}
		fn.Ref = buildRef(repoURL, commitHash, relPath, line)
		fns = append(fns, fn)
	}
	sort.Slice(fns, func(i, j int) bool { return fns[i].Name < fns[j].Name })
	return fns
}

func extractFunction(decl *ast.FuncDecl, typesInfo *types.Info, qualifier types.Qualifier) function {
	fn := function{
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
					ts := types.TypeString(params.At(i).Type(), qualifier)
					if sig.Variadic() && i == params.Len()-1 {
						ts = "..." + ts
					}
					fn.Params = append(fn.Params, ts)
				}
				results := sig.Results()
				for i := 0; i < results.Len(); i++ {
					fn.Returns = append(fn.Returns, types.TypeString(results.At(i).Type(), qualifier))
				}
				return fn
			}
		}
	}

	// AST fallback.
	if decl.Recv != nil && len(decl.Recv.List) > 0 {
		fn.Receiver = exprToString(decl.Recv.List[0].Type)
	}
	if decl.Type.Params != nil {
		for _, field := range decl.Type.Params.List {
			ts := exprToString(field.Type)
			if len(field.Names) == 0 {
				fn.Params = append(fn.Params, ts)
			} else {
				for range field.Names {
					fn.Params = append(fn.Params, ts)
				}
			}
		}
	}
	if decl.Type.Results != nil {
		for _, field := range decl.Type.Results.List {
			ts := exprToString(field.Type)
			if len(field.Names) == 0 {
				fn.Returns = append(fn.Returns, ts)
			} else {
				for range field.Names {
					fn.Returns = append(fn.Returns, ts)
				}
			}
		}
	}
	return fn
}

// ---------------------------------------------------------------------------
// Extraction: types
// ---------------------------------------------------------------------------

func extractTypes(file *ast.File) []typeDecl {
	var tds []typeDecl
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok.String() != "type" {
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			td := typeDecl{
				Name:     ts.Name.Name,
				Kind:     typeKind(ts.Type),
				Exported: ast.IsExported(ts.Name.Name),
			}
			if st, ok := ts.Type.(*ast.StructType); ok {
				td.Fields = extractStructFields(st)
			}
			tds = append(tds, td)
		}
	}
	sort.Slice(tds, func(i, j int) bool { return tds[i].Name < tds[j].Name })
	return tds
}

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

func extractStructFields(st *ast.StructType) []fieldDecl {
	var fields []fieldDecl
	for _, field := range st.Fields.List {
		ts := exprToString(field.Type)
		if len(field.Names) == 0 {
			name := extractBaseTypeName(field.Type)
			if name == "" || !ast.IsExported(name) {
				continue
			}
			fields = append(fields, fieldDecl{Name: name, TypeStr: ts})
		} else {
			for _, n := range field.Names {
				if !ast.IsExported(n.Name) {
					continue
				}
				fields = append(fields, fieldDecl{Name: n.Name, TypeStr: ts})
			}
		}
	}
	return fields
}

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
		return "[...]" + exprToString(e.Elt)
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
// Extraction: signals
// ---------------------------------------------------------------------------

func extractSignals(meta packageMeta, file *ast.File, fns []function) signals {
	importSet := make(map[string]bool, len(meta.Imports))
	for _, imp := range meta.Imports {
		importSet[imp.Path] = true
	}

	// Build a call set by walking the AST for CallExprs.
	callSet := make(map[string]bool)
	ast.Inspect(file, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch e := ce.Fun.(type) {
		case *ast.SelectorExpr:
			if ident, ok := e.X.(*ast.Ident); ok {
				callSet[ident.Name+"."+e.Sel.Name] = true
			}
		case *ast.Ident:
			callSet[e.Name] = true
		}
		return true
	})

	var sig signals

	for _, fn := range []string{"os.Open", "os.ReadFile", "ioutil.ReadFile", "filepath.Walk"} {
		if callSet[fn] {
			sig.FSReads = true
			break
		}
	}
	for _, fn := range []string{"os.Create", "os.WriteFile", "os.Remove"} {
		if callSet[fn] {
			sig.FSWrites = true
			break
		}
	}
	if importSet["database/sql"] {
		sig.DBCalls = true
	}
	if !sig.DBCalls {
		for target := range callSet {
			if strings.Contains(target, "Query") || strings.Contains(target, "Exec") || strings.Contains(target, "Scan") {
				sig.DBCalls = true
				break
			}
		}
	}
	if importSet["net"] || importSet["net/http"] {
		sig.NetCalls = true
	}
	for path := range importSet {
		if path == "sync" || strings.HasPrefix(path, "sync/") {
			sig.Concurrency = true
			break
		}
	}
	if !sig.Concurrency {
		ast.Inspect(file, func(n ast.Node) bool {
			if sig.Concurrency {
				return false
			}
			switch n.(type) {
			case *ast.GoStmt, *ast.ChanType:
				sig.Concurrency = true
				return false
			}
			return true
		})
	}
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

// AnalyzeDir walks root, generating one markdown bundle per .go file in outputDir.
// It is exported for testing without network access (no git clone).
func AnalyzeDir(root, repoURL, commitHash, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("static: create output dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "schema.md"), schemaDoc, 0o644); err != nil {
		return fmt.Errorf("static: write schema: %w", err)
	}

	filesByDir, err := collectGoFiles(root)
	if err != nil {
		return fmt.Errorf("static: walk: %w", err)
	}

	dirs := sortedKeys(filesByDir)
	var errs []error
	for _, dir := range dirs {
		files := filesByDir[dir]
		sort.Strings(files)

		pkg, fset, _ := loadPackageForDir(dir)
		for _, absPath := range files {
			relPath, err := filepath.Rel(root, absPath)
			if err != nil {
				errs = append(errs, fmt.Errorf("rel path %s: %w", absPath, err))
				continue
			}
			relPath = filepath.ToSlash(relPath)

			if err := writeBundle(absPath, relPath, repoURL, commitHash, pkg, fset, outputDir); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", relPath, err))
			}
		}
	}
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return fmt.Errorf("static: %d error(s):\n%s", len(errs), strings.Join(msgs, "\n"))
	}
	return nil
}

// Ensure bytes is used (for extractHashFromBundle).
var _ = bytes.NewBuffer
